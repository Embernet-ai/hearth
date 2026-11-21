// Package m365 connects one person's Microsoft 365 work account to Hearth.
//
// Per person, delegated, and never an application permission for the whole tenant: an app-only credential with
// Mail.Read reads every mailbox in the company, silently, and outlives whoever created it. Here each person signs
// in as themselves and every Graph call is made as them, so Hearth never holds a credential that could read a
// colleague's mail.
//
// The tenant is not configured here. An administrator connects one in Hearth (internal/connect) and the sign-in
// goes to that tenant's own authority, so a personal Microsoft account or another company's tenant has nowhere to
// connect to.
//
// Refresh tokens are sealed with the key in the environment, same as the Google connection.
package m365

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// Config is what a deployment supplies: the Entra app registration.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL must match a redirect URI on the app registration exactly.
	RedirectURL string
}

// Enabled reports whether the registration is complete.
func (c Config) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// Base scopes every connection carries. offline_access is what produces a refresh token.
var baseScopes = []string{"openid", "profile", "email", "offline_access", "User.Read"}

// Scope names, as Graph spells them.
const (
	ScopeCalendar = "Calendars.ReadWrite"
	ScopeContacts = "Contacts.Read"
	ScopeMail     = "Mail.Read"
	ScopeFiles    = "Files.Read.All"
	ScopeSites    = "Sites.Read.All"
)

// Service holds connections and talks to Microsoft.
type Service struct {
	db   *pgxpool.Pool
	cfg  Config
	box  *secret.Box
	http *http.Client
	// authBase and graphBase are overridable so the tests can drive a fake rather than Microsoft.
	authBase  string
	graphBase string
}

func New(db *pgxpool.Pool, cfg Config, box *secret.Box) *Service {
	return &Service{db: db, cfg: cfg, box: box, http: &http.Client{Timeout: 30 * time.Second},
		authBase: "https://login.microsoftonline.com", graphBase: "https://graph.microsoft.com/v1.0"}
}

var _ connect.Backend = (*Service)(nil)

func (s *Service) Provider() connect.Provider { return connect.Microsoft }

// Configured reports whether the registration is complete and there is somewhere safe to keep a token.
func (s *Service) Configured() bool { return s.cfg.Enabled() && s.box != nil }

// Scopes is what each connector needs, and only that.
func (s *Service) Scopes(k connect.Kind) []string {
	switch k {
	case connect.Calendar:
		return []string{ScopeCalendar}
	case connect.Contacts:
		return []string{ScopeContacts}
	case connect.Mail:
		return []string{ScopeMail}
	case connect.Files:
		return []string{ScopeFiles, ScopeSites}
	}
	return nil
}

func (s *Service) oauth(tenant string) *oauth2.Config {
	base := strings.TrimRight(s.authBase, "/") + "/" + url.PathEscape(tenant) + "/oauth2/v2.0"
	return &oauth2.Config{
		ClientID: s.cfg.ClientID, ClientSecret: s.cfg.ClientSecret, RedirectURL: s.cfg.RedirectURL,
		Endpoint: oauth2.Endpoint{AuthURL: base + "/authorize", TokenURL: base + "/token", AuthStyle: oauth2.AuthStyleInParams},
	}
}

// AuthURL is where a person is sent to connect. verifier is the PKCE verifier the caller keeps with the state.
func (s *Service) AuthURL(tenant, state, verifier string, scopes []string) string {
	c := s.oauth(tenant)
	c.Scopes = unique(append(append([]string{}, baseScopes...), scopes...))
	return c.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "select_account"))
}

// AdminConsentURL is where a tenant administrator grants the permissions for everybody at once. It returns to the
// same redirect address, marked, and grants nothing to Hearth beyond the scopes listed.
func (s *Service) AdminConsentURL(tenant, state string, scopes []string) string {
	v := url.Values{}
	v.Set("client_id", s.cfg.ClientID)
	v.Set("redirect_uri", s.cfg.RedirectURL)
	v.Set("state", state)
	v.Set("scope", strings.Join(unique(append(append([]string{}, baseScopes[3:]...), scopes...)), " "))
	return strings.TrimRight(s.authBase, "/") + "/" + url.PathEscape(tenant) + "/v2.0/adminconsent?" + v.Encode()
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// normalizeScopes turns what the token endpoint reports into short names, since it may spell Graph's with the
// resource in front.
func normalizeScopes(raw string) []string {
	var out []string
	for _, f := range strings.Fields(raw) {
		f = strings.TrimPrefix(f, "https://graph.microsoft.com/")
		out = append(out, f)
	}
	return out
}

// idClaims reads the unverified payload of the id token. Unverified is correct here for the reason it is in the
// Google package: the token came back over TLS from a direct request to the token endpoint in answer to a code we
// just sent, so there is nobody in the exchange to forge it.
func idClaims(idToken string) (tid, oid, email string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return
	}
	var c struct {
		TID   string `json:"tid"`
		OID   string `json:"oid"`
		Email string `json:"email"`
		UPN   string `json:"preferred_username"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return
	}
	email = c.Email
	if email == "" {
		email = c.UPN
	}
	return strings.ToLower(c.TID), c.OID, strings.ToLower(email)
}

// Connect exchanges the code and stores the connection. tenant is the one the administrator connected: an account
// from any other is refused.
func (s *Service) Connect(ctx context.Context, orgID, principalID, tenant, code, verifier string) error {
	if s.box == nil {
		return errors.New("m365: no encryption key is configured, so a refresh token cannot be stored safely")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.http)
	tok, err := s.oauth(tenant).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("m365: exchange the code: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("m365: no refresh token was returned, so this connection would stop working within the hour")
	}
	idt, _ := tok.Extra("id_token").(string)
	tid, oid, email := idClaims(idt)
	if oid == "" || tid == "" {
		return errors.New("m365: the response carried no account id")
	}
	if !strings.EqualFold(tid, tenant) {
		return connect.ErrWrongTenant
	}
	scopes := normalizeScopes(fmt.Sprint(tok.Extra("scope")))
	key := "m365:" + principalID
	access, err := s.box.SealString(tok.AccessToken, key)
	if err != nil {
		return err
	}
	refresh, err := s.box.SealString(tok.RefreshToken, key)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO microsoft_connections (principal_id, org_id, tenant_id, ms_oid, email, access_token, refresh_token,
		                                   expires_at, scopes, connected_at, updated_at, revoked_at, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now(), now(), NULL, '')
		ON CONFLICT (principal_id) DO UPDATE SET
		    tenant_id=EXCLUDED.tenant_id, ms_oid=EXCLUDED.ms_oid, email=EXCLUDED.email,
		    access_token=EXCLUDED.access_token, refresh_token=EXCLUDED.refresh_token,
		    expires_at=EXCLUDED.expires_at, scopes=EXCLUDED.scopes, updated_at=now(), revoked_at=NULL, last_error=''`,
		principalID, orgID, tid, oid, email, access, refresh, tok.Expiry, scopes)
	if err != nil {
		return fmt.Errorf("m365: store connection: %w", err)
	}
	return nil
}

