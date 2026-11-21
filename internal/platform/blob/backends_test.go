package blob_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/blob"
)

// The same behaviour is asked of every backend, against a real server for each:
//
//	garage  TEST_GARAGE_ENDPOINT, TEST_GARAGE_ACCESS_KEY, TEST_GARAGE_SECRET_KEY (region garage)
//	s3      TEST_S3_ENDPOINT, TEST_S3_ACCESS_KEY, TEST_S3_SECRET_KEY (MinIO in CI)
//	azure   TEST_AZURITE_ENDPOINT, Microsoft's Azurite emulator, with its published development account
//	gcs     fake-gcs-server, in process, so this one always runs
//
// A backend whose variables are unset skips, and CI sets every one of them.

const azuriteAccount = "devstoreaccount1"

// azuriteKey is the development account key Microsoft publishes with Azurite.
// It protects nothing.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type backendCase struct {
	name string
	// open makes a store on a bucket of this test's own.
	open func(t *testing.T, bucket string) *blob.Store
	// presign is whether a URL made by this backend can be fetched in the test.
	presignFetch bool
}

func newBucket() string { return "hearth-test-" + strings.ToLower(uuid.NewString()[:8]) }

func openOrFail(t *testing.T, cfg blob.Config) *blob.Store {
	t.Helper()
	s, err := blob.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open %s: %v", cfg.Backend, err)
	}
	return s
}

func backends(t *testing.T) []backendCase {
	t.Helper()
	var out []backendCase

	if ep := os.Getenv("TEST_GARAGE_ENDPOINT"); ep != "" {
		out = append(out, backendCase{"garage", func(t *testing.T, bucket string) *blob.Store {
			return openOrFail(t, blob.Config{
				Backend: blob.BackendGarage, Endpoint: ep, PublicEndpoint: ep,
				AccessKey: os.Getenv("TEST_GARAGE_ACCESS_KEY"), SecretKey: os.Getenv("TEST_GARAGE_SECRET_KEY"),
				Region: envOr("TEST_GARAGE_REGION", "garage"), Bucket: bucket,
			})
		}, true})
	}
	if ep := os.Getenv("TEST_S3_ENDPOINT"); ep != "" {
		out = append(out, backendCase{"s3", func(t *testing.T, bucket string) *blob.Store {
			return openOrFail(t, blob.Config{
				Backend: blob.BackendS3, Endpoint: ep, PublicEndpoint: ep,
				AccessKey: envOr("TEST_S3_ACCESS_KEY", "hearth"), SecretKey: envOr("TEST_S3_SECRET_KEY", "hearthsecret"),
				Bucket: bucket,
			})
		}, true})
	}
	if ep := os.Getenv("TEST_AZURITE_ENDPOINT"); ep != "" {
		out = append(out, backendCase{"azure", func(t *testing.T, bucket string) *blob.Store {
			return openOrFail(t, blob.Config{
				Backend: blob.BackendAzure, AzureAccount: azuriteAccount, AzureKey: azuriteKey,
				AzureEndpoint: ep, Bucket: bucket,
			})
		}, true})
	}

	srv := newFakeGCS(t)
	out = append(out, backendCase{"gcs", func(t *testing.T, bucket string) *blob.Store {
		srv.CreateBucket(bucket)
		return openOrFail(t, blob.Config{
			Backend: blob.BackendGCS, GCSEndpoint: srv.URL(), Bucket: bucket,
		})
	}, false})
	return out
}

