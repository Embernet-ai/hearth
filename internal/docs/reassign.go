package docs

// Reassigning work in bulk: put each task and work order on the people it
// belongs to, from a plan somebody has already read.
//
// This is the same write Assign makes, row for row: the assignee list, the
// primary mirrored into task_fields, and the edit grant the person doing the
// work needs. It differs in three ways on purpose, because it runs over
// thousands of records at once. It does not bump updated_at, so "recently
// updated" does not become "everything touched today". It sends no assigned
// event, so nobody gets a thousand notifications for work that was always
// theirs. And it leaves owner_id and created_by alone, which say who filed a
// record and are not the assignee.
//
// One transaction, and the transaction proves itself before it is allowed to
// commit: every row reads back as planned, and a checksum of owner_id,
// created_by and updated_at over every document is identical to what it was.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// Reassignment is one row of a plan.
type Reassignment struct {
	DocID string
	// Assignees are principal ids; Primary is one of them, or empty when the
	// list is empty.
	Assignees []string
	Primary   string
	// ExpectCurrent, when set, are the display names the plan was written
	// against. If the live assignees differ the run stops: the plan is stale.
	ExpectCurrent []string
	// SkipExpect disables that check for this row (a plan written by hand).
	SkipExpect bool
}

// ReassignReport is what a run did, or would do.
type ReassignReport struct {
	Rows    int
	Changed int
	// Before and After count how many plan rows each person is on.
	Before, After map[string]int
	// Reverse is the plan that puts every changed row back as it was.
	Reverse []Reassignment
	// Names maps a principal id to a display name, for printing.
	Names map[string]string
	// Checksum is the owner_id, created_by and updated_at fingerprint, taken
	// before and after; Apply refuses to commit if they differ.
	ChecksumBefore, ChecksumAfter string
	Committed                     bool
}

const fingerprint = `
	SELECT md5(COALESCE(string_agg(id::text || owner_id::text || created_by::text ||
	       updated_at::text, ',' ORDER BY id), ''))
	  FROM documents WHERE org_id = $1`

