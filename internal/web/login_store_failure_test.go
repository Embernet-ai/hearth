package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/auth"
)

// A sign-in that cannot be checked because the database cannot be reached must
// not say the password is wrong: 503 with a Retry-After and a message that says
// to try again, so nobody retypes a right password. No Postgres needed: the pool
// points at a port nothing listens on.
func TestALoginDuringAnOutageIsNotAWrongPassword(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s, err := New(Deps{Auth: auth.New(pool, nil, false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("a server with only auth: %v", err)
	}
	form := url.Values{"email": {"a@b.example"}, "password": {"right"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.postLogin(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("an outage answered %d (Retry-After %q), want 503 with one", rec.Code, rec.Header().Get("Retry-After"))
	}
	if strings.Contains(rec.Body.String(), auth.ErrBadCredentials.Error()) {
		t.Fatal("the page told the person their password was wrong during an outage")
	}
}
