package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

// OIDCConfig configures single sign-on.
//
// Deliberately generic. Nothing here names a vendor, because an identity
// provider is an identity provider: this works against Entra, Keycloak,
// Authentik, Zitadel, or something built in-house, and picking one to hardcode
// would be a choice to regret the day it changes.
type OIDCConfig struct {
	// Issuer is the discovery URL. Everything else is fetched from it, so a
	// provider that moves an endpoint does not need a code change here.
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string

	// Scopes beyond openid. Defaults to profile and email.
	Scopes []string

	// AllowedDomains restricts sign-in to these email domains.
	//
	// Empty means ANY identity the provider vouches for is accepted, which
	// includes guest accounts invited for unrelated reasons. That is almost
	// never what anybody means, so it is logged loudly at startup.
	AllowedDomains []string

	// RequiredRoles restricts sign-in to identities carrying one of these
	// roles or groups. Empty disables the check.
	//
	// A domain check answers "is this person from our company". It cannot
	// answer "should this person be in this application", and on a shared
	// identity provider those are different questions: every employee has an
	// address at the domain, and only some of them belong here.
	//
	// Matched against realm roles, client roles and groups together, because
	// providers disagree about where this belongs and an operator should not
	// have to know which claim their own provider chose. Keycloak puts realm
	// roles in realm_access.roles and groups in a groups claim; Entra uses
	// roles. All are accepted, any one match is enough.
	RequiredRoles []string
}

// Enabled reports whether SSO is configured.
//
// All three of issuer, client id and secret are required TOGETHER. A partial
// configuration is treated as absent rather than half-applied, because a
// half-configured provider fails at login time rather than at startup, which is
// a far worse place to find out.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != ""
}

// OIDC is the relying party.
type OIDC struct {
	cfg      OIDCConfig
	provider *oidc.Provider
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	svc      *Service
	secure   bool
}

// NewOIDC performs discovery against the issuer.
//
// Discovery happens at startup so a wrong issuer fails there, loudly, rather
// than on the first person's first login attempt.
func NewOIDC(ctx context.Context, svc *Service, cfg OIDCConfig, secure bool) (*OIDC, error) {
	if !cfg.Enabled() {
		return nil, errors.New("auth: OIDC is not fully configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	provider, err := oidc.NewProvider(ctx, strings.TrimSuffix(cfg.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery against %s: %w", cfg.Issuer, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}

	return &OIDC{
		cfg:      cfg,
		provider: provider,
		svc:      svc,
		secure:   secure,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

const (
	stateCookie = "hearth_oidc_state"
	stateTTL    = 10 * time.Minute
)

// Start redirects the browser to the identity provider.
//
// state, nonce and a PKCE verifier are generated per attempt and held in a
// short-lived cookie. state stops an attacker feeding a victim their own
// authorisation code; nonce ties the returned id token to this request; PKCE
// stops a stolen code being exchanged by anyone but us.
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request) {
	state := randomToken()
	nonce := randomToken()
	verifier := oauth2.GenerateVerifier()

	// All three travel together, so a mismatched callback fails closed.
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state + "." + nonce + "." + verifier,
		Path:     "/",
		HttpOnly: true,
		Secure:   o.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(stateTTL),
	})

	http.Redirect(w, r, o.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	), http.StatusFound)
}

