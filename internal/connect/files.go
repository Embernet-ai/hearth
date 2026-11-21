package connect

// Files: browse OneDrive, SharePoint and Google Drive, link a file to a Hearth document, or import a copy.
//
// Browsing is live and nothing is cached: what a person sees is what the provider says they may see. A link
// records where the file lives; an import downloads it once and attaches the copy to the document. Both need the
// person to be able to edit the document, which the document service decides.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxImport is the largest file an import will take. A bigger one is linked instead.
const MaxImport = 50 << 20

// Drives lists the places a person can browse.
func (s *Service) Drives(ctx context.Context, orgID, principalID string, p Provider) ([]Drive, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Files)
	if err != nil {
		return nil, err
	}
	return sess.Files().Drives(ctx)
}

// Browse lists one folder.
func (s *Service) Browse(ctx context.Context, orgID, principalID string, p Provider, driveID, folderID string) ([]FileItem, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Files)
	if err != nil {
		return nil, err
	}
	return sess.Files().List(ctx, driveID, folderID)
}

// FileLink is a file linked to a document.
type FileLink struct {
	ID         string
	Provider   Provider
	DriveID    string
	ItemID     string
	Name       string
	WebURL     string
	DocumentID string
	Imported   bool
}

// Link records a file against a document without copying it.
func (s *Service) Link(ctx context.Context, orgID, principalID string, p Provider, driveID, itemID, docID string) (*FileLink, error) {
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return nil, err
	}
	if err := s.docs.CanAttach(ctx, actor, docID); err != nil {
		return nil, err
	}
	sess, err := s.session(ctx, orgID, principalID, p, Files)
	if err != nil {
		return nil, err
	}
	it, err := sess.Files().Item(ctx, driveID, itemID)
	if err != nil {
		return nil, err
	}
	return s.saveFileLink(ctx, orgID, principalID, p, driveID, itemID, it, docID, "")
}

// Import copies a file into Hearth and attaches it to a document, and links it too so the original is one click
// away.
func (s *Service) Import(ctx context.Context, orgID, principalID string, p Provider, driveID, itemID, docID string) (*FileLink, error) {
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return nil, err
	}
	if err := s.docs.CanAttach(ctx, actor, docID); err != nil {
		return nil, err
	}
	sess, err := s.session(ctx, orgID, principalID, p, Files)
	if err != nil {
		return nil, err
	}
	it, body, err := sess.Files().Open(ctx, driveID, itemID, MaxImport)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	if it.Folder {
		return nil, errors.New("connect: a folder cannot be imported, open it and choose its files")
	}
	f, err := s.files.Upload(ctx, orgID, principalID, it.Name, it.MIME, it.Size, io.LimitReader(body, MaxImport+1))
	if err != nil {
		return nil, fmt.Errorf("connect: store the file: %w", err)
	}
	if err := s.docs.Attach(ctx, orgID, actor, docID, f.ID); err != nil {
		return nil, err
	}
	return s.saveFileLink(ctx, orgID, principalID, p, driveID, itemID, it, docID, f.ID)
}

func (s *Service) saveFileLink(ctx context.Context, orgID, principalID string, p Provider, driveID, itemID string,
	it FileItem, docID, fileID string) (*FileLink, error) {

	var file any
	if fileID != "" {
		file = fileID
	}
	id := uuid.NewString()
	err := s.db.QueryRow(ctx, `
		INSERT INTO connector_file_links (id, org_id, principal_id, provider, drive_id, item_id, name, web_url, document_id, file_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::uuid)
		ON CONFLICT (document_id, provider, drive_id, item_id) DO UPDATE
		   SET name=EXCLUDED.name, web_url=EXCLUDED.web_url,
		       file_id=COALESCE(EXCLUDED.file_id, connector_file_links.file_id)
		RETURNING id::text`,
		id, orgID, principalID, string(p), driveID, itemID, clip(it.Name, 300), it.WebURL, docID, file).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("connect: save file link: %w", err)
	}
	return &FileLink{ID: id, Provider: p, DriveID: driveID, ItemID: itemID, Name: it.Name, WebURL: it.WebURL,
		DocumentID: docID, Imported: fileID != ""}, nil
}

// LinksFor lists the cloud files linked to a document, for a person who may view it.
func (s *Service) LinksFor(ctx context.Context, orgID, principalID, docID string) ([]FileLink, error) {
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return nil, err
	}
	if _, err := s.docs.Attachments(ctx, orgID, actor, docID); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, provider, drive_id, item_id, name, web_url, file_id IS NOT NULL
		  FROM connector_file_links WHERE org_id=$1 AND document_id=$2 ORDER BY created_at`, orgID, docID)
	if err != nil {
		return nil, fmt.Errorf("connect: read file links: %w", err)
	}
	defer rows.Close()
	var out []FileLink
	for rows.Next() {
		var l FileLink
		var p string
		if err := rows.Scan(&l.ID, &p, &l.DriveID, &l.ItemID, &l.Name, &l.WebURL, &l.Imported); err != nil {
			return nil, err
		}
		l.Provider, l.DocumentID = Provider(p), docID
		out = append(out, l)
	}
	return out, rows.Err()
}

// Unlink removes a link. The imported copy, if any, stays attached.
func (s *Service) Unlink(ctx context.Context, orgID, principalID, linkID string) error {
	var docID string
	err := s.db.QueryRow(ctx, `SELECT document_id::text FROM connector_file_links WHERE org_id=$1 AND id=$2`,
		orgID, linkID).Scan(&docID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return err
	}
	if err := s.docs.CanAttach(ctx, actor, docID); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM connector_file_links WHERE org_id=$1 AND id=$2`, orgID, linkID)
	return err
}

// CanBrowse reports whether a person has at least one provider they can browse files in: Files is on and their
// connection covers it. It is what decides whether a document shows "Add from OneDrive or Drive".
func (s *Service) CanBrowse(ctx context.Context, orgID, principalID string) bool {
	for _, p := range Providers {
		b := s.Backend(p)
		if b == nil {
			continue
		}
		if on, _ := s.Enabled(ctx, orgID, p, Files); !on {
			continue
		}
		if granted, err := b.Granted(ctx, principalID); err == nil && HasScopes(granted, b.Scopes(Files)) {
			return true
		}
	}
	return false
}
