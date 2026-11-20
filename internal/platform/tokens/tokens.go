// Package tokens verifies bearer tokens issued by the identity provider.
//
// This is deliberately separate from internal/auth/oidc.go, which owns the
// browser handshake. That package's verifier is pinned to the browser login's client
// id and its exchange requires a nonce, an email, and a verified email, none of
// which a machine token has. Widening it to accept bearer tokens would weaken
// the path every human login already depends on, so bearer verification is its
// own thing with its own audience.
//
// It knows nothing about users, actors or rights: it turns a signed string into
// a subject, and stops. Turning a subject into somebody with permissions is a
// question for a feature package, and internal/architecture_test.go enforces
// that separation.
package tokens

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
)

// ErrUnavailable means the token could not be checked because the identity
// provider's signing keys could not be fetched: it is not a verdict on the
// token. go-oidc flattens the cause into the message ("oidc: get keys failed",
// "fetching keys"), so that is what is read.
var ErrUnavailable = errors.New("tokens: the signing keys could not be fetched")

func keysUnavailable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "oidc: get keys failed") || strings.Contains(msg, "fetching keys") ||
		errors.Is(err, context.DeadlineExceeded)
}

// ErrNoToken means the request carried no bearer token at all.
//
// Kept distinct from a bad token because the correct response differs: a caller
// with no token gets told where to authenticate, a caller with a bad one gets
// told it was refused.
var ErrNoToken = errors.New("tokens: no bearer token")

// ErrInvalid means the token was present and not acceptable.
//
// Every reason collapses to this one error on purpose. Telling a caller whether
// their token was expired, mis-audienced or forged tells an attacker which of
// those to fix.
var ErrInvalid = errors.New("tokens: token is not valid")

// Claims are the fields Hearth reads out of a bearer token.
type Claims struct {
	// Subject is the identity provider's stable id for the caller. It is the
	// only field authorisation is ever derived from; everything else here is
	// for display and audit.
	Subject string `json:"sub"`

	Email    string `json:"email"`
	Name     string `json:"name"`
	Username string `json:"preferred_username"`

	// ClientID is Keycloak's azp claim, naming which client obtained the token.
	// Recorded so an audit entry can say which integration acted, not merely
	// which identity it acted as.
	ClientID string `json:"azp"`

	// RealmAccess carries the realm roles the provider asserts.
	//
	// Never an authorisation input. What a caller may do comes from grants held
	// in Hearth, because a realm role is realm-wide and cannot express
	// "edit in Engineering and nothing in Sales".
	// A role is only ever asked one question: may this identity reach this
	// surface at all. That question has one answer for the whole realm, which
	// is exactly what a realm role is good for.
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// HasRealmRole reports whether the provider asserted this realm role.
func (c Claims) HasRealmRole(name string) bool {
	if name == "" {
		return true
	}
	for _, r := range c.RealmAccess.Roles {
		if r == name {
			return true
		}
	}
	return false
}

// DisplayName is the best human name the provider gave us.
//
// A service account has no name and no email, so it falls back to the username,
// which Keycloak sets to service-account-<client-id>. That is genuinely the most
// useful thing to show for a machine.
func (c Claims) DisplayName() string {
	switch {
	case c.Name != "":
		return c.Name
	case c.Username != "":
		return c.Username
	default:
		return c.Subject
	}
}

// Verifier checks tokens against one issuer and one audience.
type Verifier struct {
	verifier *coreoidc.IDTokenVerifier
	audience string
}

// Config describes what a token must satisfy.
type Config struct {
	// Issuer is the value that must appear in the token's iss claim.
	Issuer string

	// DiscoveryURL is where the metadata document is fetched from, when that
	// differs from Issuer. Same split-hostname reasoning as the browser login's OIDC
	// config; empty is the correct production setting.
	DiscoveryURL string

	// Audience is the resource the token must be for. A token minted for the
	// browser login, or for some other service in the realm, is refused here even
	// though it is perfectly valid elsewhere. That is the point: a bearer token
	// should not be a skeleton key for every service sharing an issuer.
	Audience string
}

// New discovers the provider and builds a verifier.
//
// Discovery happens once at startup, and the key set is fetched lazily and
// cached, so a signing key rotation is picked up without a restart.
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, errors.New("tokens: issuer is required")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("tokens: audience is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	discoverAt := cfg.Issuer
	if cfg.DiscoveryURL != "" && cfg.DiscoveryURL != cfg.Issuer {
		discoverAt = cfg.DiscoveryURL
		ctx = coreoidc.InsecureIssuerURLContext(ctx, cfg.Issuer)
	}

	provider, err := coreoidc.NewProvider(ctx, discoverAt)
	if err != nil {
		return nil, fmt.Errorf("tokens: discover provider at %q: %w", discoverAt, err)
	}

	return FromVerifier(provider.Verifier(&coreoidc.Config{
		// go-oidc checks that the aud claim contains ClientID, which is exactly
		// the audience check wanted here. Naming it ClientID is the library's
		// choice, not ours.
		ClientID: cfg.Audience,
	}), cfg.Audience), nil
}

// FromVerifier builds a verifier over an already-configured key set. Used by
// tests to avoid discovery against a live provider.
func FromVerifier(v *coreoidc.IDTokenVerifier, audience string) *Verifier {
	return &Verifier{verifier: v, audience: audience}
}

// Audience is the resource this verifier accepts tokens for.
func (v *Verifier) Audience() string { return v.audience }

// Verify checks a raw token and returns what it asserts.
//
// Signature, issuer, expiry and audience are all checked by the underlying
// verifier. What is deliberately not checked here is anything about the
// caller's rights: a valid token proves who is calling and nothing else.
func (v *Verifier) Verify(ctx context.Context, raw string) (Claims, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Claims{}, ErrNoToken
	}

	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		if keysUnavailable(err) {
			// The identity provider's keys could not be fetched. That says
			// nothing about the token, and a caller told it was refused drops a
			// good one. The caller answers 503, not 401.
			return Claims{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		// Wrapped, not replaced: the detail is worth logging on the server and
		// worth never returning to the caller.
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	var claims Claims
	if err := token.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("%w: claims are unreadable: %v", ErrInvalid, err)
	}
	if strings.TrimSpace(claims.Subject) == "" {
		// Without a subject there is nobody to be. This should be impossible
		// from a conforming provider, which is exactly why it is worth saying
		// out loud rather than returning an actor for the empty string.
		return Claims{}, fmt.Errorf("%w: token carries no subject", ErrInvalid)
	}
	return claims, nil
}

// BearerToken pulls the credential out of an Authorization header value.
//
// Returns ErrNoToken for anything that is not a bearer scheme, including Basic
// and including an empty header, so the caller can answer with a challenge
// rather than a refusal.
func BearerToken(header string) (string, error) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", ErrNoToken
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", ErrNoToken
	}
	return token, nil
}
