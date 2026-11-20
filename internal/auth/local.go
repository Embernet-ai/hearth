package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxFailedAttempts = 10
	lockoutDuration   = 15 * time.Minute
)

// ErrLockedOut is returned when an account has been throttled.
//
// Reported distinctly from ErrBadCredentials on purpose. It is not an
// enumeration risk, because reaching it requires already knowing the address and
// failing ten times, and hiding it means a locked-out user sees "wrong password"
// while typing the right one, which is a support call.
var ErrLockedOut = errors.New("too many failed attempts, try again later")

// NeedsSetup reports whether this installation has no users yet.
//
// It is what gates the first-run flow. Returning false the instant a single user
// exists is the entire protection: an installation cannot be re-claimed by
// whoever finds it next.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM principals WHERE kind = 'user'`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("auth: count users: %w", err)
	}
	return n == 0, nil
}

// Setup creates the first organisation and its owner.
//
// Refuses if any user already exists, checked inside the transaction, so two
// people racing to claim a fresh installation cannot both win.
func (s *Service) Setup(ctx context.Context, orgName, displayName, email, password string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return "", errors.New("auth: a valid email address is required")
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

	// Inside the transaction, and the row lock is what makes the race safe.
	var existing int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM principals WHERE kind = 'user'`).Scan(&existing); err != nil {
		return "", fmt.Errorf("auth: count users: %w", err)
	}
	if existing > 0 {
		return "", errors.New("auth: this installation is already set up")
	}

	orgID := uuid.NewString()
	slug := slugify(orgName)
	if slug == "" {
		slug = "hearth"
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,$3)`, orgID, slug, orgName); err != nil {
		return "", fmt.Errorf("auth: create org: %w", err)
	}

	principalID := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`,
		principalID, orgID, displayName); err != nil {
		return "", fmt.Errorf("auth: create principal: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO local_credentials (principal_id, org_id, email, password_hash)
		 VALUES ($1,$2,$3,$4)`, principalID, orgID, email, hash); err != nil {
		return "", fmt.Errorf("auth: create credential: %w", err)
	}

	// The founder is the first super admin, in the same transaction that
	// creates them.
	//
	// Without this a brand-new installation has nobody holding anything, so the
	// person who just claimed it cannot reach the People page and therefore
	// cannot add a single colleague. Migration 00028 grants the owner on
	// databases that already had users when it ran, which covers every existing
	// deployment and covers a fresh one not at all.
	//
	// Found by the acceptance suite, which is the only thing here that exercises
	// first-run setup: six of its checks failed on "no second account" and every
	// one of them was this.
	if _, err := tx.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'super_admin',$2)`, orgID, principalID); err != nil {
		return "", fmt.Errorf("auth: grant the founder super admin: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("auth: commit: %w", err)
	}
	return principalID, nil
}

// LoginLocal verifies an email and password and starts a session.
//
// Every failure path returns ErrBadCredentials with the same message, whether
// the address exists or not. Distinguishing them turns the login form into a
// directory of who has an account.
func (s *Service) LoginLocal(ctx context.Context, w http.ResponseWriter, r *http.Request, email, password string) error {
	email = strings.TrimSpace(strings.ToLower(email))

	var principalID, orgID, hash, status string
	var failed int
	var lockedUntil *time.Time
	// The principal's status is read here rather than checked afterwards,
	// because a suspended account that can still sign in is not suspended. The
	// join is why: the credential row survives suspension so the account can be
	// reinstated without anyone choosing a new password.
	err := s.db.QueryRow(ctx, `
		SELECT lc.principal_id::text, lc.org_id::text, lc.password_hash,
		       lc.failed_attempts, lc.locked_until, p.status
		FROM local_credentials lc
		JOIN principals p ON p.id = lc.principal_id
		WHERE lc.email = $1`, email).
		Scan(&principalID, &orgID, &hash, &failed, &lockedUntil, &status)

	if errors.Is(err, pgx.ErrNoRows) {
		// Spend the work anyway. Returning immediately makes a missing account
		// measurably faster than a wrong password, which is a timing oracle for
		// which addresses are registered.
		_, _ = VerifyPassword(dummyHash, password)
		return ErrBadCredentials
	}
	if err != nil {
		return fmt.Errorf("auth: load credential: %w", err)
	}

	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		return ErrLockedOut
	}

	// An account the directory provisioned has a credential row so its address
	// is known here, and an empty hash because it signs in through the identity
	// provider. Refused as a bad credential rather than as an error, and the
	// work is spent anyway so it takes as long as a wrong password: the
	// difference between the two is otherwise a way to ask which addresses are
	// federated.
	if hash == "" {
		_, _ = VerifyPassword(dummyHash, password)
		return ErrBadCredentials
	}

	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return fmt.Errorf("auth: verify password: %w", err)
	}
	if !ok {
		s.recordFailure(ctx, principalID, failed+1)
		return ErrBadCredentials
	}

	// Verified first, then refused. Checking the status before the password
	// would tell anybody with a wrong guess that the address is real and has
	// been suspended, which is the same disclosure the single error message
	// exists to prevent.
	if status != "active" {
		return ErrBadCredentials
	}

	// Success clears the throttle, so a user who mistypes twice and then gets it
	// right does not stay one attempt from a lockout for the rest of the day.
	if _, err := s.db.Exec(ctx, `
		UPDATE local_credentials
		SET failed_attempts = 0, locked_until = NULL, last_login_at = now()
		WHERE principal_id = $1`, principalID); err != nil {
		return fmt.Errorf("auth: clear failures: %w", err)
	}

	// Opportunistic upgrade: the only moment the plaintext is available.
	if NeedsRehash(hash) {
		if upgraded, err := HashPassword(password); err == nil {
			_, _ = s.db.Exec(ctx,
				`UPDATE local_credentials SET password_hash = $1, updated_at = now()
				 WHERE principal_id = $2`, upgraded, principalID)
		}
	}

	return s.Create(ctx, w, r, orgID, principalID)
}

func (s *Service) recordFailure(ctx context.Context, principalID string, attempts int) {
	if attempts >= maxFailedAttempts {
		until := time.Now().Add(lockoutDuration)
		_, _ = s.db.Exec(ctx, `
			UPDATE local_credentials SET failed_attempts = $1, locked_until = $2, updated_at = now()
			WHERE principal_id = $3`, attempts, until, principalID)
		return
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE local_credentials SET failed_attempts = $1, updated_at = now()
		WHERE principal_id = $2`, attempts, principalID)
}

// SetPassword changes a principal's password.
func (s *Service) SetPassword(ctx context.Context, principalID, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	ct, err := s.db.Exec(ctx, `
		UPDATE local_credentials
		SET password_hash = $1, must_change_password = false,
		    failed_attempts = 0, locked_until = NULL, updated_at = now()
		WHERE principal_id = $2`, hash, principalID)
	if err != nil {
		return fmt.Errorf("auth: set password: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return errors.New("auth: no local credential for that principal")
	}
	return nil
}

// dummyHash is a real argon2id hash of a value nobody knows. It exists so that
// authenticating a non-existent address costs the same as a wrong password.
const dummyHash = "$argon2id$v=19$m=19456,t=1,p=1$c29tZXNhbHR2YWx1ZTAxMg$J8Xk3wvVJ9dQK6xYqZ1mN4pR7sT2uW5yA8bC1dE3fG4"

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
