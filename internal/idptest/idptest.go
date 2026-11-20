// Package idptest stands in for the identity provider.
//
// Bearer authentication is the second front door into Hearth, and the tests that
// matter about it — a token for the wrong audience is refused, a subject nobody
// bound gets nothing, one person cannot reach what they were not granted — all need
// a real signed token to drive. Faking the verifier instead would prove only
// that the fake agrees with itself, and the verifier is precisely the part worth
// checking.
//
// So this serves an OIDC discovery document and a JWKS over httptest, and mints
// tokens signed by the key in it. Everything downstream is then the real thing:
// real signature verification, real issuer and audience and expiry checks.
//
// It is test support and is imported only by tests, but it lives in a normal
// package because two suites need it. Same reasoning as internal/platform/pg/pgtest.
package idptest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/Embernet-ai/hearth/internal/platform/tokens"
)

// keyBits is deliberately the smallest size still worth calling RSA.
//
// Every Provider generates its own key so that one can sign with a key another
// does not publish, and key generation is the slowest thing in these suites.
const keyBits = 2048

// Provider is a running stand-in for vord.
type Provider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
}

// New starts a provider and shuts it down when the test ends.
func New(t *testing.T) *Provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	p := &Provider{key: key, keyID: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.Issuer(),
			"authorization_endpoint":                p.Issuer() + "/authorize",
			"token_endpoint":                        p.Issuer() + "/token",
			"jwks_uri":                              p.Issuer() + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{
				Key:       p.key.Public(),
				KeyID:     p.keyID,
				Algorithm: string(jose.RS256),
				Use:       "sig",
			}},
		})
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// Issuer is the value this provider puts in iss, and the URL discovery runs
// against.
func (p *Provider) Issuer() string { return p.server.URL }

// Verifier builds the real verifier against this provider, for one audience.
func (p *Provider) Verifier(t *testing.T, audience string) *tokens.Verifier {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	v, err := tokens.New(ctx, tokens.Config{Issuer: p.Issuer(), Audience: audience})
	if err != nil {
		t.Fatalf("build verifier for audience %q: %v", audience, err)
	}
	return v
}

// Token describes a token to mint.
//
// Every field that a test might want to get wrong is settable, because the
// interesting cases here are all the wrong ones.
type Token struct {
	// Subject is the identity provider's id for the caller.
	Subject string

	// Audience is what the token is for. Empty means no aud claim at all,
	// which is its own refusal case.
	Audience []string

	// Issuer overrides iss. Empty means this provider, which is the normal
	// case; setting it is how the wrong-issuer case is built.
	Issuer string

	Email    string
	Name     string
	Username string
	ClientID string

	// RealmRoles go into realm_access.roles, the way Keycloak emits them.
	RealmRoles []string

	// Lifetime is how long from now the token is good for. Negative mints an
	// already-expired token. Zero means one hour.
	Lifetime time.Duration
}

// claimSet is the JWT payload. Separate from tokens.Claims because this side
// needs the registered claims the verifier checks and never reads back.
type claimSet struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub,omitempty"`
	Audience []string `json:"aud,omitempty"`
	Expiry   int64    `json:"exp"`
	IssuedAt int64    `json:"iat"`

	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	Username string `json:"preferred_username,omitempty"`
	ClientID string `json:"azp,omitempty"`

	RealmAccess *realmAccess `json:"realm_access,omitempty"`
}

// realmAccess mirrors Keycloak's nesting. Omitted entirely when there are no
// roles, because a token from a provider that does not emit them has no such
// claim at all, and that is a case worth being able to mint.
type realmAccess struct {
	Roles []string `json:"roles"`
}

// Mint signs a token.
func (p *Provider) Mint(t *testing.T, tok Token) string {
	t.Helper()

	issuer := tok.Issuer
	if issuer == "" {
		issuer = p.Issuer()
	}
	lifetime := tok.Lifetime
	if lifetime == 0 {
		lifetime = time.Hour
	}

	// Issued in the past by a minute so that an expired token is unambiguously
	// expired rather than sitting on the boundary, and so a clock a hair behind
	// the test does not reject a valid one.
	now := time.Now().Add(-time.Minute)

	payload, err := json.Marshal(claimSet{
		Issuer:   issuer,
		Subject:  tok.Subject,
		Audience: tok.Audience,
		Expiry:   now.Add(time.Minute + lifetime).Unix(),
		IssuedAt: now.Unix(),
		Email:    tok.Email,
		Name:     tok.Name,
		Username: tok.Username,
		ClientID: tok.ClientID,
		RealmAccess: func() *realmAccess {
			if len(tok.RealmRoles) == 0 {
				return nil
			}
			return &realmAccess{Roles: tok.RealmRoles}
		}(),
	})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{
			Algorithm: jose.RS256,
			Key:       jose.JSONWebKey{Key: p.key, KeyID: p.keyID},
		},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}

	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatalf("serialise token: %v", err)
	}
	return raw
}

// Bearer is Mint plus the header syntax, which is all most callers want.
func (p *Provider) Bearer(t *testing.T, tok Token) string {
	t.Helper()
	return "Bearer " + p.Mint(t, tok)
}
