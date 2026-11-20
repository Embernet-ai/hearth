package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A session that cannot be checked because the database cannot be reached is
// not a missing session. Require answers 503 with a Retry-After and the cookie
// stays, instead of redirecting everyone to the sign-in page over a blip. A
// request with no cookie is still sent to sign in, and a public page still
// loads. No Postgres needed: the pool points at a port nothing listens on.
func TestAnUnreachableStoreIsNotASignOut(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := New(pool, nil, false)

	private := s.Middleware(Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })))
	public := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	cookie := &http.Cookie{Name: s.cookieName(), Value: base64.RawURLEncoding.EncodeToString(make([]byte, tokenBytes))}

	req := httptest.NewRequest("GET", "/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	private.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("an unreachable store answered %d (Retry-After %q, Location %q), want 503 with a Retry-After",
			rec.Code, rec.Header().Get("Retry-After"), rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	private.ServeHTTP(rec, httptest.NewRequest("GET", "/documents", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no cookie answered %d, want the redirect to sign in", rec.Code)
	}

	req = httptest.NewRequest("GET", "/healthz", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	public.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("a public page answered %d while the store was unreachable", rec.Code)
	}
}
