package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestWithNoBuildEmbeddedAPageRequestSaysSoInsteadOfServingABlankPage(t *testing.T) {
	// A checkout embeds only the placeholder.
	w := httptest.NewRecorder()
	serveShell(w, httptest.NewRequest(http.MethodGet, "/contacts", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not built into this binary") {
		t.Errorf("no build: %d %q", w.Code, w.Body.String())
	}
}

func withBuild(t *testing.T, fsys fstest.MapFS) {
	t.Helper()
	old := shellFS
	shellFS = func() fs.FS { return fsys }
	t.Cleanup(func() { shellFS = old })
}

// A browser navigation to any address gets the interface, and a request for data gets JSON: one address, two answers.
func TestEveryAddressAnswersABrowserWithTheShellAndTheInterfaceWithData(t *testing.T) {
	withBuild(t, fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Hearth</title>")}})
	s := newServer(t)
	for _, accept := range []string{"text/html,application/xhtml+xml", ""} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/documents/abc", nil)
		if accept != "" {
			r.Header.Set("Accept", accept)
		}
		s.render(w, r, "document", page{Title: "A document"})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Hearth</title>") ||
			!strings.Contains(w.Header().Get("Cache-Control"), "must-revalidate") ||
			!strings.Contains(w.Header().Get("Vary"), "Accept") {
			t.Errorf("accept %q: %d %q %v", accept, w.Code, w.Body.String(), w.Header())
		}
	}
	// A refusal keeps its status for a browser too.
	w := httptest.NewRecorder()
	s.fail(w, httptest.NewRequest(http.MethodGet, "/documents/abc", nil), http.StatusForbidden, "no")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "<title>Hearth</title>") ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("X-Hearth-Reason") != "no" {
		t.Errorf("refusal: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
}

func TestABuiltInterfaceIsServedWithTheRightCaching(t *testing.T) {
	h := serveBuilt(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>Hearth</title>")},
		"_astro/app.abc.js":   {Data: []byte("console.log(1)")},
		"theme-next.js":       {Data: []byte("x")},
		"folder/.placeholder": {Data: []byte("x")},
	})
	// Hashed bundles cache for a year; the unhashed script does not.
	if cc := get(h, http.MethodGet, "/_astro/app.abc.js").Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("bundle cache-control %q", cc)
	}
	if w := get(h, http.MethodGet, "/theme-next.js"); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("theme script: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	// A stale page asking for last release's bundle gets a 404, not the HTML.
	if w := get(h, http.MethodGet, "/_astro/gone.js"); w.Code != http.StatusNotFound {
		t.Errorf("missing bundle: %d", w.Code)
	}
	// The shell is not a file of its own: it is what every page address answers with.
	if w := get(h, http.MethodGet, "/index.html"); w.Code != http.StatusNotFound {
		t.Errorf("index.html: %d", w.Code)
	}
	// Nothing can climb out of the build.
	if w := get(h, http.MethodGet, "/../../etc/passwd"); strings.Contains(w.Body.String(), "root:") {
		t.Error("path traversal reached a file outside the build")
	}
	if w := get(h, http.MethodPost, "/_astro/app.abc.js"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("post: %d", w.Code)
	}
}

// The first release put the interface under /next/. Those addresses, with their queries, land at the root.
func TestOldNextAddressesRedirectToTheSameAddressAtTheRoot(t *testing.T) {
	for from, to := range map[string]string{
		"/next":                       "/",
		"/next/":                      "/",
		"/next/contacts":              "/contacts",
		"/next/documents?kind=task&x": "/documents?kind=task&x",
		"/next/deals/abc":             "/deals/abc",
	} {
		w := get(http.HandlerFunc(movedFromNext), http.MethodGet, from)
		if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != to {
			t.Errorf("%s: %d %q, want 308 to %q", from, w.Code, w.Header().Get("Location"), to)
		}
	}
}
