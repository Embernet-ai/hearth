package docs_test

// The view compiler, against a real Postgres.
//
// These are integration tests rather than unit tests because the thing being
// checked is what the DATABASE does with the SQL, not what string was built. A
// test that asserted on the generated SQL would pass while the query returned
// the wrong rows, which is the failure mode that matters.

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/docs"
)

// seedProperty defines a custom field and returns its id.
func (f *fx) seedProperty(t *testing.T, key, label, dataType string, options []string) string {
	t.Helper()
	opts := "[]"
	if len(options) > 0 {
		opts = `["` + options[0] + `"`
		for _, o := range options[1:] {
			opts += `,"` + o + `"`
		}
		opts += "]"
	}
	var id string
	err := f.pools.Web.QueryRow(context.Background(), `
		INSERT INTO property_defs (id, org_id, key, display_name, data_type, applies_to, options)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, ARRAY['work_order','asset','task'], $5::jsonb)
		RETURNING id::text`, f.org, key, label, dataType, opts).Scan(&id)
	if err != nil {
		t.Fatalf("seed property %s: %v", key, err)
	}
	return id
}

func (f *fx) setProperty(t *testing.T, docID, defID, jsonValue string) {
	t.Helper()
	_, err := f.pools.Web.Exec(context.Background(), `
		INSERT INTO property_values (entity_type, entity_id, def_id, value, updated_by)
		VALUES ('document', $1, $2, $3::jsonb, $4)
		ON CONFLICT (entity_type, entity_id, def_id) DO UPDATE SET value = EXCLUDED.value`,
		docID, defID, jsonValue, f.alice)
	if err != nil {
		t.Fatalf("set property: %v", err)
	}
}

// Two filters have to narrow rather than replace, and the count has to describe
// the same set as the rows. A count compiled separately from the list is how
// "showing 12 of 40" ends up lying about both halves.
func TestFiltersComposeAndTheCountAgrees(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	mk := func(title, status string, priority int16) {
		t.Helper()
		if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindWorkOrder, Title: title,
			Task: &docs.TaskFields{Status: status, Priority: priority},
		}); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
	}
	mk("todo urgent", docs.StatusTodo, 3)
	mk("todo low", docs.StatusTodo, 0)
	mk("done urgent", docs.StatusDone, 3)
	mk("blocked low", docs.StatusBlocked, 0)

	q := docs.Query{
		Kind: docs.KindWorkOrder,
		Filters: []docs.Filter{
			{Field: docs.Col("status"), Op: docs.OpIs, Values: []string{docs.StatusTodo}},
			{Field: docs.Col("priority"), Op: docs.OpIs, Values: []string{"3"}},
		},
	}

	rows, err := f.svc.Search(ctx, f.org, alice, q)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].Title != "todo urgent" {
		t.Fatalf("two filters did not compose: got %d rows", len(rows))
	}

	n, err := f.svc.Count(ctx, f.org, alice, q)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != len(rows) {
		t.Errorf("count says %d, the list has %d", n, len(rows))
	}

	// And against the database directly, with the filter written by hand. If
	// the compiler and a plain query disagree, the compiler is wrong.
	var direct int
	err = f.pools.Web.QueryRow(ctx, `
		SELECT count(*) FROM documents d
		JOIN task_fields t ON t.document_id = d.id
		WHERE d.org_id = $1 AND d.kind = 'work_order' AND d.deleted_at IS NULL
		  AND t.status = 'todo' AND t.priority = 3`, f.org).Scan(&direct)
	if err != nil {
		t.Fatalf("direct count: %v", err)
	}
	if direct != n {
		t.Errorf("compiled count %d, direct SQL count %d", n, direct)
	}
}

// A view is a query, so it inherits the permission filter rather than applying
// one of its own. This is the check that the inheritance is real.
func TestAViewNeverShowsARowTheViewerCannotSee(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, dave := f.subj(t, f.alice), f.subj(t, f.dave)

	secret, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Rates for the Q3 contract",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	q := docs.Query{Kind: docs.KindWorkOrder}
	rows, err := f.svc.Search(ctx, f.org, dave, q)
	if err != nil {
		t.Fatalf("search as dave: %v", err)
	}
	for _, r := range rows {
		if r.ID == secret.ID {
			t.Fatal("a view showed Dave a work order he has no grant for")
		}
	}

	// The count has to lie in the same direction, or a board header reports a
	// number that reveals how much is being hidden.
	n, err := f.svc.Count(ctx, f.org, dave, q)
	if err != nil {
		t.Fatalf("count as dave: %v", err)
	}
	if n != len(rows) {
		t.Errorf("dave's count is %d but he can see %d rows", n, len(rows))
	}

	// Sanity: it is genuinely there for somebody.
	if n, err := f.svc.Count(ctx, f.org, alice, q); err != nil || n == 0 {
		t.Fatalf("alice cannot see her own work order either, so this proves nothing (n=%d err=%v)", n, err)
	}
}