// Granted is what the person's connection covers, as Microsoft reported it.
func (s *Service) Granted(ctx context.Context, principalID string) ([]string, error) {
	var scopes []string
	var revoked *time.Time
	err := s.db.QueryRow(ctx, `SELECT scopes, revoked_at FROM microsoft_connections WHERE principal_id=$1`, principalID).
		Scan(&scopes, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("m365: read connection: %w", err)
	}
	if revoked != nil {
		return nil, connect.ErrReconnect
	}
	return scopes, nil
}

// Connections lists the connections in an organisation, without any token.
func (s *Service) Connections(ctx context.Context, orgID string) ([]connect.Conn, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.principal_id::text, COALESCE(p.display_name,''), c.email, c.tenant_id, c.scopes, c.connected_at,
		       c.revoked_at IS NOT NULL, c.last_error
		  FROM microsoft_connections c JOIN principals p ON p.id = c.principal_id
		 WHERE c.org_id = $1 ORDER BY c.connected_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("m365: list connections: %w", err)
	}
	defer rows.Close()
	var out []connect.Conn
	for rows.Next() {
		c := connect.Conn{OrgID: orgID}
		if err := rows.Scan(&c.PrincipalID, &c.Name, &c.Email, &c.Tenant, &c.Scopes, &c.ConnectedAt, &c.Revoked, &c.LastError); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Disconnect removes the connection and the tokens with it.
func (s *Service) Disconnect(ctx context.Context, principalID string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM microsoft_connections WHERE principal_id=$1`, principalID); err != nil {
		return fmt.Errorf("m365: disconnect: %w", err)
	}
	return nil
}

// token returns a usable access token, refreshing it a minute early.
func (s *Service) token(ctx context.Context, principalID string) (string, error) {
	if s.box == nil {
		return "", errors.New("m365: no encryption key is configured")
	}
	var access, refresh []byte
	var expires time.Time
	var revoked *time.Time
	var tenant string
	err := s.db.QueryRow(ctx, `
		SELECT access_token, refresh_token, expires_at, revoked_at, tenant_id
		  FROM microsoft_connections WHERE principal_id=$1`, principalID).Scan(&access, &refresh, &expires, &revoked, &tenant)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", connect.ErrNotConnected
	case err != nil:
		return "", fmt.Errorf("m365: read tokens: %w", err)
	}
	if revoked != nil {
		return "", connect.ErrReconnect
	}
	key := "m365:" + principalID
	if time.Until(expires) > time.Minute {
		return s.box.OpenString(access, key)
	}
	raw, err := s.box.OpenString(refresh, key)
	if err != nil {
		return "", err
	}
	hctx := context.WithValue(ctx, oauth2.HTTPClient, s.http)
	tok, err := s.oauth(tenant).TokenSource(hctx, &oauth2.Token{RefreshToken: raw}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.ErrorCode == "interaction_required") {
			_, _ = s.db.Exec(ctx, `UPDATE microsoft_connections SET revoked_at=now(),
				last_error='the connection was revoked in Microsoft 365' WHERE principal_id=$1`, principalID)
			return "", connect.ErrReconnect
		}
		return "", fmt.Errorf("m365: refresh: %w", err)
	}
	sealed, err := s.box.SealString(tok.AccessToken, key)
	if err != nil {
		return "", err
	}
	// Microsoft rotates the refresh token on use. The new one replaces the old; keeping the old one would work
	// until it did not.
	if tok.RefreshToken != "" && tok.RefreshToken != raw {
		if nr, err := s.box.SealString(tok.RefreshToken, key); err == nil {
			_, _ = s.db.Exec(ctx, `UPDATE microsoft_connections SET refresh_token=$2 WHERE principal_id=$1`, principalID, nr)
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE microsoft_connections SET access_token=$2, expires_at=$3, updated_at=now(),
		revoked_at=NULL, last_error='' WHERE principal_id=$1`, principalID, sealed, tok.Expiry); err != nil {
		return "", fmt.Errorf("m365: store refreshed token: %w", err)
	}
	return tok.AccessToken, nil
}

// markRevoked records a 401 from Graph that a fresh token did not cure: the grant itself is gone.
func (s *Service) markRevoked(ctx context.Context, principalID string) {
	_, _ = s.db.Exec(ctx, `UPDATE microsoft_connections SET revoked_at=now(),
		last_error='Microsoft 365 refused the connection' WHERE principal_id=$1`, principalID)
}
