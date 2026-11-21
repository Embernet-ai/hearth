package stores

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// Docs serves documents with EmberOPS holding the operational record.
//
// A document is not only its fields. Hearth's files, comments, links, grants,
// versions, time entries and search index all hang off the document id, and
// they stay in Hearth's database. So the document keeps a spine row in
// Postgres (the embedded docs.Store, which is the Postgres *docs.Service, unchanged), and the operational record
// is projected into EmberOPS beside it: every write goes to Postgres first,
// which keeps Hearth's own rules (access, versions, events, search) exactly as
// they are, and then to EmberOPS as the signed-in person, which applies
// EmberOPS's permissions and validation. Reads that show a task or work order
// take its status, priority, due date, assignees and title from EmberOPS, in
// one batched call per page, so an edit made in EmberOPS is what Hearth shows.
//
// What does not flow back: an edit made in EmberOPS is displayed by Hearth but
// is not copied into the spine, so Hearth's search index and version history
// see it only when Hearth next saves the document. See docs/emberops-backend.md.
type Docs struct {
	docs.Store
	Base
	Dir Directory
	Log *slog.Logger
}

var _ docs.Store = (*Docs)(nil)

// doctypeFor is where a kind of document lives in EmberOPS. The empty string
// means the kind has no operational record there (it stays a spine row only).
func doctypeFor(kind string) string {
	switch kind {
	case docs.KindTask:
		return "Task"
	case docs.KindWorkOrder:
		return "Maintenance Work Order"
	case docs.KindAsset:
		return "Asset"
	case docs.KindPurchaseOrder:
		return "Purchase Order"
	case docs.KindDoc, docs.KindWiki, docs.KindNote, docs.KindTemplate, docs.KindDailyReport:
		return "Hearth Document"
	}
	return ""
}

