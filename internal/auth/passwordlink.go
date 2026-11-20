package auth

// Single-use set-password links.
//
// An administrator who needs somebody to get in, a new colleague or one who is
// locked out, issues a link instead of inventing a password and passing it
// along. The link goes to the person, who chooses their own password, and the
// administrator never learns it.
//
// The token is 32 random bytes. Only its SHA-256 is stored, so a copy of the
// database cannot be turned into a way into an account. It works once and it
// expires after 24 hours. Issuing a new one retires every earlier link for that
// person, and redeeming one retires the rest, so there is never more than one
// way in at a time.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// PasswordLinkTTL is how long a link stays good.
const PasswordLinkTTL = 24 * time.Hour

var (
	// ErrLinkInvalid covers a link that is unknown, already used, or expired.
	// One error for all three, so a stranger cannot tell which tokens ever
	// existed.
	ErrLinkInvalid = errors.New("this link has already been used or has expired")
	// ErrNoCredential means the person signs in through an identity provider and
	// has no password here to set.
	ErrNoCredential = errors.New("this account signs in through your identity provider, so it has no password here")
)

// PasswordLink is a freshly issued link, shown to the administrator once.
type PasswordLink struct {
	Token     string
	ExpiresAt time.Time
	// PrincipalID and Email say who it is for.
	PrincipalID string
	Email       string
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// IssuePasswordLink makes a single-use link for somebody else's account.
//
// Owner or super admin only, and a super admin cannot issue one for the owner:
// that is the same line every other account action draws. Nobody issues one for
// themselves here, because they can already change their own password.
func (s *Service) IssuePasswordLink(ctx context.Context, orgID, actorID, principalID string) (*PasswordLink, error) {
	admin, err := s.IsAdmin(ctx, orgID, actorID)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, ErrNotOwner
	}
	if principalID == actorID {
		return nil, errors.New("auth: change your own password from your account page")
	}
	ownerID, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if principalID == ownerID {
		return nil, ErrIsOwner
	}
	return s.issueLink(ctx, orgID, actorID, principalID)
}

// issueLink is the part with no authority check, for AddPerson, which has
// already made one.
func (s *Service) issueLink(ctx context.Context, orgID, actorID, principalID string) (*PasswordLink, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var email string
	err = tx.QueryRow(ctx,
		`SELECT lc.email FROM local_credentials lc
		   JOIN principals p ON p.id = lc.principal_id
		  WHERE lc.principal_id = $1 AND lc.org_id = $2 AND p.kind = 'user'
		    AND lc.password_hash <> '' FOR UPDATE OF lc`,
		principalID, orgID).Scan(&email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNoCredential
	case err != nil:
		return nil, fmt.Errorf("auth: read credential: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("auth: token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(PasswordLinkTTL)

	// One live link per person.
	if _, err := tx.Exec(ctx,
		`DELETE FROM password_links WHERE principal_id = $1 AND used_at IS NULL`, principalID); err != nil {
		return nil, fmt.Errorf("auth: retire old links: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO password_links (token_hash, org_id, principal_id, created_by, expires_at)
		VALUES ($1,$2,$3,$4,$5)`, hashToken(token), orgID, principalID, actorID, expires); err != nil {
		return nil, fmt.Errorf("auth: store link: %w", err)
	}
	// The token is never written to the audit log, only the fact and the expiry.
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionPasswordLink,
		EntityType: "principal", EntityID: principalID,
		After: map[string]any{"email": email, "expires_at": expires.UTC().Format(time.RFC3339)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit: %w", err)
	}
	return &PasswordLink{Token: token, ExpiresAt: expires, PrincipalID: principalID, Email: email}, nil
}

// AddPersonWithLink creates a colleague who chooses their own password.
//
// The account is made with a long random password nobody is ever shown, and the
// link is the only way in. Same authority and same checks as AddPerson.
func (s *Service) AddPersonWithLink(ctx context.Context, orgID, actorID, displayName, email string) (*PasswordLink, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("auth: token: %w", err)
	}
	id, err := s.AddPerson(ctx, orgID, actorID, displayName, email, base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		return nil, err
	}
	return s.issueLink(ctx, orgID, actorID, id)
}

// PeekPasswordLink says whether a token is still good, and for whom, without
// using it. The set-password page calls it to decide between a form and an
// apology.
func (s *Service) PeekPasswordLink(ctx context.Context, token string) (email string, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", ErrLinkInvalid
	}
	err = s.db.QueryRow(ctx, `
		SELECT lc.email
		  FROM password_links l
		  JOIN local_credentials lc ON lc.principal_id = l.principal_id
		 WHERE l.token_hash = $1 AND l.used_at IS NULL AND l.expires_at > now()`,
		hashToken(token)).Scan(&email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrLinkInvalid
	case err != nil:
		return "", fmt.Errorf("auth: read link: %w", err)
	}
	return email, nil
}

// RedeemPasswordLink sets the password the link was issued for and uses it up.
//
// The single use is one UPDATE ... WHERE used_at IS NULL, so two requests with
// the same token cannot both win. Setting the password clears the
// must-change flag and any lockout, and signs the account out everywhere: a
// session that existed before somebody chose a new password has no business
// surviving it.
func (s *Service) RedeemPasswordLink(ctx context.Context, token, password string) error {
	token = strings.TrimSpace(token)
	if len(password) < minPasswordLength {
		return ErrWeak
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var principalID, orgID string
	err = tx.QueryRow(ctx, `
		UPDATE password_links SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING principal_id::text, org_id::text`, hashToken(token)).Scan(&principalID, &orgID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrLinkInvalid
	case err != nil:
		return fmt.Errorf("auth: use link: %w", err)
	}

	ct, err := tx.Exec(ctx, `
		UPDATE local_credentials
		   SET password_hash = $1, must_change_password = false,
		       failed_attempts = 0, locked_until = NULL, updated_at = now()
		 WHERE principal_id = $2`, hash, principalID)
	if err != nil {
		return fmt.Errorf("auth: set password: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNoCredential
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE principal_id = $1`, principalID); err != nil {
		return fmt.Errorf("auth: end sessions: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM password_links WHERE principal_id = $1 AND used_at IS NULL`, principalID); err != nil {
		return fmt.Errorf("auth: retire links: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: principalID, Action: audit.ActionPasswordSet,
		EntityType: "principal", EntityID: principalID,
		After: map[string]any{"via": "password link"},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
