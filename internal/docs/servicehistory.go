package docs

// A department's service history: its work orders and tasks, laid out by the
// day things happened, so somebody can hand a customer what was done and when.
//
// Nothing here is new data. It reads what the records already carry: when each
// was opened and finished, every comment with its time, and the hours logged.
// A customer is a folder in the space tree (White Team Service / <customer> /
// <job>), so "this customer's history" is "everything under that space".

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// ServiceFilter narrows a history. SpaceID is required: a history is always of
// somewhere. From and To are inclusive office days, YYYY-MM-DD, and optional.
type ServiceFilter struct {
	SpaceID  string
	From, To string
}

// ServiceEvent is one thing that happened on a record on one day.
type ServiceEvent struct {
	Day  string
	At   time.Time
	Kind string // opened, comment, time, time_total, completed
	Who  string
	Text string
	// Minutes is set on a time event.
	Minutes int
}

// ServiceRecord is one work order or task and what happened on it in range.
type ServiceRecord struct {
	ID        string
	Kind      string
	Title     string
	Body      string
	Status    string
	SpacePath string
	// Job is the last folder of SpacePath, the job list it sat in.
	Job string
	// Assignees are the people it was assigned to. For work brought in from
	// ClickUp these are the names ClickUp recorded, including people with no
	// account here.
	Assignees []string
	Opened    time.Time
	Due       *time.Time
	Completed *time.Time
	Events    []ServiceEvent

	// Customer and EndCustomer are set by whoever lays the history out (the
	// page names the folder it was asked for; hearthctl also splits a reseller's
	// jobs by the end customer each job is for). They are labels for the CSV.
	Customer    string
	EndCustomer string

	assignedHere    []string
	importedMinutes int
}

// maxServiceRecords bounds one history.
const maxServiceRecords = 2000

// importedTimeNote starts the note the ClickUp import put on its one total
// per task (internal/importer/clickup, setEffort).
const importedTimeNote = "Imported from ClickUp"

var (
	// The importer appends a block of provenance notes to a body. It is useful
	// in the app and noise in a customer's report, so it is split off here.
	reNotes    = regexp.MustCompile(`(?s)\n*---\n+(- (?:Assigned in ClickUp to |ClickUp list: |Original: ).*)$`)
	reAssigned = regexp.MustCompile(`Assigned in ClickUp to ([^\n]*)`)
)

