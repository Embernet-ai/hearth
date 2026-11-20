package timelog

// Bulk sign-off, and moving imported hours onto the people who worked them.
//
// The ClickUp importer records each task's total tracked time as one entry
// under whoever ran the import, because the export says how long a task took
// and not who spent it. On the real migration that put thousands of hours on
// one name. Reassign moves each entry to the task's assignee, the only person
// the data points at, and leaves it pending so an approver still signs it off.
// ApproveMany is what makes signing off that volume possible at all.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// ErrBatchRefused is returned when any entry in a bulk decision cannot be
// decided by this actor, or was decided by somebody else meanwhile. Nothing in
// the batch is written: half an approval is harder to reason about than none.
var ErrBatchRefused = errors.New(
	"some of those entries are not yours to decide, or were already decided; nothing was approved")

// ApproveMany approves every entry named, all or nothing.
//
// The ids are the ones the approver was looking at, not "everything pending for
// this person", so an entry that arrived after the page loaded is never signed
// off unseen. Same authority clause as Decide, in the same single UPDATE.
func (s *Service) ApproveMany(ctx context.Context, orgID, actorID string, entryIDs []string) (int, error) {
	ids, err := uniqueUUIDs(entryIDs)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("timelog: choose at least one entry")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("timelog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE time_entries t
		   SET approved = true, decided_at = now(), decided_by = $2, decided_note = ''
		  FROM principals p
		 WHERE t.id = ANY($3::uuid[])
		   AND t.org_id = $1
		   AND p.id = t.principal_id
		   AND t.ended_at IS NOT NULL
		   AND t.decided_at IS NULL
		   AND t.deleted_at IS NULL
		   AND t.principal_id <> $2
		   AND `+approverClause,
		orgID, actorID, ids)
	if err != nil {
		return 0, fmt.Errorf("timelog: approve many: %w", err)
	}
	if int(tag.RowsAffected()) != len(ids) {
		return 0, ErrBatchRefused
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTimeApproveBulk,
		EntityType: "time_entry", After: map[string]any{"approved": len(ids), "entries": ids},
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("timelog: commit: %w", err)
	}
	return len(ids), nil
}

// AutoApproveSince is when this person's time began approving itself, or nil
// while it does not. Entries whose work starts at or after it are approved as
// they are logged.
func (s *Service) AutoApproveSince(ctx context.Context, orgID, principalID string) (*time.Time, error) {
	var since *time.Time
	err := s.db.QueryRow(ctx, `SELECT time_auto_approve_since FROM principals WHERE id = $1 AND org_id = $2`,
		principalID, orgID).Scan(&since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timelog: auto approve since: %w", err)
	}
	return since, nil
}

// AuditApproveMany writes the audit row for a bulk approval a back end made in
// its own store, where the entries are not in this database to be updated.
func (s *Service) AuditApproveMany(ctx context.Context, orgID, actorID string, entryIDs []string) error {
	return audit.Record(ctx, s.db, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTimeApproveBulk,
		EntityType: "time_entry", After: map[string]any{"approved": len(entryIDs), "entries": entryIDs},
	})
}

// ErrAutoApproveOff is returned when entries are to be approved by a person's
// auto-approve setting and the setting is off.
var ErrAutoApproveOff = errors.New("timelog: this person's time does not approve itself; turn the setting on first")

// AutoApproveMany approves the named waiting entries of one person by their own
// auto-approve setting. See Store.
func (s *Service) AutoApproveMany(ctx context.Context, orgID, personID string, entryIDs []string, reason string) (int, error) {
	ids, err := uniqueUUIDs(entryIDs)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("timelog: choose at least one entry")
	}
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("timelog: say why")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("timelog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE time_entries t
		   SET approved = true, decided_at = now(), decided_by = p.id, decided_note = $3, auto_approved = true
		  FROM principals p
		 WHERE t.id = ANY($4::uuid[])
		   AND t.org_id = $1
		   AND p.id = $2 AND p.org_id = $1 AND p.time_auto_approve_since IS NOT NULL
		   AND t.principal_id = p.id
		   AND t.ended_at IS NOT NULL
		   AND t.decided_at IS NULL
		   AND t.deleted_at IS NULL`,
		orgID, personID, reason, ids)
	if err != nil {
		return 0, fmt.Errorf("timelog: auto approve many: %w", err)
	}
	if int(tag.RowsAffected()) != len(ids) {
		if since, serr := s.AutoApproveSince(ctx, orgID, personID); serr == nil && since == nil {
			return 0, ErrAutoApproveOff
		}
		return 0, ErrBatchRefused
	}
	if err := audit.Record(ctx, tx, autoApproveAudit(orgID, personID, ids, reason)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("timelog: commit: %w", err)
	}
	return len(ids), nil
}

func autoApproveAudit(orgID, personID string, ids []string, reason string) audit.Entry {
	return audit.Entry{
		OrgID: orgID, Action: audit.ActionTimeApproveBulk, EntityType: "time_entry",
		After: map[string]any{"approved": len(ids), "entries": ids, "person": personID,
			"decider": "auto-approve setting", "reason": reason},
	}
}

