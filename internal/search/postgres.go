package search

// The Postgres engine: the floor that cannot be down.
//
// It reads the generated tsvector columns, so there is nothing to keep in step
// and nothing to backfill. That is the whole reason it is trustworthy as a
// fallback: an engine that needed its own indexing pipeline would be stale in
// exactly the circumstances where it is being relied on, which is when the
// other pipeline has stopped.

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Postgres answers from the database's own full text index.
type Postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) *Postgres { return &Postgres{db: db} }

func (p *Postgres) Name() string { return "postgres" }

// Ready is a query rather than a ping, because a reachable database with a
// missing index is not ready for this and would answer every search with a
// sequential scan.
func (p *Postgres) Ready(ctx context.Context) error {
	var exists bool
	err := p.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_indexes
		               WHERE schemaname='public' AND indexname='documents_search_idx')`).Scan(&exists)
	if err != nil {
		return fmt.Errorf("search: postgres unreachable: %w", err)
	}
	if !exists {
		return fmt.Errorf("search: the full text index is missing")
	}
	return nil
}

// Search runs one query across documents and attachments.
//
// The permission filter is a subquery on grants, the same expression every
// other list uses. Files are reached through the document they hang off, so a
// file id on its own grants nothing and a hit on an attachment cannot outlive
// the share it came from.
// EntityActivity is the entity type of a note or a call on a CRM timeline. It is not an entity with a page of its own, so
// it lives here and not with the ones that have grants.
const EntityActivity = "activity"

func (p *Postgres) Search(ctx context.Context, orgID string, subjects core.Subjects, q Query) (*Results, error) {
	tsq, ok := toTSQuery(q.Text)
	if !ok {
		return &Results{}, nil
	}

	wantDocs, wantFiles := true, true
	wantContacts, wantCompanies := true, true
	wantDeals, wantActivities := true, true
	if len(q.Types) > 0 {
		wantDocs, wantFiles = false, false
		wantContacts, wantCompanies = false, false
		wantDeals, wantActivities = false, false
		for _, t := range q.Types {
			switch t {
			case string(core.EntityDocument):
				wantDocs = true
			case string(core.EntityFile):
				wantFiles = true
			case string(core.EntityContact):
				wantContacts = true
			case string(core.EntityCompany):
				wantCompanies = true
			case string(core.EntityDeal):
				wantDeals = true
			case EntityActivity:
				wantActivities = true
			}
		}
	}
	// A kind filter is about documents, so asking for one excludes everything
	// that has no kind rather than silently ignoring the filter.
	if q.Kind != "" {
		wantFiles, wantContacts, wantCompanies, wantDeals, wantActivities = false, false, false, false, false
	}

	var parts []string
	args := []any{orgID, subjects.Keys(), tsq}
	super := subjects.Super()

	if wantDocs {
		kindClause := ""
		if q.Kind != "" {
			args = append(args, q.Kind)
			kindClause = fmt.Sprintf(" AND d.kind = $%d", len(args))
		}
		parts = append(parts, `
		SELECT 'document'::text AS entity_type, d.id::text AS entity_id, d.kind,
		       d.title, d.updated_at, ''::text AS parent_id,
		       ts_rank_cd(d.search_tsv, websearch_to_tsquery('english', $3)) AS score,
		       ts_headline('english', left(d.body, 8000), websearch_to_tsquery('english', $3),
		                   'MaxWords=24, MinWords=8, ShortWord=3, MaxFragments=1, StartSel=<mark>, StopSel=</mark>')
		         AS snippet
		FROM documents d
		WHERE d.org_id = $1 AND d.deleted_at IS NULL
		  AND d.search_tsv @@ websearch_to_tsquery('english', $3)`+kindClause+`
		  AND `+grantFilter(super, "document", "d.id"))
	}

	if wantFiles {
		parts = append(parts, `
		SELECT 'file'::text AS entity_type, f.id::text AS entity_id, ''::text AS kind,
		       f.filename AS title, f.created_at AS updated_at,
		       l.from_id::text AS parent_id,
		       ts_rank_cd(f.search_tsv, websearch_to_tsquery('english', $3)) AS score,
		       ts_headline('english', left(coalesce(f.extracted_text, ''), 8000), websearch_to_tsquery('english', $3),
		                   'MaxWords=24, MinWords=8, ShortWord=3, MaxFragments=1, StartSel=<mark>, StopSel=</mark>')
		         AS snippet
		FROM files f
		JOIN entity_links l
		  ON l.to_type = 'file' AND l.to_id = f.id AND l.from_type = 'document'
		WHERE f.org_id = $1 AND f.deleted_at IS NULL
		  AND f.search_tsv @@ websearch_to_tsquery('english', $3)
		  AND `+grantFilter(super, "document", "l.from_id"))
	}

	// Contacts and companies, on the same generated tsvector and the same
	// permission filter. The floor has to cover them or a cluster outage takes
	// the phone book with it, which is the one thing somebody standing next to
	// a broken machine actually needs.
	if wantContacts {
		parts = append(parts, `
		SELECT 'contact'::text AS entity_type, ct.id::text AS entity_id,
		       'contact'::text AS kind, ct.name AS title, ct.updated_at,
		       COALESCE(ct.company_id::text, '') AS parent_id,
		       ts_rank_cd(ct.search_tsv, websearch_to_tsquery('english', $3)) AS score,
		       ts_headline('english',
		           concat_ws(' ', ct.title, ct.email, ct.phone, left(ct.notes, 4000)),
		           websearch_to_tsquery('english', $3),
		           'MaxWords=24, MinWords=8, ShortWord=3, MaxFragments=1, StartSel=<mark>, StopSel=</mark>')
		         AS snippet
		FROM contacts ct
		WHERE ct.org_id = $1 AND ct.deleted_at IS NULL
		  AND ct.search_tsv @@ websearch_to_tsquery('english', $3)
		  AND `+grantFilter(super, "contact", "ct.id"))
	}

	if wantCompanies {
		parts = append(parts, `
		SELECT 'company'::text AS entity_type, co.id::text AS entity_id,
		       'company'::text AS kind, co.name AS title, co.updated_at,
		       ''::text AS parent_id,
		       ts_rank_cd(co.search_tsv, websearch_to_tsquery('english', $3)) AS score,
		       ts_headline('english',
		           concat_ws(' ', co.kind, co.domain, left(co.notes, 4000)),
		           websearch_to_tsquery('english', $3),
		           'MaxWords=24, MinWords=8, ShortWord=3, MaxFragments=1, StartSel=<mark>, StopSel=</mark>')
		         AS snippet
		FROM companies co
		WHERE co.org_id = $1 AND co.deleted_at IS NULL
		  AND co.search_tsv @@ websearch_to_tsquery('english', $3)
		  AND `+grantFilter(super, "company", "co.id"))
	}

	// Deals, and the notes and calls on the timeline. A deal is found on the same permission as its page. An activity is
	// about one or more records, and is found by somebody who can see any of them, which is the rule its timeline uses.
	// Its hit leads to the first record it is about (parent is "type:id").
	if wantDeals {
		parts = append(parts, `
		SELECT 'deal'::text AS entity_type, dl.id::text AS entity_id,
		       'deal'::text AS kind, dl.name AS title, dl.updated_at,
		       ''::text AS parent_id,
		       ts_rank_cd(dl.search_tsv, websearch_to_tsquery('english', $3)) AS score,
		       ts_headline('english',
		           concat_ws(' ', dl.stage, left(dl.notes, 4000)),
		           websearch_to_tsquery('english', $3),
		           'MaxWords=24, MinWords=8, ShortWord=3, MaxFragments=1, StartSel=<mark>, StopSel=</mark>')
		         AS snippet
		FROM deals dl
		WHERE dl.org_id = $1 AND dl.deleted_at IS NULL
		  AND dl.search_tsv @@ websearch_to_tsquery('english', $3)
		  AND `+grantFilter(super, "deal", "dl.id"))
	}

	if wantActivities {
		seen := "($2::text[] IS NOT NULL OR TRUE)"
		if !super {
			seen = `EXISTS (SELECT 1 FROM crm_activity_targets t JOIN grants g
			          ON g.entity_type = t.target_type AND g.entity_id = t.target_id
			         WHERE t.activity_id = a.id AND g.subject_key = ANY($2)
			           AND (g.expires_at IS NULL OR g.expires_at > now()))`
		}
		parts = append(parts, `
		SELECT 'activity'::text AS entity_type, a.id::text AS entity_id,
		       a.kind AS kind, COALESCE(NULLIF(a.title, ''), initcap(a.kind)) AS title, a.occurred_at AS updated_at,
		       COALESCE((SELECT t.target_type || ':' || t.target_id::text FROM crm_activity_targets t
		                  WHERE t.activity_id = a.id
		                  ORDER BY (t.target_type = 'deal') DESC, (t.target_type = 'contact') DESC LIMIT 1), '') AS parent_id,
		       ts_rank_cd(a.search_tsv, websearch_to_tsquery('english', $3)) AS score,
		       ts_headline('english', left(concat_ws(' ', a.title, a.body), 8000), websearch_to_tsquery('english', $3),
		           'MaxWords=24, MinWords=8, ShortWord=3, MaxFragments=1, StartSel=<mark>, StopSel=</mark>')
		         AS snippet
		FROM crm_activities a
		WHERE a.org_id = $1 AND a.deleted_at IS NULL
		  AND a.search_tsv @@ websearch_to_tsquery('english', $3)
		  AND `+seen)
	}

	if len(parts) == 0 {
		return &Results{}, nil
	}

	args = append(args, q.Limit)
	sql := "WITH hits AS (" + strings.Join(parts, " UNION ALL ") + `)
		SELECT entity_type, entity_id, kind, title, updated_at, parent_id, score, snippet
		FROM hits ORDER BY score DESC, updated_at DESC LIMIT $` + fmt.Sprint(len(args))

	rows, err := p.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search: postgres query: %w", err)
	}
	defer rows.Close()

	out := &Results{}
	for rows.Next() {
		var h Hit
		var snippet string
		var updated time.Time
		if err := rows.Scan(&h.EntityType, &h.EntityID, &h.Kind, &h.Title,
			&updated, &h.ParentID, &h.Score, &snippet); err != nil {
			return nil, fmt.Errorf("search: scan hit: %w", err)
		}
		h.UpdatedAt = updated
		h.Snippet = snippet
		out.Hits = append(out.Hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: iterate hits: %w", err)
	}
	out.Total = len(out.Hits)
	return out, nil
}

// toTSQuery turns what somebody typed into a query the database will accept.
//
// websearch_to_tsquery is used rather than plainto_tsquery because it
// understands quoted phrases and the word "or", which are the two things people
// actually type, and it never raises a syntax error on punctuation. Building
// the operator string by hand is how a search box becomes a way to make the
// database throw.
func toTSQuery(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	// The value is still a bind parameter; this only bounds its size, because a
	// pathological query string is a way to spend a lot of CPU on one request.
	if len(text) > 400 {
		text = text[:400]
	}
	return text, true
}

// SnippetHTML marks the snippet safe to render.
//
// ts_headline emits <mark> around the matched words and escapes everything
// else, so this is the one place raw HTML is deliberately trusted, and it is
// trusted because Postgres produced it rather than a person.
func SnippetHTML(s string) template.HTML { return template.HTML(s) }

// grantFilter is the permission clause for one kind of thing: a grant to anybody
// the caller is, or no clause at all for a super admin, who may reach everything.
// The classic lists and the document page already answer a super admin that way
// (core.Subjects.GrantsExists); search alone asked the grants table, where an
// admin has no row for work that other people created and assigned to them, so the
// admin saw nothing while its creator found it. $2 is still referenced on the
// super path so the statement uses every parameter it was given.
func grantFilter(super bool, entityType, idExpr string) string {
	if super {
		return "($2::text[] IS NOT NULL OR TRUE)"
	}
	return "EXISTS (SELECT 1 FROM grants g WHERE g.entity_type = '" + entityType + "' AND g.entity_id = " + idExpr +
		" AND g.subject_key = ANY($2) AND (g.expires_at IS NULL OR g.expires_at > now()))"
}

// Verify keeps the hits the caller may open right now. Documents are asked of
// their own grants and an attachment of the document it hangs off, the same
// expressions the search above uses; anything else is left as it came. It is how
// an answer from the cluster stops showing a document the moment a grant ends or
// is taken back, before the index has heard.
func (p *Postgres) Verify(ctx context.Context, subjects core.Subjects, hits []Hit) ([]Hit, error) {
	var docIDs []string
	for _, h := range hits {
		switch h.EntityType {
		case string(core.EntityDocument):
			docIDs = append(docIDs, h.EntityID)
		case string(core.EntityFile):
			docIDs = append(docIDs, h.ParentID)
		}
	}
	if len(docIDs) == 0 {
		return hits, nil
	}
	rows, err := p.db.Query(ctx, `
		SELECT d.id::text FROM documents d
		 WHERE d.id::text = ANY($1) AND `+grantFilter(subjects.Super(), "document", "d.id"),
		docIDs, subjects.Keys())
	if err != nil {
		return nil, fmt.Errorf("verify hits: %w", err)
	}
	defer rows.Close()
	ok := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ok[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	kept := make([]Hit, 0, len(hits))
	for _, h := range hits {
		switch h.EntityType {
		case string(core.EntityDocument):
			if !ok[h.EntityID] {
				continue
			}
		case string(core.EntityFile):
			if !ok[h.ParentID] {
				continue
			}
		}
		kept = append(kept, h)
	}
	return kept, nil
}
