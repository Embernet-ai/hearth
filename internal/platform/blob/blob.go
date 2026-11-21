// Package blob is object storage, behind one interface with four backends.
//
//	garage  the default. Garage, our own S3-compatible store, on the same box or
//	        cluster as Hearth. Nobody needs a cloud subscription to keep their data.
//	s3      any other S3-compatible store: AWS, Wasabi, Backblaze, a MinIO you
//	        already run.
//	azure   Azure Blob Storage, through the official Azure SDK for Go.
//	gcs     Google Cloud Storage, through the official Google Cloud Go client.
//
// The backend is chosen by Config.Backend, and everything above this package
// (files, backups, the migration tool) sees a *Store and nothing else.
//
// # The two-endpoint rule for S3-protocol backends
//
// There are two addresses for the same S3 store and they are NOT
// interchangeable. See s3.go: a presigned URL goes to a browser, so it is
// signed against the public address, by a client that cannot reach the cluster
// address at all.
//
// Azure and Google Cloud Storage have one public address each, so the rule does
// not apply to them; their presigned URLs (a SAS token, a V4 signed URL) point
// at the service's own host.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Backend names, as written in HEARTH_STORAGE_BACKEND.
const (
	BackendGarage = "garage"
	BackendS3     = "s3"
	BackendAzure  = "azure"
	BackendGCS    = "gcs"
)

// ErrPresignUnsupported is returned by a backend that cannot make a presigned
// URL with the credentials it was given (a Google service account with no
// private key, for one). Callers stream through the app instead.
var ErrPresignUnsupported = errors.New("blob: this backend cannot presign with the credentials it has")

// ErrNotFound is returned by Get and Stat for a key that is not there.
var ErrNotFound = errors.New("blob: object not found")

// Config describes how to reach the object store. Backend picks which of the
// fields below apply.
type Config struct {
	// Backend is garage (the default when empty), s3, azure or gcs.
	Backend string

	// Bucket is the bucket, or for Azure the container.
	Bucket string

	// --- garage and s3 ---

	// Endpoint is the in-cluster address, host:port, no scheme.
	Endpoint string
	// PublicEndpoint is the browser-facing address, host:port, no scheme.
	// Required, and it must differ from Endpoint in any real deployment.
	PublicEndpoint string
	AccessKey      string
	SecretKey      string
	// UseSSL applies to the internal connection.
	UseSSL bool
	// PublicUseSSL applies to signed browser URLs. Almost always true in
	// production even when the internal hop is plaintext inside the cluster.
	PublicUseSSL bool

	// Region must be set, and the reason is not obvious.
	//
	// Without it, the SDK discovers a bucket's region by making a live
	// GET /<bucket>/?location= call before it can sign anything. The signing
	// client is configured for the PUBLIC hostname, which frequently does not
	// resolve from inside the cluster (split-horizon DNS is the normal case),
	// so signing fails with a DNS error that looks nothing like the actual
	// problem. Setting it explicitly makes the public client a pure offline
	// signer that never touches the network.
	//
	// Defaults to us-east-1, which is what MinIO reports unless configured.
	Region string

	// PathStyle forces /bucket/key addressing on both clients. Zero leaves the
	// SDK's own choice: virtual-host style for AWS, path style elsewhere.
	PathStyle bool

	// --- azure ---

	// AzureAccount is the storage account name.
	AzureAccount string
	// AzureKey is the account's shared key (base64), which is also what signs
	// SAS URLs.
	AzureKey string
	// AzureEndpoint is the blob service URL. Empty means
	// https://<account>.blob.core.windows.net. Set it for Azurite, a sovereign
	// cloud, or a private endpoint.
	AzureEndpoint string

	// --- gcs ---

	// GCSCredentialsJSON is a service account key, as JSON. Empty uses the
	// environment's application default credentials. Presigned URLs need a
	// service account key, so with application default credentials alone a
	// backend answers ErrPresignUnsupported and downloads stream through Hearth.
	GCSCredentialsJSON string
	// GCSEndpoint is a GCS emulator, as host:port or a URL (fake-gcs-server,
	// for one). Empty is Google. It sets STORAGE_EMULATOR_HOST for the process.
	GCSEndpoint string
}

// Object is what a backup or a migration needs to know about one stored blob.
type Object struct {
	Key  string
	Size int64
	// ETag is the store's own checksum when it has one. It is not trusted for
	// integrity: backups and the migration compute SHA-256 over the bytes they
	// actually read.
	ETag string
	// ContentType is what the store recorded for it, when it records one. The
	// migration carries it across.
	ContentType string
}

