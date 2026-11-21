package backup_test

// Backup and restore, against a real Postgres and a real object store.
//
// There is no useful unit test here. Every claim this package makes is about
// what a database and a bucket do, and a version of it with both mocked out
// would prove that the mocks agree with each other.
//
// The drill in scripts/restore-drill.sh goes further than this file does: it
// restores a whole deployment and runs the acceptance suite against the copy.
// What is here is the part that has to fail loudly rather than quietly, which
// is everything about an archive that is not what it says it is.

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/backup"
	"github.com/Embernet-ai/hearth/internal/platform/blob"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fx struct {
	pools *pg.Pools
	store *blob.Store
	org   string
	dana  string
	doc   string
	keys  []string
}

func setup(t *testing.T) *fx {
	t.Helper()
	// A database of this package's own.
	//
	// A backup archives EVERY table, so sharing a database with the other
	// integration packages meant archiving their rows mid-flight. The counts
	// then moved between writing an archive and comparing against it, and
	// TestCompareNoticesAMissingFile failed for a reason that had nothing to
	// do with a missing file.
	dsn := pgtest.Database(t, "hearth_test_backup")
	endpoint := os.Getenv("TEST_GARAGE_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_GARAGE_ENDPOINT not set")
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

	// A bucket of this test's own, so a backup of it is a backup of exactly
	// what this test put there and nothing another one is doing at the time.
	bucket := "backup-test-" + strings.ToLower(uuid.NewString()[:8])
	store, err := blob.Open(ctx, blob.Config{
		Endpoint:       endpoint,
		PublicEndpoint: endpoint,
		AccessKey:      os.Getenv("TEST_GARAGE_ACCESS_KEY"),
		SecretKey:      os.Getenv("TEST_GARAGE_SECRET_KEY"),
		Region:         envOr("TEST_GARAGE_REGION", "garage"),
		Bucket:         bucket,
	})
	if err != nil {
		t.Skipf("no object store: %v", err)
	}

	f := &fx{pools: pools, store: store, org: uuid.NewString(),
		dana: uuid.NewString(), doc: uuid.NewString()}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Backup Test')`,
		f.org, "bk-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`,
		f.dana, f.org)
	exec(`INSERT INTO documents (id, org_id, kind, title, created_by, updated_by, owner_id)
	      VALUES ($1,$2,'work_order','Conveyor 3 bearing',$3,$3,$3)`, f.doc, f.org, f.dana)
	// A per-org mail relay, so the table and its two foreign keys (org and the
	// principal who saved it) are exercised by a real backup and restore.
	exec(`INSERT INTO org_mail_settings (org_id, enabled, host, from_address, password_sealed, updated_by)
	      VALUES ($1, true, 'smtp.example.com', 'hearth@example.com', 'c2VhbGVk', $2)`, f.org, f.dana)

	for i := 0; i < 3; i++ {
		body := make([]byte, 4096)
		if _, err := rand.Read(body); err != nil {
			t.Fatalf("random: %v", err)
		}
		key := fmt.Sprintf("%s/file-%d.bin", f.org, i)
		if _, err := store.Put(ctx, key, bytes.NewReader(body),
			int64(len(body)), "application/octet-stream"); err != nil {
			t.Fatalf("put: %v", err)
		}
		f.keys = append(f.keys, key)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		for _, key := range f.keys {
			_ = store.Remove(ctx, key)
		}
		for _, q := range []string{
			`SELECT audit_purge_org($1)`,
			`DELETE FROM org_mail_settings WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// An archive has to describe itself accurately, and verifying has to be able to
// tell when it does not. Both directions, because a verifier that always passes
// is worse than no verifier: it is a reason to stop checking.
func TestAnArchiveVerifiesAndAModifiedOneDoesNot(t *testing.T) {
	f, ctx := setup(t), context.Background()

	var archive bytes.Buffer
	res, err := backup.Write(ctx, f.pools.Web, f.store, "testrev", &archive)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.Objects != 3 {
		t.Errorf("%d files in the archive, want the three that are in the bucket", res.Objects)
	}
	if res.Rows == 0 {
		t.Error("no rows in the archive at all")
	}
	if res.Manifest.Revision != "testrev" {
		t.Errorf("the manifest says revision %q", res.Manifest.Revision)
	}
	if res.Manifest.Migration == 0 {
		t.Error("the manifest does not record the schema version, so a restore " +
			"cannot refuse a database at a different one")
	}
	// Every table in the schema, discovered rather than listed. A hard-coded
	// list is wrong the first time somebody adds a migration.
	if len(res.Manifest.Tables) < 40 {
		t.Errorf("%d tables, which is fewer than this schema has", len(res.Manifest.Tables))
	}

	checked, err := backup.Verify(ctx, bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatalf("a fresh archive did not verify: %v", err)
	}
	if checked.Rows != res.Rows || checked.Objects != res.Objects {
		t.Errorf("verify counted %d rows and %d files, the write said %d and %d",
			checked.Rows, checked.Objects, res.Rows, res.Objects)
	}

	// One byte changed somewhere in the middle, which is what a bad disk or a
	// truncated transfer looks like.
	damaged := append([]byte(nil), archive.Bytes()...)
	middle := len(damaged) / 2
	damaged[middle] ^= 0xff
	if _, err := backup.Verify(ctx, bytes.NewReader(damaged)); err == nil {
		t.Error("an archive with a flipped byte verified, so verifying proves nothing")
	}

	// And an archive that stops before the end. The manifest is written last on
	// purpose: a truncated backup with a manifest at the front looks complete.
	cut := archive.Bytes()[:len(archive.Bytes())/2]
	_, err = backup.Verify(ctx, bytes.NewReader(cut))
	if !errors.Is(err, backup.ErrIncomplete) && err == nil {
		t.Error("a truncated archive verified")
	}
}

// Restoring over a database with anything in it would mix two deployments
// together, and the resulting mess is indistinguishable from a corrupt backup.
func TestRestoringOverSomethingIsRefused(t *testing.T) {
	f, ctx := setup(t), context.Background()

	var archive bytes.Buffer
	if _, err := backup.Write(ctx, f.pools.Web, f.store, "testrev", &archive); err != nil {
		t.Fatalf("write: %v", err)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "archive-*.tar")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	// Closed explicitly: on Windows the directory cannot be removed while the
	// file in it is open, and the cleanup failure reads as a test failure.
	defer tmp.Close()
	if _, err := tmp.Write(archive.Bytes()); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind: %v", err)
	}

	// The database this archive came from is, by definition, not empty.
	_, err = backup.Restore(ctx, f.pools.Web, f.store, tmp)
	if err == nil {
		t.Fatal("a restore into a populated database was allowed, which mixes " +
			"two deployments together")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("the refusal was %v, and it has to say why", err)
	}
}

// A restore reads the archive twice, so it needs a file rather than a stream.
// Said plainly rather than by reading a whole deployment into memory to find
// out.
func TestRestoringNeedsSomethingItCanRewind(t *testing.T) {
	f, ctx := setup(t), context.Background()

	var archive bytes.Buffer
	if _, err := backup.Write(ctx, f.pools.Web, f.store, "testrev", &archive); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A reader with no Seek on it at all.
	stream := struct{ io.Reader }{bytes.NewReader(archive.Bytes())}
	if _, err := backup.Restore(ctx, f.pools.Web, f.store, stream); err == nil {
		t.Error("a restore from an unseekable stream was accepted")
	}
}

// Compare is what turns "the restore finished" into "the restore is right", so
// it has to notice when it is not.
func TestCompareNoticesAMissingFile(t *testing.T) {
	f, ctx := setup(t), context.Background()

	var archive bytes.Buffer
	res, err := backup.Write(ctx, f.pools.Web, f.store, "testrev", &archive)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	problems, err := backup.Compare(ctx, f.pools.Web, f.store, &res.Manifest)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("a deployment did not match the archive taken from it: %v", problems)
	}

	// Take one file away, which is what restoring the database without the
	// object store leaves behind.
	if err := f.store.Remove(ctx, f.keys[0]); err != nil {
		t.Fatalf("remove: %v", err)
	}
	problems, err = backup.Compare(ctx, f.pools.Web, f.store, &res.Manifest)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(problems) == 0 {
		t.Error("comparing did not notice a file that is in the archive and not " +
			"in the store, which is exactly what restoring one half looks like")
	}
}

// The order tables come out in is the order they can go back in. A child before
// its parent is a restore that fails on a foreign key, and a shuffling order is
// two archives of identical data that do not compare equal.
func TestTablesComeOutInAnOrderTheyCanGoBackIn(t *testing.T) {
	f, ctx := setup(t), context.Background()

	first, err := backup.Discover(ctx, f.pools.Web)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	second, err := backup.Discover(ctx, f.pools.Web)
	if err != nil {
		t.Fatalf("discover again: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("two runs found %d and %d tables", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("the order is not stable: position %d is %s then %s",
				i, first[i].Name, second[i].Name)
		}
	}

	at := map[string]int{}
	for i, t := range first {
		at[t.Name] = i
	}
	// The ones this schema actually depends on, spelled out. A generic
	// assertion that the order satisfies the graph would be the same code as
	// the sort, checking itself.
	for _, pair := range [][2]string{
		{"orgs", "principals"},
		{"principals", "documents"},
		{"documents", "task_fields"},
		{"documents", "entity_links"},
		{"channels", "channel_members"},
		{"teams", "team_members"},
		{"orgs", "audit_log"},
		{"pm_schedules", "pm_occurrences"},
	} {
		parent, child := pair[0], pair[1]
		pi, ok := at[parent]
		ci, ok2 := at[child]
		if !ok || !ok2 {
			t.Errorf("%s or %s is not in the archive at all", parent, child)
			continue
		}
		if pi > ci {
			t.Errorf("%s comes out after %s, so restoring would fail on a "+
				"foreign key", parent, child)
		}
	}

	// A generated column cannot be written back, so it must not be in the
	// archive at all. Found by a restore failing on exactly this.
	for _, table := range first {
		for _, column := range table.Columns {
			if column == "search_tsv" {
				t.Errorf("%s.search_tsv is in the archive, and Postgres refuses "+
					"a COPY that supplies a generated column", table.Name)
			}
		}
	}
}
