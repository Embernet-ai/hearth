package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/api"
)

// A token that cannot be checked because the database cannot be reached is not
// a bad token. The API answers 503 with a Retry-After, so a caller keeps a good
// token and tries again, instead of 401, which tells it the token is dead.
// The pool points at a port nothing listens on, so no Postgres is needed.
func TestAnUnreachableStoreIsNotABadToken(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := api.New(pool, nil, nil).Routes()

	req := httptest.NewRequest("GET", "/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("an unreachable store answered %d (Retry-After %q), want 503 with one", rec.Code, rec.Header().Get("Retry-After"))
	}

	// No token at all is still a plain 401.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/whoami", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token answered %d, want 401", rec.Code)
	}
}