// newFakeGCS starts fake-gcs-server on a port of its own and tells it that is
// also the host it answers object downloads on, which the Google client reads
// from when STORAGE_EMULATOR_HOST is set.
func newFakeGCS(t *testing.T) *fakestorage.Server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	srv, err := fakestorage.NewServerWithOptions(fakestorage.Options{
		Scheme: "http", Host: "127.0.0.1", Port: uint16(port),
		PublicHost: fmt.Sprintf("127.0.0.1:%d", port),
	})
	if err != nil {
		t.Fatalf("fake-gcs-server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func readAll(t *testing.T, s *blob.Store, key string) []byte {
	t.Helper()
	rc, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return b
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEveryBackendBehavesTheSame(t *testing.T) {
	ctx := context.Background()
	for _, bc := range backends(t) {
		bc := bc
		t.Run(bc.name, func(t *testing.T) {
			s := bc.open(t, newBucket())
			if s.Backend() != bc.name {
				t.Fatalf("Backend() = %q, want %q", s.Backend(), bc.name)
			}
			if err := s.Probe(ctx); err != nil {
				t.Fatalf("probe: %v", err)
			}

			// A small file round trip with a matching SHA-256: the check the
			// predecessor never passed.
			body := randomBytes(t, 4096)
			key := uuid.NewString() + "/file-one.bin"
			n, err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/pdf")
			if err != nil || n != int64(len(body)) {
				t.Fatalf("put: n=%d err=%v", n, err)
			}
			if got := readAll(t, s, key); sum(got) != sum(body) {
				t.Fatal("bytes came back different")
			}
			o, err := s.Stat(ctx, key)
			if err != nil || o.Size != int64(len(body)) {
				t.Fatalf("stat: %+v %v", o, err)
			}
			if o.ContentType != "application/pdf" {
				t.Errorf("content type %q, want application/pdf", o.ContentType)
			}

			// A large one, which every SDK sends in parts.
			big := randomBytes(t, 12<<20)
			bigKey := uuid.NewString() + "/big.bin"
			if _, err := s.Put(ctx, bigKey, bytes.NewReader(big), int64(len(big)), "application/octet-stream"); err != nil {
				t.Fatalf("put big: %v", err)
			}
			if got := readAll(t, s, bigKey); sum(got) != sum(big) {
				t.Fatal("12 MB object came back different")
			}

			// An unknown length, which is what a streamed upload is.
			streamed := randomBytes(t, 3<<20)
			streamKey := uuid.NewString() + "/stream.bin"
			if n, err := s.Put(ctx, streamKey, io.NopCloser(bytes.NewReader(streamed)), -1, "application/octet-stream"); err != nil || n != int64(len(streamed)) {
				t.Fatalf("put unknown length: n=%d err=%v", n, err)
			}
			if got := readAll(t, s, streamKey); sum(got) != sum(streamed) {
				t.Fatal("streamed object came back different")
			}

			// Missing keys are ErrNotFound, from both Get and Stat.
			if _, err := s.Get(ctx, "nope/"+uuid.NewString()); !errors.Is(err, blob.ErrNotFound) {
				t.Errorf("get missing: %v, want ErrNotFound", err)
			}
			if _, err := s.Stat(ctx, "nope/"+uuid.NewString()); !errors.Is(err, blob.ErrNotFound) {
				t.Errorf("stat missing: %v, want ErrNotFound", err)
			}

			// List sees exactly what is there.
			seen := map[string]int64{}
			if err := s.List(ctx, func(o blob.Object) error { seen[o.Key] = o.Size; return nil }); err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(seen) != 3 || seen[key] != int64(len(body)) || seen[bigKey] != int64(len(big)) {
				t.Errorf("list saw %v", seen)
			}

			// Remove, and removing again is not an error.
			if err := s.Remove(ctx, key); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if err := s.Remove(ctx, key); err != nil {
				t.Errorf("remove twice: %v", err)
			}
			if _, err := s.Stat(ctx, key); !errors.Is(err, blob.ErrNotFound) {
				t.Errorf("still there after remove: %v", err)
			}

			// A bucket opened a second time is the same bucket, not an error.
			_ = bc.open(t, s.Bucket())
		})
	}
}

func TestPresignedURLsWork(t *testing.T) {
	ctx := context.Background()
	for _, bc := range backends(t) {
		bc := bc
		if !bc.presignFetch {
			continue
		}
		t.Run(bc.name, func(t *testing.T) {
			s := bc.open(t, newBucket())
			body := randomBytes(t, 8192)
			key := uuid.NewString() + "/report.pdf"
			if _, err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
				t.Fatal(err)
			}

			u, err := s.PresignGet(ctx, key, `Q3 "final", report.pdf`, time.Minute)
			if err != nil {
				t.Fatalf("presign get: %v", err)
			}
			resp, err := http.Get(u.String())
			if err != nil {
				t.Fatalf("fetch presigned: %v", err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || sum(got) != sum(body) {
				t.Fatalf("presigned download: HTTP %d, %d bytes", resp.StatusCode, len(got))
			}
			if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "Q3 final, report.pdf") {
				t.Errorf("Content-Disposition %q lost the download name", cd)
			}

			// A browser can upload straight in.
			up := randomBytes(t, 2048)
			upKey := uuid.NewString() + "/direct.bin"
			pu, err := s.PresignPut(ctx, upKey, time.Minute)
			if err != nil {
				t.Fatalf("presign put: %v", err)
			}
			req, _ := http.NewRequest(http.MethodPut, pu.String(), bytes.NewReader(up))
			if bc.name == "azure" {
				req.Header.Set("x-ms-blob-type", "BlockBlob")
			}
			pr, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("presigned upload: %v", err)
			}
			pr.Body.Close()
			if pr.StatusCode/100 != 2 {
				t.Fatalf("presigned upload: HTTP %d", pr.StatusCode)
			}
			if sum(readAll(t, s, upKey)) != sum(up) {
				t.Fatal("directly uploaded bytes came back different")
			}

			// A tampered URL is refused.
			bad := *u
			q := bad.Query()
			for k := range q {
				if strings.Contains(strings.ToLower(k), "signature") || k == "sig" {
					q.Set(k, "AAAA"+q.Get(k)[4:])
				}
			}
			bad.RawQuery = q.Encode()
			tr, err := http.Get(bad.String())
			if err == nil {
				tr.Body.Close()
				if tr.StatusCode == 200 {
					t.Errorf("a URL with a damaged signature was accepted")
				}
			}
		})
	}
}

// A Google service account key makes V4 signed URLs, offline. They point at
// Google's own host, carry the signature and the expiry, and never at the
// emulator or the cluster. Fetching one needs Google, so this checks the shape.
func TestGCSSignedURLs(t *testing.T) {
	ctx := context.Background()
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	creds, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "hearth@example-project.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    "https://oauth2.googleapis.com/token",
	})

	srv := newFakeGCS(t)
	bucket := newBucket()
	srv.CreateBucket(bucket)

	// With a key.
	s := openOrFail(t, blob.Config{Backend: blob.BackendGCS, GCSEndpoint: srv.URL(),
		GCSCredentialsJSON: string(creds), Bucket: bucket})
	u, err := s.PresignGet(ctx, "a/b.pdf", "b.pdf", 5*time.Minute)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	q := u.Query()
	if !strings.HasPrefix(u.Path, "/"+bucket+"/a/b.pdf") ||
		q.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" || q.Get("X-Goog-Signature") == "" ||
		(q.Get("X-Goog-Expires") != "300" && q.Get("X-Goog-Expires") != "299") || !strings.Contains(q.Get("response-content-disposition"), `filename="b.pdf"`) {
		t.Errorf("signed URL has the wrong shape: %s", u)
	}
	if _, err := s.PresignPut(ctx, "a/c.bin", time.Minute); err != nil {
		t.Errorf("presign put: %v", err)
	}

	// Without one, it says so rather than inventing a URL.
	s2 := openOrFail(t, blob.Config{Backend: blob.BackendGCS, GCSEndpoint: srv.URL(), Bucket: bucket})
	if _, err := s2.PresignGet(ctx, "a/b.pdf", "", time.Minute); !errors.Is(err, blob.ErrPresignUnsupported) {
		t.Errorf("no key: %v, want ErrPresignUnsupported", err)
	}

	// A bucket that is not there is an error, never a guess at a project.
	if _, err := blob.Open(ctx, blob.Config{Backend: blob.BackendGCS, GCSEndpoint: srv.URL(), Bucket: "no-such-bucket-" + newBucket()}); err == nil {
		t.Error("a missing GCS bucket was accepted")
	}
}