// Custom fields filter in SQL. Doing it in Go afterwards breaks LIMIT, which is
// the whole reason the compiler exists.
func TestFilterOnACustomProperty(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	tag := f.seedProperty(t, "asset_tag", "Asset tag", docs.PropText, nil)
	hours := f.seedProperty(t, "run_hours", "Run hours", docs.PropNumber, nil)

	one, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: "Conveyor"})
	two, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: "Pump"})
	f.setProperty(t, one.ID, tag, `"CV-3"`)
	f.setProperty(t, two.ID, tag, `"PMP-14"`)
	f.setProperty(t, one.ID, hours, `900`)
	f.setProperty(t, two.ID, hours, `1200`)

	rows, err := f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind:    docs.KindAsset,
		Filters: []docs.Filter{{Field: docs.Prop("asset_tag"), Op: docs.OpIs, Values: []string{"CV-3"}}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != one.ID {
		t.Fatalf("property filter matched %d rows, want the conveyor", len(rows))
	}

	// A number is stored as a JSON number, so it must match as one and not as
	// the string "1200".
	rows, err = f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind:    docs.KindAsset,
		Filters: []docs.Filter{{Field: docs.Prop("run_hours"), Op: docs.OpIs, Values: []string{"1200"}}},
	})
	if err != nil {
		t.Fatalf("search by number: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != two.ID {
		t.Fatalf("numeric property filter matched %d rows, want the pump", len(rows))
	}

	// is_empty has to mean "no row", not "empty string", because an unset
	// property has exactly one representation and it is absence.
	three, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: "Untagged"})
	rows, err = f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind:    docs.KindAsset,
		Filters: []docs.Filter{{Field: docs.Prop("asset_tag"), Op: docs.OpEmpty}},
	})
	if err != nil {
		t.Fatalf("search is_empty: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != three.ID {
		t.Fatalf("is_empty matched %d rows, want the untagged asset", len(rows))
	}
}

// Sorting on a number property has to order numerically. Stored as a quoted
// string it sorts 10 before 9, which is the bug the encoder exists to avoid and
// this is the sort that would expose it.
func TestSortOnANumericProperty(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	hours := f.seedProperty(t, "run_hours", "Run hours", docs.PropNumber, nil)
	for _, tc := range []struct {
		title string
		value string
	}{{"nine", `9`}, {"ten", `10`}, {"ninety", `90`}} {
		d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: tc.title})
		if err != nil {
			t.Fatalf("create %s: %v", tc.title, err)
		}
		f.setProperty(t, d.ID, hours, tc.value)
	}

	rows, err := f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind:  docs.KindAsset,
		Sorts: []docs.Sort{{Field: docs.Prop("run_hours")}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Title
	}
	want := []string{"nine", "ten", "ninety"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted %v, want %v (a string sort would put ten before nine)", got, want)
		}
	}
}

// A board grouped by a select property. The group value comes back unwrapped,
// so a column header reads CV-3 rather than "CV-3" with the quotes.
func TestBoardGroupsByACustomSelectProperty(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	crit := f.seedProperty(t, "criticality", "Criticality", docs.PropSelect,
		[]string{"low", "medium", "high", "safety"})

	high, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: "Press"})
	low, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: "Fan"})
	unset, _ := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindAsset, Title: "Cart"})
	f.setProperty(t, high.ID, crit, `"safety"`)
	f.setProperty(t, low.ID, crit, `"low"`)

	rows, err := f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind: docs.KindAsset, GroupBy: docs.Prop("criticality"),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	byID := map[string]string{}
	for _, r := range rows {
		byID[r.ID] = r.Group
	}
	if byID[high.ID] != "safety" {
		t.Errorf("group for the press is %q, want safety", byID[high.ID])
	}
	if byID[low.ID] != "low" {
		t.Errorf("group for the fan is %q, want low", byID[low.ID])
	}
	if byID[unset.ID] != "" {
		t.Errorf("group for the unset asset is %q, want empty", byID[unset.ID])
	}

	def, err := f.svc.PropertyDefByKey(ctx, f.org, "criticality")
	if err != nil {
		t.Fatalf("load def: %v", err)
	}
	groups := docs.GroupRows(rows, def.Options, nil)
	// Every declared option is a column, including the empty ones, because an
	// empty column is where a card gets dragged to.
	if len(groups) < len(def.Options) {
		t.Fatalf("board has %d columns for %d options", len(groups), len(def.Options))
	}
	if groups[0].Value != "low" {
		t.Errorf("first column is %q, want the first declared option", groups[0].Value)
	}
	// The unset column comes last and only because something is in it.
	last := groups[len(groups)-1]
	if last.Value != "" || len(last.Rows) != 1 {
		t.Errorf("last column is %q with %d rows, want the unset one with the cart",
			last.Value, len(last.Rows))
	}
}

