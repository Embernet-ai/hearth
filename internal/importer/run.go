package importer

// Running an import, and the property that makes it usable: it is safe to run
// twice.
//
// An import is never right the first time. Somebody picks the wrong kind, or
// exports the wrong board, or finds out afterwards that half the descriptions
// were in a column they did not map. If the only way to fix that is to start
// again with an empty database, they will not fix it, and the old tool stays
// running beside the new one forever.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// Store is the slice of the document service an import needs.
//
// Declared here by the consumer, like every other interface in this codebase,
// so the dependency points one way and the parsing above can be tested with no
// database at all.
type Store interface {
	Create(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error)
	Update(ctx context.Context, orgID string, actor core.Subjects, id string, p docs.UpdateParams) (*docs.Document, error)
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*docs.Document, error)
	FindByExternalID(ctx context.Context, orgID, source, externalID string) (string, error)
	RecordExternalRef(ctx context.Context, orgID string, ref core.Ref, e docs.ExternalRef) error
	Link(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) error
	// SetParent makes a subtask a real subtask, so a list can fold it under
	// its parent the way the source showed it.
	SetParent(ctx context.Context, orgID string, actor core.Subjects, id, parentID string) error
}

// Result is what happened, in the terms the person who pressed import cares
// about. Updated is not a consolation prize: it is the second run of the same
// file doing exactly the right thing.
type Result struct {
	Source   string
	Kind     string
	Created  int
	Updated  int
	Failed   int
	Total    int
	Problems []string
}

// Options controls how the import lands.
type Options struct {
	// Kind is what the rows become. Every one of these tools calls everything
	// a task, and the reason somebody is leaving is usually that their work
	// orders were pretending to be tasks.
	Kind string
	// SpaceID files the lot somewhere, so one board becomes one space rather
	// than four hundred loose documents.
	SpaceID *string
}

// Run imports an export.
//
// One row at a time rather than one transaction. A four thousand row import
// that fails on row 3999 and rolls everything back is worse than one that
// imports 3998 and names the two that were bad: the second can be fixed and
// re-run, and the first can only be attempted again and hoped over.
func Run(ctx context.Context, store Store, orgID string, actor core.Subjects,
	source string, tasks []Task, opt Options) (*Result, error) {

	kind := opt.Kind
	if kind == "" {
		kind = docs.KindTask
	}
	res := &Result{Source: source, Kind: kind, Total: len(tasks)}

	// Remembered so a parent relationship inside the export becomes a real
	// link, whichever order the rows arrive in.
	byExternal := make(map[string]string, len(tasks))

	for _, t := range tasks {
		id, err := upsert(ctx, store, orgID, actor, source, t, kind, opt, res)
		if err != nil {
			res.Failed++
			if len(res.Problems) < 20 {
				res.Problems = append(res.Problems,
					fmt.Sprintf("%s: %v", firstNonEmpty(t.Title, t.ExternalID), err))
			}
			continue
		}
		if t.ExternalID != "" {
			byExternal[t.ExternalID] = id
		}
	}

	for _, t := range tasks {
		if t.Parent == "" {
			continue
		}
		child, ok := byExternal[t.ExternalID]
		parent, ok2 := byExternal[t.Parent]
		if !ok || !ok2 {
			continue
		}
		_ = store.Link(ctx, orgID, actor,
			core.Ref{Type: core.EntityDocument, ID: parent},
			core.Ref{Type: core.EntityDocument, ID: child}, docs.LinkConcerns)
		_ = store.SetParent(ctx, orgID, actor, child, parent)
	}
	return res, nil
}

func upsert(ctx context.Context, store Store, orgID string, actor core.Subjects,
	source string, t Task, kind string, opt Options, res *Result) (string, error) {

	title := strings.TrimSpace(t.Title)
	if title == "" {
		title = "Untitled " + source + " row"
	}
	body := buildBody(source, t)

	// Only rows that carried an id can be matched on a re-run.
	var existing string
	if t.ExternalID != "" {
		var err error
		existing, err = store.FindByExternalID(ctx, orgID, source, t.ExternalID)
		if err != nil {
			return "", err
		}
		// And an id that is one of our own is that document.
		//
		// This is what makes re-importing an export idempotent inside the
		// instance it came from. The export writes each document's own id in
		// the id column, and external_refs has no entry for those because they
		// were never imported from anywhere: without this, exporting and
		// re-importing doubles everything, which the acceptance run found and
		// the integration test did not, because the test imported into an
		// empty org where creating was the right answer.
		//
		// It cannot collide with another tool's ids. Those are Jira keys and
		// Trello short links, and a document id that is not a document here
		// falls through to being created exactly as before.
		if existing == "" && looksLikeUUID(t.ExternalID) {
			if d, err := store.Get(ctx, orgID, actor, t.ExternalID); err == nil {
				existing = d.ID
			}
		}
	}

	if existing != "" {
		// The version has to come from the row as it is now, or the second run
		// of an import loses to its own optimistic concurrency check.
		current, err := store.Get(ctx, orgID, actor, existing)
		if err != nil {
			return "", err
		}
		priority := t.Priority
		status := t.Status
		if _, err := store.Update(ctx, orgID, actor, existing, docs.UpdateParams{
			Title: &title, Body: &body,
			Task: &docs.TaskPatch{
				Status: &status, Priority: &priority, DueAt: t.DueAt, KeepSubtasks: true,
			},
			ExpectedVersion: current.Version,
		}); err != nil {
			return "", err
		}
		res.Updated++
		return existing, nil
	}

	p := docs.CreateParams{
		Kind: kind, Title: title, Body: body,
		BodyFormat: docs.FormatPlain, SpaceID: opt.SpaceID,
	}
	if docs.HasTaskFields(kind) {
		p.Task = &docs.TaskFields{Status: t.Status, Priority: t.Priority, DueAt: t.DueAt}
	}
	d, err := store.Create(ctx, orgID, actor, p)
	if err != nil {
		return "", err
	}
	res.Created++

	if t.ExternalID != "" {
		if err := store.RecordExternalRef(ctx, orgID, d.Ref(), docs.ExternalRef{
			Source: source, ExternalID: t.ExternalID, URL: t.URL,
		}); err != nil {
			// The document exists and is usable; only the ability to recognise
			// it on a future import is lost. Worth reporting, not worth
			// failing the row over.
			return d.ID, fmt.Errorf("imported but not linked to its %s id: %w", source, err)
		}
	}
	return d.ID, nil
}

// buildBody keeps what has no column of its own rather than discarding it.
//
// Assignees are names from another system and cannot be resolved to principals
// here. Recording them as text is better than dropping them and far better
// than guessing by matching a display name, which puts somebody else's work on
// the wrong person's list.
func buildBody(source string, t Task) string {
	var b strings.Builder
	b.WriteString(t.Body)

	var notes []string
	if len(t.Assignees) > 0 {
		notes = append(notes, "Assigned in "+source+" to "+strings.Join(t.Assignees, ", "))
	}
	if t.List != "" {
		notes = append(notes, source+" list: "+t.List)
	}
	if t.URL != "" {
		notes = append(notes, "Original: "+t.URL)
	}
	for key, value := range t.Extra {
		notes = append(notes, key+": "+value)
	}
	if len(notes) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("---\n")
		for _, n := range notes {
			b.WriteString("\n- " + n)
		}
	}
	return b.String()
}

// looksLikeUUID is a shape check, not a parse. Get would error on anything
// else, and an error on every Jira key in a four thousand row import is four
// thousand pointless round trips.
func looksLikeUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "a row"
}
