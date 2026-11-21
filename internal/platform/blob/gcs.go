package blob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// gcsBackend is Google Cloud Storage through the official Google Cloud Go
// client. With a service account key it also makes V4 signed URLs; with only
// application default credentials it cannot, and says so with
// ErrPresignUnsupported so downloads stream through Hearth.
type gcsBackend struct {
	client   *storage.Client
	bucket   *storage.BucketHandle
	name     string
	email    string
	key      []byte
	endpoint string
}

func openGCS(ctx context.Context, cfg Config) (*gcsBackend, error) {
	var opts []option.ClientOption
	g := &gcsBackend{name: cfg.Bucket, endpoint: cfg.GCSEndpoint}
	switch {
	case cfg.GCSEndpoint != "":
		// An emulator. The Google client finds one only through this variable,
		// which Google documents for exactly this; with it set the client sends
		// uploads, downloads and the JSON API there with no credentials.
		if err := os.Setenv("STORAGE_EMULATOR_HOST", cfg.GCSEndpoint); err != nil {
			return nil, err
		}
	case cfg.GCSCredentialsJSON != "":
		opts = append(opts, option.WithAuthCredentialsJSON(option.ServiceAccount, []byte(cfg.GCSCredentialsJSON)))
	}
	if cfg.GCSCredentialsJSON != "" {
		var sa struct {
			Email string `json:"client_email"`
			Key   string `json:"private_key"`
		}
		if err := json.Unmarshal([]byte(cfg.GCSCredentialsJSON), &sa); err != nil {
			return nil, fmt.Errorf("blob: GCS credentials are not JSON: %w", err)
		}
		g.email, g.key = sa.Email, []byte(sa.Key)
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("blob: gcs client: %w", err)
	}
	g.client = client
	g.bucket = client.Bucket(cfg.Bucket)
	if g.endpoint == "" {
		g.endpoint = "storage.googleapis.com"
	}
	// Google bucket names are global, so this is a read and not a create: a
	// bucket that is not ours is an error, not something to make.
	if _, err := g.bucket.Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) {
			return nil, fmt.Errorf("blob: GCS bucket %s does not exist; create it first (bucket names are global, so Hearth will not guess a project for you)", cfg.Bucket)
		}
		return nil, fmt.Errorf("blob: reach GCS bucket %s: %w", cfg.Bucket, err)
	}
	return g, nil
}

func (g *gcsBackend) Name() string     { return BackendGCS }
func (g *gcsBackend) Bucket() string   { return g.name }
func (g *gcsBackend) Endpoint() string { return g.endpoint }

func (g *gcsBackend) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	w := g.bucket.Object(key).NewWriter(ctx)
	w.ContentType = contentType
	n, err := io.Copy(w, r)
	if err != nil {
		_ = w.Close()
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return n, nil
}

func (g *gcsBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := g.bucket.Object(key).NewReader(ctx)
	if err != nil {
		return nil, gcsErr(err)
	}
	return rc, nil
}

func (g *gcsBackend) Stat(ctx context.Context, key string) (Object, error) {
	a, err := g.bucket.Object(key).Attrs(ctx)
	if err != nil {
		return Object{}, gcsErr(err)
	}
	return Object{Key: key, Size: a.Size, ETag: a.Etag, ContentType: a.ContentType}, nil
}

func gcsErr(err error) error {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}

func (g *gcsBackend) Remove(ctx context.Context, key string) error {
	if err := g.bucket.Object(key).Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return err
	}
	return nil
}

func (g *gcsBackend) signed(key, method string, ttl time.Duration, q url.Values) (*url.URL, error) {
	if g.email == "" || len(g.key) == 0 {
		return nil, ErrPresignUnsupported
	}
	raw, err := storage.SignedURL(g.name, key, &storage.SignedURLOptions{
		GoogleAccessID:  g.email,
		PrivateKey:      g.key,
		Method:          method,
		Expires:         time.Now().Add(ttl),
		Scheme:          storage.SigningSchemeV4,
		QueryParameters: q,
	})
	if err != nil {
		return nil, err
	}
	return url.Parse(raw)
}

func (g *gcsBackend) PresignGet(ctx context.Context, key, downloadName string, ttl time.Duration) (*url.URL, error) {
	q := url.Values{}
	if downloadName != "" {
		q.Set("response-content-disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitiseFilename(downloadName)))
	}
	return g.signed(key, "GET", ttl, q)
}

func (g *gcsBackend) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	return g.signed(key, "PUT", ttl, nil)
}

func (g *gcsBackend) Probe(ctx context.Context) error {
	_, err := g.bucket.Attrs(ctx)
	return err
}

func (g *gcsBackend) List(ctx context.Context, fn func(Object) error) error {
	it := g.bucket.Objects(ctx, nil)
	for {
		a, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(Object{Key: a.Name, Size: a.Size, ETag: a.Etag}); err != nil {
			return err
		}
	}
}
