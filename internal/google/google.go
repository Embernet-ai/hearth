// Package google connects one person's Google account to Hearth.
//
// Per user, never a service account for the whole company. A domain-wide
// credential can read every mailbox in the organisation, is invisible to the
// people it reads, and outlives anybody leaving. Here each person grants access
// to their own account and every call is made as them, so Hearth never holds a
// credential that could read a colleague's mail.
//
// The refresh token is the one thing in this application that cannot be hashed,
// because Google needs it back verbatim months after it was issued. It is
// encrypted at rest with a key that lives in the environment rather than in the
// database, so the two are compromised separately.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// Scopes Hearth asks for. Each one is requested only when the feature that
// needs it is wanted, so somebody who only wants their calendar is never asked
// for their mail.
const (
	// The full calendar and the full mailbox: read, write, send, organise. The
	// connect form asks for these by default.
	ScopeCalendar = "https://www.googleapis.com/auth/calendar"
	ScopeMail     = "https://mail.google.com/"

	// The narrower scopes stay recognised so a connection made earlier with
	// them keeps working.
	ScopeCalendarRead  = "https://www.googleapis.com/auth/calendar.readonly"
	ScopeCalendarWrite = "https://www.googleapis.com/auth/calendar.events"
	ScopeGmailRead     = "https://www.googleapis.com/auth/gmail.readonly"
	ScopeGmailSend     = "https://www.googleapis.com/auth/gmail.send"
)

var (
	// ErrNotConnected means this person has never connected, or has revoked.
	ErrNotConnected = fmt.Errorf("google: this account is not connected: %w", connect.ErrNotConnected)
	// ErrReconnect means the refresh token no longer works. The person revoked
	// Hearth in their Google account, or an admin did, and the only way back is
	// for them to connect again. It is distinct from a transient failure
	// because the answer is different: one is "try later", this is "ask them".
	ErrReconnect = fmt.Errorf("google: the connection has been revoked and must be made again: %w", connect.ErrReconnect)
	// ErrNoKey means no encryption key is configured, so nothing can be stored.
	ErrNoKey = errors.New("google: no encryption key is configured, so a refresh token cannot be stored safely")
)

// Config is what a deployment supplies.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL must match a URI registered on the OAuth client exactly,
	// including scheme and with no trailing slash. Google compares the string
	// literally and a mismatch fails at the moment somebody tries to connect
	// rather than at configuration time.
	RedirectURL string
}

// Enabled reports whether the integration can run at all.
func (c Config) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// Service holds connections and talks to Google.
type Service struct {
	db  *pgxpool.Pool
	cfg Config
	box *secret.Box
	// http is injectable so the tests can drive a fake Google rather than the
	// real one. Every request in this package goes through it.
	http *http.Client
	// endpoints are overridable for the same reason. Nothing else varies them.
	authURL  string
	tokenURL string
	apiBase  string
	// revokeURL is where a refused grant is withdrawn.
	revokeURL string
	// api points the official clients somewhere else in a test. Empty means Google.
	api apiEndpoints
}

func New(db *pgxpool.Pool, cfg Config, box *secret.Box) *Service {
	return &Service{
		db: db, cfg: cfg, box: box,
		http:      &http.Client{Timeout: 30 * time.Second},
		authURL:   "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:  "https://oauth2.googleapis.com/token",
		apiBase:   "https://www.googleapis.com",
		revokeURL: "https://oauth2.googleapis.com/revoke",
	}
}

// Connection is one person's link to their Google account.
type Connection struct {
	PrincipalID string
	Email       string
	GoogleSub   string
	Scopes      []string
	ConnectedAt time.Time
	RevokedAt   *time.Time
	LastError   string
}