// Callback completes the flow and starts a session.
func (o *OIDC) Callback(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return errors.New("auth: no sign-in was in progress")
	}
	// Clear it immediately: one attempt per cookie, so a replayed callback
	// cannot reuse it.
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode,
	})

	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return errors.New("auth: malformed sign-in state")
	}
	wantState, wantNonce, verifier := parts[0], parts[1], parts[2]

	if r.URL.Query().Get("state") != wantState {
		return errors.New("auth: sign-in state did not match")
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		return fmt.Errorf("auth: identity provider returned %s: %s",
			errParam, r.URL.Query().Get("error_description"))
	}

	token, err := o.oauth.Exchange(ctx, r.URL.Query().Get("code"),
		oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("auth: exchange code: %w", err)
	}

	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		return errors.New("auth: no id token in the response")
	}
	idToken, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return fmt.Errorf("auth: verify id token: %w", err)
	}
	if idToken.Nonce != wantNonce {
		return errors.New("auth: id token nonce did not match")
	}

	var claims struct {
		Subject  string `json:"sub"`
		Email    string `json:"email"`
		Verified *bool  `json:"email_verified"`
		Name     string `json:"name"`
		// Entra puts the stable object id here. Preferred over sub where
		// present, because sub is pairwise per application: the same human
		// signing into a second app gets a different sub, and linking on it
		// would strand them with two accounts.
		OID string `json:"oid"`

		// Where the providers put authorisation. Keycloak: realm_access for
		// realm roles, resource_access for per-client ones, groups for group
		// membership. Entra: roles.
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
		ResourceAccess map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
		Groups []string `json:"groups"`
		Roles  []string `json:"roles"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return fmt.Errorf("auth: read claims: %w", err)
	}

	subject := claims.OID
	if subject == "" {
		subject = claims.Subject
	}
	if subject == "" {
		return errors.New("auth: identity provider returned no stable subject")
	}

	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if err := o.checkDomain(email); err != nil {
		return err
	}

	if err := o.checkRoles(claims.RealmAccess.Roles, claims.ResourceAccess, claims.Groups, claims.Roles); err != nil {
		return err
	}

	principalID, orgID, err := o.link(ctx, idToken.Issuer, subject, email, claims.Name)
	if err != nil {
		return err
	}
	if o.svc.VordTokensEnabled() {
		return o.svc.CreateWithTokens(ctx, w, r, orgID, principalID, token)
	}
	return o.svc.Create(ctx, w, r, orgID, principalID)
}

// OAuth is the client the sign-in uses, for refreshing a person's tokens as
// that same client.
func (o *OIDC) OAuth() *oauth2.Config { return &o.oauth }

// checkDomain enforces the allowed-domain list.
func (o *OIDC) checkDomain(email string) error {
	if len(o.cfg.AllowedDomains) == 0 {
		return nil
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return errors.New("auth: no email address was provided by the identity provider")
	}
	domain := email[at+1:]
	for _, allowed := range o.cfg.AllowedDomains {
		if strings.EqualFold(domain, allowed) {
			return nil
		}
	}
	// Deliberately vague to the user, specific in the log. Telling a stranger
	// which domains are permitted is a small disclosure with no upside.
	return fmt.Errorf("auth: %s is not permitted to sign in", email)
}

// link finds or creates the principal behind a federated identity.
//
// Matching is on (issuer, subject), NEVER on email. An address is mutable:
// people marry, and domains get acquired. Keying on it means a rename silently
// creates a second account, or worse, hands somebody an account that used to
// belong to a colleague whose old address was recycled.
//
// Email is still used ONCE, to adopt an existing local account on first
// federated sign-in, so that turning on SSO does not strand everybody's history
// behind a second identity.
func (o *OIDC) link(ctx context.Context, issuer, subject, email, name string) (string, string, error) {
	var principalID, orgID string

	err := o.svc.db.QueryRow(ctx, `
		SELECT ui.principal_id::text, p.org_id::text
		FROM user_identities ui
		JOIN principals p ON p.id = ui.principal_id
		WHERE ui.issuer = $1 AND ui.subject = $2`, issuer, subject).
		Scan(&principalID, &orgID)
	if err == nil {
		// Refresh the display copy of the address; it is informational only.
		_, _ = o.svc.db.Exec(ctx,
			`UPDATE user_identities SET email = $1, updated_at = now()
			 WHERE issuer = $2 AND subject = $3`, email, issuer, subject)
		return principalID, orgID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("auth: look up federated identity: %w", err)
	}

	tx, err := o.svc.db.Begin(ctx)
	if err != nil {
		return "", "", fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Adopt an existing local account with the same address, once.
	if email != "" {
		err = tx.QueryRow(ctx,
			`SELECT principal_id::text, org_id::text FROM local_credentials WHERE email = $1`,
			email).Scan(&principalID, &orgID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("auth: look up local account: %w", err)
		}
	}

	if principalID == "" {
		// A genuinely new person. They join the existing organisation, because
		// a self-host has one; a second org would be a silent tenancy split.
		// Playgrounds are organisations too, and never the one to join.
		if err := tx.QueryRow(ctx,
			`SELECT id::text FROM orgs WHERE playground_of IS NULL ORDER BY created_at LIMIT 1`).Scan(&orgID); err != nil {
			return "", "", fmt.Errorf("auth: no organisation exists to join: %w", err)
		}
		display := name
		if display == "" {
			display = email
		}
		principalID = uuid.NewString()
		if _, err := tx.Exec(ctx,
			`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user',$3)`,
			principalID, orgID, display); err != nil {
			return "", "", fmt.Errorf("auth: create principal: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_identities (principal_id, issuer, subject, email)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (issuer, subject) DO UPDATE SET email = EXCLUDED.email, updated_at = now()`,
		principalID, issuer, subject, email); err != nil {
		return "", "", fmt.Errorf("auth: record federated identity: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", "", fmt.Errorf("auth: commit: %w", err)
	}
	return principalID, orgID, nil
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice. Falling back to something
		// predictable would defeat the point of state and nonce entirely, so
		// derive from time and let the mismatch fail closed instead.
		sum := sha256.Sum256([]byte(time.Now().String()))
		return base64.RawURLEncoding.EncodeToString(sum[:16])
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// checkRoles enforces the required-role list.
//
// Every place a provider might have put it is gathered and matched together.
// That is deliberate: an operator configuring HEARTH_OIDC_REQUIRED_ROLES knows
// the name of the role their identity team created, and should not also have to
// know whether their provider files it under realm_access, resource_access,
// groups, or roles. Getting that wrong fails closed at login, which is the
// worst place to discover a configuration detail.
//
// Group paths are matched on their last segment as well as in full, so
// "/Fireball/fireball-staff" satisfies a requirement for "fireball-staff". A
// provider that emits full paths should not force the config to encode a
// hierarchy that can be reorganised.
func (o *OIDC) checkRoles(realm []string, resource map[string]struct {
	Roles []string `json:"roles"`
}, groups, roles []string) error {
	if len(o.cfg.RequiredRoles) == 0 {
		return nil
	}

	held := make(map[string]bool)
	add := func(vs []string) {
		for _, v := range vs {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			held[strings.ToLower(v)] = true
			if i := strings.LastIndex(v, "/"); i >= 0 && i+1 < len(v) {
				held[strings.ToLower(v[i+1:])] = true
			}
		}
	}
	add(realm)
	add(groups)
	add(roles)
	for _, r := range resource {
		add(r.Roles)
	}

	for _, want := range o.cfg.RequiredRoles {
		if held[strings.ToLower(strings.TrimSpace(want))] {
			return nil
		}
	}
	// Vague to the user, specific in the log, same as the domain check. Naming
	// the role a stranger lacks tells them what to go and ask for.
	return errors.New("auth: this account is not permitted to sign in")
}

// ErrNotBound means a Vord identity has no Hearth account linked to it.
var ErrNotBound = errors.New("auth: that identity is not linked to a Hearth account")

// ResolveFederated finds the Hearth person a Vord identity is linked to, for
// a bearer token (internal/mcpserver).
//
// Lookup only, on purpose: link() above creates an account on a first browser
// sign-in, which is right for a person at the login page and wrong for a
// token -- a token for somebody who has never signed in to Hearth is refused,
// not turned into an account. The person must be active, and never somebody
// in a playground (those have no identities, and this says so twice).
func (s *Service) ResolveFederated(ctx context.Context, issuer, subject string) (principalID, orgID, name string, err error) {
	var status string
	err = s.db.QueryRow(ctx, `
		SELECT p.id::text, p.org_id::text, p.display_name, p.status
		FROM user_identities ui
		JOIN principals p ON p.id = ui.principal_id
		JOIN orgs o ON o.id = p.org_id AND o.playground_of IS NULL
		WHERE ui.issuer = $1 AND ui.subject = $2 AND p.kind = 'user'`,
		strings.TrimSuffix(issuer, "/"), subject).Scan(&principalID, &orgID, &name, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotBound
	}
	if err != nil {
		return "", "", "", fmt.Errorf("auth: resolve identity: %w", err)
	}
	if status != "active" {
		return "", "", "", ErrNotBound
	}
	return principalID, orgID, name, nil
}

// ResolveByEmail finds the Hearth person an address belongs to, for the Hub
// module (internal/hubmodule) when the identity subject is not linked yet: a
// colleague who has an account here but has not signed in with Vord, or whose
// first sign-in was before the identity table existed.
//
// Lookup only, like ResolveFederated, and it links nothing: the next browser
// sign-in does that. The address is the one the core took from the verified
// Vord token. It matches a recorded Vord identity of this issuer first, then a
// local credential. An address that matches more than one person is refused
// rather than guessed at, and so is anybody inactive or in a playground.
func (s *Service) ResolveByEmail(ctx context.Context, issuer, email string) (principalID, orgID, name string, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", "", "", ErrNotBound
	}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT p.id::text, p.org_id::text, p.display_name
		FROM principals p
		JOIN orgs o ON o.id = p.org_id AND o.playground_of IS NULL
		WHERE p.kind = 'user' AND p.status = 'active'
		  AND (EXISTS (SELECT 1 FROM user_identities ui
		                WHERE ui.principal_id = p.id AND ui.issuer = $1 AND ui.email = $2)
		    OR EXISTS (SELECT 1 FROM local_credentials lc
		                WHERE lc.principal_id = p.id AND lc.email = $2))
		LIMIT 2`, strings.TrimSuffix(issuer, "/"), email)
	if err != nil {
		return "", "", "", fmt.Errorf("auth: resolve address: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if err := rows.Scan(&principalID, &orgID, &name); err != nil {
			return "", "", "", fmt.Errorf("auth: resolve address: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", "", fmt.Errorf("auth: resolve address: %w", err)
	}
	if n != 1 {
		return "", "", "", ErrNotBound
	}
	return principalID, orgID, name, nil
}