// Reassign runs a plan. With apply false it does all the work and rolls back,
// so what it reports is exactly what an apply would do.
func (s *Service) Reassign(ctx context.Context, orgID, actorID string, plan []Reassignment, apply, restore bool) (*ReassignReport, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rep := &ReassignReport{Rows: len(plan), Before: map[string]int{}, After: map[string]int{}, Names: map[string]string{}}
	if err := tx.QueryRow(ctx, fingerprint, orgID).Scan(&rep.ChecksumBefore); err != nil {
		return nil, fmt.Errorf("docs: fingerprint: %w", err)
	}

	names := map[string]string{}
	nrows, err := tx.Query(ctx, `SELECT id::text, COALESCE(NULLIF(display_name,''), id::text) FROM principals WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	for nrows.Next() {
		var id, n string
		if err := nrows.Scan(&id, &n); err != nil {
			nrows.Close()
			return nil, err
		}
		names[id] = n
	}
	nrows.Close()
	rep.Names = names

	// Who may be assigned: active people. A restore may put back someone who
	// has since been deactivated, which is the point of a restore.
	active := map[string]bool{}
	arows, err := tx.Query(ctx, `SELECT id::text FROM principals WHERE org_id = $1 AND kind = 'user' AND (status = 'active' OR $2)`, orgID, restore)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var id string
		if err := arows.Scan(&id); err != nil {
			arows.Close()
			return nil, err
		}
		active[id] = true
	}
	arows.Close()

	ids := make([]string, 0, len(plan))
	for _, r := range plan {
		ids = append(ids, r.DocID)
		for _, a := range r.Assignees {
			if !active[a] {
				return nil, fmt.Errorf("docs: reassign %s: %s is not an active person here", r.DocID, names[a])
			}
		}
		if len(r.Assignees) > 0 && !contains(r.Assignees, r.Primary) {
			return nil, fmt.Errorf("docs: reassign %s: the primary is not in the list", r.DocID)
		}
	}

	// The documents must be live tasks or work orders in this org.
	drows, err := tx.Query(ctx, `
		SELECT d.id::text FROM documents d
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL AND d.kind IN ('task', 'work_order')
		   AND d.id = ANY($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for drows.Next() {
		var id string
		if err := drows.Scan(&id); err != nil {
			drows.Close()
			return nil, err
		}
		live[id] = true
	}
	drows.Close()
	for _, id := range ids {
		if !live[id] {
			return nil, fmt.Errorf("docs: reassign %s: not a live task or work order here", id)
		}
	}

	current, err := currentAssignees(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range plan {
		for _, c := range current[r.DocID] {
			rep.Before[c]++
		}
	}

	for _, r := range plan {
		cur := current[r.DocID]
		if !r.SkipExpect && r.ExpectCurrent != nil {
			var got []string
			for _, c := range cur {
				got = append(got, names[c])
			}
			if !sameSet(got, r.ExpectCurrent) {
				return nil, fmt.Errorf("docs: reassign %s: the plan expected [%s] but it is now [%s]; the plan is stale",
					r.DocID, strings.Join(r.ExpectCurrent, "; "), strings.Join(got, "; "))
			}
		}
		if sameSet(cur, r.Assignees) && primaryOf(tx, ctx, r.DocID) == r.Primary {
			continue
		}
		rep.Changed++
		rev := Reassignment{DocID: r.DocID, Assignees: cur}
		if len(cur) > 0 {
			rev.Primary = cur[0]
		}
		rep.Reverse = append(rep.Reverse, rev)

		if _, err := tx.Exec(ctx, `DELETE FROM document_assignees WHERE document_id = $1`, r.DocID); err != nil {
			return nil, fmt.Errorf("docs: clear assignees: %w", err)
		}
		for _, a := range r.Assignees {
			if _, err := tx.Exec(ctx, `
				INSERT INTO document_assignees (document_id, principal_id, is_primary)
				VALUES ($1, $2::uuid, $3)`, r.DocID, a, a == r.Primary); err != nil {
				return nil, fmt.Errorf("docs: add assignee: %w", err)
			}
			// The same edit grant Assign gives: the person doing the work has
			// to be able to record what they did. Nothing here removes one.
			if _, err := tx.Exec(ctx, `
				INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
				VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'edit', $4)
				ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
				orgID, r.DocID, "principal:"+a, actorID); err != nil {
				return nil, fmt.Errorf("docs: grant to assignee: %w", err)
			}
		}
		var primaryVal any
		if r.Primary != "" {
			primaryVal = r.Primary
		}
		// No updated_at here, and no event: see the file comment.
		if _, err := tx.Exec(ctx, `UPDATE task_fields SET assignee_id = $2 WHERE document_id = $1`, r.DocID, primaryVal); err != nil {
			return nil, fmt.Errorf("docs: set primary assignee: %w", err)
		}
	}

	// Prove it before it can commit.
	after, err := currentAssignees(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range plan {
		if !sameSet(after[r.DocID], r.Assignees) {
			return nil, fmt.Errorf("docs: reassign %s: read back [%s], planned [%s]", r.DocID,
				strings.Join(after[r.DocID], ","), strings.Join(r.Assignees, ","))
		}
		if got := primaryOf(tx, ctx, r.DocID); got != r.Primary {
			return nil, fmt.Errorf("docs: reassign %s: primary reads back %q, planned %q", r.DocID, got, r.Primary)
		}
		var tfPrimary *string
		if err := tx.QueryRow(ctx, `SELECT assignee_id::text FROM task_fields WHERE document_id = $1`, r.DocID).Scan(&tfPrimary); err != nil {
			return nil, fmt.Errorf("docs: read task fields for %s: %w", r.DocID, err)
		}
		if (tfPrimary == nil && r.Primary != "") || (tfPrimary != nil && *tfPrimary != r.Primary) {
			return nil, fmt.Errorf("docs: reassign %s: task_fields primary does not match", r.DocID)
		}
		for _, a := range after[r.DocID] {
			rep.After[a]++
		}
	}
	if err := tx.QueryRow(ctx, fingerprint, orgID).Scan(&rep.ChecksumAfter); err != nil {
		return nil, fmt.Errorf("docs: fingerprint: %w", err)
	}
	if rep.ChecksumAfter != rep.ChecksumBefore {
		return nil, errors.New("docs: reassign changed owner_id, created_by or updated_at; rolled back")
	}

	if apply {
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, ActorID: actorID, Action: audit.ActionAssignBulk,
			EntityType: "document", Before: map[string]int{"rows": rep.Rows},
			After: map[string]any{"rows": rep.Rows, "changed": rep.Changed, "restore": restore},
		}); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("docs: commit: %w", err)
		}
		rep.Committed = true
	}
	return rep, nil
}

// currentAssignees reads who each document is assigned to, primary first.
func currentAssignees(ctx context.Context, tx pgx.Tx, ids []string) (map[string][]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT document_id::text, principal_id::text FROM document_assignees
		 WHERE document_id = ANY($1::uuid[])
		 ORDER BY document_id, is_primary DESC, created_at, principal_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("docs: read assignees: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var d, p string
		if err := rows.Scan(&d, &p); err != nil {
			return nil, err
		}
		out[d] = append(out[d], p)
	}
	return out, rows.Err()
}

// primaryOf is the document's primary assignee, or "".
func primaryOf(tx pgx.Tx, ctx context.Context, docID string) string {
	var p string
	if err := tx.QueryRow(ctx, `SELECT principal_id::text FROM document_assignees WHERE document_id = $1 AND is_primary`, docID).Scan(&p); err != nil {
		return ""
	}
	return p
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
