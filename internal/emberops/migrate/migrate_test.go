package migrate

import (
	"bytes"
	"context"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// memSource serves rows by step name and keeps progress in memory.
type memSource struct {
	rows     map[string][]Row
	progress map[string]*struct {
		cursor string
		done   bool
	}
	pages int
}

func newMem() *memSource {
	return &memSource{rows: map[string][]Row{}, progress: map[string]*struct {
		cursor string
		done   bool
	}{}}
}

func (m *memSource) add(step, id string, rec mapping.Rec) {
	rec["id"] = id
	m.rows[step] = append(m.rows[step], Row{Cur: id, Rec: rec})
	sort.Slice(m.rows[step], func(i, j int) bool { return m.rows[step][i].Cur < m.rows[step][j].Cur })
}

func (m *memSource) Org(context.Context) (string, error) { return "org", nil }
func (m *memSource) Count(_ context.Context, st Step, _ string) (int, error) {
	return len(m.rows[st.name]), nil
}
func (m *memSource) Page(_ context.Context, st Step, cursor string, limit int, _ string) ([]Row, error) {
	m.pages++
	var out []Row
	for _, r := range m.rows[st.name] {
		if r.Cur > cursor && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memSource) Progress(_ context.Context, step string) (string, bool, error) {
	if p := m.progress[step]; p != nil {
		return p.cursor, p.done, nil
	}
	return "", false, nil
}
func (m *memSource) Save(_ context.Context, step, cursor string, _ int) error {
	if m.progress[step] == nil {
		m.progress[step] = &struct {
			cursor string
			done   bool
		}{}
	}
	m.progress[step].cursor = cursor
	return nil
}
func (m *memSource) Finish(_ context.Context, step, cursor string) error {
	_ = m.Save(nil, step, cursor, 0)
	m.progress[step].done = true
	return nil
}
func (m *memSource) Reset(_ context.Context, step string) error { delete(m.progress, step); return nil }

func fixture(t *testing.T) (*memSource, *emberops.Client, *fake.Frappe) {
	t.Helper()
	f := fake.New(map[string]string{"svc": "svc@example.com"})
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})

	m := newMem()
	m.add("users", "p1", mapping.Rec{"display_name": "Ann", "kind": "user", "status": "active", "email": "ann@example.com"})
	m.add("users", "p2", mapping.Rec{"display_name": "Bot", "kind": "agent", "status": "active"}) // no email
	m.add("spaces", "s1", mapping.Rec{"name": "Ops", "kind": "folder"})
	m.add("spaces", "s2", mapping.Rec{"name": "Line 1", "kind": "folder", "parent_id": "s1"})
	for _, id := range []string{"t1", "t2", "t3"} {
		m.add("tasks", id, mapping.Rec{"title": "Task " + id, "kind": "task", "status": "todo", "space_id": "s1"})
	}
	return m, c, f
}

