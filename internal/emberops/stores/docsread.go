package stores

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// How a document is read.
//
// The spine (Postgres) decides which documents a caller may see and carries
// the ids everything else hangs off. For the fields the addon stores,
// EmberOPS is the record: every page overlays them from EmberOPS, in one call
// per doctype however many documents there are. When EmberOPS's `modified`
// stamp differs from the one Hearth last wrote or read, the record was edited
// in EmberOPS, and the change is copied into the spine (a version, a search
// event) so Hearth's history and index follow. That copy is bounded per read
// and batched: one query for the marks, one fetch for the changed bodies, and
// at most maxSyncPerRead documents written.
//
// The space and the parent of a document stay the spine's: Hearth's own access
// control is computed from them, so a move made in EmberOPS is not followed.
const maxSyncPerRead = 25

// fieldNames is where each Hearth field lives on a doctype.
type fieldNames struct {
	title, body, status, priority, due, started, completed string
	statuses                                               map[string]string // EmberOPS status -> Hearth
	priorities                                             []string
	tasky                                                  bool
}

func specFor(doctype string) fieldNames {
	switch doctype {
	case "Task":
		return fieldNames{title: "subject", body: "hearth_body", status: "status", priority: "priority",
			due: "hearth_due_at", started: "hearth_started_at", completed: "hearth_completed_at",
			statuses: mapping.Invert(mapping.TaskStatus), priorities: mapping.TaskPriority, tasky: true}
	case "Maintenance Work Order":
		return fieldNames{title: "title", body: "hearth_body", status: "status", priority: "priority",
			due: "planned_end", started: "actual_start", completed: "actual_end",
			statuses: mapping.Invert(mapping.WOStatus), priorities: mapping.WOPriority, tasky: true}
	case "Asset":
		return fieldNames{title: "asset_name", body: "hearth_body"}
	case "Purchase Order":
		return fieldNames{title: "hearth_title", body: "hearth_body"}
	default: // Hearth Document
		return fieldNames{title: "title", body: "body"}
	}
}

func (f fieldNames) light() []string {
	out := []string{"name", "hearth_id", "modified", "modified_by", f.title}
	if f.tasky {
		out = append(out, f.status, f.priority, f.due, f.started, f.completed, "hearth_primary_assignee", "_assign")
	}
	return out
}

// Children, Linked, LinkedTo and SearchWatched return documents too.
func (s *Docs) Children(ctx context.Context, orgID string, actor core.Subjects, parentID string) ([]*docs.Document, error) {
	ds, err := s.Store.Children(ctx, orgID, actor, parentID)
	if err != nil {
		return nil, err
	}
	return ds, s.overlay(ctx, orgID, ds, false)
}

func (s *Docs) Linked(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]*docs.Document, error) {
	ds, err := s.Store.Linked(ctx, orgID, actor, docID)
	if err != nil {
		return nil, err
	}
	return ds, s.overlay(ctx, orgID, ds, false)
}

func (s *Docs) LinkedTo(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref) ([]*docs.Document, error) {
	ds, err := s.Store.LinkedTo(ctx, orgID, actor, ref)
	if err != nil {
		return nil, err
	}
	return ds, s.overlay(ctx, orgID, ds, false)
}

func (s *Docs) SearchWatched(ctx context.Context, orgID string, actor core.Subjects, principalID string, q docs.Query) ([]*docs.Row, error) {
	rows, err := s.Store.SearchWatched(ctx, orgID, actor, principalID, q)
	if err != nil {
		return nil, err
	}
	ds := make([]*docs.Document, 0, len(rows))
	for _, r := range rows {
		ds = append(ds, r.Document)
	}
	return rows, s.overlay(ctx, orgID, ds, false)
}

