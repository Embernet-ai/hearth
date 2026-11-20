package auth

// Adding a second person to an installation.
//
// This lands with views rather than with roles because a view that can be
// shared needs somebody to share it with, and until now the only way to get a
// second account was to configure an identity provider. A self-hosted
// workspace where the owner cannot add a colleague is not a workspace.
//
// Authority is deliberately crude for the moment: the owner is the principal
// who claimed the installation, identified as the earliest one in the org.
// Phase sixteen replaces this with real roles and teams. It is written as one
// function so that when it does, there is exactly one place to change and no
// scattered "if you are the owner" checks to find.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/audit"
)

var (
	// ErrNotOwner is returned when somebody who is neither the owner nor a
	// super admin tries to administer accounts. A super admin sits one step
	// below the owner: they can do anything to anyone except the owner.
	ErrNotOwner = errors.New("only the owner or a super admin can do that")
	// ErrIsOwner is returned when somebody tries to change the owner's own
	// account. Nobody outranks the owner, so nobody can lock them out.
	ErrIsOwner = errors.New("the owner's account cannot be changed by anybody else")
	// ErrEmailTaken means that address already signs in here.
	ErrEmailTaken = errors.New("that email address already has an account")
	ErrWeak       = errors.New("a password must be at least 12 characters")
	// ErrNoPerson means no such person in this organisation.
	ErrNoPerson = errors.New("no such person")
)

// minPasswordLength is length rather than a composition rule on purpose. A
// rule that demands a symbol produces Passw0rd!, which is worse than a long
// phrase and much harder to remember.
const minPasswordLength = 12

