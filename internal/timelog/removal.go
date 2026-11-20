package timelog

// Taking time entries out, and saying so.
//
// Every path that removes an entry writes an audit row in the same breath:
// Remove (an author or an admin of the task, one entry), RemoveMany (a super
// admin, many, with a reason), and DeleteImported (an importer clearing its own
// rows). A removal nobody can see in the audit log is the one somebody asks about
// a year later and nobody can answer.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
)

// ErrNeedsReason is an admin removal with no reason. The reason is what the
// audit row says, so it is never optional.
var ErrNeedsReason = errors.New("timelog: say why these entries are being removed")

// RemovedEntry is what an entry was when it went, kept on its audit row.
type RemovedEntry struct {
	Person    string
	Document  *string
	Minutes   *int
	StartedAt time.Time
	Note      string
}

// RemovalLine is one entry an admin removal looked at.
type RemovalLine struct {
	ID         string
	Person     string
	PersonName string
	Minutes    int
	StartedAt  time.Time
	Note       string
	// AlreadyRemoved entries are left alone and are not counted.
	AlreadyRemoved bool
	// Removed is true once apply wrote it.
	Removed bool
}

func removalEntry(orgID, actorID, entryID, via, reason string, b RemovedEntry) audit.Entry {
	before := map[string]any{"person": b.Person, "started_at": b.StartedAt.UTC().Format(time.RFC3339), "note": b.Note}
	if b.Minutes != nil {
		before["minutes"] = *b.Minutes
	}
	if b.Document != nil {
		before["document"] = *b.Document
	}
	after := map[string]any{"removed": true, "via": via}
	if reason != "" {
		after["reason"] = reason
	}
	return audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTimeRemove,
		EntityType: "time_entry", EntityID: entryID, Before: before, After: after,
	}
}

// AuditRemoval writes the row for a removal a back end made in its own store,
// where the entry is not in this database to be updated.
func (s *Service) AuditRemoval(ctx context.Context, orgID, actorID, entryID, via, reason string, before RemovedEntry) error {
	return audit.Record(ctx, s.db, removalEntry(orgID, actorID, entryID, via, reason, before))
}

// RemoveMany removes the named entries as one act of a super admin. With apply
// false nothing changes and the lines say what would. With apply true every
// entry still standing is removed and gets its own audit row naming the admin
// and the reason, all in one transaction: either every one goes or none does.
// An entry already removed, or not in this org, is reported and left alone.
func (s *Service) RemoveMany(ctx context.Context, orgID string, actor core.Subjects, ids []string, reason string, apply bool) ([]RemovalLine, error) {
	ok, err := s.authz.HasCapability(ctx, orgID, actor.Principal(), "super_admin")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotAllowed
	}
	reason = strings.TrimSpace(reason)
	if apply && reason == "" {
		return nil, ErrNeedsReason
	}
	seen := map[string]bool{}
	var clean []string
	for _, id := range ids {
		u, err := uuid.Parse(strings.TrimSpace(id))
		if err != nil {
			return nil, fmt.Errorf("timelog: %q is not an entry id", id)
		}
		if k := u.String(); !seen[k] {
			seen[k] = true
			clean = append(clean, k)
		}
	}
	if len(clean) == 0 {
		return nil, errors.New("timelog: no entries named")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("timelog: remove many: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var out []RemovalLine
	for _, id := range clean {
		var l RemovalLine
		var b RemovedEntry
		var deleted *time.Time
		var mins *int
		err := tx.QueryRow(ctx, `
			SELECT t.principal_id::text, COALESCE(p.display_name,''), t.document_id::text, t.minutes,
			       t.started_at, t.note, t.deleted_at
			  FROM time_entries t JOIN principals p ON p.id = t.principal_id
			 WHERE t.id=$1 AND t.org_id=$2 FOR UPDATE OF t`, id, orgID).
			Scan(&b.Person, &l.PersonName, &b.Document, &mins, &b.StartedAt, &b.Note, &deleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if err != nil {
			return nil, fmt.Errorf("timelog: remove many: %w", err)
		}
		b.Minutes = mins
		l.ID, l.Person, l.StartedAt, l.Note = id, b.Person, b.StartedAt, b.Note
		if mins != nil {
			l.Minutes = *mins
		}
		l.AlreadyRemoved = deleted != nil
		if apply && !l.AlreadyRemoved {
			if _, err := tx.Exec(ctx, `UPDATE time_entries SET deleted_at = now() WHERE id=$1 AND org_id=$2`, id, orgID); err != nil {
				return nil, fmt.Errorf("timelog: remove many: %w", err)
			}
			if err := audit.Record(ctx, tx, removalEntry(orgID, actor.Principal(), id, "admin remove", reason, b)); err != nil {
				return nil, err
			}
			l.Removed = true
		}
		out = append(out, l)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("timelog: remove many: %w", err)
	}
	return out, nil
}