func TestDryRunReadsAndMapsButWritesNothing(t *testing.T) {
	m, _, f := fixture(t)
	r := &Runner{Src: m} // no client at all: a dry run must not need EmberOPS
	rep, err := r.Run(context.Background(), Options{DryRun: true, BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry run called EmberOPS: %v", f.Calls)
	}
	if len(m.progress) != 0 {
		t.Fatalf("dry run saved progress: %v", m.progress)
	}
	by := map[string]StepReport{}
	for _, s := range rep.Steps {
		by[s.Name] = s
	}
	if u := by["users"]; u.Source != 2 || u.Mapped != 1 || u.Skipped != 1 || !strings.Contains(strings.Join(u.Notes, " "), "no email") {
		t.Fatalf("users = %+v", u)
	}
	if by["tasks"].Mapped != 3 || by["spaces"].Mapped != 2 {
		t.Fatalf("tasks=%+v spaces=%+v", by["tasks"], by["spaces"])
	}
	var out bytes.Buffer
	rep.Write(&out)
	if !strings.Contains(out.String(), "dry run (nothing was written)") || !strings.Contains(out.String(), "tasks") {
		t.Fatalf("report:\n%s", out.String())
	}
}

func TestRunWritesInOrderAndIsIdempotent(t *testing.T) {
	m, c, f := fixture(t)
	r := &Runner{Src: m, C: c}
	opts := Options{BatchSize: 2}

	rep, err := r.Run(context.Background(), opts)
	if err != nil || !rep.Ok() {
		t.Fatalf("run: %v %+v", err, rep)
	}
	if n := len(f.All("Task")); n != 3 {
		t.Fatalf("%d tasks in EmberOPS", n)
	}
	// The child space was written after its parent, so the reference resolved.
	var line1 map[string]any
	for _, s := range f.All("Hearth Space") {
		if s["hearth_id"] == "s2" {
			line1 = s
		}
	}
	if line1 == nil || line1["parent_hearth_space"] == nil || line1["parent_hearth_space"] == "hearth:s1" {
		t.Fatalf("child space = %v", line1)
	}
	for _, s := range rep.Steps {
		if s.Name == "tasks" && (s.Created != 3 || s.Remote != 3) {
			t.Fatalf("tasks = %+v", s)
		}
	}

	// Run it again from scratch: records are updated, none duplicated.
	rep, err = r.Run(context.Background(), Options{BatchSize: 2, Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.All("Task")); n != 3 {
		t.Fatalf("second run duplicated: %d tasks", n)
	}
	for _, s := range rep.Steps {
		if s.Name == "tasks" && (s.Created != 0 || s.Updated != 3) {
			t.Fatalf("tasks on rerun = %+v", s)
		}
	}
}

func TestResumeSkipsFinishedStepsAndContinuesAPartialOne(t *testing.T) {
	m, c, f := fixture(t)
	m.progress["users"] = &struct {
		cursor string
		done   bool
	}{"p2", true}
	m.progress["tasks"] = &struct {
		cursor string
		done   bool
	}{"t1", false} // t1 was sent before the run stopped
	r := &Runner{Src: m, C: c}

	rep, err := r.Run(context.Background(), Options{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.All("User")) != 0 {
		t.Fatal("a finished step was sent again")
	}
	tasks := f.All("Task")
	if len(tasks) != 2 {
		t.Fatalf("resumed tasks = %d, want t2 and t3 only", len(tasks))
	}
	for _, s := range rep.Steps {
		if s.Name == "tasks" && !s.Resumed {
			t.Fatal("tasks not reported as resumed")
		}
	}
}

func TestOnlyLimitsTheRun(t *testing.T) {
	m, _, _ := fixture(t)
	rep, err := (&Runner{Src: m}).Run(context.Background(), Options{DryRun: true, Only: []string{"Tasks"}})
	if err != nil || len(rep.Steps) != 1 || rep.Steps[0].Name != "tasks" {
		t.Fatalf("%v %+v", err, rep.Steps)
	}
}

func TestUnauthorisedRunStopsWithAnError(t *testing.T) {
	m, c, _ := fixture(t)
	// A real site rejects what its validation refuses; the fake does not, so
	// fail one by making the client's token unknown to it for this run.
	r := &Runner{Src: m, C: emberops.New(emberops.Config{BaseURL: urlOf(c), Service: emberops.StaticToken("wrong")})}
	if _, err := r.Run(context.Background(), Options{}); err == nil {
		t.Fatal("an unauthorised run should stop with an error")
	}
}

func urlOf(c *emberops.Client) *url.URL { return c.BaseURL() }

func TestStepOrderFollowsTheAddon(t *testing.T) {
	var names []string
	for _, s := range append(steps(Options{}), documentSteps(Options{})...) {
		names = append(names, s.name)
	}
	pos := func(n string) int {
		for i, x := range names {
			if x == n {
				return i
			}
		}
		t.Fatalf("no step %q", n)
		return -1
	}
	for _, c := range [][2]string{
		{"users", "employees"}, {"employees", "employee approvers"}, {"employees", "teams"}, {"spaces", "tasks"},
		{"assets", "work orders"}, {"maintenance plans", "work orders"}, {"tasks", "time entries"},
		{"work orders", "time entries"}, {"companies", "contacts"}, {"parts", "stock movements"},
		{"tasks", "entity links"}, {"employees", "time off"},
	} {
		if pos(c[0]) > pos(c[1]) {
			t.Errorf("%s must come before %s", c[0], c[1])
		}
	}
}

// Companies and contacts stay in Hearth's CRM unless HEARTH_EMBEROPS_CRM_PARTIES says otherwise. Copying them anyway would
// have made a Customer or a Supplier of every one of Fireball's 6668 companies, prospects included, beside the ones the crm
// sync projects.
func TestCompaniesAndContactsStayInHearthUnlessTheyLiveInEmberOps(t *testing.T) {
	m, _, _ := fixture(t)
	m.add("companies", "c1", mapping.Rec{"name": "Acme", "kind": "customer"})
	m.add("contacts", "p1", mapping.Rec{"first_name": "Pat", "company_id": "c1"})
	r := &Runner{Src: m}
	has := func(rep Report, name string) bool {
		for _, s := range rep.Steps {
			if s.Name == name {
				return true
			}
		}
		return false
	}
	rep, err := r.Run(context.Background(), Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if has(rep, "companies") || has(rep, "contacts") {
		t.Fatalf("the CRM was migrated by default: %+v", rep.Steps)
	}
	rep, err = r.Run(context.Background(), Options{DryRun: true, CRMParties: true})
	if err != nil {
		t.Fatal(err)
	}
	if !has(rep, "companies") || !has(rep, "contacts") {
		t.Fatalf("the CRM was left out when EmberOps holds it: %+v", rep.Steps)
	}
}

// A refusal is reported under its cause, with ids and numbers taken out, so one cause is one line however many records it
// hit. The notes stop at five and skips come first, which on the first real run hid every refusal.
func TestRefusalsAreCountedByCause(t *testing.T) {
	sr := &StepReport{}
	for _, id := range []string{"a", "b", "c"} {
		refused(sr, id, "Task: Hearth Space with Hearth ID 5f3c1d1e-0000-4000-8000-000000000001 not found")
	}
	refused(sr, "d", "Task: Cannot add an open task under completed Parent Task TASK-2026-00031")
	refused(sr, "e", "")
	if len(sr.Classes) != 3 {
		t.Fatalf("classes = %v", sr.Classes)
	}
	if got := sr.Classes["Task: Hearth Space with Hearth ID # not found"]; got != 3 {
		t.Fatalf("space refusals = %d: %v", got, sr.Classes)
	}
	if sr.Classes["(no message)"] != 1 {
		t.Fatalf("blank refusal lost: %v", sr.Classes)
	}
	rep := Report{Steps: []StepReport{{Name: "tasks", Failed: 5, Classes: sr.Classes, Sample: sr.Sample}}}
	var out bytes.Buffer
	rep.Write(&out)
	if !strings.Contains(out.String(), "refused tasks: 3 x Task: Hearth Space with Hearth ID # not found (e.g. a)") {
		t.Fatalf("report:\n%s", out.String())
	}
}

// A link to a Hearth contact is carried only when contacts live in EmberOps. Fireball's CRM stays in Hearth, so 143 links
// to contacts were refused as "Contact with Hearth ID not found" on the first real run.
func TestLinksToContactsWaitForTheCRMToMove(t *testing.T) {
	link := mapping.Rec{"id": "l1", "from_type": "document", "from_id": "d1", "from_kind": "task", "to_type": "contact", "to_id": "c1", "kind": "mentions"}
	var build func(*builder, mapping.Rec) ([]emberops.BulkItem, string)
	for _, opts := range []Options{{}, {CRMParties: true}} {
		for _, s := range documentSteps(opts) {
			if s.name == "entity links" {
				build = s.build
			}
		}
		items, why := build(&builder{z: mapping.TZ{}}, link)
		if !opts.CRMParties && (len(items) != 0 || !strings.Contains(why, "stays in Hearth's CRM")) {
			t.Fatalf("a contact link was carried with the CRM in Hearth: %v %q", items, why)
		}
		if opts.CRMParties && (len(items) != 1 || why != "") {
			t.Fatalf("a contact link was dropped with the CRM in EmberOps: %v %q", items, why)
		}
	}
}

// A person with no email address has no User and no Employee, so their time cannot be filed in EmberOps. The report says so
// and counts it as skipped, instead of refusing it as an unknown employee.
func TestTimeOfAPersonWithNoEmailIsSkippedWithAReason(t *testing.T) {
	var build func(*builder, mapping.Rec) ([]emberops.BulkItem, string)
	for _, s := range documentSteps(Options{}) {
		if s.name == "time entries" {
			build = s.build
		}
	}
	b := &builder{z: mapping.TZ{}}
	rec := mapping.Rec{"id": "e1", "principal_id": "p1", "minutes": float64(30)}
	if items, why := build(b, rec); len(items) != 0 || !strings.Contains(why, "no email address") {
		t.Fatalf("an entry with no person email was not skipped: %v %q", items, why)
	}
	rec["principal_email"] = "ann@example.com"
	if items, why := build(b, rec); len(items) != 1 || why != "" {
		t.Fatalf("an entry with a person email was not carried: %v %q", items, why)
	}
}
