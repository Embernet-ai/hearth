package web

import (
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("templates did not parse: %v", err)
	}
	return s
}

// No template may use a data- attribute whose name starts with "on".
//
// html/template strips the data- prefix and then treats anything beginning
// with "on" as a JavaScript event handler, so data-on-hand="-2" is escaped as
// JavaScript and reaches the page as " -2 ". Padded, not broken, which is the
// worst kind: it renders, it looks right, and every reader of the attribute
// silently gets nothing.
//
// This has now happened twice, with data-ontime and data-on-hand, and both
// times it was found by an acceptance run rather than by reading the template.
// A grep is cheaper than a third time.
func TestEveryServiceHandedToNewIsKept(t *testing.T) {
	deps := reflect.New(reflect.TypeOf(Deps{})).Elem()
	for i := 0; i < deps.NumField(); i++ {
		f := deps.Field(i)
		if f.Kind() == reflect.Pointer && f.Type().Elem().Kind() == reflect.Struct {
			f.Set(reflect.New(f.Type().Elem()))
		}
	}
	deps.FieldByName("Log").Set(
		reflect.ValueOf(slog.New(slog.NewTextHandler(io.Discard, nil))))

	s, err := New(deps.Interface().(Deps))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	server := reflect.ValueOf(s).Elem()

	for i := 0; i < deps.NumField(); i++ {
		field := deps.Type().Field(i)
		if deps.Field(i).Kind() != reflect.Pointer || deps.Field(i).IsNil() {
			continue
		}
		kept := false
		for j := 0; j < server.NumField(); j++ {
			if server.Type().Field(j).Type != field.Type {
				continue
			}
			if !server.Field(j).IsNil() {
				kept = true
				break
			}
		}
		if !kept {
			t.Errorf("Deps.%s (%s) is never stored on the Server.\n"+
				"Every page that uses it will panic on a nil pointer, and every "+
				"other page will be fine, which is how this ships.",
				field.Name, field.Type)
		}
	}
}

// The Settings page is open to everybody, so the Administration section inside
// it is the only thing standing between an ordinary account and links to pages
// it cannot open. Each group has to appear for exactly the grant it needs.
func TestSubtasksStayInTheirParentsGroup(t *testing.T) {
	str := func(v string) *string { return &v }
	row := func(id string, parent *string) TableRow {
		return TableRow{Doc: &docs.Document{ID: id, ParentID: parent}}
	}
	groups := []TableGroup{
		{Value: "todo", Rows: []TableRow{row("aquiles", str("course")), row("loose", nil)}},
		{Value: "in_progress", Rows: []TableRow{row("course", nil), row("elsa", str("course"))}},
		{Value: "done", Rows: []TableRow{row("joe", str("course")), row("step", str("joe")), row("orphan", str("gone"))}},
	}
	got := map[string][]string{}
	for _, g := range keepWithParents(groups) {
		for _, r := range nestRows(g.Rows) {
			got[g.Value] = append(got[g.Value], r.Doc.ID)
		}
	}
	ip := strings.Join(got["in_progress"], ",")
	// The course first, all three people under it (in the list's own order),
	// and Joe's own subtask straight after Joe.
	if !strings.HasPrefix(ip, "course,") || len(got["in_progress"]) != 5 ||
		!strings.Contains(ip, "aquiles") || !strings.Contains(ip, "elsa") || !strings.Contains(ip, "joe,step") {
		t.Errorf("in progress: %s", ip)
	}
	if s := strings.Join(got["todo"], ","); s != "loose" {
		t.Errorf("todo: %s", s)
	}
	if s := strings.Join(got["done"], ","); s != "orphan" {
		t.Errorf("a subtask whose parent is not listed should stay in its own group: %s", s)
	}
}

// The Spaces page shows edit controls and the "+" only for spaces the reader
// may manage; others are read-only rows.

// Approvals is picked rather than scrolled: department, then employee, then that one person's entries. The grouping and the
// fall-backs are decided here; the page that shows them is held by ui/src/views/approvals.spec.ts.
func TestApprovalViewPicksOnePersonAtATime(t *testing.T) {
	end := time.Now()
	min := 90
	waiting := []timelog.Entry{
		{ID: "e1", PrincipalID: "p1", PersonName: "Gabriel", StartedAt: end, EndedAt: &end, Minutes: &min},
		{ID: "e2", PrincipalID: "p2", PersonName: "Aria", StartedAt: end, EndedAt: &end, Minutes: &min},
		{ID: "e3", PrincipalID: "p1", PersonName: "Gabriel", StartedAt: end, EndedAt: &end, Minutes: &min},
		{ID: "e4", PrincipalID: "p3", PersonName: "Constanza", StartedAt: end, EndedAt: &end, Minutes: &min},
	}
	chart := map[string]*authz.Structure{
		"p1": {Departments: []authz.Department{{ID: "white", Name: "White Team"}}},
		"p2": {Departments: []authz.Department{{ID: "eng", Name: "Engineering"}}},
		// p3 is in no department.
	}
	if v := approvalView(groupByPerson(waiting), chart, "eng", "p1"); v.Selected != nil {
		t.Error("Gabriel was selected while Engineering was picked")
	}
	if v := approvalView(groupByPerson(waiting), chart, "gone", ""); v.Dept != "" || len(v.People) != 3 {
		t.Error("a department with nothing waiting should fall back to everybody")
	}
	if v := approvalView(groupByPerson(waiting), chart, "none", ""); len(v.People) != 1 || v.People[0].PersonName != "Constanza" {
		t.Error("No department should list the people in none")
	}
}

// Subtasks fold under their parent in a table, depth first. A subtask whose parent is not in the list stays visible on its
// own, marked, rather than disappearing.
func TestTableFoldsSubtasksUnderTheirParent(t *testing.T) {
	str := func(v string) *string { return &v }
	cols := []docs.Field{docs.Col("title")}
	// Out of order on purpose: children before the parent, as a sort can leave them.
	rows := []*docs.Row{
		{Document: &docs.Document{ID: "luis", Title: "Luis", ParentID: str("ignition")}},
		{Document: &docs.Document{ID: "ignition", Title: "Ignition"}},
		{Document: &docs.Document{ID: "ivan", Title: "Ivan", ParentID: str("ignition")}},
		{Document: &docs.Document{ID: "step", Title: "Step 1", ParentID: str("luis")}},
		{Document: &docs.Document{ID: "stray", Title: "Stray", ParentID: str("not-in-this-list")}},
	}
	nested := nestRows(tableRows(rows, cols, nil, map[string]string{}))
	var order []string
	for _, r := range nested {
		order = append(order, r.Doc.ID)
	}
	if got := strings.Join(order, ","); got != "ignition,luis,step,ivan,stray" {
		t.Fatalf("order: %s", got)
	}
	if nested[0].Children != 2 || nested[1].Depth != 1 || nested[2].Depth != 2 || nested[1].Parent != "ignition" {
		t.Fatalf("depths and counts: %+v", nested[:3])
	}
	if !nested[4].Subtask || nested[4].Depth != 0 {
		t.Fatal("a subtask whose parent is elsewhere should stay top-level, marked")
	}
}
