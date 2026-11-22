package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
)

func loginPage(t *testing.T, d Deps) map[string]any {
	t.Helper()
	d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	s.render(rec, req, "login", page{Title: "Sign in"})
	var env struct {
		Page map[string]any `json:"page"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return env.Page
}

// The sign-in page is told what the provider button says and whether the form is off. Zero values keep
// today's page, and the form is never reported off when there is no provider to replace it.
func TestLoginPageCarriesTheDoorSettings(t *testing.T) {
	p := loginPage(t, Deps{OIDC: &auth.OIDC{}, OIDCName: "Vord", HideLocalLogin: true})
	if p["SSO"] != true || p["SSOName"] != "Vord" || p["HideLocalLogin"] != true {
		t.Fatalf("provider-only door: %v", p)
	}
	p = loginPage(t, Deps{OIDC: &auth.OIDC{}, OIDCName: "Vord"})
	if p["HideLocalLogin"] == true {
		t.Fatalf("the form is hidden by default: %v", p)
	}
	p = loginPage(t, Deps{HideLocalLogin: true})
	if p["HideLocalLogin"] == true || p["SSO"] == true {
		t.Fatalf("no provider, so the form must stay: %v", p)
	}
}
