package docs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// A customer is a folder, its jobs are folders beneath it, and the history is
// everything under the customer by the day it happened.
func TestServiceHistoryLaysACustomersWorkOutByDay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	mk := func(name string, parent *string) string {
		var id string
		if err := f.pools.Web.QueryRow(ctx, `
			INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by)
			VALUES (gen_random_uuid(), $1, $2, $3, 'folder', $4) RETURNING id::text`,
			f.org, parent, name, f.alice).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := f.az.Grant(ctx, f.org, core.Ref{Type: core.EntitySpace, ID: id},
			core.Subject{Kind: core.SubjectPrincipal, ID: f.alice}, core.AccessEdit, f.alice); err != nil {
			t.Fatal(err)
		}
		return id
	}
	svc := mk("White Team Service", nil)
	acme := mk("Acme", &svc)
	job := mk("ACM0001 Line 3", &acme)
	other := mk("Other Co", &svc)

	body := "Replace the drive.\n\n---\n\n- Assigned in ClickUp to Marco Dell, Alice\n- ClickUp list: ACM0001 Line 3\n- Original: https://app.clickup.com/t/abc"
	wo, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindWorkOrder, Title: "Drive fault",
		Body: body, SpaceID: &job})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindWorkOrder, Title: "Not Acme's",
		SpaceID: &other}); err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2025, 3, 3, 15, 0, 0, 0, time.UTC)
	day2 := time.Date(2025, 3, 5, 15, 0, 0, 0, time.UTC)
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`UPDATE documents SET created_at = $2 WHERE id = $1`, wo.ID, day1)
	exec(`UPDATE task_fields SET status = 'done', completed_at = $2 WHERE document_id = $1`, wo.ID, day2)
	var c1 string
	if err := f.pools.Web.QueryRow(ctx, `
		INSERT INTO messages (id, org_id, entity_type, entity_id, author_id, body, created_at)
		VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'Found a failed contactor.', $4) RETURNING id::text`,
		f.org, wo.ID, f.bob, day1.Add(time.Hour)).Scan(&c1); err != nil {
		t.Fatal(err)
	}
	// A reply: the thread view leaves these out, the history must not.
	exec(`INSERT INTO messages (id, org_id, entity_type, entity_id, author_id, body, parent_id, created_at)
	      VALUES (gen_random_uuid(), $1, 'document', $2, $3, 'Replaced it.', $4, $5)`,
		f.org, wo.ID, f.bob, c1, day2.Add(-time.Hour))
	exec(`INSERT INTO time_entries (id, org_id, document_id, principal_id, started_at, ended_at, minutes, note)
	      VALUES (gen_random_uuid(), $1, $2, $3, $4, $4, 90, 'travel')`, f.org, wo.ID, f.alice, day1)
	// The import's one total per task, dated the day it was imported.
	exec(`INSERT INTO time_entries (id, org_id, document_id, principal_id, started_at, ended_at, minutes, note)
	      VALUES (gen_random_uuid(), $1, $2, $3, $4, $4, 600, 'Imported from ClickUp: total time tracked there')`,
		f.org, wo.ID, f.alice, time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))

	recs, err := f.svc.ServiceHistory(ctx, f.org, alice, docs.ServiceFilter{SpaceID: acme})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != wo.ID {
		t.Fatalf("want exactly Acme's work order, got %d: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.Job != "ACM0001 Line 3" || r.SpacePath != "White Team Service / Acme / ACM0001 Line 3" {
		t.Fatalf("job/path = %q / %q", r.Job, r.SpacePath)
	}
	if r.Body != "Replace the drive." || strings.Contains(r.Body, "ClickUp") {
		t.Fatalf("import notes leaked into the body: %q", r.Body)
	}
	if len(r.Assignees) != 2 || r.Assignees[0] != "Marco Dell" {
		t.Fatalf("assignees should be the names ClickUp recorded: %v", r.Assignees)
	}
	kinds := map[string]int{}
	for _, e := range r.Events {
		kinds[e.Kind]++
	}
	if kinds["opened"] != 1 || kinds["completed"] != 1 || kinds["comment"] != 2 || kinds["time"] != 1 || kinds["time_total"] != 1 {
		t.Fatalf("events = %v (a reply must count)", kinds)
	}
	// The imported total lands on the completion day, never on the import day.
	for _, e := range r.Events {
		if e.Kind == "time_total" && (e.Day != "2025-03-05" || e.Minutes != 600) {
			t.Fatalf("imported total on %s for %d minutes, want 2025-03-05 and 600", e.Day, e.Minutes)
		}
		if e.Day >= "2026-08-27" {
			t.Fatalf("an event landed on the import day: %+v", e)
		}
	}
	for i := 1; i < len(r.Events); i++ {
		if r.Events[i].At.Before(r.Events[i-1].At) {
			t.Fatal("events are not in time order")
		}
	}

	// A range keeps only the days in it.
	rng, err := f.svc.ServiceHistory(ctx, f.org, alice, docs.ServiceFilter{SpaceID: acme, From: "2025-03-05", To: "2025-03-06"})
	if err != nil || len(rng) != 1 {
		t.Fatalf("range: %d records (%v)", len(rng), err)
	}
	for _, e := range rng[0].Events {
		if e.Day < "2025-03-05" {
			t.Fatalf("event outside the range: %+v", e)
		}
	}
	none, err := f.svc.ServiceHistory(ctx, f.org, alice, docs.ServiceFilter{SpaceID: acme, From: "2026-01-01"})
	if err != nil || len(none) != 0 {
		t.Fatalf("later range: %d records (%v)", len(none), err)
	}

	// A reader with no access to the customer gets nothing, not an error.
	dave, err := f.svc.ServiceHistory(ctx, f.org, f.subj(t, f.dave), docs.ServiceFilter{SpaceID: acme})
	if err != nil || len(dave) != 0 {
		t.Fatalf("dave sees %d records (%v)", len(dave), err)
	}
	if _, err := f.svc.ServiceHistory(ctx, f.org, alice, docs.ServiceFilter{}); err == nil {
		t.Fatal("a history of nowhere should be refused")
	}
}
