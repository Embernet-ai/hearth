package auth

// Accounts the directory owns.
//
// # Why this is separate from AddPerson
//
// AddPerson is the owner typing somebody's name in, and it checks that the
// caller is the owner and sets a password they will be made to change. Neither
// of those is true here: the caller is a provisioning job with no person behind
// it, and the account signs in through the identity provider, so there is no
// password to set and nothing to make anybody change.
//
// Sharing one function between the two would mean a boolean parameter deciding
// whether the authority check applies, and a boolean that switches off an
// authority check is the most dangerous kind of parameter there is.
//
// # The owner is never taken over
//
// A directory that finds an address matching the person who claimed this
// installation must not adopt that account, because the next thing it does is
// deactivate everybody who is not in a group it knows about. Locking the owner
// out of their own self-hosted workspace, from a job nobody is watching, on a
// weekend, is not a recoverable failure: there is nobody left who can undo it.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// ErrOwnerIsNotProvisioned is returned when a directory tries to take over or
// deactivate the account that claimed this installation.
var ErrOwnerIsNotProvisioned = errors.New(
	"the account that claimed this installation is not managed by the directory")

// ProvisionUser creates or updates an account the directory owns, and returns
// its principal id.
//
// Matched on the directory's own id first, because that is what survives
// somebody changing their name at work. Email second, so a directory switched
// on for an installation that already has people adopts them rather than
// creating a second account for every one of them.
func (s *Service) ProvisionUser(ctx context.Context, orgID, externalID, displayName, email string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	displayName = strings.TrimSpace(displayName)
	if email == "" || !strings.Contains(email, "@") {
		return "", errors.New("auth: a provisioned account needs an email address")
	}
	if displayName == "" {
		displayName, _, _ = strings.Cut(email, "@")
	}
	externalID = strings.TrimSpace(externalID)

	owner, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return "", err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existing string
	if externalID != "" {
		err := tx.QueryRow(ctx,
			`SELECT id::text FROM principals
			  WHERE org_id=$1 AND external_id=$2 AND kind='user'`,
			orgID, externalID).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("auth: match by directory id: %w", err)
		}
	}
	if existing == "" {
		err := tx.QueryRow(ctx,
			`SELECT p.id::text FROM principals p
			   JOIN local_credentials c ON c.principal_id = p.id
			  WHERE p.org_id=$1 AND c.email=$2 AND p.kind='user'`,
			orgID, email).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("auth: match by email: %w", err)
		}
		if existing != "" && existing == owner {
			return "", ErrOwnerIsNotProvisioned
		}
	}

	if existing != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE principals
			   SET display_name = $3,
			       source = 'scim',
			       external_id = COALESCE(NULLIF($4,''), external_id),
			       updated_at = now()
			 WHERE id = $1 AND org_id = $2`,
			existing, orgID, displayName, externalID); err != nil {
			return "", fmt.Errorf("auth: update provisioned account: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("auth: commit: %w", err)
		}
		return existing, nil
	}

	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO principals (id, org_id, kind, display_name, source, external_id)
		VALUES ($1,$2,'user',$3,'scim',NULLIF($4,''))`,
		id, orgID, displayName, externalID); err != nil {
		return "", fmt.Errorf("auth: create provisioned principal: %w", err)
	}
	// A credential row with no password hash, so the address is known here and
	// resolves to this person, and there is nothing to sign in with locally.
	// Without it, an account provisioned from the directory has no email
	// anywhere in this database and cannot be found by one, which is how a
	// provisioning job ends up creating the same person on every run.
	if _, err := tx.Exec(ctx, `
		INSERT INTO local_credentials (principal_id, org_id, email, password_hash, must_change_password)
		VALUES ($1,$2,$3,'',false)
		ON CONFLICT (principal_id) DO NOTHING`, id, orgID, email); err != nil {
		return "", fmt.Errorf("auth: record provisioned address: %w", err)
	}
	// Actor is empty: the directory did this and there is nobody behind it.
	// That is a fact worth recording as a fact rather than attributing to
	// whoever happened to configure the connection months ago.
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, Action: audit.ActionPersonAdd,
		EntityType: "principal", EntityID: id,
		After: map[string]any{"name": displayName, "email": email, "by": "directory"},
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("auth: commit: %w", err)
	}
	return id, nil
}

// SetProvisionedStatus suspends or reinstates an account on the directory's
// say-so, with no owner check because there is no owner making the request.
//
// The owner's own account is refused. See above for why that is not a
// recoverable mistake.
func (s *Service) SetProvisionedStatus(ctx context.Context, orgID, principalID, status string) error {
	if status != "active" && status != "suspended" {
		return fmt.Errorf("auth: %q is not an account status", status)
	}
	owner, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return err
	}
	if principalID == owner {
		return ErrOwnerIsNotProvisioned
	}
	return s.setStatus(ctx, orgID, "", principalID, status)
}

// SignInAllowed reports whether an account may start a session.
//
// Read on every sign-in rather than only when a password is checked, because a
// suspended account signing in through the identity provider would otherwise
// walk straight past the check that exists for the local one.
func (s *Service) SignInAllowed(ctx context.Context, principalID string) (bool, error) {
	var status string
	err := s.db.QueryRow(ctx,
		`SELECT status FROM principals WHERE id = $1`, principalID).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("auth: read status: %w", err)
	}
	return status == "active", nil
}

// OrgOf is the organisation a principal belongs to.
func (s *Service) OrgOf(ctx context.Context, principalID string) (string, error) {
	var org string
	err := s.db.QueryRow(ctx, `SELECT org_id::text FROM principals WHERE id = $1`, principalID).Scan(&org)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoPerson
	}
	return org, err
}

// DisplayNameOf is what to call a principal.
func (s *Service) DisplayNameOf(ctx context.Context, principalID string) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT display_name FROM principals WHERE id = $1`, principalID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoPerson
	}
	return name, err
}
