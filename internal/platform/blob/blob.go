// Package blob is object storage: S3 protocol, served by MinIO.
//
// # The two-endpoint rule, which is the whole reason this package is careful
//
// There are two addresses for the same object store and they are NOT
// interchangeable:
//
//   - Internal: how this process reaches MinIO. An in-cluster hostname like
//     http://minio:9000, resolvable only inside the cluster.
//   - Public: how a BROWSER reaches it. A real, routable hostname.
//
// A presigned URL is handed to a browser, so it must always be signed against
// the public endpoint. Signing against the internal one produces a URL that is
// cryptographically valid and completely useless: the browser cannot resolve the
// host, the request never arrives, and nothing appears in any log because no
// server was ever contacted.
//
// This is not hypothetical. It is precisely how the predecessor shipped a build
// where every upload succeeded and every download was dead, and it survived a
// full port because the test asserted the URL was rewritten rather than
// asserting where it pointed. Uploads report success either way, so nothing
// looked wrong until a human clicked a link.
//
// So: two clients, and the one that signs browser-facing URLs cannot reach the
// cluster address at all. Getting it wrong requires using the wrong client
// rather than forgetting a flag.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config describes how to reach the object store.
type Config struct {
	// Endpoint is the in-cluster address, host:port, no scheme.
	Endpoint string
	// PublicEndpoint is the browser-facing address, host:port, no scheme.
	// Required, and it must differ from Endpoint in any real deployment.
	PublicEndpoint string
	AccessKey      string
	SecretKey      string
	Bucket         string
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
}

// Store is object storage with a bucket already selected.
type Store struct {
	internal *minio.Client
	public   *minio.Client
	bucket   string
	cfg      Config
}

// Open connects, verifies the bucket exists, and creates it if not.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("blob: Endpoint is required")
	}
	if cfg.PublicEndpoint == "" {
		return nil, errors.New("blob: PublicEndpoint is required (presigned URLs are handed to browsers)")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("blob: Bucket is required")
	}

	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}

	internal, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: creds, Secure: cfg.UseSSL, Region: cfg.Region, BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: internal client: %w", err)
	}

	// A second client, configured for the public address. It exists solely to
	// sign URLs and is never used for I/O, so a presigned URL cannot
	// accidentally be signed against the cluster hostname.
	//
	// Region is set so this client never makes a network call. The public
	// hostname routinely does not resolve from inside the cluster, and without
	// an explicit region the SDK would try to look the bucket's region up
	// before signing.
	public, err := minio.New(cfg.PublicEndpoint, &minio.Options{
		Creds: creds, Secure: cfg.PublicUseSSL, Region: cfg.Region, BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: public client: %w", err)
	}

	s := &Store{internal: internal, public: public, bucket: cfg.Bucket, cfg: cfg}

	exists, err := internal.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("blob: reach object store at %s: %w", cfg.Endpoint, err)
	}
	if !exists {
		if err := internal.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("blob: create bucket %s: %w", cfg.Bucket, err)
		}
	}
	return s, nil
}

// Put stores an object and returns the number of bytes written.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	info, err := s.internal.PutObject(ctx, s.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return 0, fmt.Errorf("blob: put %s: %w", key, err)
	}
	return info.Size, nil
}

// Get opens an object for reading. The caller closes it.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.internal.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("blob: get %s: %w", key, err)
	}
	// GetObject is lazy and returns no error for a missing key until first
	// read, so stat here to fail at the point the caller can handle it.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("blob: stat %s: %w", key, err)
	}
	return obj, nil
}

// Remove deletes an object. Removing one that is absent is not an error.
func (s *Store) Remove(ctx context.Context, key string) error {
	if err := s.internal.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("blob: remove %s: %w", key, err)
	}
	return nil
}

// PresignGet returns a time-limited URL a BROWSER can fetch.
//
// Always signed against the public endpoint. See the package comment for why
// that distinction is the difference between a working download and a silent
// dead link.
func (s *Store) PresignGet(ctx context.Context, key, downloadName string, ttl time.Duration) (*url.URL, error) {
	params := url.Values{}
	if downloadName != "" {
		// Quote the filename: an unquoted one containing a comma or a space
		// truncates the header, and the browser saves the file under a
		// mangled name.
		params.Set("response-content-disposition",
			fmt.Sprintf(`attachment; filename="%s"`, sanitiseFilename(downloadName)))
	}
	u, err := s.public.PresignedGetObject(ctx, s.bucket, key, ttl, params)
	if err != nil {
		return nil, fmt.Errorf("blob: presign get %s: %w", key, err)
	}
	return u, nil
}

// PresignPut returns a time-limited URL a BROWSER can upload to directly,
// so large files never pass through this process.
func (s *Store) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	u, err := s.public.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return nil, fmt.Errorf("blob: presign put %s: %w", key, err)
	}
	return u, nil
}

// Probe reports whether object storage is usable, for readiness.
func (s *Store) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := s.internal.BucketExists(ctx, s.bucket); err != nil {
		return fmt.Errorf("object store unreachable: %w", err)
	}
	return nil
}

// InternalEndpoint reports the in-cluster address, for diagnostics and for the
// test that asserts presigned URLs never contain it.
func (s *Store) InternalEndpoint() string { return s.cfg.Endpoint }

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

// Object is what a backup needs to know about one stored blob.
type Object struct {
	Key  string
	Size int64
	// ETag is MinIO's own checksum. For an object written in one part it is the
	// MD5, which is not what this codebase uses anywhere else and is not
	// trusted here for integrity: a backup computes SHA-256 over the bytes it
	// actually read. It is kept because it is free and it is what an operator
	// sees in the MinIO console when they go looking.
	ETag string
}

// List walks every object in the bucket.
//
// A callback rather than a slice, because a bucket with two hundred thousand
// attachments in it is not a slice anybody wants in memory, and the one caller
// is a backup that streams each object out as it is named.
func (s *Store) List(ctx context.Context, fn func(Object) error) error {
	for obj := range s.internal.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Recursive: true,
	}) {
		if obj.Err != nil {
			return fmt.Errorf("blob: list %s: %w", s.bucket, obj.Err)
		}
		if err := fn(Object{Key: obj.Key, Size: obj.Size, ETag: obj.ETag}); err != nil {
			return err
		}
	}
	return nil
}

// Bucket is which bucket this store writes to, for a backup manifest to record
// and a restore to warn about.
func (s *Store) Bucket() string { return s.bucket }
