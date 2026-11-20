package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
)

func gateRequest(t *testing.T, method, path string, id *auth.Identity) *httptest.ResponseRecorder {
	t.Helper()
	reached := false
	s := &Server{}
	h := s.mustChangeGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(method, path, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if reached && w.Code != http.StatusOK {
		t.Fatalf("%s %s reached the page and still answered %d", method, path, w.Code)
	}
	return w
}

func TestSomebodyOnAHandedOverPasswordIsSentToTheAccountPageFirst(t *testing.T) {
	id := &auth.Identity{PrincipalID: "p1", MustChangePassword: true}
	for _, path := range []string{"/", "/contacts", "/documents", "/people", "/companies?owner=x"} {
		w := gateRequest(t, http.MethodGet, path, id)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/account" {
			t.Errorf("GET %s = %d to %q, want 303 to /account", path, w.Code, w.Header().Get("Location"))
		}
	}
	// A write is refused the same way, not carried out.
	if w := gateRequest(t, http.MethodPost, "/contacts", id); w.Code != http.StatusSeeOther {
		t.Errorf("POST /contacts = %d, want 303", w.Code)
	}
}

func TestTheAccountPageAndThePasswordFormStayOpen(t *testing.T) {
	id := &auth.Identity{PrincipalID: "p1", MustChangePassword: true}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/account"},
		{http.MethodPost, "/account/password"},
		{http.MethodGet, "/people/p1/avatar"},
	} {
		if w := gateRequest(t, c.method, c.path, id); w.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want it open", c.method, c.path, w.Code)
		}
	}
	// Opening somebody's avatar is a GET only.
	if w := gateRequest(t, http.MethodPost, "/people/p1/avatar", id); w.Code == http.StatusOK {
		t.Error("POST to an avatar path got through the gate")
	}
}

func TestEverybodyElseIsUntouchedByTheGate(t *testing.T) {
	ok := &auth.Identity{PrincipalID: "p2"}
	if w := gateRequest(t, http.MethodGet, "/contacts", ok); w.Code != http.StatusOK {
		t.Errorf("an ordinary account = %d, want 200", w.Code)
	}
	// No identity at all is Require's business, not the gate's.
	if w := gateRequest(t, http.MethodGet, "/contacts", nil); w.Code != http.StatusOK {
		t.Errorf("no identity = %d, want the gate to stand aside", w.Code)
	}
}
