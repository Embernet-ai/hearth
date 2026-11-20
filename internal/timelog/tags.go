package timelog

// Tags: "onsite", "remote", whatever billing needs to sort an hour by. Set at
// log time normally, but editable on an entry already logged -- tags did not
// exist when older entries were recorded, and the only way anything gets a
// tag retroactively is somebody opening that entry and adding one by hand.
// BatchTag exists so that "by hand" does not mean one click per entry.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Tag is a label an org defines for sorting time entries. Description is the
// glossary line -- when to use it, not just its name.
type Tag struct {
	ID          string
	Name        string
	Description string
}

// ErrTagExists means a tag by this name (case-insensitively) already exists
// in the org -- the caller should offer the existing one, not retry.
var ErrTagExists = errors.New("timelog: a tag with that name already exists")

// ListTags returns every tag defined for the org, alphabetically. Reading is
// not gated: anybody who can log time needs to see what is available to
// apply, the same as picking an assignee does not require the People
// capability. Only defining a new one does.
func (s *Service) ListTags(ctx context.Context, orgID string) ([]Tag, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id::text, name, description FROM time_entry_tags
		  WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("timelog: list tags: %w", err)
	}
	defer rows.Close()

	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Description); err != nil {
			return nil, fmt.Errorf("timelog: scan tag: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTag defines a new tag for the org. Gated on People: this is org
// taxonomy, the same shape of decision as who is on the roster, not a
// per-entry permission. (Open question, not settled: whether this is the
// right capability for it long-term -- reused here rather than adding a new
// one so the feature is testable today.)
func (s *Service) CreateTag(ctx context.Context, orgID, actorID, name, description string) (*Tag, error) {
	if err := s.requireTagAdmin(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("timelog: a tag needs a name")
	}

	t := &Tag{ID: uuid.NewString(), Name: name, Description: strings.TrimSpace(description)}
	_, err := s.db.Exec(ctx,
		`INSERT INTO time_entry_tags (id, org_id, name, description, created_by)
		 VALUES ($1, $2, $3, $4, $5)`,
		t.ID, orgID, t.Name, t.Description, actorID)
	if isUniqueViolation(err) {
		return nil, ErrTagExists
	}
	if err != nil {
		return nil, fmt.Errorf("timelog: create tag: %w", err)
	}
	return t, nil
}

// UpdateTag renames a tag or rewrites its glossary line. Same gate as
// CreateTag: whoever may define a tag may also fix one.
func (s *Service) UpdateTag(ctx context.Context, orgID, actorID, tagID, name, description string) error {
	if err := s.requireTagAdmin(ctx, orgID, actorID); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("timelog: a tag needs a name")
	}
	_, err := s.db.Exec(ctx,
		`UPDATE time_entry_tags SET name = $1, description = $2 WHERE id = $3 AND org_id = $4`,
		name, strings.TrimSpace(description), tagID, orgID)
	if isUniqueViolation(err) {
		return ErrTagExists
	}
	if err != nil {
		return fmt.Errorf("timelog: update tag: %w", err)
	}
	return nil
}

// DeleteTag removes a tag entirely -- every entry that carried it loses it,
// by the same cascade that makes untagging a single entry work. Same gate as
// creating one.
func (s *Service) DeleteTag(ctx context.Context, orgID, actorID, tagID string) error {
	if err := s.requireTagAdmin(ctx, orgID, actorID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx,
		`DELETE FROM time_entry_tags WHERE id = $1 AND org_id = $2`, tagID, orgID); err != nil {
		return fmt.Errorf("timelog: delete tag: %w", err)
	}
	return nil
}

// requireTagAdmin is CreateTag's gate, factored out for Update and Delete too.
func (s *Service) requireTagAdmin(ctx context.Context, orgID, actorID string) error {
	allowed, err := s.authz.HasCapability(ctx, orgID, actorID, capPeople)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotAllowed
	}
	return nil
}

// entryEditAllowed is entryAuthority (timelog.go) plus the one rule tagging
// adds on top: a removed entry cannot be tagged either, same reasoning as
// Correct -- restore it first, so a tag change is not silently reviving it.
func (s *Service) entryEditAllowed(ctx context.Context, orgID string, actor core.Subjects, entryID string) error {
	deletedAt, err := s.entryAuthority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if deletedAt != nil {
		return ErrDeleted
	}
	return nil
}

// EntryTags lists the tags on one entry, for rendering the edit form.
func (s *Service) EntryTags(ctx context.Context, orgID, entryID string) ([]Tag, error) {
	rows, err := s.db.Query(ctx, `
		SELECT tg.id::text, tg.name, tg.description
		  FROM time_entry_tag_links l
		  JOIN time_entry_tags tg ON tg.id = l.tag_id
		 WHERE l.entry_id = $1 AND tg.org_id = $2
		 ORDER BY tg.name`, entryID, orgID)
	if err != nil {
		return nil, fmt.Errorf("timelog: entry tags: %w", err)
	}
	defer rows.Close()

	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Description); err != nil {
			return nil, fmt.Errorf("timelog: scan entry tag: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TagEntry applies a tag to an entry. Idempotent by construction: the link
// table's primary key means applying a tag an entry already carries is a
// no-op, not a duplicate row or an error -- which is what makes BatchTag safe
// to run over a week that is only partly tagged already.
func (s *Service) TagEntry(ctx context.Context, orgID string, actor core.Subjects, entryID, tagID string) error {
	if err := s.entryEditAllowed(ctx, orgID, actor, entryID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO time_entry_tag_links (entry_id, tag_id)
		SELECT $1, tg.id FROM time_entry_tags tg WHERE tg.id = $2 AND tg.org_id = $3
		ON CONFLICT DO NOTHING`, entryID, tagID, orgID)
	if err != nil {
		return fmt.Errorf("timelog: tag entry: %w", err)
	}
	return nil
}

// UntagEntry removes a tag from an entry.
func (s *Service) UntagEntry(ctx context.Context, orgID string, actor core.Subjects, entryID, tagID string) error {
	if err := s.entryEditAllowed(ctx, orgID, actor, entryID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx,
		`DELETE FROM time_entry_tag_links WHERE entry_id = $1 AND tag_id = $2`, entryID, tagID)
	if err != nil {
		return fmt.Errorf("timelog: untag entry: %w", err)
	}
	return nil
}

// BatchTag applies one tag to every finished entry a person logged in a date
// range that does not already carry it. Self-service only -- the actor must
// be the person whose history is being tagged, the same boundary as My Time
// itself. This is not an admin bulk-edit tool; it exists so an employee is
// not clicking through a week of entries one at a time to backfill what
// tagging never captured before the feature existed.
//
// Returns how many entries actually changed, so the caller can say "12
// entries tagged" rather than a bare "done".
func (s *Service) BatchTag(ctx context.Context, orgID string, actor core.Subjects, principalID, tagID string, from, to time.Time) (int, error) {
	if actor.Principal() != principalID {
		return 0, ErrForbidden
	}
	if to.Before(from) {
		from, to = to, from
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO time_entry_tag_links (entry_id, tag_id)
		SELECT t.id, tg.id
		  FROM time_entries t, time_entry_tags tg
		 WHERE t.org_id = $1 AND t.principal_id = $2 AND t.minutes IS NOT NULL
		   AND t.deleted_at IS NULL
		   AND t.started_at >= $3 AND t.started_at < $4
		   AND tg.id = $5 AND tg.org_id = $1
		ON CONFLICT DO NOTHING`, orgID, principalID, from, to, tagID)
	if err != nil {
		return 0, fmt.Errorf("timelog: batch tag: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