// splitNotes returns the body without the import notes, and the assignee names
// ClickUp recorded.
func splitNotes(body string) (string, []string) {
	var names []string
	if m := reAssigned.FindStringSubmatch(body); m != nil {
		for _, n := range strings.Split(m[1], ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
	}
	if loc := reNotes.FindStringIndex(body); loc != nil {
		body = body[:loc[0]]
	}
	return strings.TrimSpace(body), names
}

// ServiceHistory returns the work orders and tasks under a space that the
// caller may read and that had something happen in the range, each with its
// events in range, oldest record first.
func (s *Service) ServiceHistory(ctx context.Context, orgID string, actor core.Subjects, f ServiceFilter) ([]*ServiceRecord, error) {
	if f.SpaceID == "" {
		return nil, fmt.Errorf("%w: a service history is of a department", ErrField)
	}
	args := []any{orgID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	vis := actor.GrantsExists(core.EntityDocument, "d.id", arg)
	spaceArg := arg(f.SpaceID)

	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id, 0 AS depth FROM spaces WHERE id = `+spaceArg+`::uuid AND org_id = $1
			UNION ALL
			SELECT c.id, t.depth + 1 FROM spaces c JOIN tree t ON c.parent_id = t.id WHERE t.depth < 10)
		SELECT d.id::text, d.kind, d.title, d.body, COALESCE(tf.status, ''), COALESCE(d.space_id::text, ''),
		       d.created_at, tf.due_at, tf.completed_at
		  FROM documents d
		  LEFT JOIN task_fields tf ON tf.document_id = d.id
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL AND d.kind IN ('work_order', 'task')
		   AND d.space_id IN (SELECT id FROM tree) AND `+vis+`
		 ORDER BY d.created_at
		 LIMIT `+fmt.Sprint(maxServiceRecords), args...)
	if err != nil {
		return nil, fmt.Errorf("docs: service history: %w", err)
	}
	var recs []*ServiceRecord
	byID := map[string]*ServiceRecord{}
	var ids, spaceIDs []string
	for rows.Next() {
		r := &ServiceRecord{}
		var body, spaceID string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &body, &r.Status, &spaceID,
			&r.Opened, &r.Due, &r.Completed); err != nil {
			rows.Close()
			return nil, fmt.Errorf("docs: scan service record: %w", err)
		}
		r.Body, r.Assignees = splitNotes(body)
		r.SpacePath = spaceID // resolved to a path below
		recs = append(recs, r)
		byID[r.ID] = r
		ids = append(ids, r.ID)
		spaceIDs = append(spaceIDs, spaceID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}

	paths, err := s.spacePaths(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		p := paths[r.SpacePath]
		r.SpacePath = p
		if i := strings.LastIndex(p, " / "); i >= 0 {
			r.Job = p[i+3:]
		} else {
			r.Job = p
		}
	}

	// Assignees recorded here, for records that did not come with a list of
	// names from the system they were imported from.
	if err := s.fillAssignees(ctx, ids, byID); err != nil {
		return nil, err
	}

	add := func(id string, e ServiceEvent) {
		if r := byID[id]; r != nil {
			e.Day = ReportDay(e.At)
			r.Events = append(r.Events, e)
		}
	}
	for _, r := range recs {
		add(r.ID, ServiceEvent{At: r.Opened, Kind: "opened"})
		if r.Completed != nil {
			add(r.ID, ServiceEvent{At: *r.Completed, Kind: "completed"})
		}
	}

	// Every comment, replies included: the thread is the record of what was
	// done, and the list view of comments leaves replies out.
	mrows, err := s.db.Query(ctx, `
		SELECT m.entity_id::text, m.created_at, COALESCE(NULLIF(p.display_name, ''), m.author_id::text), m.body
		  FROM messages m JOIN principals p ON p.id = m.author_id
		 WHERE m.org_id = $1 AND m.deleted_at IS NULL AND m.entity_type = 'document'
		   AND m.entity_id = ANY($2::uuid[])
		 ORDER BY m.created_at`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("docs: service comments: %w", err)
	}
	for mrows.Next() {
		var id, who, text string
		var at time.Time
		if err := mrows.Scan(&id, &at, &who, &text); err != nil {
			mrows.Close()
			return nil, err
		}
		add(id, ServiceEvent{At: at, Kind: "comment", Who: who, Text: strings.TrimSpace(text)})
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	// Hours. Who logged them is left off on purpose: work brought in from
	// another system was recorded under one account, so a name here would be
	// wrong more often than right.
	trows, err := s.db.Query(ctx, `
		SELECT document_id::text, started_at, minutes, COALESCE(note, '')
		  FROM time_entries
		 WHERE org_id = $1 AND deleted_at IS NULL AND document_id = ANY($2::uuid[])
		 ORDER BY started_at`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("docs: service time: %w", err)
	}
	for trows.Next() {
		var id, note string
		var at time.Time
		var minutes int
		if err := trows.Scan(&id, &at, &minutes, &note); err != nil {
			trows.Close()
			return nil, err
		}
		// Work brought in from ClickUp carries one total per task, dated the
		// day it was imported, not the day the hours were worked. Showing it
		// as a day's work would put hours on a date nothing happened. It is
		// kept as the job's total and placed on the last day of the record.
		if strings.HasPrefix(note, importedTimeNote) {
			if r := byID[id]; r != nil {
				r.importedMinutes += minutes
			}
			continue
		}
		add(id, ServiceEvent{At: at, Kind: "time", Minutes: minutes, Text: strings.TrimSpace(note)})
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, err
	}

	for _, r := range recs {
		if r.importedMinutes == 0 {
			continue
		}
		at := r.Opened
		if r.Completed != nil {
			at = *r.Completed
		}
		for _, e := range r.Events {
			if r.Completed == nil && e.At.After(at) {
				at = e.At
			}
		}
		add(r.ID, ServiceEvent{At: at, Kind: "time_total", Minutes: r.importedMinutes})
	}

	// Keep what falls in the range, in time order, and drop records with
	// nothing left.
	out := recs[:0]
	for _, r := range recs {
		kept := r.Events[:0]
		for _, e := range r.Events {
			if (f.From != "" && e.Day < f.From) || (f.To != "" && e.Day > f.To) {
				continue
			}
			kept = append(kept, e)
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].At.Before(kept[j].At) })
		r.Events = kept
		if len(kept) > 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

// fillAssignees gives a record the people it is assigned to here when the
// import did not leave names behind.
func (s *Service) fillAssignees(ctx context.Context, ids []string, byID map[string]*ServiceRecord) error {
	rows, err := s.db.Query(ctx, `
		SELECT a.document_id::text, COALESCE(NULLIF(p.display_name, ''), p.id::text)
		  FROM document_assignees a JOIN principals p ON p.id = a.principal_id
		 WHERE a.document_id = ANY($1::uuid[])
		 ORDER BY a.is_primary DESC, a.created_at`, ids)
	if err != nil {
		return fmt.Errorf("docs: service assignees: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		if r := byID[id]; r != nil && len(r.Assignees) == 0 {
			r.assignedHere = append(r.assignedHere, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range byID {
		if len(r.Assignees) == 0 {
			r.Assignees = r.assignedHere
		}
	}
	return nil
}
