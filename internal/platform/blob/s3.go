package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3Backend speaks the S3 protocol, for Garage and for any other S3-compatible
// store. Two clients, and the one that signs browser-facing URLs cannot reach
// the cluster address at all, so getting it wrong requires using the wrong
// client rather than forgetting a flag.
//
// This is how the predecessor shipped a build where every upload succeeded and
// every download was dead: a presigned URL signed against the in-cluster host
// is valid and useless, and nothing in any log says so.
type s3Backend struct {
	name     string
	internal *minio.Client
	public   *minio.Client
	bucket   string
	endpoint string
}

func openS3(ctx context.Context, cfg Config, name string) (*s3Backend, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("blob: Endpoint is required")
	}
	if cfg.PublicEndpoint == "" {
		return nil, errors.New("blob: PublicEndpoint is required (presigned URLs are handed to browsers)")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	lookup := minio.BucketLookupAuto
	// Garage answers path style only.
	if cfg.PathStyle || name == BackendGarage {
		lookup = minio.BucketLookupPath
	}

	internal, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: creds, Secure: cfg.UseSSL, Region: cfg.Region, BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("blob: internal client: %w", err)
	}

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

	s := &s3Backend{name: name, internal: internal, public: public, bucket: cfg.Bucket, endpoint: cfg.Endpoint}

	exists, err := internal.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("blob: reach object store at %s: %w", cfg.Endpoint, err)
	}
	if !exists {
		if err := internal.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("blob: create bucket %s: %w", cfg.Bucket, err)
		}
	}
	return s, nil
}

func (s *s3Backend) Name() string     { return s.name }
func (s *s3Backend) Bucket() string   { return s.bucket }
func (s *s3Backend) Endpoint() string { return s.endpoint }

func (s *s3Backend) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	info, err := s.internal.PutObject(ctx, s.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (s *s3Backend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.internal.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// GetObject is lazy and returns no error for a missing key until first
	// read, so stat here to fail at the point the caller can handle it.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, s3Err(err)
	}
	return obj, nil
}

func (s *s3Backend) Stat(ctx context.Context, key string) (Object, error) {
	info, err := s.internal.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return Object{}, s3Err(err)
	}
	return Object{Key: key, Size: info.Size, ETag: info.ETag, ContentType: info.ContentType}, nil
}

func s3Err(err error) error {
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}

func (s *s3Backend) Remove(ctx context.Context, key string) error {
	return s.internal.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

// PresignGet is always signed against the public endpoint.
func (s *s3Backend) PresignGet(ctx context.Context, key, downloadName string, ttl time.Duration) (*url.URL, error) {
	params := url.Values{}
	if downloadName != "" {
		// Quote the filename: an unquoted one containing a comma or a space
		// truncates the header, and the browser saves the file under a
		// mangled name.
		params.Set("response-content-disposition",
			fmt.Sprintf(`attachment; filename="%s"`, sanitiseFilename(downloadName)))
	}
	return s.public.PresignedGetObject(ctx, s.bucket, key, ttl, params)
}

func (s *s3Backend) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	return s.public.PresignedPutObject(ctx, s.bucket, key, ttl)
}

func (s *s3Backend) Probe(ctx context.Context) error {
	_, err := s.internal.BucketExists(ctx, s.bucket)
	return err
}

func (s *s3Backend) List(ctx context.Context, fn func(Object) error) error {
	for obj := range s.internal.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return obj.Err
		}
		if err := fn(Object{Key: obj.Key, Size: obj.Size, ETag: obj.ETag}); err != nil {
			return err
		}
	}
	return nil
}
