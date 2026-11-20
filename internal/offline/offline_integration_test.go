package offline_test

// The record of what the phones did, against a real Postgres.
//
// The claim being tested is exactly once, and it is a claim about a unique
// index rather than about the code around it, so it cannot be tested without
// the database that holds the index.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/offline"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fx struct {
	svc   *offline.Service
	pools *pg.Pools
	org   string
	dana  string
	sam   string
	// doc is a real document id, because the entity column is a uuid with a
	// foreign key on nothing and a page joins to it.
	doc string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
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

	f := &fx{
		svc: offline.New(pools.Web), pools: pools,
		org: uuid.NewString(), dana: uuid.NewString(), sam: uuid.NewString(),
		doc: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Offline Test')`,
		f.org, "of-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`,
		f.dana, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Sam')`,
		f.sam, f.org)
	exec(`INSERT INTO documents (id, org_id, kind, title, created_by, updated_by, owner_id)
	      VALUES ($1,$2,'work_order','Conveyor 3 bearing',$3,$3,$3)`, f.doc, f.org, f.dana)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM offline_ops WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) op(key string) offline.Op {
	return offline.Op{
		Key:  key,
		Path: "/documents/" + f.doc + "/time",
		Body: "minutes=40&note=stripped+the+guard",
		At:   time.Now().Add(-30 * time.Minute),
	}
}

// The whole reason the key exists.
//
// A phone at the edge of a signal sends a batch, loses the reply, and sends it
// again. Forty minutes logged twice is not caught by anybody until the
// invoice.
func TestTheSameKeyIsOnlyEverDoneOnce(t *testing.T) {
	f, ctx := setup(t), context.Background()

	id, prior, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-1"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if prior != nil {
		t.Fatal("the first claim of a fresh key found a prior record")
	}
	if id == "" {
		t.Fatal("the first claim did not say which row it took")
	}
	if err := f.svc.Finish(ctx, id, offline.StatusApplied, 303, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// The reply never arrived, so the phone sends the same thing again.
	again, prior, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-1"))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if again != "" {
		t.Error("the second arrival of the same key was allowed to do the work " +
			"again, which is forty minutes logged twice")
	}
	if prior == nil {
		t.Fatal("the second arrival got no answer to read back")
	}
	if prior.Status != offline.StatusApplied || prior.Code != 303 {
		t.Errorf("the answer read back is %s/%d, want applied/303", prior.Status, prior.Code)
	}

	// And the same key from a different person is a different operation. Two
	// phones can generate the same key and neither is the other's.
	other, prior, err := f.svc.Claim(ctx, f.org, f.sam, f.op("k-1"))
	if err != nil {
		t.Fatalf("claim as somebody else: %v", err)
	}
	if other == "" || prior != nil {
		t.Error("one person's key blocked another person's, and a key is only " +
			"unique within the phone that made it")
	}
}

// A replay that started and never recorded its outcome is a question about the
// world. Retrying it on the assumption that a repeat is harmless is exactly
// what the key exists to prevent, so it becomes a decision for a person.
func TestAReplayThatNeverFinishedBecomesAQuestion(t *testing.T) {
	f, ctx := setup(t), context.Background()

	if _, _, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-2")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// No Finish. The process died mid batch.

	id, prior, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-2"))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if id != "" {
		t.Error("a half finished operation was quietly redone")
	}
	if prior == nil || prior.Status != offline.StatusUnknown {
		t.Fatalf("the record is %+v, want unknown", prior)
	}

	waiting, err := f.svc.Undecided(ctx, f.org, f.dana)
	if err != nil {
		t.Fatalf("undecided: %v", err)
	}
	if len(waiting) != 1 {
		t.Fatalf("%d operations are waiting on a person, want 1", len(waiting))
	}
	if n, err := f.svc.Count(ctx, f.org, f.dana); err != nil || n != 1 {
		t.Errorf("the count is %d (%v), want 1", n, err)
	}
	// And it is not Sam's problem.
	if n, err := f.svc.Count(ctx, f.org, f.sam); err != nil || n != 0 {
		t.Errorf("somebody else's unanswered question showed up as %d", n)
	}
}

func TestAConflictWaitsForAPersonAndThenStops(t *testing.T) {
	f, ctx := setup(t), context.Background()

	id, _, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-3"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.svc.Finish(ctx, id, offline.StatusConflict, 409,
		"you edited version 2, current is 4"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	waiting, err := f.svc.Undecided(ctx, f.org, f.dana)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("%d waiting (%v), want 1", len(waiting), err)
	}
	if waiting[0].Message == "" {
		t.Error("the conflict carries no reason, so the page has nothing to show")
	}
	// It survived a round trip through the database with the phone's own clock
	// on it, which is what lets the page say how long the signal was out.
	if !waiting[0].Late() {
		t.Error("an operation queued half an hour ago did not read as late")
	}

	// Somebody answers.
	if err := f.svc.Decide(ctx, f.dana, id, offline.StatusDiscarded, "not mine"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if n, _ := f.svc.Count(ctx, f.org, f.dana); n != 0 {
		t.Errorf("%d still waiting after it was answered", n)
	}
	// And answering it twice is not a thing. A page left open on two phones
	// would otherwise let the second tap undo the first decision.
	if err := f.svc.Decide(ctx, f.dana, id, offline.StatusResolved, ""); !errors.Is(err, offline.ErrNotFound) {
		t.Errorf("answering a settled operation gave %v, want not found", err)
	}
}

