package api_test

// A Cascade flow, or any other receiver, hears about CRM changes: a deal, a company, a person, and what is owed on them.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/api"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

func TestCRMChangesReachAWebhookReceiver(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_test_apicrmhooks")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatal(err)
	}
	org, who, hook := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Hooks Test')`, org, "wh-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Pat')`, who, org)
	exec(`INSERT INTO webhooks (id, org_id, url, secret, events, enabled, created_by) VALUES ($1,$2,'http://127.0.0.1:1/hook','s','{}',true,$3)`, hook, org, who)

	queue := jobs.NewQueue(pools.Jobs, time.Minute)
	relay := jobs.NewRelay(pools.Jobs, log)
	worker := jobs.NewWorker(queue, relay, log, jobs.WorkerOptions{ID: "hooks-test"})
	api.NewDispatcher(pools.Jobs, queue, log).Register(worker)
	relay.Route(api.Routes()...)

	az := authz.New(pools.Web)
	svc := crm.New(pools.Web, az)
	actor, _ := az.SubjectsFor(ctx, org, who)
	_, err = svc.CreateCompany(ctx, org, actor, crm.Company{Name: "Hook Co", Kind: crm.KindProspect})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = svc.CreateContact(ctx, org, actor, crm.Contact{Name: "Hook Person", Email: "p@hook.example"})
	d, _ := svc.CreateDeal(ctx, org, actor, crm.Deal{Name: "Hook deal"})
	if err := svc.MoveDeal(ctx, org, actor, d.ID, crm.StageProposal, nil); err != nil {
		t.Fatal(err)
	}
	fu, err := svc.AddFollowup(ctx, org, actor, crm.FollowupParams{Title: "Ring them", Target: d.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetFollowupDone(ctx, org, actor, fu.ID, true); err != nil {
		t.Fatal(err)
	}

	// Fan every event out to the receiver (the delivery itself goes to a closed port and is not the point).
	for round := 0; round < 200; round++ {
		if _, err := relay.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		job, err := queue.Claim(ctx, "t", []string{api.FanKind})
		if errors.Is(err, jobs.ErrNoJob) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.RunOne(ctx, job); err != nil {
			t.Fatal(err)
		}
		_ = queue.Complete(ctx, job.ID)
	}
	got := map[string]int{}
	rows, err := pools.Web.Query(ctx, `SELECT entity_type || ':' || event, count(*) FROM webhook_deliveries WHERE org_id=$1 GROUP BY 1`, org)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var n int
		_ = rows.Scan(&k, &n)
		got[k] = n
	}
	rows.Close()
	for _, want := range []string{"company:created", "contact:created", "deal:created", "deal:updated"} {
		if got[want] == 0 {
			t.Errorf("the receiver never heard %s (heard %v)", want, got)
		}
	}
	// A deal moved, a follow-up added, and a follow-up done are three updates of the deal.
	if got["deal:updated"] != 3 {
		t.Errorf("deal updates heard = %d, want 3 (the move, the follow-up added, the follow-up done)", got["deal:updated"])
	}
}
