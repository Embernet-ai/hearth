package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The phone's server-address page and the answer it checks an address with are public, the answer can be asked from
// another origin, and only the page widens what the browser may connect to.
func TestConnectPageAndWellKnown(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.getWellKnown(w, httptest.NewRequest(http.MethodGet, "/.well-known/hearth", nil))
	if w.Header().Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(w.Body.String(), `"app":"hearth"`) {
		t.Errorf("well-known = %v %q", w.Header(), w.Body.String())
	}
	w = httptest.NewRecorder()
	s.getConnect(w, httptest.NewRequest(http.MethodGet, "/connect", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src https: http:") || !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("csp = %q", csp)
	}
	if !strings.Contains(w.Body.String(), `id="server-form"`) || !strings.Contains(w.Body.String(), "/connect.js") {
		t.Error("the page lacks its form or its script")
	}
}
