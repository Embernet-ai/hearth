package stores_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/rules"
)

type outbox struct{ evs []jobs.Event }

func (o outbox) RecentEvents(_ context.Context, _, event string, limit int) ([]jobs.Event, error) {
	return o.evs, nil
}

func TestRulesLiveInEmberOPSAndRunsFireOnce(t *testing.T) {
	e := newEnv(t)
	s := &stores.Rules{Base: e.base, Dir: e.dir, Events: outbox{evs: []jobs.Event{{ID: 7}}}}
	ctx := e.as("svc-tok")

	if _, err := s.Save(ctx, org, ann, rules.Rule{Name: "x"}); !errors.Is(err, rules.ErrInvalid) {
		t.Fatalf("an empty rule: %v", err)
	}
	r, err := s.Save(ctx, org, ann, rules.Rule{Name: " Page me ", Trigger: rules.Trigger{Event: "status_changed", Kind: "work_order"},
		Conditions: []rules.Condition{{Field: docs.Field{Column: "status"}, Op: "is", Values: []string{"done"}}},
		Actions:    []rules.Action{{Kind: rules.ActionComment, Text: "done!"}}})
	if err != nil {
		t.Fatal(err)
	}
	stored := e.f.All("Hearth Rule")
	if len(stored) != 1 || stored[0]["rule_name"] != "Page me" || stored[0]["trigger_event"] != "status_changed" ||
		stored[0]["trigger_kind"] != "work_order" || stored[0]["enabled"] != 0.0 {
		t.Fatalf("rule = %v", stored)
	}
	got, err := s.Get(ctx, org, r.ID)
	if err != nil || got.Name != "Page me" || got.Enabled || len(got.Conditions) != 1 || got.Conditions[0].Op != "is" ||
		len(got.Actions) != 1 || got.Actions[0].Text != "done!" || got.Trigger.Kind != "work_order" {
		t.Fatalf("rule back = %+v %v", got, err)
	}
	// Rules arrive disabled, so they match nothing until switched on.
	if m, _ := s.Matching(ctx, org, "status_changed"); len(m) != 0 {
		t.Fatalf("a disabled rule matched: %v", m)
	}
	if err := s.SetEnabled(ctx, org, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Matching(ctx, org, "status_changed"); len(m) != 1 {
		t.Fatalf("enabled rule did not match: %v", m)
	}
	if m, _ := s.Matching(ctx, org, "created"); len(m) != 0 {
		t.Fatalf("matched the wrong event: %v", m)
	}

	// Fires once per (rule, event).
	run := &rules.Run{RuleID: r.ID, OrgID: org, EventID: 7, EntityType: "document", EntityID: taskID, Matched: true}
	if first, err := s.RecordRun(ctx, run); err != nil || !first {
		t.Fatalf("first delivery: %v %v", first, err)
	}
	if first, err := s.RecordRun(ctx, run); err != nil || first {
		t.Fatalf("redelivery recorded again: %v %v", first, err)
	}
	if err := s.FinishRun(ctx, r.ID, 7, []string{"comment"}, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.All("Hearth Rule Run")); n != 1 {
		t.Fatalf("%d runs", n)
	}
	// A rehearsal is recorded every time and cleared before the next.
	for i := 0; i < 2; i++ {
		if err := s.ClearDryRuns(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordRun(ctx, &rules.Run{RuleID: r.ID, OrgID: org, EventID: 7, EntityType: "document", EntityID: taskID, DryRun: true, Matched: true}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, org, r.ID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	var real *rules.Run
	for _, x := range runs {
		if !x.DryRun {
			real = x
		}
	}
	if real == nil || real.RuleID != r.ID || real.RuleName != "Page me" || real.EntityID != taskID || !real.Matched || len(real.ActionsTaken) != 1 || real.EventID != 7 {
		t.Fatalf("run = %+v", real)
	}
	evs, _ := s.RecentEvents(ctx, org, "status_changed", 10)
	if len(evs) != 1 {
		t.Fatal("rehearsal events come from Hearth's outbox")
	}

	// One call reads every run of a page, whatever the rule count.
	before := len(e.f.Calls)
	if _, err := s.Runs(ctx, org, "", 50); err != nil {
		t.Fatal(err)
	}
	if n := len(e.f.Calls) - before; n > 3 {
		t.Fatalf("%d calls to list runs", n)
	}

	if err := s.Delete(ctx, org, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, org, r.ID); !errors.Is(err, rules.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