// Has reports whether the connection carries a scope. Read from what Google
// returned rather than what was asked for: somebody can untick a box on the
// consent screen, and believing the request instead of the response is how a
// feature fails at its first API call with nothing to explain it.
func (c *Connection) Has(scope string) bool {
	if c == nil {
		return false
	}
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Live reports a connection that can still be used.
func (c *Connection) Live() bool { return c != nil && c.RevokedAt == nil }

// AuthURL builds the address somebody is sent to in order to connect.
//
// access_type=offline and prompt=consent together are what produce a refresh
// token. Google issues one only on the first grant unless consent is forced,
// so without prompt=consent a second connection returns an access token and no
// refresh token, and the connection silently stops working in an hour. That is
// the detail this usually gets wrong.
//
// state is the caller's, and must be verified on the way back.
func (s *Service) AuthURL(state string, scopes []string) string {
	return s.AuthURLFor(state, scopes, "")
}

// AuthURLFor is AuthURL for a Workspace domain: hd narrows Google's account chooser to that domain. It is only a
// hint to the person, and the domain is checked again on the way back (ConnectIn), because a request parameter
// restricts nothing.
func (s *Service) AuthURLFor(state string, scopes []string, domain string) string {
	v := url.Values{}
	if domain != "" {
		v.Set("hd", domain)
	}
	v.Set("client_id", s.cfg.ClientID)
	v.Set("redirect_uri", s.cfg.RedirectURL)
	v.Set("response_type", "code")
	v.Set("access_type", "offline")
	v.Set("prompt", "consent")
	// include_granted_scopes so connecting for the calendar and later for mail
	// leaves one grant carrying both, rather than the second replacing the
	// first.
	v.Set("include_granted_scopes", "true")
	v.Set("state", state)
	v.Set("scope", strings.Join(append([]string{"openid", "email"}, scopes...), " "))
	return s.authURL + "?" + v.Encode()
}

// tokenResponse is what Google returns from the token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (s *Service) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: token endpoint: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("google: read token response: %w", err)
	}
	if tr.Error != "" {
		// invalid_grant is the one that matters and the one that needs its own
		// answer: the refresh token is dead, and no amount of retrying brings
		// it back. Everything else is reported as itself.
		if tr.Error == "invalid_grant" {
			return nil, ErrReconnect
		}
		return nil, fmt.Errorf("google: %s: %s", tr.Error, tr.ErrorDesc)
	}
	return &tr, nil
}

// claims reads the unverified payload of an id token.
//
// Unverified is correct HERE and nowhere else. This token came back over TLS
// from a direct request to Google's token endpoint, in response to a code we
// just sent, so there is no third party in the exchange to have forged it. The
// signature check exists for tokens that arrive through a browser, which this
// one did not.
func claims(idToken string) (sub, email string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	raw, err := base64URLDecode(parts[1])
	if err != nil {
		return "", ""
	}
	var c struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", ""
	}
	return c.Sub, c.Email
}

// hostedDomain reads the hd claim of an id token that came straight from the token endpoint.
func hostedDomain(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64URLDecode(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		HD string `json:"hd"`
	}
	_ = json.Unmarshal(raw, &c)
	return c.HD
}

