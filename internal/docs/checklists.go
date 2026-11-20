package docs

// Checklists.
//
// A checklist item is deliberately NOT a document. It has no assignee, no due
// date, no lifecycle, and no reason to carry a grant, a version history, and a
// search entry. Making "verify dead" a row in the spine would mean every list
// of work orders had to filter it out, forever.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
)

// ChecklistItem is one line somebody ticks.
type ChecklistItem struct {
	ID          string
	Position    int
	Text        string
	Description string
	DoneAt      *time.Time
	DoneBy      *string
	DoneName    string
}

// Done reports whether it has been ticked, for a template that cannot
// dereference a pointer.
func (c ChecklistItem) Done() bool { return c.DoneAt != nil }

// positionStep is the gap left between items, so one can be dropped between
// two others without rewriting the list.
const positionStep = 1000

// Checklist returns a document's items, if the caller may view it.
func (s *Service) Checklist(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]ChecklistItem, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT c.id::text, c.position, c.text, COALESCE(c.description, ''), c.done_at, c.done_by::text,
		       COALESCE(NULLIF(p.display_name,''), '')
		FROM checklist_items c
		LEFT JOIN principals p ON p.id = c.done_by
		WHERE c.document_id = $1
		ORDER BY c.position, c.created_at`, docID)
	if err != nil {
		return nil, fmt.Errorf("docs: checklist: %w", err)
	}
	defer rows.Close()

	var out []ChecklistItem
	for rows.Next() {
		var c ChecklistItem
		if err := rows.Scan(&c.ID, &c.Position, &c.Text, &c.Description, &c.DoneAt, &c.DoneBy, &c.DoneName); err != nil {
			return nil, fmt.Errorf("docs: scan checklist item: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddChecklistItem appends a line. description is optional -- the room for
// "use the staging account, not prod" that text alone does not have.
//
// Comment access is enough. Ticking off a step on a procedure is doing the
// work rather than changing what the work is, and a technician who may not
// rewrite a runbook still has to be able to record that they followed it.
func (s *Service) AddChecklistItem(ctx context.Context, orgID string, actor core.Subjects, docID, text, description string) (*ChecklistItem, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("docs: a checklist item needs some text")
	}
	description = strings.TrimSpace(description)
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
		return nil, err
	}

	item := &ChecklistItem{ID: uuid.NewString(), Text: text, Description: description}
	err := s.db.QueryRow(ctx, `
		INSERT INTO checklist_items (id, document_id, position, text, description)
		VALUES ($1, $2,
		        COALESCE((SELECT max(position) FROM checklist_items WHERE document_id = $2), 0) + $5,
		        $3, NULLIF($4, ''))
		RETURNING position`, item.ID, docID, text, description, positionStep).Scan(&item.Position)
	if err != nil {
		return nil, fmt.Errorf("docs: add checklist item: %w", err)
	}
	return item, nil
}

// SetChecklistItemDone ticks or unticks a line.
//
// done_by moves with done_at, always, because a checklist with times and no
// names is a record that something happened and nobody did it.
func (s *Service) SetChecklistItemDone(ctx context.Context, orgID string, actor core.Subjects, itemID string, done bool) error {
	docID, err := s.checklistParent(ctx, orgID, itemID)
	if err != nil {
		return err
	}
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
		return err
	}

	if done {
		_, err = s.db.Exec(ctx,
			`UPDATE checklist_items SET done_at = now(), done_by = $2 WHERE id = $1`,
			itemID, actor.Principal())
	} else {
		_, err = s.db.Exec(ctx,
			`UPDATE checklist_items SET done_at = NULL, done_by = NULL WHERE id = $1`, itemID)
	}
	if err != nil {
		return fmt.Errorf("docs: set checklist item: %w", err)
	}
	return nil
}

// RemoveChecklistItem deletes a line. Editing the procedure, so it needs edit.
func (s *Service) RemoveChecklistItem(ctx context.Context, orgID string, actor core.Subjects, itemID string) error {
	docID, err := s.checklistParent(ctx, orgID, itemID)
	if err != nil {
		return err
	}
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessEdit); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM checklist_items WHERE id = $1`, itemID); err != nil {
		return fmt.Errorf("docs: remove checklist item: %w", err)
	}
	return nil
}

// checklistParent resolves which document an item belongs to, which is where
// the permission question actually lives.
func (s *Service) checklistParent(ctx context.Context, orgID, itemID string) (string, error) {
	var docID string
	err := s.db.QueryRow(ctx, `
		SELECT c.document_id::text FROM checklist_items c
		JOIN documents d ON d.id = c.document_id
		WHERE c.id = $1 AND d.org_id = $2 AND d.deleted_at IS NULL`, itemID, orgID).Scan(&docID)
	if err != nil {
		return "", ErrNotFound
	}
	return docID, nil
}

// ChecklistProgress is how much of a procedure has been done, for a badge.
type ChecklistProgress struct {
	Done  int
	Total int
}

// Complete reports whether every step has been ticked.
func (p ChecklistProgress) Complete() bool { return p.Total > 0 && p.Done == p.Total }

// Progress counts a document's checklist.
func Progress(items []ChecklistItem) ChecklistProgress {
	p := ChecklistProgress{Total: len(items)}
	for _, i := range items {
		if i.Done() {
			p.Done++
		}
	}
	return p
}