func (s *Docs) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// record builds the mapping row for a document from its spine and the
// operational pieces Hearth holds in Postgres.
func (s *Docs) record(ctx context.Context, orgID string, actor *core.Subjects, d *docs.Document) (mapping.Rec, error) {
	rec := mapping.Rec{
		"id": d.ID, "kind": d.Kind, "title": d.Title, "body": d.Body, "body_format": d.BodyFormat,
		"space_id": deref(d.SpaceID), "parent_id": deref(d.ParentID),
	}
	if d.Task != nil {
		rec["status"] = d.Task.Status
		rec["priority"] = float64(d.Task.Priority)
		rec["due_at"] = timeOrNil(d.Task.DueAt)
		rec["started_at"] = timeOrNil(d.Task.StartedAt)
		rec["completed_at"] = timeOrNil(d.Task.CompletedAt)
	}
	ids, err := s.Store.Assignees(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	emails, err := s.Dir.Emails(ctx)
	if err != nil {
		return nil, err
	}
	if vals, err := s.Store.PropertyValuesFor(ctx, orgID, []string{d.ID}); err == nil && len(vals[d.ID]) > 0 {
		props := make(map[string]any, len(vals[d.ID]))
		for k, v := range vals[d.ID] {
			props[k] = v
		}
		rec["properties"] = props
	}
	if len(ids) > 0 {
		var list []any
		for _, id := range ids {
			if e := emails[id]; e != "" {
				list = append(list, e)
			}
		}
		rec["assignee_emails"] = list
		if len(list) > 0 {
			rec["primary_assignee_email"] = list[0]
		}
	}
	if actor != nil {
		items, err := s.Store.Checklist(ctx, orgID, *actor, d.ID)
		if err == nil {
			var cl []any
			for _, it := range items {
				m := map[string]any{"id": it.ID, "text": it.Text, "description": it.Description, "position": float64(it.Position)}
				if it.DoneAt != nil {
					m["done_at"] = it.DoneAt.UTC().Format(time.RFC3339)
					if it.DoneBy != nil {
						if e := emails[*it.DoneBy]; e != "" {
							m["done_by_email"] = e
						}
					}
				}
				cl = append(cl, m)
			}
			rec["checklist"] = cl
		}
	}
	return rec, nil
}

func timeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// payload is the upsert body for a document.
func (s *Docs) payload(d *docs.Document, rec mapping.Rec) (string, mapping.Doc) {
	z := mapping.TZ{Loc: s.loc()}
	var doc mapping.Doc
	switch d.Kind {
	case docs.KindTask:
		doc = z.TaskDoc(rec)
	case docs.KindWorkOrder:
		doc = z.WorkOrderDoc(rec)
	case docs.KindAsset:
		doc = z.AssetDoc(rec)
	case docs.KindPurchaseOrder:
		doc = z.PurchaseOrderDoc(rec)
	default:
		doc = z.DocumentDoc(rec)
	}
	dt := doctypeFor(d.Kind)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	return dt, doc
}

// push projects a document into EmberOPS.
func (s *Docs) push(ctx context.Context, orgID string, actor *core.Subjects, d *docs.Document) error {
	dt := doctypeFor(d.Kind)
	if dt == "" {
		return nil
	}
	rec, err := s.record(ctx, orgID, actor, d)
	if err != nil {
		return err
	}
	dt, doc := s.payload(d, rec)
	res, err := s.C.Upsert(ctx, dt, d.ID, doc)
	if err != nil {
		return fmt.Errorf("emberops: save %s %s: %w", dt, d.ID, err)
	}
	// What Hearth just wrote is the baseline: a later `modified` that differs
	// from this is an edit made in EmberOPS.
	if res.Modified != "" {
		if err := s.Store.SetExternalMarks(ctx, map[string]string{d.ID: res.Modified}); err != nil {
			s.log().Warn("emberops: could not record the sync mark", "id", d.ID, "error", err)
		}
	}
	return nil
}

// pushByID is push for a write that returns no document.
func (s *Docs) pushByID(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	d, err := s.Store.Get(ctx, orgID, actor, id)
	if err != nil {
		return err
	}
	return s.push(ctx, orgID, &actor, d)
}

// Create writes the spine, then the EmberOPS record. If EmberOPS refuses, the
// spine row is removed again so the two do not disagree about whether it exists.
func (s *Docs) Create(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error) {
	d, err := s.Store.Create(ctx, orgID, actor, p)
	if err != nil {
		return nil, err
	}
	if err := s.push(ctx, orgID, &actor, d); err != nil {
		if derr := s.Store.Delete(ctx, orgID, actor, d.ID); derr != nil {
			s.log().Error("emberops: could not undo a document EmberOps refused", "id", d.ID, "error", derr)
		}
		return nil, err
	}
	return d, nil
}

// CreateAsAgent is Create for an agent.
func (s *Docs) CreateAsAgent(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error) {
	return s.Create(ctx, orgID, actor, p)
}

// CreateAsSystem is Create for a rule; it has no person, so EmberOPS is called
// as the service client and the checklist (which needs a person) is left out.
func (s *Docs) CreateAsSystem(ctx context.Context, orgID string, actor docs.RuleActor, p docs.CreateParams, linkTo core.Ref) (*docs.Document, error) {
	d, err := s.Store.CreateAsSystem(ctx, orgID, actor, p, linkTo)
	if err != nil {
		return nil, err
	}
	return d, s.push(ctx, orgID, nil, d)
}

// CreateAsRule is CreateAsSystem without the document.
func (s *Docs) CreateAsRule(ctx context.Context, orgID string, actor docs.RuleActor, p docs.CreateParams, linkTo core.Ref) error {
	_, err := s.CreateAsSystem(ctx, orgID, actor, p, linkTo)
	return err
}

// Update saves the spine, then the EmberOPS record, and the subtasks a close
// took with it.
func (s *Docs) Update(ctx context.Context, orgID string, actor core.Subjects, id string, p docs.UpdateParams) (*docs.Document, error) {
	ctx, closed := docs.WithClosedSinkShared(ctx)
	d, err := s.Store.Update(ctx, orgID, actor, id, p)
	if err != nil {
		return nil, err
	}
	if err := s.push(ctx, orgID, &actor, d); err != nil {
		return d, err
	}
	return d, s.pushClosed(ctx, orgID, closed.IDs)
}

// pushClosed copies the subtasks a close took with its parent into EmberOPS,
// as the service client: the person who closed the parent may not see each of
// them, and the spine already did the change.
func (s *Docs) pushClosed(ctx context.Context, orgID string, ids []string) error {
	for _, id := range ids {
		d, err := s.Store.GetAsSystem(ctx, orgID, id)
		if err != nil {
			return err
		}
		if err := s.push(ctx, orgID, nil, d); err != nil {
			return err
		}
	}
	return nil
}

// Restore puts back an earlier version.
func (s *Docs) Restore(ctx context.Context, orgID string, actor core.Subjects, docID string, version int64) (*docs.Document, error) {
	d, err := s.Store.Restore(ctx, orgID, actor, docID, version)
	if err != nil {
		return nil, err
	}
	return d, s.push(ctx, orgID, &actor, d)
}

// Instantiate makes a document from a template.
func (s *Docs) Instantiate(ctx context.Context, orgID string, actor core.Subjects, p docs.InstantiateParams) (*docs.Document, error) {
	d, err := s.Store.Instantiate(ctx, orgID, actor, p)
	if err != nil {
		return nil, err
	}
	return d, s.push(ctx, orgID, &actor, d)
}

// Delete soft-deletes in both places.
func (s *Docs) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	d, err := s.Store.Get(ctx, orgID, actor, id)
	if err != nil {
		return err
	}
	if err := s.Store.Delete(ctx, orgID, actor, id); err != nil {
		return err
	}
	if dt := doctypeFor(d.Kind); dt != "" {
		// A task or a document is taken out of EmberOPS with it. Soft deleted
		// there it still showed in EmberOPS's own task list and kept a link to
		// its space, which stopped the space ever being removed. If EmberOPS
		// will not delete it (a subtask under it, a timesheet on it), it is
		// marked deleted instead, as every other kind is.
		if dt == "Task" || dt == "Hearth Document" {
			// As the service account: Hearth has already decided this person may delete it, and
			// EmberOPS lets a person edit a document or a task but not always delete one.
			err := s.C.DeleteByHearthID(emberops.WithUser(ctx, nil), dt, id)
			if err == nil || errors.Is(err, emberops.ErrNotFound) {
				return nil
			}
			s.log().Warn("emberops: could not delete, marking deleted instead", "doctype", dt, "id", id, "error", err)
		}
		now := time.Now().UTC()
		field := "hearth_deleted_at"
		if dt == "Hearth Document" {
			field = "deleted_at"
		}
		if _, err := s.C.Upsert(ctx, dt, id, map[string]any{field: s.ts(&now)}); err != nil {
			return fmt.Errorf("emberops: delete %s %s: %w", dt, id, err)
		}
	}
	return nil
}

