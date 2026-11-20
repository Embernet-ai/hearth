package docs

import (
	"context"
	"fmt"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Attachment is a file hanging off a document.
type Attachment struct {
	FileID      string
	Filename    string
	ContentType string
	Size        int64
	Digest      string
}

// Attach records that a file belongs to a document.
//
// The file gets NO grant of its own. Access is resolved through the parent
// document every time it is served, so the two can never disagree. Copying the
// document's grants onto the file would mean re-copying them on every share, and
// the first time somebody forgot, a file would outlive the permission that was
// supposed to protect it.
func (s *Service) Attach(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error {
	ref := core.Ref{Type: core.EntityDocument, ID: docID}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
		INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, 'file', $3, 'attachment', $4)
		ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
		orgID, docID, fileID, actor.Principal())
	if err != nil {
		return fmt.Errorf("docs: attach: %w", err)
	}
	// Only on the first attach. Re-attaching the same file is a no-op and must
	// not queue the extraction again.
	if ct.RowsAffected() > 0 {
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityFile), EntityID: fileID,
			Event: jobs.EventCreated, ActorID: actor.Principal(),
			Payload: map[string]any{"document_id": docID},
		}); err != nil {
			return err
		}
		// The document changed too, because what it holds is part of what it
		// is. Without this, attaching a manual to a work order leaves the work
		// order's own index entry describing a version that had no manual.
		if err := jobs.Publish(ctx, tx, jobs.EventSpec{
			OrgID: orgID, EntityType: string(core.EntityDocument), EntityID: docID,
			Event: jobs.EventUpdated, ActorID: actor.Principal(),
			Payload: map[string]any{"attached": fileID},
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// CanAttach reports whether the caller may add files to a document.
//
// Checked BEFORE any bytes are stored. Uploading first and authorising after
// would let anyone with the URL write into the object store regardless of
// whether the attachment ever succeeds.
func (s *Service) CanComment(ctx context.Context, actor core.Subjects, docID string) error {
	return s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment)
}

func (s *Service) CanAttach(ctx context.Context, actor core.Subjects, docID string) error {
	return s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessEdit)
}

// Attachments lists a document's files, if the caller may view the document.
func (s *Service) Attachments(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]Attachment, error) {
	ref := core.Ref{Type: core.EntityDocument, ID: docID}
	if err := s.authz.Require(ctx, actor, ref, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT f.id::text, f.filename, f.content_type, f.size_bytes, encode(f.sha256, 'hex')
		FROM entity_links l
		JOIN files f ON f.id = l.to_id
		WHERE l.from_type = 'document' AND l.from_id = $1
		  AND l.to_type = 'file' AND l.kind = 'attachment'
		  AND f.org_id = $2 AND f.deleted_at IS NULL
		ORDER BY f.created_at`, docID, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: attachments: %w", err)
	}
	defer rows.Close()

	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.FileID, &a.Filename, &a.ContentType, &a.Size, &a.Digest); err != nil {
			return nil, fmt.Errorf("docs: scan attachment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CanReadAttachment reports whether the caller may read a file THROUGH a
// document, which is the only way files are reachable.
//
// A file id alone grants nothing. The permission question is always about the
// parent, which is what keeps a shared link from outliving the share.
func (s *Service) CanReadAttachment(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return ErrNotFound
	}
	var n int
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM entity_links
		WHERE from_type='document' AND from_id=$1 AND to_type='file' AND to_id=$2
		  AND kind='attachment' AND org_id=$3`, docID, fileID, orgID).Scan(&n); err != nil {
		return fmt.Errorf("docs: check attachment: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveAttachment hides a file from its document without deleting it, so a
// wrong upload can go and still be brought back. Same permission as attaching.
// Removing one already removed is a no-op, not an error.
func (s *Service) RemoveAttachment(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error {
	return s.setAttachmentRemoved(ctx, orgID, actor, docID, fileID, true)
}

// RestoreAttachment brings a removed file back onto its document.
func (s *Service) RestoreAttachment(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error {
	return s.setAttachmentRemoved(ctx, orgID, actor, docID, fileID, false)
}

func (s *Service) setAttachmentRemoved(ctx context.Context, orgID string, actor core.Subjects,
	docID, fileID string, removed bool) error {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessEdit); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Only a file that really is attached to this document: a file id from
	// somewhere else must not be removable through a document the caller can
	// edit.
	ct, err := tx.Exec(ctx, `
		UPDATE files f
		   SET deleted_at = CASE WHEN $4 THEN now() ELSE NULL END
		 WHERE f.id = $2 AND f.org_id = $3
		   AND (f.deleted_at IS NULL) = $4
		   AND EXISTS (SELECT 1 FROM entity_links l
		                WHERE l.from_type = 'document' AND l.from_id = $1
		                  AND l.to_type = 'file' AND l.to_id = f.id AND l.kind = 'attachment')`,
		docID, fileID, orgID, removed)
	if err != nil {
		return fmt.Errorf("docs: remove attachment: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	// So search stops (or starts again) finding the file's contents.
	event := jobs.EventDeleted
	if !removed {
		event = jobs.EventCreated
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityFile), EntityID: fileID,
		Event: event, ActorID: actor.Principal(),
		Payload: map[string]any{"document_id": docID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemovedAttachments lists a document's removed files, for restoring them.
func (s *Service) RemovedAttachments(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]Attachment, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT f.id::text, f.filename, f.content_type, f.size_bytes, encode(f.sha256, 'hex')
		FROM entity_links l
		JOIN files f ON f.id = l.to_id
		WHERE l.from_type = 'document' AND l.from_id = $1
		  AND l.to_type = 'file' AND l.kind = 'attachment'
		  AND f.org_id = $2 AND f.deleted_at IS NOT NULL
		ORDER BY f.deleted_at DESC`, docID, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: removed attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.FileID, &a.Filename, &a.ContentType, &a.Size, &a.Digest); err != nil {
			return nil, fmt.Errorf("docs: scan removed attachment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