// Backend is one object store. A Store wraps exactly one.
type Backend interface {
	// Name is garage, s3, azure or gcs.
	Name() string
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
	// Get opens an object. A missing key is ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat reports an object without reading it. A missing key is ErrNotFound.
	Stat(ctx context.Context, key string) (Object, error)
	// Remove deletes an object. Removing one that is absent is not an error.
	Remove(ctx context.Context, key string) error
	// PresignGet and PresignPut may return ErrPresignUnsupported.
	PresignGet(ctx context.Context, key, downloadName string, ttl time.Duration) (*url.URL, error)
	PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error)
	Probe(ctx context.Context) error
	List(ctx context.Context, fn func(Object) error) error
	Bucket() string
	// Endpoint is the address this process reaches the store at, for diagnostics.
	Endpoint() string
}

// Store is object storage with a bucket already selected.
type Store struct {
	b Backend
}

// Open connects to the backend Config names, verifies the bucket exists, and
// creates it if not.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("blob: Bucket is required")
	}
	var (
		b   Backend
		err error
	)
	switch strings.ToLower(strings.TrimSpace(cfg.Backend)) {
	case "", BackendGarage:
		b, err = openS3(ctx, cfg, BackendGarage)
	case BackendS3:
		b, err = openS3(ctx, cfg, BackendS3)
	case BackendAzure:
		b, err = openAzure(ctx, cfg)
	case BackendGCS:
		b, err = openGCS(ctx, cfg)
	default:
		return nil, fmt.Errorf("blob: unknown backend %q (want garage, s3, azure or gcs)", cfg.Backend)
	}
	if err != nil {
		return nil, err
	}
	return &Store{b: b}, nil
}

// New wraps a Backend. For tests that bring their own.
func New(b Backend) *Store { return &Store{b: b} }

// Backend reports which backend this store runs on: garage, s3, azure or gcs.
func (s *Store) Backend() string { return s.b.Name() }

// Put stores an object and returns the number of bytes written.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	n, err := s.b.Put(ctx, key, r, size, contentType)
	if err != nil {
		return 0, fmt.Errorf("blob: put %s: %w", key, err)
	}
	return n, nil
}

// Get opens an object for reading. The caller closes it.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.b.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("blob: get %s: %w", key, err)
	}
	return rc, nil
}

// Stat reports one object's size and store checksum.
func (s *Store) Stat(ctx context.Context, key string) (Object, error) {
	o, err := s.b.Stat(ctx, key)
	if err != nil {
		return Object{}, fmt.Errorf("blob: stat %s: %w", key, err)
	}
	return o, nil
}

// Remove deletes an object. Removing one that is absent is not an error.
func (s *Store) Remove(ctx context.Context, key string) error {
	if err := s.b.Remove(ctx, key); err != nil {
		return fmt.Errorf("blob: remove %s: %w", key, err)
	}
	return nil
}

// PresignGet returns a time-limited URL a BROWSER can fetch, or
// ErrPresignUnsupported when the backend has no way to make one.
func (s *Store) PresignGet(ctx context.Context, key, downloadName string, ttl time.Duration) (*url.URL, error) {
	u, err := s.b.PresignGet(ctx, key, downloadName, ttl)
	if err != nil {
		return nil, fmt.Errorf("blob: presign get %s: %w", key, err)
	}
	return u, nil
}

// PresignPut returns a time-limited URL a BROWSER can upload to directly, so
// large files never pass through this process. On Azure the upload must send
// the header x-ms-blob-type: BlockBlob.
func (s *Store) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	u, err := s.b.PresignPut(ctx, key, ttl)
	if err != nil {
		return nil, fmt.Errorf("blob: presign put %s: %w", key, err)
	}
	return u, nil
}

// Probe reports whether object storage is usable, for readiness.
func (s *Store) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.b.Probe(ctx); err != nil {
		return fmt.Errorf("object store unreachable: %w", err)
	}
	return nil
}

// InternalEndpoint reports the address this process reaches the store at, for
// diagnostics and for the test that asserts presigned URLs never contain it.
func (s *Store) InternalEndpoint() string { return s.b.Endpoint() }

// List walks every object in the bucket.
//
// A callback rather than a slice, because a bucket with two hundred thousand
// attachments in it is not a slice anybody wants in memory, and the callers
// stream each object as it is named.
func (s *Store) List(ctx context.Context, fn func(Object) error) error {
	if err := s.b.List(ctx, fn); err != nil {
		return fmt.Errorf("blob: list %s: %w", s.b.Bucket(), err)
	}
	return nil
}

// Bucket is which bucket this store writes to, for a backup manifest to record
// and a restore to warn about.
func (s *Store) Bucket() string { return s.b.Bucket() }

// sanitiseFilename strips characters that would break a Content-Disposition
// header or let a filename escape its directory when saved.
func sanitiseFilename(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if i := strings.LastIndexAny(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}