// Somebody else's unresolved edit is not a decision anybody can make for them.
func TestOnlyThePersonWhoQueuedItCanAnswerIt(t *testing.T) {
	f, ctx := setup(t), context.Background()

	id, _, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-4"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.svc.Finish(ctx, id, offline.StatusConflict, 409, "somebody changed it"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if err := f.svc.Decide(ctx, f.sam, id, offline.StatusDiscarded, ""); !errors.Is(err, offline.ErrNotFound) {
		t.Errorf("somebody else threw away Dana's conflict: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.sam, id); !errors.Is(err, offline.ErrNotFound) {
		t.Errorf("somebody else could read Dana's queued action: %v", err)
	}
	if n, _ := f.svc.Count(ctx, f.org, f.dana); n != 1 {
		t.Error("Dana's conflict did not survive somebody else trying to answer it")
	}
}

// The record is not history. Settled rows go after ninety days, and a question
// nobody answered stays forever, because the phone it came from is not a
// reliable place to keep one.
func TestTidyingUpKeepsTheQuestions(t *testing.T) {
	f, ctx := setup(t), context.Background()

	settled, _, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-5"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.svc.Finish(ctx, settled, offline.StatusApplied, 303, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	asked, _, err := f.svc.Claim(ctx, f.org, f.dana, f.op("k-6"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.svc.Finish(ctx, asked, offline.StatusConflict, 409, "changed"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Age both of them past the horizon.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE offline_ops SET received_at = now() - interval '200 days' WHERE org_id = $1`,
		f.org); err != nil {
		t.Fatalf("age the rows: %v", err)
	}

	n, err := f.svc.Prune(ctx, f.org, offline.Keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want exactly the settled one", n)
	}
	if _, err := f.svc.Get(ctx, f.dana, settled); !errors.Is(err, offline.ErrNotFound) {
		t.Error("a settled operation from two hundred days ago is still here")
	}
	if _, err := f.svc.Get(ctx, f.dana, asked); err != nil {
		t.Errorf("an unanswered question was tidied away: %v", err)
	}
}

// The list a person reads after a shift in a basement, answering "did my
// morning actually go in".
func TestRecentShowsWhatThePhoneSentAndInWhatOrder(t *testing.T) {
	f, ctx := setup(t), context.Background()

	for i, key := range []string{"r-1", "r-2", "r-3"} {
		op := f.op(key)
		op.At = time.Now().Add(-time.Duration(30-i) * time.Minute)
		id, _, err := f.svc.Claim(ctx, f.org, f.dana, op)
		if err != nil {
			t.Fatalf("claim %s: %v", key, err)
		}
		if err := f.svc.Finish(ctx, id, offline.StatusApplied, 303, ""); err != nil {
			t.Fatalf("finish %s: %v", key, err)
		}
	}
	recent, err := f.svc.Recent(ctx, f.org, f.dana, 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("%d rows, want 3", len(recent))
	}
	for _, r := range recent {
		if r.Kind != "time logged" {
			t.Errorf("a row is described as %q, and the page shows that word to a "+
				"person rather than showing them a URL", r.Kind)
		}
		if r.EntityID == nil || *r.EntityID != f.doc {
			t.Error("a row does not say which job it was about")
		}
	}
	// Newest first, because the question is about this morning.
	if recent[0].ReceivedAt.Before(recent[len(recent)-1].ReceivedAt) {
		t.Error("the list is oldest first")
	}
}

// A photograph's bytes are never kept here. They are in the object store once
// it lands, and a base64 copy of every photograph in a bookkeeping table is how
// a small table becomes the largest one.
func TestAPhotographLeavesNoCopyBehind(t *testing.T) {
	f, ctx := setup(t), context.Background()

	data := []byte("a photograph, near enough")
	id, _, err := f.svc.Claim(ctx, f.org, f.dana, offline.Op{
		Key: "p-1", Path: "/documents/" + f.doc + "/files", At: time.Now(),
		Body: "caption=cracked+weld",
		File: &offline.File{Name: "weld.jpg", ContentType: "image/jpeg",
			Data: data, Digest: sha256Hex(data)},
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err := f.svc.Get(ctx, f.dana, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Body != "" {
		t.Errorf("the row kept %q, and a bookkeeping table is not where a "+
			"photograph lives", rec.Body)
	}
	if rec.Kind != "a photograph" {
		t.Errorf("described as %q", rec.Kind)
	}
}

// A digest that does not match is refused before anything is written, so the
// key is still free for a phone that sends the photograph properly.
func TestATruncatedPhotographIsRefusedBeforeAnythingIsRecorded(t *testing.T) {
	f, ctx := setup(t), context.Background()

	data := []byte("a photograph, near enough")
	bad := offline.Op{
		Key: "p-2", Path: "/documents/" + f.doc + "/files", At: time.Now(),
		File: &offline.File{Name: "weld.jpg", ContentType: "image/jpeg",
			Data: data[:5], Digest: sha256Hex(data)},
	}
	if _, _, err := f.svc.Claim(ctx, f.org, f.dana, bad); !errors.Is(err, offline.ErrInvalid) {
		t.Fatalf("a truncated photograph gave %v, want invalid", err)
	}

	good := bad
	good.File = &offline.File{Name: "weld.jpg", ContentType: "image/jpeg",
		Data: data, Digest: sha256Hex(data)}
	id, prior, err := f.svc.Claim(ctx, f.org, f.dana, good)
	if err != nil {
		t.Fatalf("the second attempt with the same key failed: %v", err)
	}
	if id == "" || prior != nil {
		t.Error("the refused attempt took the key with it, so the photograph " +
			"that did arrive intact could never be sent")
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
