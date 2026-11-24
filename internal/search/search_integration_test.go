package search_test

// Search, against a real Postgres and, when one is configured, a real
// OpenSearch.
//
// The interesting checks are about what a caller CANNOT find. A search that
// returns too much is a disclosure, and it is the one bug in this area that
// nothing else in the system would catch, because every other read path goes
// through a grant filter in SQL and this one has to carry the answer inside the
// index instead.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/search"
)

type fx struct {
	pools  *pg.Pools
	docs   *docs.Service
	az     *authz.Service
	pg     *search.Postgres
	queue  *jobs.Queue
	relay  *jobs.Relay
	log    *slog.Logger
	org    string
	alice  string
	bob    string
	aliceS core.Subjects
	bobS   core.Subjects
}

func setup(t *testing.T) *fx {
	t.Helper()
	// A database of this package's own. Sharing one with every other
	// integration package meant this fixture's drain claimed jobs belonging
	// to another one, and the failure surfaced over there as work that
	// never happened. See internal/platform/pg/pgtest.
	dsn := pgtest.Database(t, "hearth_test_search")
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	f := &fx{
		pools: pools, az: az, docs: docs.New(pools.Web, az),
		pg:    search.NewPostgres(pools.Web),
		queue: jobs.NewQueue(pools.Jobs, time.Minute),
		relay: jobs.NewRelay(pools.Jobs, log), log: log,
		org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Search Test')`, f.org, "search-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	if f.aliceS, err = az.SubjectsFor(ctx, f.org, f.alice); err != nil {
		t.Fatalf("subjects: %v", err)
	}
	if f.bobS, err = az.SubjectsFor(ctx, f.org, f.bob); err != nil {
		t.Fatalf("subjects: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM search_state WHERE org_id=$1`,
			`DELETE FROM jobs WHERE org_id=$1`,
			`DELETE FROM jobs_dead WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM files WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

// cluster returns an OpenSearch engine when one is configured for the test run.
func cluster(t *testing.T) *search.OpenSearch {
	t.Helper()
	url := os.Getenv("TEST_OPENSEARCH_URL")
	if url == "" {
		t.Skip("TEST_OPENSEARCH_URL not set")
	}
	// An index per run, so two runs cannot see each other's documents and a
	// leftover mapping from an older schema cannot make a green test lie.
	os := search.NewOpenSearch(search.OpenSearchConfig{
		URL: url, Index: "hearth-test-" + uuid.NewString()[:8],
	})
	ctx := context.Background()
	if err := os.EnsureIndex(ctx); err != nil {
		t.Skipf("opensearch not reachable: %v", err)
	}
	return os
}

// A word typed into a body has to be findable. The Postgres engine is checked
// on its own here, because it is the one that has to work when nothing else
// does.
func TestAWordInABodyIsFindable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Line 3 conveyor faulting",
		Body: "Motor current spiking on start. Suspect the outboard bearing.",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	svc := search.New(nil, f.pg)
	res, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "outboard bearing"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("%d hits for a phrase in the body, want 1", len(res.Hits))
	}
	if res.Engine != "postgres" {
		t.Errorf("answered by %q", res.Engine)
	}
	// The snippet has to show the match, or a result list is a list of titles.
	if !strings.Contains(res.Hits[0].Snippet, "<mark>") {
		t.Errorf("no highlight in the snippet: %q", res.Hits[0].Snippet)
	}

	// Stemming, because somebody typing "bearings" means the same thing.
	res, err = svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "bearings"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Errorf("%d hits for a stemmed word, want 1", len(res.Hits))
	}
}

// The check that matters. Somebody with no grant gets zero hits for a document
// that definitely exists and definitely contains the word.
func TestNoGrantMeansNoHits(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	d, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
		Kind: docs.KindDoc, Title: "Rates for the Q3 contract",
		Body: "The renegotiated tarpaulin pricing is confidential.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	svc := search.New(nil, f.pg)

	mine, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "tarpaulin"})
	if err != nil {
		t.Fatalf("search as alice: %v", err)
	}
	if len(mine.Hits) != 1 {
		t.Fatalf("alice cannot find her own document, so this proves nothing")
	}

	theirs, err := svc.Search(ctx, f.org, f.bobS, search.Query{Text: "tarpaulin"})
	if err != nil {
		t.Fatalf("search as bob: %v", err)
	}
	if len(theirs.Hits) != 0 {
		t.Fatalf("bob found %d hits for a document he has no grant for", len(theirs.Hits))
	}

	// And granting makes it appear, so the zero above is the filter working
	// rather than the index being empty.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bobNow, err := f.az.SubjectsFor(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	theirs, err = svc.Search(ctx, f.org, bobNow, search.Query{Text: "tarpaulin"})
	if err != nil {
		t.Fatalf("search after grant: %v", err)
	}
	if len(theirs.Hits) != 1 {
		t.Errorf("bob still cannot find a document shared with him")
	}
}

// A super admin reaches everything, in every list and on every document page. Search
// asked the grants table instead, where an admin has no row for work other people
// created and assigned elsewhere, so the admin found nothing for what its creator
// found at once.
func TestASuperAdminFindsWhatNobodyGrantedThem(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
		Kind: docs.KindDoc, Title: "Rates for the Q3 contract",
		Body: "The renegotiated tarpaulin pricing is confidential.",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	svc := search.New(nil, f.pg)

	super := core.Subjects{{Kind: core.SubjectSuper}, {Kind: core.SubjectPrincipal, ID: f.bob}}
	got, err := svc.Search(ctx, f.org, super, search.Query{Text: "tarpaulin"})
	if err != nil {
		t.Fatalf("search as a super admin: %v", err)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("a super admin found %d hits for work somebody else created, want 1", len(got.Hits))
	}
	// And an ordinary person with the same principal still finds none: the bypass
	// is the super flag and nothing else.
	plain, err := svc.Search(ctx, f.org, f.bobS, search.Query{Text: "tarpaulin"})
	if err != nil {
		t.Fatalf("search as bob: %v", err)
	}
	if len(plain.Hits) != 0 {
		t.Fatalf("bob found %d hits with no grant", len(plain.Hits))
	}
}

// An empty subject set is nobody, not everybody. This is the sort of thing an
// unset variable does, and the consequence is the whole permission model gone.
func TestNoSubjectsFindsNothing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
		Kind: docs.KindDoc, Title: "Anything", Body: "distinctive word: xyzzy",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	svc := search.New(nil, f.pg)
	res, err := svc.Search(ctx, f.org, nil, search.Query{Text: "xyzzy"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("a caller with no subjects got %d hits", len(res.Hits))
	}
}

// Search has to keep answering when the cluster does not. The whole reason
// Postgres is maintained in parallel is this case.
func TestSearchStillAnswersWithTheClusterDown(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Gearbox", Body: "leaking from the input seal",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A cluster that is definitely not there. Port zero is unroutable rather
	// than slow, so this fails the way a stopped cluster does.
	dead := search.NewOpenSearch(search.OpenSearchConfig{
		URL: "http://127.0.0.1:1", Timeout: time.Second,
	})
	svc := search.New(dead, f.pg)

	res, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "leaking"})
	if err != nil {
		t.Fatalf("search returned an error rather than falling back: %v", err)
	}
	if res.Engine != "postgres" {
		t.Errorf("answered by %q, want the fallback", res.Engine)
	}
	if len(res.Hits) != 1 {
		t.Errorf("%d hits from the fallback, want 1", len(res.Hits))
	}
}

// The external index, when there is one. Everything above is about the floor;
// this is about the engine that does the real work.
func TestTheClusterIndexesAndFilters(t *testing.T) {
	f := setup(t)
	os := cluster(t)
	ctx := context.Background()

	pipeline := search.NewPipeline(f.pools.Jobs, os, nil, f.queue, f.log)
	f.relay.Route(search.Routes()...)

	d, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Compressor trip",
		Body: "The intercooler thermostat is intermittent.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	runJobs(t, ctx, f, pipeline)

	svc := search.New(os, f.pg)
	res, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "intercooler"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Engine != "opensearch" {
		t.Fatalf("answered by %q, want the cluster", res.Engine)
	}
	if len(res.Hits) != 1 || res.Hits[0].EntityID != d.ID {
		t.Fatalf("%d hits from the cluster: %+v", len(res.Hits), res.Hits)
	}

	// The permission filter lives INSIDE the indexed document, because the
	// cluster cannot join against the grants table. This is the check that it
	// is actually applied.
	theirs, err := svc.Search(ctx, f.org, f.bobS, search.Query{Text: "intercooler"})
	if err != nil {
		t.Fatalf("search as bob: %v", err)
	}
	if len(theirs.Hits) != 0 {
		t.Fatalf("the cluster gave bob %d hits for a document he cannot see", len(theirs.Hits))
	}

	// Sharing has to reach the index, or the answer is "I shared it and they
	// still cannot find it".
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	runJobs(t, ctx, f, pipeline)

	bobNow, _ := f.az.SubjectsFor(ctx, f.org, f.bob)
	theirs, err = svc.Search(ctx, f.org, bobNow, search.Query{Text: "intercooler"})
	if err != nil {
		t.Fatalf("search after grant: %v", err)
	}
	if len(theirs.Hits) != 1 {
		t.Errorf("sharing did not reach the index: bob has %d hits", len(theirs.Hits))
	}

	// And deleting has to remove it, or search keeps offering a ghost.
	if err := f.docs.Delete(ctx, f.org, f.aliceS, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	runJobs(t, ctx, f, pipeline)

	res, err = svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "intercooler"})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("a deleted document is still in the index: %d hits", len(res.Hits))
	}
}

// A backfill has to reproduce the same result set from Postgres alone. An index
// that cannot be rebuilt is a liability, and one that can be rebuilt but comes
// back different is worse.
func TestABackfillReproducesTheSameResults(t *testing.T) {
	f := setup(t)
	os := cluster(t)
	ctx := context.Background()

	pipeline := search.NewPipeline(f.pools.Jobs, os, nil, f.queue, f.log)
	f.relay.Route(search.Routes()...)

	for _, title := range []string{"Pump alpha", "Pump beta", "Conveyor gamma"} {
		if _, err := f.docs.Create(ctx, f.org, f.aliceS, docs.CreateParams{
			Kind: docs.KindAsset, Title: title,
			Body: "distinctive corpus marker zzyzx " + title,
		}); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
	}
	runJobs(t, ctx, f, pipeline)

	svc := search.New(os, f.pg)
	before, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "zzyzx"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(before.Hits) != 3 {
		t.Fatalf("%d hits before the backfill, want 3", len(before.Hits))
	}

	// Wipe the index out from under it, the way a restored cluster with an
	// empty volume would be.
	for _, h := range before.Hits {
		if err := os.Delete(ctx, h.EntityType, h.EntityID); err != nil {
			t.Fatalf("clear index: %v", err)
		}
	}
	if _, err := f.pools.Web.Exec(ctx, `DELETE FROM search_state WHERE org_id=$1`, f.org); err != nil {
		t.Fatalf("clear state: %v", err)
	}
	empty, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "zzyzx"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(empty.Hits) != 0 {
		t.Fatalf("the index was not actually emptied: %d hits", len(empty.Hits))
	}

	if err := search.StartBackfill(ctx, f.queue, f.org); err != nil {
		t.Fatalf("start backfill: %v", err)
	}
	runJobs(t, ctx, f, pipeline)

	after, err := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "zzyzx"})
	if err != nil {
		t.Fatalf("search after backfill: %v", err)
	}
	if len(after.Hits) != len(before.Hits) {
		t.Fatalf("the backfill produced %d hits, the original index had %d",
			len(after.Hits), len(before.Hits))
	}
	got := map[string]bool{}
	for _, h := range after.Hits {
		got[h.EntityID] = true
	}
	for _, h := range before.Hits {
		if !got[h.EntityID] {
			t.Errorf("the backfill lost %s (%s)", h.EntityID, h.Title)
		}
	}
}

// runJobs drains the outbox and runs everything it produced, until nothing is
// left. Synchronous rather than starting a worker, because a test that races a
// background goroutine is a test that fails on a busy machine.
func runJobs(t *testing.T, ctx context.Context, f *fx, p *search.Pipeline) {
	t.Helper()
	worker := jobs.NewWorker(f.queue, f.relay, f.log, jobs.WorkerOptions{ID: "test"})
	p.Register(worker)

	for round := 0; round < 200; round++ {
		if _, err := f.relay.Drain(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		job, err := f.queue.Claim(ctx, "test", worker.Kinds())
		if errors.Is(err, jobs.ErrNoJob) {
			// One more drain, in case a job that just ran published something.
			n, err := f.relay.Drain(ctx)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if n == 0 {
				return
			}
			continue
		}
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := worker.RunOne(ctx, job); err != nil {
			t.Fatalf("run %s: %v", job.Kind, err)
		}
		if err := f.queue.Complete(ctx, job.ID); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
	t.Fatal("the queue never emptied")
}

// Deals, and the notes and calls on a CRM timeline, are found, on the permission of the records they are about.
func TestDealsNotesAndCallsAreFoundOnTheirRecordsPermission(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM crm_activities WHERE org_id=$1`, `DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM deals WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`,
		} {
			_, _ = f.pools.Web.Exec(context.Background(), q, f.org)
		}
	})
	deal, contact, act := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO deals (id, org_id, name, stage, notes, created_by) VALUES ($1,$2,'Quincunx line upgrade','proposal','needs the zirconium kit',$3)`, deal, f.org, f.alice)
	exec(`INSERT INTO contacts (id, org_id, name, created_by) VALUES ($1,$2,'Pat Contact',$3)`, contact, f.org, f.alice)
	exec(`INSERT INTO crm_activities (id, org_id, kind, title, body, created_by) VALUES ($1,$2,'call','Call about pricing','they asked about the vermilion option',$3)`, act, f.org, f.alice)
	exec(`INSERT INTO crm_activity_targets (activity_id, target_type, target_id) VALUES ($1,'deal',$2),($1,'contact',$3)`, act, deal, contact)
	for _, e := range []struct{ typ, id string }{{"deal", deal}, {"contact", contact}} {
		exec(`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by) VALUES (gen_random_uuid(),$1,$2,$3,$4,'view',$5)`,
			f.org, e.typ, e.id, core.Subject{Kind: core.SubjectPrincipal, ID: f.alice}.Key(), f.alice)
	}

	svc := search.New(nil, f.pg)
	find := func(subj core.Subjects, text string) []search.Hit {
		t.Helper()
		r, err := svc.Search(ctx, f.org, subj, search.Query{Text: text})
		if err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
		return r.Hits
	}
	if h := find(f.aliceS, "quincunx"); len(h) != 1 || h[0].EntityType != "deal" || h[0].EntityID != deal {
		t.Errorf("a deal by its name: %+v", h)
	}
	if h := find(f.aliceS, "zirconium"); len(h) != 1 || h[0].EntityType != "deal" {
		t.Errorf("a deal by its notes: %+v", h)
	}
	h := find(f.aliceS, "vermilion")
	if len(h) != 1 || h[0].EntityType != search.EntityActivity || h[0].ParentID != "deal:"+deal {
		t.Errorf("a call by its note should lead to the deal: %+v", h)
	}
	if h := find(f.bobS, "vermilion"); len(h) != 0 {
		t.Errorf("bob found %d hits for a call about records he cannot see", len(h))
	}
	if h := find(f.bobS, "quincunx"); len(h) != 0 {
		t.Errorf("bob found a deal he has no grant for")
	}
	// Seeing any one record it is about is enough, as on the timeline.
	exec(`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by) VALUES (gen_random_uuid(),$1,'contact',$2,$3,'view',$4)`,
		f.org, contact, core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}.Key(), f.alice)
	bobNow, err := f.az.SubjectsFor(ctx, f.org, f.bob)
	if err != nil {
		t.Fatal(err)
	}
	if h := find(bobNow, "vermilion"); len(h) != 1 || h[0].ParentID != "deal:"+deal {
		t.Errorf("a call about a contact bob can see: %+v", h)
	}
	if h := find(bobNow, "quincunx"); len(h) != 0 {
		t.Errorf("seeing the contact must not show him the deal")
	}
	// A type filter narrows to one kind.
	if r, _ := svc.Search(ctx, f.org, f.aliceS, search.Query{Text: "vermilion", Types: []string{"deal"}}); len(r.Hits) != 0 {
		t.Errorf("asking for deals returned the call")
	}
}

// A super admin who is not on any team finds every deal, company and contact through search, and a person with no
// grants finds none. Verify, which re-checks hits from the cluster, keeps a super admin's documents too.
func TestASuperAdminOutsideSalesFindsEveryCRMRecord(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	admin, outsider := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$3,'user','Ada Admin'), ($2,$3,'user','Outside')`, admin, outsider, f.org)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, admin)
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM capability_grants WHERE org_id=$1`, `DELETE FROM deals WHERE org_id=$1`,
			`DELETE FROM contacts WHERE org_id=$1`, `DELETE FROM companies WHERE org_id=$1`, `DELETE FROM grants WHERE org_id=$1`} {
			_, _ = f.pools.Web.Exec(context.Background(), q, f.org)
		}
	})
	deal, contact, company := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO deals (id, org_id, name, stage, created_by) VALUES ($1,$2,'Phlogiston deal','new',$3)`, deal, f.org, f.alice)
	exec(`INSERT INTO contacts (id, org_id, name, created_by) VALUES ($1,$2,'Phlogiston Person',$3)`, contact, f.org, f.alice)
	exec(`INSERT INTO companies (id, org_id, name, kind, created_by) VALUES ($1,$2,'Phlogiston Works','prospect',$3)`, company, f.org, f.alice)
	for _, e := range []struct{ typ, id string }{{"deal", deal}, {"contact", contact}, {"company", company}} {
		exec(`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by) VALUES (gen_random_uuid(),$1,$2,$3,$4,'admin',$5)`,
			f.org, e.typ, e.id, core.Subject{Kind: core.SubjectPrincipal, ID: f.alice}.Key(), f.alice)
	}
	adminS, err := f.az.SubjectsFor(ctx, f.org, admin)
	if err != nil {
		t.Fatal(err)
	}
	outS, err := f.az.SubjectsFor(ctx, f.org, outsider)
	if err != nil {
		t.Fatal(err)
	}
	svc := search.New(nil, f.pg)
	got := map[string]bool{}
	r, err := svc.Search(ctx, f.org, adminS, search.Query{Text: "phlogiston"})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range r.Hits {
		got[h.EntityType] = true
	}
	for _, want := range []string{"deal", "contact", "company"} {
		if !got[want] {
			t.Errorf("a super admin outside sales did not find the %s: %+v", want, r.Hits)
		}
	}
	r, _ = svc.Search(ctx, f.org, outS, search.Query{Text: "phlogiston"})
	if len(r.Hits) != 0 {
		t.Errorf("a person with no grants found %d records", len(r.Hits))
	}
	// Verify keeps a super admin's document hit and drops a stranger's.
	doc := uuid.NewString()
	exec(`INSERT INTO documents (id, org_id, kind, title, owner_id, created_by, updated_by) VALUES ($1,$2,'doc','Phlogiston memo',$3,$3,$3)`, doc, f.org, f.alice)
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM documents WHERE id=$1`, doc) })
	hit := []search.Hit{{EntityType: "document", EntityID: doc}}
	if kept, err := f.pg.Verify(ctx, adminS, hit); err != nil || len(kept) != 1 {
		t.Errorf("verify for a super admin kept %d (%v), want 1", len(kept), err)
	}
	if kept, _ := f.pg.Verify(ctx, outS, hit); len(kept) != 0 {
		t.Errorf("verify for a stranger kept %d, want 0", len(kept))
	}
}
