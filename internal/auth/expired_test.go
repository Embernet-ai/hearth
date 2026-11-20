package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// failing is a handler that makes an EmberOPS call that cannot get a token and
// then answers with the given status, as a page handler does.
func failing(tokenErr error, status int) (http.Handler, func(*emberops.AuthWatch)) {
	var watch *emberops.AuthWatch
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenErr != nil {
			c := emberops.New(emberops.Config{BaseURL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}})
			_, _ = c.Get(r.Context(), "Task", "x")
		}
		http.Error(w, "could not load", status)
	})
	return h, func(w *emberops.AuthWatch) { watch = w; _ = watch }
}

type badSource struct{ err error }

func (b badSource) Token(context.Context) (string, error) { return "", b.err }

func serve(t *testing.T, tokenErr error, status int, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	s := &Service{}
	h, _ := failing(tokenErr, status)
	rec := httptest.NewRecorder()
	ctx := req.Context()
	if tokenErr != nil {
		ctx = emberops.WithUser(ctx, badSource{tokenErr})
	}
	ctx, watch := emberops.WithAuthWatch(ctx)
	req = req.WithContext(ctx)
	h.ServeHTTP(&expiryWriter{ResponseWriter: rec, s: s, r: req, watch: watch}, req)
	return rec
}

func TestExpiredSessionRedirectsToLoginKeepingTheReturnURL(t *testing.T) {
	for _, e := range []error{ErrTokenExpired, ErrNoUserToken, fmt.Errorf("wrapped: %w", ErrTokenExpired)} {
		rec := serve(t, e, 500, httptest.NewRequest("GET", "/tasks?status=open", nil))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%v: status %d, want 303", e, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != "/login?next=%2Ftasks%3Fstatus%3Dopen" {
			t.Fatalf("%v: Location %q", e, got)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("the failed page leaked: %q", rec.Body.String())
		}
	}
}

func TestExpiredSessionAnswersJSONAPIWith401(t *testing.T) {
	rec := serve(t, ErrTokenExpired, 500, httptest.NewRequest("GET", "/api/v1/documents", nil))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	req := httptest.NewRequest("GET", "/something", nil)
	req.Header.Set("Accept", "application/json")
	if rec := serve(t, ErrNoUserToken, 502, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("Accept json: %d", rec.Code)
	}
}

func TestOtherFailuresAreLeftAlone(t *testing.T) {
	rec := serve(t, errors.New("vord is down"), 500, httptest.NewRequest("GET", "/tasks", nil))
	if rec.Code != 500 || rec.Body.Len() == 0 {
		t.Fatalf("an unrelated failure was rewritten: %d", rec.Code)
	}
	rec = serve(t, nil, 404, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPostIsNotReturnedTo(t *testing.T) {
	rec := serve(t, ErrTokenExpired, 500, httptest.NewRequest("POST", "/tasks", nil))
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location %q, want a bare /login for a POST", got)
	}
}
