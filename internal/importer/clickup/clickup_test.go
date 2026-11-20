package clickup

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseCSV(t *testing.T) {
	// Headers as ClickUp actually writes them, including the ragged final row
	// that a missing custom field produces.
	in := `Task ID,Task Name,Status,Priority,Due Date,Assignees,List Name,Task Content
abc123,Replace bearing,in review,urgent,2026-09-01,"Sam, Dana",Maintenance,Bearing is noisy
def456,Order filters,Complete,low,,Sam,Purchasing
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tasks, want 2", len(got))
	}

	first := got[0]
	if first.ExternalID != "abc123" || first.Title != "Replace bearing" {
		t.Errorf("first = %+v", first)
	}
	if MapStatus(first.Status) != "in_progress" {
		t.Errorf("in review mapped to %q, want in_progress", MapStatus(first.Status))
	}
	if first.Priority != 3 {
		t.Errorf("urgent priority = %d, want 3", first.Priority)
	}
	if first.DueAt == nil {
		t.Error("due date did not parse")
	}
	if len(first.Assignees) != 2 {
		t.Errorf("assignees = %v, want two", first.Assignees)
	}

	// The short row must still import rather than being rejected for having
	// fewer fields than the header.
	if MapStatus(got[1].Status) != "done" {
		t.Errorf("Complete mapped to %q, want done", MapStatus(got[1].Status))
	}
}

func TestParseJSON(t *testing.T) {
	in := `{"tasks":[
	  {"id":"9x","name":"Calibrate sensor","description":"drifting",
	   "status":{"status":"blocked"},"priority":{"priority":"high"},
	   "due_date":"1789331200000","url":"https://app.clickup.com/t/9x",
	   "assignees":[{"username":"dana","email":"dana@example.com"}],
	   "list":{"name":"Line 2"}}]}`

	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	task := got[0]
	if task.ExternalID != "9x" || task.Title != "Calibrate sensor" {
		t.Errorf("task = %+v", task)
	}
	if MapStatus(task.Status) != "blocked" {
		t.Errorf("status = %q", MapStatus(task.Status))
	}
	if task.Priority != 2 {
		t.Errorf("high priority = %d, want 2", task.Priority)
	}
	// Unix MILLISECONDS as a string, which is the shape that quietly parses as
	// a year in the far future if treated as seconds.
	if task.DueAt == nil || task.DueAt.Year() != 2026 {
		t.Errorf("due = %v, want a 2026 date", task.DueAt)
	}
	if len(task.Assignees) != 1 || task.Assignees[0] != "dana" {
		t.Errorf("assignees = %v", task.Assignees)
	}
}

func TestParseBareJSONArray(t *testing.T) {
	got, err := Parse(strings.NewReader(`[{"id":"1","name":"Only one"}]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Only one" {
		t.Errorf("got %+v", got)
	}
}

// Unknown statuses must import as todo. Putting a task in the wrong column is
// recoverable; dropping it on the floor is not.
func TestUnknownStatusBecomesTodo(t *testing.T) {
	for _, s := range []string{"", "Backlog", "Fridge", "whatever nonsense"} {
		if got := MapStatus(s); got != "todo" {
			t.Errorf("MapStatus(%q) = %q, want todo", s, got)
		}
	}
}

// ClickUp numbers priority with 1 as most urgent, which is backwards from how
// it reads in a list. It must be inverted, not passed through.
func TestPriorityIsInverted(t *testing.T) {
	if parsePriority("1") <= parsePriority("4") {
		t.Error("ClickUp priority 1 (urgent) must outrank 4 (low)")
	}
	if parsePriority("urgent") != parsePriority("1") {
		t.Error("the word and the number must agree")
	}
}

func TestEmptyExportIsAnError(t *testing.T) {
	if _, err := Parse(strings.NewReader("   ")); err == nil {
		t.Error("an empty export should be reported, not silently import nothing")
	}
}