// SetField changes one field from a board or table.
func (s *Docs) SetField(ctx context.Context, orgID string, actor core.Subjects, docID string, field docs.Field, value string) error {
	ctx, closed := docs.WithClosedSinkShared(ctx)
	if err := s.Store.SetField(ctx, orgID, actor, docID, field, value); err != nil {
		return err
	}
	if err := s.pushByID(ctx, orgID, actor, docID); err != nil {
		return err
	}
	return s.pushClosed(ctx, orgID, closed.IDs)
}

// Assign sets a task's assignees.
func (s *Docs) Assign(ctx context.Context, orgID string, actor core.Subjects, docID string, assigneeIDs []string, primary string) error {
	if err := s.Store.Assign(ctx, orgID, actor, docID, assigneeIDs, primary); err != nil {
		return err
	}
	return s.pushByID(ctx, orgID, actor, docID)
}

// SetParent files a task under another.
func (s *Docs) SetParent(ctx context.Context, orgID string, actor core.Subjects, id, parentID string) error {
	if err := s.Store.SetParent(ctx, orgID, actor, id, parentID); err != nil {
		return err
	}
	return s.pushByID(ctx, orgID, actor, id)
}

// AddChecklistItem adds an item and sends the checklist.
func (s *Docs) AddChecklistItem(ctx context.Context, orgID string, actor core.Subjects, docID, text, description string) (*docs.ChecklistItem, error) {
	it, err := s.Store.AddChecklistItem(ctx, orgID, actor, docID, text, description)
	if err != nil {
		return nil, err
	}
	return it, s.pushByID(ctx, orgID, actor, docID)
}

// SetChecklistItemDone ticks or unticks an item and sends the checklist at
// once, so EmberOPS shows the tick as soon as the person makes it.
func (s *Docs) SetChecklistItemDone(ctx context.Context, orgID string, actor core.Subjects, itemID string, done bool) error {
	docID, err := s.Store.ChecklistItemDoc(ctx, orgID, itemID)
	if err != nil {
		return err
	}
	if err := s.Store.SetChecklistItemDone(ctx, orgID, actor, itemID, done); err != nil {
		return err
	}
	return s.pushByID(ctx, orgID, actor, docID)
}

// RemoveChecklistItem removes an item and sends the checklist at once.
func (s *Docs) RemoveChecklistItem(ctx context.Context, orgID string, actor core.Subjects, itemID string) error {
	docID, err := s.Store.ChecklistItemDoc(ctx, orgID, itemID)
	if err != nil {
		return err
	}
	if err := s.Store.RemoveChecklistItem(ctx, orgID, actor, itemID); err != nil {
		return err
	}
	return s.pushByID(ctx, orgID, actor, docID)
}

// ---- reads ----

// Get returns the spine with its operational fields from EmberOPS.
func (s *Docs) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*docs.Document, error) {
	d, err := s.Store.Get(ctx, orgID, actor, id)
	if err != nil {
		return nil, err
	}
	if err := s.overlay(ctx, orgID, []*docs.Document{d}, true); err != nil {
		return nil, err
	}
	return d, nil
}

// Search runs the query against the spine, which decides what the caller may
// see, and lays EmberOPS's fields over the rows, two calls for the page.
func (s *Docs) Search(ctx context.Context, orgID string, actor core.Subjects, q docs.Query) ([]*docs.Row, error) {
	rows, err := s.Store.Search(ctx, orgID, actor, q)
	if err != nil {
		return nil, err
	}
	ds := make([]*docs.Document, 0, len(rows))
	for _, r := range rows {
		ds = append(ds, r.Document)
	}
	return rows, s.overlay(ctx, orgID, ds, false)
}

// List is Search by another name.
func (s *Docs) List(ctx context.Context, orgID string, actor core.Subjects, p docs.ListParams) ([]*docs.Document, error) {
	ds, err := s.Store.List(ctx, orgID, actor, p)
	if err != nil {
		return nil, err
	}
	return ds, s.overlay(ctx, orgID, ds, false)
}
