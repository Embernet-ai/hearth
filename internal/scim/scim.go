// Package scim is the directory pushing changes at us.
//
// # Why this exists rather than reading group claims at sign-in
//
// Because "revoked at their next sign-in" is not revoked. A session here lasts
// up to thirty days, and the day somebody leaves is exactly the day nobody is
// going to sign in again. Reading the groups out of an ID token is easy and it
// answers a different question than the one being asked.
//
// SCIM is the directory telling us within seconds. Entra sends a PATCH when
// somebody is removed from a group, the team membership goes, and the very next
// request that person makes resolves a subject set without that team in it,
// because subjects are resolved per request and never cached into the session.
// Deactivating an account goes further and drops their sessions on the spot.
//
// # What is implemented, and what is deliberately not
//
// Users and Groups, with the operations Entra actually sends: create, replace,
// patch (active, displayName, and members add/remove/replace), delete, get, and
// list with the two filters it uses. That is the whole of what a provisioning
// job does.
//
// Not implemented: the Schemas, ResourceTypes, and ServiceProviderConfig
// discovery endpoints, PATCH paths with value filters beyond membership, sort,
// and ETags. They are in the specification and no provisioning job depends on
// them. Every one of them would be code nothing exercises, which is the same as
// code that does not work.
//
// # Nobody, deliberately
//
// The token here acts as the directory rather than as a person. It creates and
// suspends accounts and edits teams, and it cannot read a work order. That is
// why it has its own table rather than another row in api_tokens: sharing the
// table is how the boundary gets lost the next time somebody refactors
// authentication.
package scim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/teams"
)

var (
	ErrNoToken  = errors.New("no valid provisioning token")
	ErrNotFound = errors.New("no such resource")
)

// TokenPrefix makes a leaked secret searchable. A string that announces what
// it is gets found in a paste and revoked; an anonymous blob of base64 does not.
const TokenPrefix = "hearth_scim_"

// Schema URNs, spelled out because a provisioning job checks them.
const (
	schemaUser      = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup     = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaList      = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaError     = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaPatchOp   = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	contentTypeSCIM = "application/scim+json"
)

// People is the slice of the account service this package needs, declared by
// the consumer like every other interface here.
type People interface {
	ProvisionUser(ctx context.Context, orgID, externalID, displayName, email string) (string, error)
	SetProvisionedStatus(ctx context.Context, orgID, principalID, status string) error
}

// Service is the provisioning endpoint.
type Service struct {
	db    *pgxpool.Pool
	teams teams.Store
	users People
	log   *slog.Logger
}

func New(db *pgxpool.Pool, t teams.Store, p People, log *slog.Logger) *Service {
	return &Service{db: db, teams: t, users: p, log: log}
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

// Token is a provisioning credential as the settings page shows it.
type Token struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Mint issues a token and returns it once. There is nowhere to read it back
// from, which is the point.
func (s *Service) Mint(ctx context.Context, orgID, actorID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("scim: a token needs a name, so it can be revoked by name later")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("scim: generate token: %w", err)
	}
	secret := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("scim: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO scim_tokens (id, org_id, name, hash, created_by)
		VALUES ($1,$2,$3,$4,$5)`, id, orgID, name, sum[:], actorID); err != nil {
		return "", fmt.Errorf("scim: store token: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTokenMint,
		EntityType: "scim_token", EntityID: id,
		After: map[string]any{"name": name, "kind": "provisioning"},
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("scim: commit: %w", err)
	}
	return secret, nil
}

func (s *Service) Revoke(ctx context.Context, orgID, actorID, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("scim: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx,
		`UPDATE scim_tokens SET revoked_at = now()
		  WHERE id=$1 AND org_id=$2 AND revoked_at IS NULL`, id, orgID)
	if err != nil {
		return fmt.Errorf("scim: revoke: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionTokenRevoke,
		EntityType: "scim_token", EntityID: id,
		Before: map[string]any{"kind": "provisioning"},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Tokens(ctx context.Context, orgID string) ([]*Token, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, name, created_at, last_used_at
		  FROM scim_tokens
		 WHERE org_id=$1 AND revoked_at IS NULL
		 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("scim: tokens: %w", err)
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		t := &Token{}
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scim: scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// authenticate resolves a bearer token to the org it provisions.
//
// Constant time, and the lookup is on the hash so the table is useless to
// anybody who reads it.
func (s *Service) authenticate(ctx context.Context, r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || !strings.HasPrefix(raw, TokenPrefix) {
		return "", ErrNoToken
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))

	var id, orgID string
	var stored []byte
	err := s.db.QueryRow(ctx, `
		SELECT id::text, org_id::text, hash FROM scim_tokens
		 WHERE hash=$1 AND revoked_at IS NULL`, sum[:]).Scan(&id, &orgID, &stored)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrNoToken
	case err != nil:
		return "", fmt.Errorf("scim: authenticate: %w", err)
	}
	if subtle.ConstantTimeCompare(sum[:], stored) != 1 {
		return "", ErrNoToken
	}
	// Best effort: a token that works and cannot record that it was used is
	// still a token that works, and refusing the directory over a bookkeeping
	// write would be the wrong trade.
	_, _ = s.db.Exec(ctx, `UPDATE scim_tokens SET last_used_at = now() WHERE id=$1`, id)
	return orgID, nil
}