// The fields the 2026-08-18 inventory said were actually populated: custom
// values on 43% of tasks, tracked time on 25%, tags on 10%, start dates on 9%.
// Before this, every one of them was parsed and thrown away.
func TestParseCarriesCustomFieldsTimeAndTags(t *testing.T) {
	const export = `{"tasks":[{
      "id":"abc123",
      "name":"Replace bearing on line 3",
      "description":"body",
      "status":{"status":"in progress"},
      "start_date":"1755000000000",
      "due_date":"1755600000000",
      "time_estimate":7200000,
      "time_spent":"5400000",
      "tags":[{"name":"urgent"},{"name":"line-3"}],
      "custom_fields":[
        {"id":"f1","name":"Compliance Status","type":"drop_down","value":1},
        {"id":"f2","name":"Budget","type":"currency","value":"1250.50"},
        {"id":"f3","name":"Supervisor","type":"users","value":[{"id":7}]},
        {"id":"f4","name":"Never Filled In","type":"short_text"},
        {"id":"f5","name":"Explicit Null","type":"short_text","value":null},
        {"id":"f6","name":"Empty String","type":"short_text","value":""},
        {"id":"f7","name":"Empty List","type":"labels","value":[]}
      ]}]}`

	tasks, err := Parse(strings.NewReader(export))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	got := tasks[0]

	if got.Estimate != 2*time.Hour {
		t.Errorf("estimate = %v, want 2h", got.Estimate)
	}
	// A string where the sibling field was a number. The export is inconsistent
	// about this and a task losing its tracked time to a type is a task whose
	// hours somebody has to re-enter.
	if got.Spent != 90*time.Minute {
		t.Errorf("spent = %v, want 1h30m", got.Spent)
	}
	if got.StartAt == nil {
		t.Error("start date was dropped")
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags = %v, want 2", got.Tags)
	}

	// Four ways of saying "nobody filled this in", and only the three real
	// values should survive. A check for null alone lets the other three
	// through as values, which is how an import produces a field grid full of
	// blanks that look deliberate.
	if len(got.Fields) != 3 {
		names := make([]string, 0, len(got.Fields))
		for _, f := range got.Fields {
			names = append(names, f.Name)
		}
		t.Fatalf("carried %d fields %v, want exactly the 3 that were set", len(got.Fields), names)
	}

	byName := map[string]FieldValue{}
	for _, f := range got.Fields {
		byName[f.Name] = f
	}
	if f := byName["Compliance Status"]; f.Type != "select" {
		t.Errorf("drop_down normalised to %q, want select", f.Type)
	}
	if f := byName["Budget"]; f.Type != "currency" {
		t.Errorf("currency normalised to %q", f.Type)
	}
	if f := byName["Supervisor"]; f.Type != "principal" {
		t.Errorf("users normalised to %q, want principal", f.Type)
	}
}

// An unknown type must not lose the value. ClickUp adds field types, and a task
// whose value vanished because the vendor shipped a feature is worse than one
// rendered as text.
func TestUnknownFieldTypeKeepsTheValue(t *testing.T) {
	const export = `{"tasks":[{"id":"x","name":"t","custom_fields":[
      {"id":"f9","name":"Brand New Type","type":"quantum_flux","value":"still here"}]}]}`
	tasks, err := Parse(strings.NewReader(export))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks[0].Fields) != 1 {
		t.Fatal("value dropped for an unrecognised type")
	}
	if got := tasks[0].Fields[0]; got.Type != "text" || got.Value != "still here" {
		t.Errorf("got type=%q value=%v, want text/still here", got.Type, got.Value)
	}
}