// revokeToken tells Google to drop a grant Hearth has decided not to keep, best effort. Refusing an account and
// leaving its grant alive at Google would be refusing it halfway.
func (s *Service) revokeToken(ctx context.Context, token string) {
	if token == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.revokeURL,
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := s.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

// Connect exchanges the code and stores the connection.
func (s *Service) Connect(ctx context.Context, orgID, principalID, code string) (*Connection, error) {
	return s.ConnectIn(ctx, orgID, principalID, code, "")
}

// ConnectIn is Connect for an account that has to belong to one Workspace domain. The domain is read from the id
// token's hd claim, which Google sets only for a Workspace account and sets to its primary domain; a personal Gmail
// account has none, so it is refused.
func (s *Service) ConnectIn(ctx context.Context, orgID, principalID, code, domain string) (*Connection, error) {
	if s.box == nil {
		return nil, ErrNoKey
	}
	tr, err := s.postToken(ctx, url.Values{
		"code":          {code},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"redirect_uri":  {s.cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return nil, err
	}
	if tr.RefreshToken == "" {
		// Without one the connection dies in an hour with no way to renew it,
		// so it is refused rather than stored. Almost always means
		// prompt=consent was dropped from the authorisation URL.
		return nil, errors.New("google: no refresh token was returned, so this " +
			"connection would stop working within the hour. Disconnect in your " +
			"Google account settings and connect again")
	}
	sub, email := claims(tr.IDToken)
	if sub == "" {
		return nil, errors.New("google: the response carried no account id")
	}
	if domain != "" && !strings.EqualFold(hostedDomain(tr.IDToken), domain) {
		s.revokeToken(ctx, tr.RefreshToken)
		return nil, connect.ErrWrongTenant
	}

	ctxKey := "google:" + principalID
	access, err := s.box.SealString(tr.AccessToken, ctxKey)
	if err != nil {
		return nil, err
	}
	refresh, err := s.box.SealString(tr.RefreshToken, ctxKey)
	if err != nil {
		return nil, err
	}

	conn := &Connection{
		PrincipalID: principalID, Email: email, GoogleSub: sub,
		Scopes: strings.Fields(tr.Scope), ConnectedAt: time.Now(),
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO google_connections
		    (principal_id, org_id, google_sub, email, access_token, refresh_token,
		     expires_at, scopes, connected_at, updated_at, revoked_at, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), now(), NULL, '')
		ON CONFLICT (principal_id) DO UPDATE SET
		    google_sub = EXCLUDED.google_sub, email = EXCLUDED.email,
		    access_token = EXCLUDED.access_token,
		    refresh_token = EXCLUDED.refresh_token,
		    expires_at = EXCLUDED.expires_at, scopes = EXCLUDED.scopes,
		    updated_at = now(), revoked_at = NULL, last_error = ''`,
		principalID, orgID, sub, email, access, refresh,
		time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second), conn.Scopes)
	if err != nil {
		return nil, fmt.Errorf("google: store connection: %w", err)
	}
	return conn, nil
}

// Get reads somebody's connection without any token in it.
func (s *Service) Get(ctx context.Context, principalID string) (*Connection, error) {
	c := &Connection{PrincipalID: principalID}
	err := s.db.QueryRow(ctx, `
		SELECT email, google_sub, scopes, connected_at, revoked_at, last_error
		  FROM google_connections WHERE principal_id = $1`, principalID).
		Scan(&c.Email, &c.GoogleSub, &c.Scopes, &c.ConnectedAt, &c.RevokedAt, &c.LastError)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotConnected
	case err != nil:
		return nil, fmt.Errorf("google: read connection: %w", err)
	}
	return c, nil
}

// Disconnect removes the connection and the cached events with it.
//
// The row goes rather than being marked revoked: somebody disconnecting wants
// their tokens gone, and keeping an encrypted refresh token for a connection
// they have ended is keeping a credential nobody asked us to hold.
func (s *Service) Disconnect(ctx context.Context, principalID string) error {
	if _, err := s.db.Exec(ctx,
		`DELETE FROM google_events WHERE principal_id = $1`, principalID); err != nil {
		return fmt.Errorf("google: clear events: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`DELETE FROM google_connections WHERE principal_id = $1`, principalID); err != nil {
		return fmt.Errorf("google: disconnect: %w", err)
	}
	return nil
}

// token returns a usable access token, refreshing it if it has expired.
//
// Refreshed a minute early, because a token that expires between this check and
// the request it is for produces a 401 that looks like a permissions problem.
func (s *Service) token(ctx context.Context, principalID string) (string, error) {
	if s.box == nil {
		return "", ErrNoKey
	}
	var access, refresh []byte
	var expires time.Time
	var revoked *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT access_token, refresh_token, expires_at, revoked_at
		  FROM google_connections WHERE principal_id = $1`, principalID).
		Scan(&access, &refresh, &expires, &revoked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrNotConnected
	case err != nil:
		return "", fmt.Errorf("google: read tokens: %w", err)
	}
	if revoked != nil {
		return "", ErrReconnect
	}

	ctxKey := "google:" + principalID
	if time.Until(expires) > time.Minute {
		return s.box.OpenString(access, ctxKey)
	}

	raw, err := s.box.OpenString(refresh, ctxKey)
	if err != nil {
		return "", err
	}
	tr, err := s.postToken(ctx, url.Values{
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"refresh_token": {raw},
		"grant_type":    {"refresh_token"},
	})
	if errors.Is(err, ErrReconnect) {
		// Mark it, so the page can say "reconnect" instead of failing the same
		// way every time somebody opens their calendar.
		_, _ = s.db.Exec(ctx, `
			UPDATE google_connections
			   SET revoked_at = now(), last_error = 'the connection was revoked in Google'
			 WHERE principal_id = $1`, principalID)
		return "", err
	}
	if err != nil {
		return "", err
	}

	sealed, err := s.box.SealString(tr.AccessToken, ctxKey)
	if err != nil {
		return "", err
	}
	// A refresh response usually carries no new refresh token, and when it does
	// the old one stops working. Only replaced when one actually came back.
	if tr.RefreshToken != "" {
		if newRefresh, err := s.box.SealString(tr.RefreshToken, ctxKey); err == nil {
			_, _ = s.db.Exec(ctx,
				`UPDATE google_connections SET refresh_token = $2 WHERE principal_id = $1`,
				principalID, newRefresh)
		}
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE google_connections
		   SET access_token = $2, expires_at = $3, updated_at = now(),
		       revoked_at = NULL, last_error = ''
		 WHERE principal_id = $1`,
		principalID, sealed, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second)); err != nil {
		return "", fmt.Errorf("google: store refreshed token: %w", err)
	}
	return tr.AccessToken, nil
}