// Person is somebody with an account here.
type Person struct {
	PrincipalID string
	DisplayName string
	Email       string
	Status      string
	IsOwner     bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

// OwnerID returns the principal who claimed this installation.
//
// The earliest user in the org, which is exactly who Setup created and cannot
// be transferred by accident. A tie is impossible in practice and would resolve
// on the id, which is stable, so this never returns two different answers.
//
// Cached, because every page render asks and the answer cannot change: the
// earliest row stays the earliest row. Ownership becoming transferable is a
// phase sixteen problem, and the cache is the second thing to delete when it is.
func (s *Service) OwnerID(ctx context.Context, orgID string) (string, error) {
	if v, ok := s.owners.Load(orgID); ok {
		return v.(string), nil
	}
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id::text FROM principals
		WHERE org_id = $1 AND kind = 'user'
		ORDER BY created_at, id
		LIMIT 1`, orgID).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", errors.New("auth: this org has no users")
	case err != nil:
		return "", fmt.Errorf("auth: resolve owner: %w", err)
	}
	s.owners.Store(orgID, id)
	return id, nil
}

// IsOwner reports whether this principal claimed the installation.
func (s *Service) IsOwner(ctx context.Context, orgID, principalID string) (bool, error) {
	owner, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return false, err
	}
	return owner == principalID, nil
}

// IsAdmin reports whether this principal may administer the installation: the
// owner, or a super admin. The two differ in exactly one place -- a super admin
// cannot act on the owner's own account -- and that is checked where it
// applies, not here.
func (s *Service) IsAdmin(ctx context.Context, orgID, principalID string) (bool, error) {
	owner, err := s.IsOwner(ctx, orgID, principalID)
	if err != nil || owner {
		return owner, err
	}
	if s.subjects == nil {
		return false, nil
	}
	sub, err := s.subjects.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return false, err
	}
	return sub.Super(), nil
}

// AddPerson creates a colleague with a local account.
//
// The whole thing is one transaction. A principal with no credential is a row
// nobody can sign in as and nothing cleans up, and it is exactly what a partial
// failure here would leave behind.
func (s *Service) AddPerson(ctx context.Context, orgID, actorID, displayName, email, password string) (string, error) {
	admin, err := s.IsAdmin(ctx, orgID, actorID)
	if err != nil {
		return "", err
	}
	if !admin {
		return "", ErrNotOwner
	}

	displayName = strings.TrimSpace(displayName)
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return "", errors.New("auth: a valid email address is required")
	}
	if displayName == "" {
		// Better than an empty row in every assignee dropdown.
		displayName, _, _ = strings.Cut(email, "@")
	}
	if len(password) < minPasswordLength {
		return "", ErrWeak
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var taken bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM local_credentials WHERE email = $1)`, email).Scan(&taken); err != nil {
		return "", fmt.Errorf("auth: check email: %w", err)
	}
	if taken {
		return "", ErrEmailTaken
	}

	principalID := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`,
		principalID, orgID, displayName); err != nil {
		return "", fmt.Errorf("auth: create principal: %w", err)
	}
	// must_change_password is set because the owner chose this password and now
	// knows it. The account is usable immediately either way; the flag is what
	// the sign-in path reads to insist on a new one.
	if _, err := tx.Exec(ctx, `
		INSERT INTO local_credentials (principal_id, org_id, email, password_hash, must_change_password)
		VALUES ($1,$2,$3,$4,true)`, principalID, orgID, email, hash); err != nil {
		return "", fmt.Errorf("auth: create credential: %w", err)
	}

	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionPersonAdd,
		EntityType: "principal", EntityID: principalID,
		After: map[string]any{"name": displayName, "email": email},
	}); err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("auth: commit: %w", err)
	}
	return principalID, nil
}

// SetPersonStatus suspends or reinstates an account.
//
// Being able to add a colleague and never remove one is a half-feature, and the
// half that is missing is the one that matters on the day somebody leaves.
// Suspension rather than deletion, because a principal is the foreign key on
// every "who did this" in the database and deleting one would take the history
// with it.
func (s *Service) SetPersonStatus(ctx context.Context, orgID, actorID, principalID, status string) error {
	admin, err := s.IsAdmin(ctx, orgID, actorID)
	if err != nil {
		return err
	}
	if !admin {
		return ErrNotOwner
	}
	if status != "active" && status != "suspended" {
		return fmt.Errorf("auth: %q is not an account status", status)
	}
	// Locking yourself out of your own installation is not a thing anybody
	// means to do, and there is nobody else who could undo it.
	if principalID == actorID {
		return errors.New("auth: you cannot suspend your own account")
	}
	// The one thing a super admin cannot do: touch the owner. The owner is
	// the account that can undo anything else, so it must never be the one
	// that gets locked out.
	ownerID, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return err
	}
	if principalID == ownerID {
		return ErrIsOwner
	}

	return s.setStatus(ctx, orgID, actorID, principalID, status)
}

// setStatus is the part with no owner check in it, so the directory can suspend
// somebody without being the owner. Unexported, and the two callers above and
// in the provisioning path are the only ones: an exported version of this is a
// way to change an account's status with no authority at all.
func (s *Service) setStatus(ctx context.Context, orgID, actorID, principalID, status string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read what it was first, in the same transaction. A subquery inside
	// RETURNING would also give the old value, for reasons about snapshots that
	// nobody should have to know to read this.
	var was string
	err = tx.QueryRow(ctx,
		`SELECT status FROM principals
		  WHERE id = $1 AND org_id = $2 AND kind = 'user' FOR UPDATE`,
		principalID, orgID).Scan(&was)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errors.New("auth: no such account")
	case err != nil:
		return fmt.Errorf("auth: read account status: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE principals SET status = $1, updated_at = now() WHERE id = $2`,
		status, principalID); err != nil {
		return fmt.Errorf("auth: set account status: %w", err)
	}
	// Suspension has to take effect now rather than in up to thirty days, which
	// is how long an existing session would otherwise keep working.
	var offTeams []string
	if status == "suspended" {
		// And off every team in the same step. A team is a grant subject, so a
		// suspended account that stays in one keeps counting everywhere grants
		// are read without a sign-in. Reinstating does NOT put them back: that
		// was an admin's removal, so restoring it is an admin's act, and the
		// names are in the audit entry below for exactly that.
		rows, err := tx.Query(ctx, `
			DELETE FROM team_members m USING teams t
			 WHERE m.team_id = t.id AND m.principal_id = $1 AND t.org_id = $2
			RETURNING t.name`, principalID, orgID)
		if err != nil {
			return fmt.Errorf("auth: take off teams: %w", err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return fmt.Errorf("auth: read removed team: %w", err)
			}
			offTeams = append(offTeams, name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("auth: take off teams: %w", err)
		}
		sort.Strings(offTeams)
		if _, err := tx.Exec(ctx,
			// impersonated_by too: suspending somebody must also end any
			// playground session they are in, which belongs to their
			// playground self and so would otherwise outlive them.
			`DELETE FROM sessions WHERE principal_id = $1 OR impersonated_by = $1`, principalID); err != nil {
			return fmt.Errorf("auth: revoke sessions: %w", err)
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{
		OrgID: orgID, ActorID: actorID, Action: audit.ActionPersonStatus,
		EntityType: "principal", EntityID: principalID,
		Before: map[string]any{"status": was},
		After:  afterStatus(status, offTeams),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// afterStatus is the audit entry's after: the status, and the teams the account
// was taken off when it was suspended.
func afterStatus(status string, offTeams []string) map[string]any {
	after := map[string]any{"status": status}
	if len(offTeams) > 0 {
		after["removed_from_teams"] = offTeams
	}
	return after
}

// ChangeOwnPassword lets somebody replace their own password, having proved
// they know the current one.
//
// Requiring the old password is not theatre. Without it, an unattended laptop
// becomes a permanent account takeover instead of a session somebody can
// revoke.
func (s *Service) ChangeOwnPassword(ctx context.Context, principalID, current, next string) error {
	if len(next) < minPasswordLength {
		return ErrWeak
	}
	var hash string
	err := s.db.QueryRow(ctx,
		`SELECT password_hash FROM local_credentials WHERE principal_id = $1`, principalID).Scan(&hash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// A federated account has no password here to change.
		return errors.New("auth: this account signs in through your identity provider")
	case err != nil:
		return fmt.Errorf("auth: load credential: %w", err)
	}

	ok, err := VerifyPassword(hash, current)
	if err != nil {
		return fmt.Errorf("auth: verify password: %w", err)
	}
	if !ok {
		return ErrBadCredentials
	}
	return s.SetPassword(ctx, principalID, next)
}

// MustChangePassword reports whether this account is still on a password
// somebody else chose.
func (s *Service) MustChangePassword(ctx context.Context, principalID string) (bool, error) {
	var must bool
	err := s.db.QueryRow(ctx,
		`SELECT must_change_password FROM local_credentials WHERE principal_id = $1`,
		principalID).Scan(&must)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("auth: read password state: %w", err)
	}
	return must, nil
}

// People lists everybody with an account, for the owner's admin page.
//
// Federated accounts appear with no email, because their address belongs to the
// identity provider and is only recorded when it has been seen at least once.
func (s *Service) People(ctx context.Context, orgID string) ([]Person, error) {
	owner, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	// last_login_at only ever moves on a local password sign-in -- an SSO
	// login never touches local_credentials at all. What it does touch is
	// user_identities.updated_at, bumped on every federated callback (see
	// oidc.go's link()), so a federated-only account's real activity was
	// already being recorded, just never read here. GREATEST of the two
	// (Postgres skips a NULL side rather than propagating it) is "whichever
	// happened more recently", which is what "last seen" is supposed to mean
	// regardless of which door somebody came in. DISTINCT ON picks the single
	// most recently touched identity per principal, so a person who has
	// linked more than one issuer still produces one row here, not a fan-out.
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.display_name, p.status, p.created_at,
		       COALESCE(lc.email::text, ui.email::text, ''),
		       GREATEST(lc.last_login_at, ui.last_seen)
		FROM principals p
		LEFT JOIN local_credentials lc ON lc.principal_id = p.id
		LEFT JOIN (
		    SELECT DISTINCT ON (principal_id) principal_id, email, updated_at AS last_seen
		      FROM user_identities
		     ORDER BY principal_id, updated_at DESC
		) ui ON ui.principal_id = p.id
		WHERE p.org_id = $1 AND p.kind = 'user'
		ORDER BY p.created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("auth: list people: %w", err)
	}
	defer rows.Close()

	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.PrincipalID, &p.DisplayName, &p.Status,
			&p.CreatedAt, &p.Email, &p.LastLoginAt); err != nil {
			return nil, fmt.Errorf("auth: scan person: %w", err)
		}
		p.IsOwner = p.PrincipalID == owner
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetAvatar puts a picture on somebody's own profile, or takes it off when
// fileID is empty. The file is one the caller already stored (files.Upload);
// this only records which one is theirs, in the avatar_key column that has
// been waiting for it since the first migration.
func (s *Service) SetAvatar(ctx context.Context, orgID, principalID, fileID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE principals SET avatar_key = NULLIF($3, ''), updated_at = now()
		WHERE id = $1 AND org_id = $2`, principalID, orgID, fileID)
	if err != nil {
		return fmt.Errorf("auth: set avatar: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoPerson
	}
	return nil
}

// Avatar is what a picture of somebody needs: their name, for the initials
// when there is no picture, and the file that is their picture, if any.
func (s *Service) Avatar(ctx context.Context, orgID, principalID string) (name, fileID string, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT display_name, coalesce(avatar_key, '')
		FROM principals WHERE id = $1 AND org_id = $2`, principalID, orgID).Scan(&name, &fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNoPerson
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: avatar: %w", err)
	}
	return name, fileID, nil
}