// AuditAutoApprove writes the audit row for a bulk auto-approval a back end made
// in its own store.
func (s *Service) AuditAutoApprove(ctx context.Context, orgID, personID string, entryIDs []string, reason string) error {
	return audit.Record(ctx, s.db, autoApproveAudit(orgID, personID, entryIDs, reason))
}

// ImportedEntry is one imported entry still waiting on a decision, beside
// everybody its task names.
type ImportedEntry struct {
	ID          string
	DocumentID  string
	DocTitle    string
	PrincipalID string
	PersonName  string
	StartedAt   time.Time
	Minutes     int
	// Assignees is everybody the task names. SuggestedID is set only when
	// that is exactly one person: with two there is no way to know the split,
	// and with none there is nobody to point at.
	Assignees     []string
	SuggestedID   string
	SuggestedName string
}

// Imported lists the undecided entries an import wrote, identified by the note
// the importer puts on every one of them. Billing only: this is about whose
// name hours sit under, which is what an invoice reads.
func (s *Service) Imported(ctx context.Context, orgID, actorID, note string) ([]ImportedEntry, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	// The same ORDER BY on both arrays keeps ids and names lined up; the id
	// breaks ties so two people with one display name cannot swap places.
	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, t.document_id::text, COALESCE(d.title, ''),
		       t.principal_id::text, COALESCE(NULLIF(p.display_name, ''), p.id::text),
		       t.started_at, t.minutes,
		       COALESCE(a.ids, '{}'), COALESCE(a.names, '{}')
		  FROM time_entries t
		  JOIN principals p ON p.id = t.principal_id
		  JOIN documents d ON d.id = t.document_id
		  LEFT JOIN LATERAL (
		       SELECT array_agg(ap.id::text ORDER BY ap.display_name, ap.id) AS ids,
		              array_agg(COALESCE(NULLIF(ap.display_name, ''), ap.id::text)
		                        ORDER BY ap.display_name, ap.id) AS names
		         FROM document_assignees da
		         JOIN principals ap ON ap.id = da.principal_id
		        WHERE da.document_id = t.document_id) a ON true
		 WHERE t.org_id = $1 AND t.note = $2
		   AND t.decided_at IS NULL AND t.minutes IS NOT NULL
		   AND t.deleted_at IS NULL
		 ORDER BY d.title, t.started_at`, orgID, note)
	if err != nil {
		return nil, fmt.Errorf("timelog: imported: %w", err)
	}
	defer rows.Close()

	var out []ImportedEntry
	for rows.Next() {
		var e ImportedEntry
		var ids []string
		if err := rows.Scan(&e.ID, &e.DocumentID, &e.DocTitle, &e.PrincipalID, &e.PersonName,
			&e.StartedAt, &e.Minutes, &ids, &e.Assignees); err != nil {
			return nil, fmt.Errorf("timelog: scan imported: %w", err)
		}
		if len(ids) == 1 {
			e.SuggestedID, e.SuggestedName = ids[0], e.Assignees[0]
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Move is one imported entry and who it belongs on.
type Move struct {
	EntryID     string
	PrincipalID string
}

// Reassign moves imported entries onto the people named, all in one
// transaction, and returns how many actually moved.
//
// Only entries the importer wrote and nobody has decided: a decided entry has
// been signed off under its current name, and hours somebody logged by hand are
// theirs. The entry stays pending, so whoever approves the new person's time
// still sees it. An entry already on the person named is simply not counted.
func (s *Service) Reassign(ctx context.Context, orgID, actorID, note string, moves []Move) (int, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return 0, err
	}
	for _, m := range moves {
		if _, err := uuid.Parse(m.EntryID); err != nil {
			return 0, errors.New("timelog: that is not an entry")
		}
		if _, err := uuid.Parse(m.PrincipalID); err != nil {
			return 0, errors.New("timelog: that is not a person")
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("timelog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type moved struct{ Entry, From, To string }
	var done []moved
	for _, m := range moves {
		var from string
		err := tx.QueryRow(ctx, `
			UPDATE time_entries t
			   SET principal_id = $3
			  FROM (SELECT id, principal_id AS was FROM time_entries WHERE id = $2) o
			 WHERE t.id = o.id
			   AND t.org_id = $1
			   AND t.note = $4
			   AND t.decided_at IS NULL
			   AND t.deleted_at IS NULL
			   AND t.principal_id <> $3
			   AND EXISTS (SELECT 1 FROM principals p
			                WHERE p.id = $3 AND p.org_id = $1 AND p.kind = 'user')
			RETURNING o.was::text`,
			orgID, m.EntryID, m.PrincipalID, note).Scan(&from)
		switch {
		case err == nil:
			done = append(done, moved{m.EntryID, from, m.PrincipalID})
		case errors.Is(err, pgx.ErrNoRows):
			// Already there, decided, not imported, or a person outside the
			// org: none of those is an error worth failing a batch over.
		default:
			return 0, fmt.Errorf("timelog: reassign: %w", err)
		}
	}
	if len(done) == 0 {
		return 0, nil
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTimeReassign,
		EntityType: "time_entry", After: map[string]any{"moved": len(done), "entries": done},
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("timelog: commit: %w", err)
	}
	return len(done), nil
}

func uniqueUUIDs(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		u, err := uuid.Parse(v)
		if err != nil {
			return nil, errors.New("timelog: that is not an entry")
		}
		if s := u.String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}