// The status vocabularies this workspace actually uses, both of them.
//
// ClickUp lets every space define its own, and these eight spaces use two
// different sets. Testing one and assuming the other is how "not started"
// arrived as in-progress on five spaces at once.
func TestMapStatusHandlesBothRealVocabularies(t *testing.T) {
	cases := map[string]string{
		// Ops, Embernet, Sales, HR, Marketing.
		"not started":     "todo",
		"on hold":         "blocked",
		"in progress":     "in_progress",
		"done":            "done",
		"closed (hidden)": "done",
		// White Team, White Team Service, Red Team.
		"to do":    "todo",
		"complete": "done",
		// Shapes neither space uses but a third might.
		"NOT STARTED": "todo",
		"Not Started": "todo",
		"":            "todo",
		"cancelled":   "cancelled",
	}
	for in, want := range cases {
		if got := MapStatus(in); got != want {
			t.Errorf("MapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// A dropdown must arrive with its choices. Nineteen of the sixty-two fields in
// the workspace this was built against are dropdowns, and a select with no
// options cannot be validated, filtered or rendered as anything but a text box.
func TestDropdownOptionsSurviveTheImport(t *testing.T) {
	const export = `{"tasks":[{"id":"a","name":"t","custom_fields":[
      {"id":"f1","name":"Impact Level","type":"drop_down","value":2,
       "type_config":{"options":[
         {"id":"o0","name":"Track Only"},{"id":"o1","name":"Low"},
         {"id":"o2","name":"Medium"},{"id":"o3","name":"High"}]}},
      {"id":"f2","name":"Role","type":"labels","value":["x"],
       "type_config":{"options":[{"id":"l1","label":"Role 1"}]}}
    ]}]}`
	tasks, err := Parse(strings.NewReader(export))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]FieldValue{}
	for _, f := range tasks[0].Fields {
		byName[f.Name] = f
	}
	if got := byName["Impact Level"].Options; len(got) != 4 || got[0] != "Track Only" {
		t.Errorf("drop_down options = %v, want the four it declared", got)
	}
	// ClickUp says "label" on a label field and "name" on a dropdown. Both are
	// the text a person reads.
	if got := byName["Role"].Options; len(got) != 1 || got[0] != "Role 1" {
		t.Errorf("labels options = %v, want [Role 1]", got)
	}
}

// An assignee has to become the real field, not a sentence in the body.
// Two thirds of the tasks in this workspace carry one, and the workload page,
// the board and every "assigned to me" filter read that field and nothing else.
func TestAssigneeMatchesByEmailOrName(t *testing.T) {
	people := map[string]string{
		"pat@example.com": "p-1",
		"luis":            "p-2",
	}
	cases := []struct {
		name  string
		given []string
		want  string
	}{
		{"by email", []string{"pat@example.com"}, "p-1"},
		{"by display name", []string{"Luis"}, "p-2"},
		{"case and padding do not decide", []string{"  PAT@Example.com "}, "p-1"},
		// ClickUp allows several assignees and a task field holds one. The
		// first that resolves wins rather than the task arriving unassigned.
		{"first resolvable of several", []string{"nobody@example.com", "luis"}, "p-2"},
		{"nobody known", []string{"stranger@example.com"}, ""},
		{"none given", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchAssignee(c.given, people)
			if c.want == "" {
				if got != nil {
					t.Errorf("got %q, want unassigned", *got)
				}
				return
			}
			if got == nil || *got != c.want {
				t.Errorf("got %v, want %q", got, c.want)
			}
		})
	}
	// No directory at all must not panic or invent an assignee.
	if matchAssignee([]string{"pat@example.com"}, nil) != nil {
		t.Error("assigned somebody from an empty directory")
	}
}

// Checklists come across as steps, keeping which checklist they belonged to.
// A task with a "Prep" and a "Teardown" list reads wrong as one flat run of
// steps with no idea which phase each belongs to.
func TestChecklistsFlattenWithTheirNames(t *testing.T) {
	const export = `{"tasks":[{"id":"a","name":"t","checklists":[
      {"name":"Prep","items":[
        {"name":"Isolate the line","resolved":true},
        {"name":"Lock out","resolved":false}]},
      {"name":"","items":[{"name":"Unnamed list item","resolved":false}]},
      {"name":"Teardown","items":[{"name":"  ","resolved":false}]}
    ]}]}`
	tasks, err := Parse(strings.NewReader(export))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := tasks[0].Checklist
	// The blank-text item is dropped; a step with no words is not a step.
	if len(got) != 3 {
		t.Fatalf("got %d items %v, want 3", len(got), got)
	}
	if got[0].Text != "Prep: Isolate the line" || !got[0].Done {
		t.Errorf("first item = %+v, want the prefixed, ticked one", got[0])
	}
	if got[1].Text != "Prep: Lock out" || got[1].Done {
		t.Errorf("second item = %+v", got[1])
	}
	// An unnamed checklist contributes no prefix rather than ": ".
	if got[2].Text != "Unnamed list item" {
		t.Errorf("unnamed checklist item = %q", got[2].Text)
	}
}

// The history. Without these every imported document claims to have been
// created on migration day: lists sort wrong, "recently updated" is
// meaningless, and years of work appear to have happened in one afternoon.
func TestTheSourcesOwnHistorySurvives(t *testing.T) {
	const export = `{"tasks":[{
      "id":"a","name":"t","status":{"status":"complete"},
      "date_created":"1700000000000",
      "date_updated":"1710000000000",
      "date_done":"1720000000000",
      "custom_id":"DEV-1234",
      "creator":{"username":"Dana","email":"dana@example.com"}}]}`
	tasks, err := Parse(strings.NewReader(export))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := tasks[0]
	for name, ts := range map[string]*time.Time{
		"created": got.CreatedAt, "updated": got.UpdatedAt, "completed": got.CompletedAt,
	} {
		if ts == nil {
			t.Errorf("%s date was dropped", name)
		}
	}
	if got.CreatedAt != nil && !got.CreatedAt.Before(*got.UpdatedAt) {
		t.Error("created should precede updated")
	}
	if got.Creator != "dana@example.com" {
		t.Errorf("creator = %q, want the email", got.Creator)
	}
	if got.CustomID != "DEV-1234" {
		t.Errorf("custom id = %q", got.CustomID)
	}
}

// date_closed is the fallback when date_done is absent: both answer "when did
// this finish", and a task closed without being ticked still finished.
func TestCompletedFallsBackToClosed(t *testing.T) {
	const export = `{"tasks":[{"id":"a","name":"t","date_closed":"1720000000000"}]}`
	tasks, _ := Parse(strings.NewReader(export))
	if tasks[0].CompletedAt == nil {
		t.Error("a task closed but not marked done has no completion date")
	}
}

// Every assignee, not just the one the task field can hold.
func TestAllAssigneesAreRecorded(t *testing.T) {
	people := map[string]string{
		"a@x.com": "p-1", "b@x.com": "p-2", "c@x.com": "p-3",
	}
	var gotIDs []string
	var gotPrimary string
	st := &captureAssignees{fn: func(ids []string, primary string) {
		gotIDs, gotPrimary = ids, primary
	}}

	setAssignees(context.Background(), st, "doc-1", Task{
		// One unknown in the middle, and one repeated.
		Assignees: []string{"a@x.com", "nobody@x.com", "b@x.com", "a@x.com"},
	}, people)

	if len(gotIDs) != 2 {
		t.Fatalf("recorded %v, want the two that resolve, deduplicated", gotIDs)
	}
	// Primary matches what task_fields.assignee_id holds -- the first that
	// resolves -- so the column and the table cannot disagree about who owns it.
	if gotPrimary != "p-1" || gotIDs[0] != "p-1" {
		t.Errorf("primary = %q, ids = %v; want p-1 first", gotPrimary, gotIDs)
	}
}

// Nobody resolvable means nothing written, rather than an empty set that would
// delete assignees somebody set here by hand.
func TestNoResolvableAssigneesWritesNothing(t *testing.T) {
	called := false
	st := &captureAssignees{fn: func([]string, string) { called = true }}
	setAssignees(context.Background(), st, "doc-1",
		Task{Assignees: []string{"stranger@x.com"}}, map[string]string{"a@x.com": "p-1"})
	if called {
		t.Error("wrote an empty assignee set, which would clear real assignments")
	}
}

// captureAssignees is a Store that only implements what setAssignees uses.
type captureAssignees struct {
	Store
	fn func(ids []string, primary string)
}

func (c *captureAssignees) SetAssignees(_ context.Context, _ string, ids []string, primary string) error {
	c.fn(ids, primary)
	return nil
}