// A field nobody defined is dropped rather than becoming SQL. The values go
// through bind parameters, so the only way a name could reach the statement is
// through the column table, and this proves nothing else does.
func TestUnknownAndHostileFieldsAreDroppedNotExecuted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Still here",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, name := range []string{
		"nonexistent",
		"d.title; DROP TABLE documents",
		"1=1 OR d.org_id IS NOT NULL",
	} {
		q := docs.Query{
			Kind:    docs.KindWorkOrder,
			Filters: []docs.Filter{{Field: docs.Col(name), Op: docs.OpIs, Values: []string{"x"}}},
			Sorts:   []docs.Sort{{Field: docs.Col(name)}},
		}
		rows, err := f.svc.Search(ctx, f.org, alice, q)
		if err != nil {
			t.Fatalf("a bad field name produced a database error rather than being dropped: %v", err)
		}
		if len(rows) != 1 {
			t.Errorf("filtering on %q returned %d rows, want the unfiltered 1", name, len(rows))
		}
	}

	// And the table is still there, which is the point of saying it out loud.
	var exists bool
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema='public' AND table_name='documents')`).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if !exists {
		t.Fatal("the documents table is gone, so a field name reached the statement")
	}
}

// A value that will not parse makes its filter vanish, rather than binding a
// zero that quietly matches rows nobody asked for.
func TestAnUnparseableValueDropsItsFilter(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Priority zero",
		Task: &docs.TaskFields{Status: docs.StatusTodo, Priority: 0},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	rows, err := f.svc.Search(ctx, f.org, alice, docs.Query{
		Kind:    docs.KindWorkOrder,
		Filters: []docs.Filter{{Field: docs.Col("priority"), Op: docs.OpIs, Values: []string{"urgent"}}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("a junk priority matched %d rows; if it had bound zero it would have matched the one", len(rows))
	}
}

// Board drag: the card moved column, so the grouped field takes that value.
func TestSetFieldMovesACardBetweenColumns(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Bearing",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := f.svc.SetField(ctx, f.org, alice, d.ID, docs.Col("status"), docs.StatusInProgress); err != nil {
		t.Fatalf("move to in_progress: %v", err)
	}
	got, _ := f.svc.Get(ctx, f.org, alice, d.ID)
	if got.Task.Status != docs.StatusInProgress {
		t.Errorf("status is %q after the drag", got.Task.Status)
	}
	// The derived timestamps still follow the status, because the drag goes
	// through the same Update as the form does rather than writing the column.
	if got.Task.StartedAt == nil {
		t.Error("started_at was not set, so the drag bypassed the lifecycle rules")
	}

	// A field a board cannot honour is refused rather than silently ignored.
	if err := f.svc.SetField(ctx, f.org, alice, d.ID, docs.Col("kind"), "asset"); err == nil {
		t.Error("dragging changed the kind, which no board offers")
	}
	if err := f.svc.SetField(ctx, f.org, alice, d.ID, docs.Col("status"), "finished"); err == nil {
		t.Error("an invalid status was accepted")
	}

	// Dragging a card onto a custom property column writes the property.
	crit := f.seedProperty(t, "criticality", "Criticality", docs.PropSelect, []string{"low", "high"})
	_ = crit
	if err := f.svc.SetField(ctx, f.org, alice, d.ID, docs.Prop("criticality"), "high"); err != nil {
		t.Fatalf("move by property: %v", err)
	}
	props, err := f.svc.PropertiesFor(ctx, f.org, docs.KindWorkOrder, d.ID)
	if err != nil {
		t.Fatalf("read properties: %v", err)
	}
	var found string
	for _, p := range props {
		if p.Key == "criticality" {
			found = p.Value
		}
	}
	if found != "high" {
		t.Errorf("criticality is %q after dragging into the high column", found)
	}
}

// The URL is the view. A query that cannot survive a round trip through the
// address bar is a filtered list nobody can send to a colleague.
func TestQuerySurvivesTheAddressBar(t *testing.T) {
	original := docs.Query{
		Kind: docs.KindWorkOrder,
		Filters: []docs.Filter{
			{Field: docs.Col("status"), Op: docs.OpAnyOf, Values: []string{"todo", "blocked"}},
			{Field: docs.Prop("asset_tag"), Op: docs.OpIs, Values: []string{"CV-3"}},
			{Field: docs.Col("due"), Op: docs.OpNotEmpty},
		},
		Sorts:   []docs.Sort{{Field: docs.Col("priority"), Desc: true}, {Field: docs.Prop("run_hours")}},
		GroupBy: docs.Prop("criticality"),
		Columns: []docs.Field{docs.Col("title"), docs.Prop("asset_tag")},
	}

	encoded := original.Encode().Encode()
	parsed, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatalf("the encoding is not a valid query string: %v", err)
	}
	back := docs.ParseQuery(parsed)

	if back.Kind != original.Kind {
		t.Errorf("kind became %q", back.Kind)
	}
	if len(back.Filters) != len(original.Filters) {
		t.Fatalf("%d filters survived out of %d: %+v", len(back.Filters), len(original.Filters), back.Filters)
	}
	for i, f := range original.Filters {
		if back.Filters[i].Field != f.Field || back.Filters[i].Op != f.Op {
			t.Errorf("filter %d became %+v", i, back.Filters[i])
		}
	}
	if len(back.Sorts) != 2 || !back.Sorts[0].Desc || back.Sorts[1].Field != docs.Prop("run_hours") {
		t.Errorf("sorts became %+v", back.Sorts)
	}
	if back.GroupBy != original.GroupBy {
		t.Errorf("group by became %+v", back.GroupBy)
	}
	if len(back.Columns) != 2 || back.Columns[1] != docs.Prop("asset_tag") {
		t.Errorf("columns became %+v", back.Columns)
	}
}

// Offset paging needs a total order or rows repeat and rows vanish. Every sort
// ends with the id for exactly that reason, and a tie on the sort key is where
// it shows up.
func TestPagingIsStableWhenTheSortKeyTies(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// Same priority on all of them, so the sort key cannot separate any two.
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindTask, Title: "tied",
			Task: &docs.TaskFields{Status: docs.StatusTodo, Priority: 1},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	seen := map[string]bool{}
	for offset := 0; offset < n; offset += 4 {
		rows, err := f.svc.Search(ctx, f.org, alice, docs.Query{
			Kind:   docs.KindTask,
			Sorts:  []docs.Sort{{Field: docs.Col("priority"), Desc: true}},
			Limit:  4,
			Offset: offset,
		})
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("row %s appeared on two pages, so the ordering is not total", r.ID)
			}
			seen[r.ID] = true
		}
	}
	if len(seen) != n {
		t.Errorf("paged through %d of %d rows", len(seen), n)
	}
}

// The GIN index is only reachable through containment, which was measured
// rather than assumed. This asserts the planner actually uses it, because an
// index that is never chosen is a write cost with no read benefit.
func TestPropertyFilterUsesTheIndex(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// One connection for the whole thing, because the planner setting is
	// per-session and a pool hands out whichever connection is free.
	conn, err := f.pools.Web.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	// Force the choice. On a handful of rows a sequential scan is genuinely
	// cheaper and the planner is right to take it, so the question being asked
	// is "can this index answer the query", not "is it worth it today".
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("steer the planner: %v", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "RESET enable_seqscan") }()

	rows, err := conn.Query(ctx, `
		EXPLAIN SELECT 1 FROM property_values pv
		WHERE pv.def_id = gen_random_uuid() AND pv.value @> '"CV-3"'::jsonb`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}

	if !strings.Contains(plan.String(), "property_values_def_value_gin") {
		t.Errorf("a property filter cannot use the GIN index, so every custom field "+
			"is a scan. Plan:\n%s", plan.String())
	}
}
