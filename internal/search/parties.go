package search

// Indexing contacts and companies.
//
// One function for both, because the difference between them is two column
// names. They are ordinary entities keyed by (entity_type, entity_id) like
// everything else, which is the whole reason making them findable is thirty
// lines rather than a second search implementation.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// indexParty pushes a contact or a company into the external index.
//
// The body is everything a person might search for that is not the name: an
// address, a phone number, a job title. Somebody looking for "the chap at
// Baldor who does the rewinds" types one of those three, and a title that only
// carries the name finds none of them.
func (ix *Pipeline) indexParty(ctx context.Context, orgID string, t core.EntityType, id string) error {
	table := "contacts"
	if t == core.EntityCompany {
		table = "companies"
	}

	var d Document
	var deleted *time.Time
	var fields []string
	err := ix.db.QueryRow(ctx, `
		SELECT e.org_id::text, e.name, e.updated_at, e.deleted_at,
		       `+partyBody(table)+`,
		       COALESCE(array_agg(g.subject_key) FILTER (WHERE g.subject_key IS NOT NULL), '{}')
		FROM `+table+` e
		LEFT JOIN grants g
		       ON g.entity_type = $2 AND g.entity_id = e.id
		      AND (g.expires_at IS NULL OR g.expires_at > now())
		WHERE e.id = $1
		GROUP BY e.id`, id, string(t)).
		Scan(&d.OrgID, &d.Title, &d.UpdatedAt, &deleted, &fields, &d.Subjects)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ix.removeAndForget(ctx, string(t), id)
	case err != nil:
		return fmt.Errorf("search: load %s %s: %w", t, id, err)
	}
	if deleted != nil {
		return ix.removeAndForget(ctx, string(t), id)
	}

	d.EntityType, d.EntityID = string(t), id
	d.Kind = string(t)
	d.Body = strings.TrimSpace(strings.Join(fields, " "))
	// Neither table carries a version, because neither has snapshots. One is
	// enough to record that it was indexed, and the bookkeeping row is what
	// the backfill compares against.
	d.Version = 1

	if err := ix.target.Index(ctx, d); err != nil {
		ix.recordFailure(ctx, orgID, string(t), id, err)
		return err
	}
	return ix.recordIndexed(ctx, orgID, string(t), id, d.Version)
}

// partyBody is the searchable text besides the name, as a SQL array so the two
// tables can differ without two queries.
func partyBody(table string) string {
	if table == "companies" {
		return `ARRAY[e.domain, e.kind, e.notes]`
	}
	return `ARRAY[e.email, e.phone, e.title, e.notes]`
}