// overlay lays EmberOPS's fields over the spine rows and copies EmberOPS-side
// edits into the spine. withBody also reads and shows the body, which a list
// does not carry.
func (s *Docs) overlay(ctx context.Context, orgID string, ds []*docs.Document, withBody bool) error {
	byType := map[string][]any{}
	index := map[string]*docs.Document{}
	for _, d := range ds {
		if d == nil {
			continue
		}
		if dt := doctypeFor(d.Kind); dt != "" {
			byType[dt] = append(byType[dt], d.ID)
			index[strings.ToLower(d.ID)] = d
		}
	}
	if len(index) == 0 {
		return nil
	}
	people, err := s.Dir.People(ctx)
	if err != nil {
		return err
	}
	byEmail := make(map[string]string, len(people))
	for _, p := range people {
		byEmail[p.Email] = p.ID
	}

	type seen struct {
		d    *docs.Document
		row  emberops.Row
		spec fieldNames
	}
	var all []seen
	for dt, ids := range byType {
		spec := specFor(dt)
		fields := spec.light()
		if withBody {
			fields = append(fields, spec.body)
		}
		for len(ids) > 0 {
			n := min(len(ids), 200)
			rows, err := s.C.List(ctx, emberops.ListQuery{
				Doctype: dt, Fields: fields, Limit: 500,
				Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids[:n]}},
			})
			if err != nil {
				return err
			}
			ids = ids[n:]
			for _, r := range rows {
				if d := index[strings.ToLower(r.S("hearth_id"))]; d != nil {
					all = append(all, seen{d, r, spec})
				}
			}
		}
	}

	// Which of them were edited in EmberOPS? One query for every mark.
	spineIDs := make([]string, 0, len(all))
	for _, x := range all {
		spineIDs = append(spineIDs, x.d.ID)
	}
	marks, err := s.Store.ExternalMarks(ctx, spineIDs)
	if err != nil {
		return err
	}
	var baseline = map[string]string{}
	var changed []seen
	for _, x := range all {
		m, ok := marks[x.d.ID]
		switch {
		case !ok:
			// Never compared (the migration wrote it from Hearth's own data):
			// take EmberOPS's stamp as the baseline rather than guessing.
			baseline[x.d.ID] = x.row.S("modified")
		case m != x.row.S("modified"):
			changed = append(changed, x)
		}
	}
	if err := s.Store.SetExternalMarks(ctx, baseline); err != nil {
		return err
	}
	// Keep the work per read bounded; the rest are copied on a later read.
	if len(changed) > maxSyncPerRead {
		changed = changed[:maxSyncPerRead]
	}

	// A list does not carry bodies; the ones that changed need theirs.
	bodies := map[string]string{}
	if !withBody && len(changed) > 0 {
		perType := map[string][]any{}
		for _, x := range changed {
			perType[doctypeFor(x.d.Kind)] = append(perType[doctypeFor(x.d.Kind)], x.d.ID)
		}
		for dt, ids := range perType {
			spec := specFor(dt)
			rows, err := s.C.List(ctx, emberops.ListQuery{
				Doctype: dt, Fields: []string{"hearth_id", spec.body}, Limit: 500,
				Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids}},
			})
			if err != nil {
				return err
			}
			for _, r := range rows {
				bodies[strings.ToLower(r.S("hearth_id"))] = r.S(spec.body)
			}
		}
	}
	isChanged := map[string]bool{}
	for _, x := range changed {
		isChanged[x.d.ID] = true
	}

	for _, x := range all {
		d, r, spec := x.d, x.row, x.spec
		if t := r.S(spec.title); t != "" {
			d.Title = t
		}
		if withBody {
			d.Body = r.S(spec.body)
		}
		var ext *docs.ExternalTask
		var assignees []string
		if spec.tasky && d.Task != nil {
			if st := spec.statuses[r.S(spec.status)]; st != "" {
				d.Task.Status = st
			}
			if r.Has(spec.priority) {
				d.Task.Priority = int16(mapping.PriorityNumber(r.S(spec.priority), spec.priorities))
			}
			d.Task.DueAt = r.TP(spec.due, s.loc())
			d.Task.StartedAt = r.TP(spec.started, s.loc())
			d.Task.CompletedAt = r.TP(spec.completed, s.loc())
			if a := r.S("hearth_primary_assignee"); a != "" {
				if id := byEmail[a]; id != "" {
					d.Task.AssigneeID = &id
				}
			}
			for _, e := range assignList(r.S("_assign")) {
				if id := byEmail[e]; id != "" {
					assignees = append(assignees, id)
				}
			}
			cp := *d.Task
			ext = &docs.ExternalTask{Status: cp.Status, Priority: cp.Priority, AssigneeID: cp.AssigneeID,
				DueAt: cp.DueAt, StartedAt: cp.StartedAt, CompletedAt: cp.CompletedAt}
		}
		if !isChanged[d.ID] {
			continue
		}
		body := d.Body
		if !withBody {
			body = bodies[strings.ToLower(d.ID)]
		}
		c := docs.ExternalChange{Title: d.Title, Body: body, Task: ext, Modified: r.S("modified")}
		if spec.tasky {
			c.AssigneeIDs = assignees
			if c.AssigneeIDs == nil {
				c.AssigneeIDs = []string{}
			}
		}
		if by := byEmail[r.S("modified_by")]; by != "" {
			c.By = by
		}
		cctx, closed := docs.WithClosedSinkShared(ctx)
		if err := s.Store.ApplyExternal(cctx, orgID, d.ID, c); err != nil {
			// The page still shows EmberOPS's state; the copy is retried on
			// the next read.
			s.log().Warn("emberops: could not copy an EmberOps edit into the spine", "id", d.ID, "error", err)
		} else if err := s.pushClosed(ctx, orgID, closed.IDs); err != nil {
			// A task closed in EmberOPS closed its subtasks in the spine; the
			// same close goes to their EmberOPS records.
			s.log().Warn("emberops: could not send the subtasks closed with a task to EmberOps", "id", d.ID, "error", err)
		}
	}
	return nil
}

// assignList reads Frappe's _assign column, a JSON list of emails.
func assignList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
