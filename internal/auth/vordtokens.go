package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// With the EmberOPS back end, a person's own Vord access token is what
// EmberOPS authorises, so the session has to keep it. The access and refresh
// tokens are sealed (internal/platform/secret) and held in the sessions row,
// never in the cookie and never in the clear: the cookie is an opaque id, and a
// stolen database backup without HEARTH_ENCRYPTION_KEY is not a pile of working
// credentials.
//
// The access token is refreshed with the refresh token shortly before it
// expires. Refresh runs under a row lock so two requests (or two replicas) do
// not both spend a refresh token the provider rotates.

var (
	// ErrNoUserToken means this session holds no Vord token for EmberOPS:
	// it was started before the switch, or by a local password. Signing in
	// again with Vord fixes it.
	ErrNoUserToken = errors.New("auth: this session has no Vord token for EmberOps; sign in again")
	// ErrTokenExpired means the refresh token no longer works, which is what
	// an expired or revoked Vord session looks like.
	ErrTokenExpired = errors.New("auth: the Vord session behind this one has ended; sign in again")
)

// refreshSkew is how early the access token is treated as expired.
const refreshSkew = 60 * time.Second

// storedTokens is what is sealed into sessions.vord_tokens.
type storedTokens struct {
	Access  string    `json:"a"`
	Refresh string    `json:"r"`
	Expiry  time.Time `json:"e"`
}

// tokenRow is the lock-and-update seam over the sessions row, so the refresh
// logic is testable without a database.
type tokenRow interface {
	// update runs fn with the current sealed blob under a lock. fn returns the
	// replacement blob, or nil to leave it.
	update(ctx context.Context, sessionID []byte, fn func(blob []byte) ([]byte, error)) error
}

type pgTokenRow struct{ svc *Service }

func (p pgTokenRow) update(ctx context.Context, id []byte, fn func([]byte) ([]byte, error)) error {
	tx, err := p.svc.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var blob []byte
	err = tx.QueryRow(ctx, `SELECT vord_tokens FROM sessions WHERE id = $1 FOR UPDATE`, id).Scan(&blob)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSession
	}
	if err != nil {
		return err
	}
	next, err := fn(blob)
	if err != nil {
		return err
	}
	if next != nil {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET vord_tokens = $1 WHERE id = $2`, next, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// VordTokens keeps and refreshes the signed-in person's Vord tokens.
type VordTokens struct {
	box   *secret.Box
	oauth *oauth2.Config
	rows  tokenRow
	now   func() time.Time
}

// EnableVordTokens turns on token keeping for this service. oauth is the same
// client the sign-in used, so a refresh is made as that client. Called at
// startup, only with the EmberOPS back end.
func (s *Service) EnableVordTokens(box *secret.Box, oauth *oauth2.Config) {
	s.vord = &VordTokens{box: box, oauth: oauth, rows: pgTokenRow{s}, now: time.Now}
}

// VordTokensEnabled reports whether sessions keep Vord tokens.
func (s *Service) VordTokensEnabled() bool { return s.vord != nil }

// CreateWithTokens is Create for a session that must also carry the person's
// Vord tokens. With tokens disabled it is exactly Create.
func (s *Service) CreateWithTokens(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID, principalID string, tok *oauth2.Token) error {
	if s.vord == nil || tok == nil {
		return s.create(ctx, w, r, orgID, principalID, "", nil)
	}
	return s.create(ctx, w, r, orgID, principalID, "", tok)
}

// sealContext binds a blob to its session row, so one session's tokens cannot
// be copied into another.
func sealContext(id []byte) string { return "session-vord-tokens:" + hex.EncodeToString(id) }

func (v *VordTokens) seal(id []byte, access, refresh string, expiry time.Time) ([]byte, error) {
	b, _ := json.Marshal(storedTokens{Access: access, Refresh: refresh, Expiry: expiry})
	return v.box.Seal(b, sealContext(id))
}

func (v *VordTokens) open(id []byte, blob []byte) (storedTokens, error) {
	var st storedTokens
	if len(blob) == 0 {
		return st, ErrNoUserToken
	}
	raw, err := v.box.Open(blob, sealContext(id))
	if err != nil {
		return st, fmt.Errorf("auth: open session tokens: %w", err)
	}
	return st, json.Unmarshal(raw, &st)
}

// sessionTokens is one session's emberops.TokenSource.
type sessionTokens struct {
	v  *VordTokens
	id []byte
}

// TokenSource returns the token source for a session. It satisfies
// emberops.TokenSource and emberops.Invalidator.
func (v *VordTokens) TokenSource(sessionID []byte) emberops.TokenSource {
	return &sessionTokens{v: v, id: sessionID}
}

// Token returns a Vord access token good for at least refreshSkew, refreshing
// it first when it is not.
func (t *sessionTokens) Token(ctx context.Context) (string, error) {
	var access string
	err := t.v.rows.update(ctx, t.id, func(blob []byte) ([]byte, error) {
		st, err := t.v.open(t.id, blob)
		if err != nil {
			return nil, err
		}
		if st.Access != "" && t.v.now().Add(refreshSkew).Before(st.Expiry) {
			access = st.Access
			return nil, nil
		}
		if st.Refresh == "" {
			return nil, ErrTokenExpired
		}
		// A zero expiry on the stale token forces oauth2 to refresh.
		fresh, err := t.v.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: st.Refresh}).Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTokenExpired, err)
		}
		if fresh.RefreshToken == "" {
			fresh.RefreshToken = st.Refresh // providers that do not rotate omit it
		}
		access = fresh.AccessToken
		return t.v.seal(t.id, fresh.AccessToken, fresh.RefreshToken, fresh.Expiry)
	})
	return access, err
}

// Invalidate marks the stored access token expired, so the next Token call
// refreshes. EmberOPS answered 401 to a token we thought was good.
func (t *sessionTokens) Invalidate() {
	_ = t.v.rows.update(context.Background(), t.id, func(blob []byte) ([]byte, error) {
		st, err := t.v.open(t.id, blob)
		if err != nil {
			return nil, nil
		}
		st.Expiry = time.Time{}
		return t.v.seal(t.id, st.Access, st.Refresh, st.Expiry)
	})
}