func TestOpenRefusesWhatItCannotUse(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range map[string]blob.Config{
		"no bucket":          {Backend: "garage", Endpoint: "x:1", PublicEndpoint: "y:1"},
		"unknown backend":    {Backend: "tape", Bucket: "b"},
		"garage no endpoint": {Backend: "garage", Bucket: "b", PublicEndpoint: "y:1"},
		"azure no account":   {Backend: "azure", Bucket: "b", AzureKey: "a2V5"},
		"azure no key":       {Backend: "azure", Bucket: "b", AzureAccount: "acct"},
	} {
		if _, err := blob.Open(ctx, cfg); err == nil {
			t.Errorf("%s: Open accepted it", name)
		}
	}
	// Garage is the default.
	if _, err := blob.Open(ctx, blob.Config{Bucket: "b"}); err == nil || !strings.Contains(err.Error(), "Endpoint") {
		t.Errorf("an empty backend should mean garage and ask for its endpoint, got %v", err)
	}
}

// Every pair of real backends: a bucket seeded in one is copied into another,
// read back and counted, and a second run finds nothing to do.
func TestMigrateBetweenRealBackends(t *testing.T) {
	ctx := context.Background()
	cases := backends(t)
	if len(cases) < 2 {
		t.Skip("needs at least two backends")
	}
	for _, from := range cases {
		for _, to := range cases {
			if from.name == to.name {
				continue
			}
			from, to := from, to
			t.Run(from.name+" to "+to.name, func(t *testing.T) {
				src := from.open(t, newBucket())
				dst := to.open(t, newBucket())
				want := map[string]string{}
				for i := 0; i < 12; i++ {
					size := 1000 + i*977
					if i == 5 {
						size = 11 << 20
					}
					b := randomBytes(t, size)
					k := fmt.Sprintf("%s/doc-%02d.bin", uuid.NewString(), i)
					if _, err := src.Put(ctx, k, bytes.NewReader(b), int64(len(b)), "application/octet-stream"); err != nil {
						t.Fatal(err)
					}
					want[k] = sum(b)
				}
				// One the destination already holds, identical.
				var shared string
				for k := range want {
					shared = k
					break
				}
				same := readAll(t, src, shared)
				if _, err := dst.Put(ctx, shared, bytes.NewReader(same), int64(len(same)), "application/octet-stream"); err != nil {
					t.Fatal(err)
				}
				// One written to the destination since the switch.
				if _, err := dst.Put(ctx, "newer/after-switch.bin", strings.NewReader("hello"), 5, "text/plain"); err != nil {
					t.Fatal(err)
				}

				dry, err := blob.Migrate(ctx, src, dst, blob.MigrateOptions{})
				if err != nil || !dry.OK(false) || dry.Copied != 11 || dry.AlreadyThere != 1 {
					t.Fatalf("dry run: %+v %v", dry, err)
				}
				if _, err := dst.Stat(ctx, anyOther(want, shared)); !errors.Is(err, blob.ErrNotFound) {
					t.Fatal("a dry run wrote to the destination")
				}

				rep, err := blob.Migrate(ctx, src, dst, blob.MigrateOptions{Apply: true})
				if err != nil || !rep.OK(true) {
					t.Fatalf("apply: %+v %v", rep, err)
				}
				if rep.SourceObjects != 12 || rep.Copied != 11 || rep.AlreadyThere != 1 ||
					rep.DestinationObjects != 13 || rep.ExtraInDestination != 1 {
					t.Errorf("counts: %+v", rep)
				}
				for k, h := range want {
					if sum(readAll(t, dst, k)) != h {
						t.Errorf("%s differs in the destination", k)
					}
				}
				if string(readAll(t, dst, "newer/after-switch.bin")) != "hello" {
					t.Error("an object only the destination held was touched")
				}

				again, err := blob.Migrate(ctx, src, dst, blob.MigrateOptions{Apply: true})
				if err != nil || !again.OK(true) || again.Copied != 0 || again.AlreadyThere != 12 {
					t.Errorf("second run: %+v %v", again, err)
				}
				// The source is whole.
				n := 0
				_ = src.List(ctx, func(blob.Object) error { n++; return nil })
				if n != 12 {
					t.Errorf("source has %d objects after the copy, want 12", n)
				}
			})
		}
	}
}

func anyOther(m map[string]string, not string) string {
	for k := range m {
		if k != not {
			return k
		}
	}
	return ""
}
