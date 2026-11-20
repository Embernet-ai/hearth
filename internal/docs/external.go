package docs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// ExternalRef ties a document to whatever the system it came from called it.
type ExternalRef struct {
	Source     string
	ExternalID string
	URL        string
}

// FindByExternalID returns the document id previously imported under this
// remote id, or "" if this is the first time we have seen it.
//
// This is the whole of import idempotency. An importer that skips this check
// works perfectly on the first run and doubles the data on the second.
func (s *Service) FindByExternalID(ctx context.Context, orgID, source, externalID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT entity_id::text FROM external_refs
		WHERE org_id = $1 AND source = $2 AND external_id = $3`,
		orgID, source, externalID).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("docs: lookup external ref: %w", err)
	}
	return id, nil
}

// RecordExternalRef remembers that a document came from somewhere.
func (s *Service) RecordExternalRef(ctx context.Context, orgID string, ref core.Ref, e ExternalRef) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id, external_url)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (org_id, source, external_id)
		DO UPDATE SET entity_id = EXCLUDED.entity_id,
		              external_url = EXCLUDED.external_url,
		              updated_at = now()`,
		orgID, e.Source, e.ExternalID, string(ref.Type), ref.ID, nullIfEmpty(e.URL))
	if err != nil {
		return fmt.Errorf("docs: record external ref: %w", err)
	}
	return nil
}

// ExternalRefFor returns where a document was imported from, if anywhere.
func (s *Service) ExternalRefFor(ctx context.Context, orgID, docID string) (*ExternalRef, error) {
	e := &ExternalRef{}
	var url *string
	err := s.db.QueryRow(ctx, `
		SELECT source, external_id, external_url FROM external_refs
		WHERE org_id = $1 AND entity_type = 'document' AND entity_id = $2`,
		orgID, docID).Scan(&e.Source, &e.ExternalID, &url)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("docs: external ref for %s: %w", docID, err)
	}
	if url != nil {
		e.URL = *url
	}
	return e, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
