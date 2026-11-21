package files_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/files"
	"github.com/Embernet-ai/hearth/internal/platform/blob"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type env struct {
	svc   *files.Service
	store *blob.Store
	org   string
	user  string
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	endpoint := os.Getenv("TEST_GARAGE_ENDPOINT")
	if dsn == "" || endpoint == "" {
		t.Skip("TEST_DATABASE_URL and TEST_GARAGE_ENDPOINT not set, skipping")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(pools.Close)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := blob.Open(ctx, blob.Config{
		Endpoint: endpoint,
		// Deliberately different from Endpoint, which is what makes the
		// presigned-URL test below meaningful.
		PublicEndpoint: "files.hearth-next.example.com",
		AccessKey:      os.Getenv("TEST_GARAGE_ACCESS_KEY"),
		SecretKey:      os.Getenv("TEST_GARAGE_SECRET_KEY"),
		Region:         envOr("TEST_GARAGE_REGION", "garage"),
		Bucket:         "hearth-test",
		UseSSL:         false,
		PublicUseSSL:   true,
	})
	if err != nil {
		t.Fatalf("object store: %v", err)
	}

	e := &env{
		svc:   files.New(pools.Web, store),
		store: store,
		org:   uuid.NewString(),
		user:  uuid.NewString(),
	}
	if _, err := pools.Web.Exec(ctx,
		`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Files Test')`,
		e.org, "files-"+e.org[:8]); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pools.Web.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Uploader')`,
		e.user, e.org); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// The audit log refuses an ordinary delete, so it takes the one
		// deliberate way out. That function removes a whole org and nothing
		// finer, and nothing outside a fixture calls it.
		_, _ = pools.Web.Exec(ctx, `SELECT audit_purge_org($1)`, e.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM files WHERE org_id=$1`, e.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM principals WHERE org_id=$1`, e.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, e.org)
	})
	return e
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// THE test. Upload a file and download it again, comparing SHA-256.
//
// This is the exact step the predecessor never passed. Uploads succeed and
// report success whether or not the download path works, so an upload-only test
// proves nothing at all.
func TestUploadAndDownloadAgainWithMatchingDigest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	// Non-trivial size, so this exercises multi-chunk streaming rather than a
	// single buffer that would hide an offset bug.
	original := make([]byte, 3<<20) // 3 MiB
	if _, err := rand.Read(original); err != nil {
		t.Fatalf("generate content: %v", err)
	}
	want := sha256.Sum256(original)

	f, err := e.svc.Upload(ctx, e.org, e.user, "line-3-rebuild.pdf", "application/pdf",
		int64(len(original)), bytes.NewReader(original))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if f.Digest() != hex.EncodeToString(want[:]) {
		t.Fatalf("digest recorded at upload = %s, want %s", f.Digest(), hex.EncodeToString(want[:]))
	}
	if f.Size != int64(len(original)) {
		t.Errorf("size = %d, want %d", f.Size, len(original))
	}

	// Now read it back. This is the half that was broken.
	_, rc, err := e.svc.Open(ctx, e.org, f.ID)
	if err != nil {
		t.Fatalf("open for download: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != len(original) {
		t.Fatalf("read back %d bytes, want %d", len(got), len(original))
	}
	if !bytes.Equal(got, original) {
		t.Fatal("bytes read back differ from bytes uploaded")
	}
	gotSum := sha256.Sum256(got)
	if gotSum != want {
		t.Fatalf("SHA-256 mismatch: uploaded %x, downloaded %x", want, gotSum)
	}
}

// The presigned URL must be signed against the PUBLIC endpoint, never the
// cluster address.
//
// This is the specific bug that made every download dead in the predecessor: a
// URL signed against an in-cluster hostname is cryptographically valid and
// completely unreachable, and it fails with nothing in any log because no server
// is ever contacted. The old test asserted the URL had been rewritten; it never
// asserted where it pointed.
func TestPresignedURLNeverPointsAtTheClusterAddress(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	f, err := e.svc.Upload(ctx, e.org, e.user, "report.txt", "text/plain",
		5, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	u, err := e.svc.DownloadURL(ctx, e.org, f.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	internal := e.store.InternalEndpoint()
	if strings.Contains(u.String(), internal) {
		t.Fatalf("presigned URL contains the in-cluster address %q, so no browser can fetch it:\n%s",
			internal, u)
	}
	if u.Host != "files.hearth-next.example.com" {
		t.Errorf("presigned URL host = %q, want the public endpoint", u.Host)
	}
	if u.Scheme != "https" {
		t.Errorf("presigned URL scheme = %q, want https for a browser-facing URL", u.Scheme)
	}
	// A presigned URL without a signature is just a URL.
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Error("presigned URL carries no signature")
	}
	// The download should arrive under its real name, not the storage key.
	if d := u.Query().Get("response-content-disposition"); !strings.Contains(d, "report.txt") {
		t.Errorf("content-disposition does not name the file: %q", d)
	}
}

func TestCorruptedContentIsDetectedOnRead(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	f, err := e.svc.Upload(ctx, e.org, e.user, "notes.txt", "text/plain",
		11, strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Overwrite the object behind the metadata's back. This is what silent
	// corruption looks like, and the digest check is the only thing standing
	// between it and a user opening a file that is quietly wrong.
	if _, err := e.store.Put(ctx, f.StorageKey,
		strings.NewReader("goodbye w"), 9, "text/plain"); err != nil {
		t.Fatalf("corrupt object: %v", err)
	}

	_, rc, err := e.svc.Open(ctx, e.org, f.ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()

	_, err = io.ReadAll(rc)
	if !errors.Is(err, files.ErrCorrupted) {
		t.Fatalf("corrupted content was returned without error, got: %v", err)
	}
}

func TestUploadRejectsSizeMismatch(t *testing.T) {
	e := setup(t)
	// Claiming a size that does not match the content must fail rather than
	// storing a truncated file that looks fine.
	_, err := e.svc.Upload(context.Background(), e.org, e.user, "short.txt", "text/plain",
		100, strings.NewReader("only eleven"))
	if err == nil {
		t.Fatal("a size mismatch was accepted")
	}
}

func TestStorageKeyDoesNotContainTheUserFilename(t *testing.T) {
	e := setup(t)
	// A filename is attacker-controlled. If it reached the storage key, this
	// would write outside the org prefix.
	f, err := e.svc.Upload(context.Background(), e.org, e.user,
		"../../../etc/passwd", "text/plain", 3, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if strings.Contains(f.StorageKey, "..") || strings.Contains(f.StorageKey, "passwd") {
		t.Fatalf("storage key derived from the user filename: %q", f.StorageKey)
	}
	if !strings.HasPrefix(f.StorageKey, "org/"+e.org+"/") {
		t.Errorf("storage key %q is not namespaced under the org", f.StorageKey)
	}
}

func TestGetIsScopedToTheOrg(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	f, err := e.svc.Upload(ctx, e.org, e.user, "secret.txt", "text/plain", 3, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Another tenant asking for a real id must be told it does not exist.
	if _, err := e.svc.Get(ctx, uuid.NewString(), f.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("a file leaked across an org boundary: %v", err)
	}
}
