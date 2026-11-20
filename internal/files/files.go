// Package files owns uploads: bytes in object storage, metadata in Postgres.
package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/platform/blob"
)

// MaxUploadBytes bounds a single upload. A limit that exists is worth more than
// the exact number, because without one a single request can fill the volume.
const MaxUploadBytes int64 = 512 << 20 // 512 MiB

var (
	ErrNotFound = errors.New("file not found")
	ErrTooLarge = fmt.Errorf("file exceeds %d bytes", MaxUploadBytes)
	// ErrCorrupted means the bytes read back do not match what was stored.
	// It should be impossible. It is checked anyway, because silent corruption
	// is the failure nobody notices until the file is needed.
	ErrCorrupted = errors.New("file content does not match its recorded digest")
)

// File is an uploaded file's metadata.
type File struct {
	ID          string
	OrgID       string
	StorageKey  string
	Filename    string
	ContentType string
	Size        int64
	SHA256      []byte
	Status      string
	UploadedBy  string
	CreatedAt   time.Time
}

// Digest returns the SHA-256 as hex, which is how it appears in APIs and logs.
func (f *File) Digest() string { return hex.EncodeToString(f.SHA256) }

// Ref returns the file as an addressable entity, for grants and links.
func (f *File) Ref() core.Ref { return core.Ref{Type: core.EntityFile, ID: f.ID} }

// Service stores and retrieves files.
type Service struct {
	db    *pgxpool.Pool
	store *blob.Store
}

func New(db *pgxpool.Pool, store *blob.Store) *Service {
	return &Service{db: db, store: store}
}

// Upload streams content into object storage and records it.
//
// The digest is computed from the bytes AS THEY ARE WRITTEN, using a TeeReader,
// rather than from anything the caller claims. A client-supplied checksum
// verifies the client, not the storage.
//
// Order matters and is deliberate: bytes land first, metadata second. The
// reverse leaves a row pointing at an object that does not exist, which is a
// broken download. This way a failure leaves an orphaned object, which is
// invisible to users and reclaimable by a sweep.
func (s *Service) Upload(ctx context.Context, orgID, uploaderID, filename, contentType string, size int64, content io.Reader) (*File, error) {
	if size < 0 {
		return nil, errors.New("files: negative size")
	}
	if size > MaxUploadBytes {
		return nil, ErrTooLarge
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	id := uuid.NewString()
	// The key is generated and namespaced by org. It never contains the
	// user-supplied filename, which is attacker-controlled and would otherwise
	// allow writing outside the prefix or colliding across tenants.
	key := path.Join("org", orgID, "file", id)

	digest := sha256.New()
	written, err := s.store.Put(ctx, key, io.TeeReader(content, digest), size, contentType)
	if err != nil {
		return nil, err
	}
	sum := digest.Sum(nil)

	// Trust the storage layer's count over the caller's declared size.
	if written != size {
		_ = s.store.Remove(ctx, key)
		return nil, fmt.Errorf("files: declared %d bytes but stored %d", size, written)
	}

	f := &File{
		ID: id, OrgID: orgID, StorageKey: key, Filename: filename,
		ContentType: contentType, Size: written, SHA256: sum,
		Status: "ready", UploadedBy: uploaderID,
	}

	err = s.db.QueryRow(ctx, `
		INSERT INTO files (id, org_id, storage_key, filename, content_type,
		                   size_bytes, sha256, status, uploaded_by, finalized_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'ready',$8, now())
		RETURNING created_at`,
		f.ID, f.OrgID, f.StorageKey, f.Filename, f.ContentType,
		f.Size, f.SHA256, f.UploadedBy).Scan(&f.CreatedAt)
	if err != nil {
		// Metadata failed, so the object is unreferenced. Remove it rather
		// than leaving bytes nothing knows about.
		_ = s.store.Remove(ctx, key)
		return nil, fmt.Errorf("files: record metadata: %w", err)
	}
	return f, nil
}

// Get returns metadata by id, scoped to an org.
func (s *Service) Get(ctx context.Context, orgID, id string) (*File, error) {
	f := &File{}
	err := s.db.QueryRow(ctx, `
		SELECT id::text, org_id::text, storage_key, filename, content_type,
		       size_bytes, sha256, status, uploaded_by::text, created_at
		FROM files
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		id, orgID).Scan(&f.ID, &f.OrgID, &f.StorageKey, &f.Filename, &f.ContentType,
		&f.Size, &f.SHA256, &f.Status, &f.UploadedBy, &f.CreatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("files: get %s: %w", id, err)
	}
	return f, nil
}

// Open streams a file's content back, verifying the digest as it reads.
//
// The verification is the point of this function existing rather than callers
// using the store directly. "The upload returned 200" and "the bytes that come
// back are the bytes that went in" are different claims, and only the second one
// matters to whoever needs the file.
func (s *Service) Open(ctx context.Context, orgID, id string) (*File, io.ReadCloser, error) {
	f, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.store.Get(ctx, f.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return f, &verifyingReader{inner: rc, want: f.SHA256, digest: sha256.New()}, nil
}

// DownloadURL returns a presigned URL a browser can fetch directly.
//
// Signed against the PUBLIC endpoint. See the blob package comment: signing
// against the cluster address produces a valid URL that no browser can resolve,
// and it fails with nothing in any log.
func (s *Service) DownloadURL(ctx context.Context, orgID, id string, ttl time.Duration) (*url.URL, error) {
	f, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return s.store.PresignGet(ctx, f.StorageKey, f.Filename, ttl)
}

// Delete soft-deletes metadata and removes the object.
func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	f, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE files SET deleted_at = now() WHERE id = $1 AND org_id = $2`, id, orgID); err != nil {
		return fmt.Errorf("files: mark deleted: %w", err)
	}
	// Metadata first. If this fails the row is already gone from every query,
	// so the object is invisible and reclaimable rather than dangling.
	return s.store.Remove(ctx, f.StorageKey)
}

// verifyingReader checks the SHA-256 as bytes are read and fails at EOF if it
// does not match.
//
// Verifying at EOF rather than up front is what keeps this streaming. Reading
// the whole object into memory to check it first would defeat the point and
// would not survive a 512 MiB file.
type verifyingReader struct {
	inner  io.ReadCloser
	want   []byte
	digest hash
	done   bool
}

// hash is the subset of hash.Hash used here, kept narrow so the intent is
// obvious at the call site.
type hash interface {
	io.Writer
	Sum(b []byte) []byte
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.inner.Read(p)
	if n > 0 {
		_, _ = v.digest.Write(p[:n])
	}
	if errors.Is(err, io.EOF) && !v.done {
		v.done = true
		got := v.digest.Sum(nil)
		if !equalBytes(got, v.want) {
			return n, fmt.Errorf("%w: stored %x, read back %x", ErrCorrupted, v.want, got)
		}
	}
	return n, err
}

func (v *verifyingReader) Close() error { return v.inner.Close() }

// equalBytes compares without short-circuiting on length alone, which keeps the
// intent explicit even though these are not secrets.
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
