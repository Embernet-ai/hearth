package mcpserver_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/mcpserver"
	"github.com/Embernet-ai/hearth/internal/platform/tokens"
)

// An agent token that cannot be checked because the database cannot be reached
// is not an invalid token: 503 with a Retry-After, not a 401 challenge that
// makes a client drop a good token. No Postgres needed, the pool points at a
// port nothing listens on.
func TestAnUnreachableStoreIsNotAnInvalidAgentToken(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pub, _ := url.Parse("https://hearth.example")
	srv := mcpserver.New(mcpserver.Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PublicURL: pub,
		Agents: agents.New(pool, nil),
	})
	h := srv.Handlers()[mcpserver.Path]

	req := httptest.NewRequest("POST", mcpserver.Path, nil)
	req.Header.Set("Authorization", "Bearer hearth_agent_abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("an unreachable store answered %d (Retry-After %q), want 503 with one", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// permissiveKeys accepts any signature and hands back the payload, so a test can
// present a token that verifies without a real identity provider.
type permissiveKeys struct{}

func (permissiveKeys) VerifySignature(_ context.Context, jwt string) ([]byte, error) {
	parts := strings.Split(jwt, ".")
	return base64.RawURLEncoding.DecodeString(parts[1])
}

type people struct{ err error }

func (p people) ResolveFederated(context.Context, string, string) (string, string, string, error) {
	return "", "", "", p.err
}

// A person with a good Vord token whose Hearth account cannot be looked up
// because the store failed is not "not provisioned": 503 with a Retry-After. A
// person with no account (auth.ErrNotBound) still gets the 403 that tells them
// to sign in to Hearth once.
func TestAnAccountLookupDuringAnOutageIsNotNotProvisioned(t *testing.T) {
	const issuer, aud = "https://vord.example/realms/x", "hearth"
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	token := seg(map[string]string{"alg": "RS256"}) + "." + seg(map[string]any{
		"iss": issuer, "aud": aud, "sub": "s1", "azp": "c", "exp": time.Now().Add(time.Hour).Unix(),
		"realm_access": map[string]any{"roles": []string{"fireball-staff"}},
	}) + ".sig"
	pub, _ := url.Parse("https://hearth.example")
	serve := func(p people) *httptest.ResponseRecorder {
		srv := mcpserver.New(mcpserver.Options{
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PublicURL: pub, Issuer: issuer, RequiredRole: "fireball-staff",
			Verifier: tokens.FromVerifier(coreoidc.NewVerifier(issuer, permissiveKeys{}, &coreoidc.Config{ClientID: aud}), aud),
			People:   p,
		})
		req := httptest.NewRequest("POST", mcpserver.Path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handlers()[mcpserver.Path].ServeHTTP(rec, req)
		return rec
	}
	if rec := serve(people{auth.ErrNotBound}); rec.Code != http.StatusForbidden {
		t.Fatalf("a person with no account answered %d, want 403", rec.Code)
	}
	rec := serve(people{errors.New("auth: resolve identity: connection refused")})
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("an outage answered %d (Retry-After %q), want 503 with one", rec.Code, rec.Header().Get("Retry-After"))
	}
}
