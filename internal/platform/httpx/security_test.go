package httpx

// Security headers and the origin check.
//
// The assertions are about behaviour rather than about the exact text of a
// header, except where the exact text is the point: 'unsafe-inline' being
// absent from script-src is the whole value of that directive, so it is checked
// literally and a future change that weakens it should fail here loudly.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testGuard() Security {
	return Security{
		HTTPS:      true,
		Origin:     "https://hearth.example.com",
		BlobOrigin: "https://files.example.com",
	}
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestTheHeadersAreSetOnAnOrdinaryResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	testGuard().Headers(http.HandlerFunc(ok)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	for header, want := range map[string]string{
		"Strict-Transport-Security":  "max-age=63072000; includeSubDomains",
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s is %q, want %q", header, got, want)
		}
	}
}

func TestHSTSIsWithheldWhenTheSiteIsNotHTTPS(t *testing.T) {
	g := testGuard()
	g.HTTPS = false

	rec := httptest.NewRecorder()
	g.Headers(http.HandlerFunc(ok)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	// Sending this over plain http pins the host for two years on whatever
	// machine reads it, which in development is somebody's own laptop.
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS was sent over http as %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") == "" {
		t.Error("the rest of the headers went missing along with HSTS")
	}
}

func TestScriptSrcAllowsNoInlineScript(t *testing.T) {
	csp := testGuard().policy()

	var script string
	for _, directive := range strings.Split(csp, "; ") {
		if strings.HasPrefix(directive, "script-src ") {
			script = directive
		}
	}
	if script == "" {
		t.Fatalf("there is no script-src at all in %q", csp)
	}
	if script != "script-src 'self'" {
		t.Errorf("script-src is %q. Every script in this application is a file "+
			"loaded by src, so this should not need widening; if a template has "+
			"grown an inline handler, remove it rather than relaxing this", script)
	}
}

func TestThePolicyNamesTheObjectStoreWhereFilesAreFetched(t *testing.T) {
	csp := testGuard().policy()

	// Files are handed out as presigned URLs pointing at the object store, so
	// leaving it out of these three blocks every attachment and every upload.
	for _, directive := range []string{"img-src", "media-src", "connect-src"} {
		var found bool
		for _, d := range strings.Split(csp, "; ") {
			if strings.HasPrefix(d, directive+" ") &&
				strings.Contains(d, "https://files.example.com") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not name the object store, so files will not load: %q",
				directive, csp)
		}
	}

	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("frame-ancestors is missing, so the page can be framed: %q", csp)
	}
}

func TestThePolicyLeavesTheObjectStoreOutWhenThereIsNotOne(t *testing.T) {
	g := testGuard()
	g.BlobOrigin = ""

	if got := g.policy(); strings.Contains(got, "files.example.com") {
		t.Errorf("an empty BlobOrigin still ended up in the policy: %q", got)
	}
}

func TestReadsArePassedThroughWhateverTheyClaim(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		r := httptest.NewRequest(method, "/documents", nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")

		rec := httptest.NewRecorder()
		testGuard().SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("a %s was refused with %d; the check is about writes", method, rec.Code)
		}
	}
}

func TestAWriteFromAnotherSiteIsRefused(t *testing.T) {
	// same-site is refused as firmly as cross-site, and that is the case this
	// adds over SameSite=Lax: Lax counts every host under the registrable
	// domain as the same site and lets the cookie through.
	for _, site := range []string{"cross-site", "same-site"} {
		r := httptest.NewRequest(http.MethodPost, "/documents/x/delete", nil)
		r.Header.Set("Sec-Fetch-Site", site)

		rec := httptest.NewRecorder()
		testGuard().SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

		if rec.Code != http.StatusForbidden {
			t.Errorf("a %s write got %d, want 403", site, rec.Code)
		}
	}
}

func TestAWriteFromThisSiteGoesThrough(t *testing.T) {
	for _, site := range []string{"same-origin", "none"} {
		r := httptest.NewRequest(http.MethodPost, "/documents/x/delete", nil)
		r.Header.Set("Sec-Fetch-Site", site)

		rec := httptest.NewRecorder()
		testGuard().SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("a %s write got %d, want 200", site, rec.Code)
		}
	}
}

func TestAMismatchedOriginIsRefusedWithoutSecFetchSite(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/documents/x/delete", nil)
	r.Header.Set("Origin", "https://attacker.example")

	rec := httptest.NewRecorder()
	testGuard().SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a write from another origin got %d, want 403", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "hearth.example.com") {
		t.Errorf("the refusal names the origin we expect, which tells the "+
			"sender what to forge next time: %q", body)
	}
}

func TestAMatchingOriginGoesThrough(t *testing.T) {
	// Trailing slashes and case are both things a real client sends.
	for _, origin := range []string{
		"https://hearth.example.com",
		"https://hearth.example.com/",
		"https://Hearth.Example.com",
	} {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.Header.Set("Origin", origin)

		rec := httptest.NewRecorder()
		testGuard().SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("origin %q got %d, want 200", origin, rec.Code)
		}
	}
}

func TestAWriteCarryingNeitherHeaderIsAllowed(t *testing.T) {
	// The acceptance suite, curl, and a monitoring probe all land here. None of
	// them carries an ambient session, so there is nothing to forge, and
	// refusing them would break real callers to close a door the cookie has
	// already shut.
	r := httptest.NewRequest(http.MethodPost, "/login", nil)

	rec := httptest.NewRecorder()
	testGuard().SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("a write with no browser headers got %d, want 200", rec.Code)
	}
}

func TestTheCheckStandsDownWhenTheSiteDoesNotKnowItsOwnAddress(t *testing.T) {
	g := testGuard()
	g.Origin = ""

	r := httptest.NewRequest(http.MethodPost, "/documents/x/delete", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	rec := httptest.NewRecorder()
	g.SameOrigin(http.HandlerFunc(ok)).ServeHTTP(rec, r)

	// Failing every write closed would be a worse failure than not checking,
	// and it would arrive as "the application stopped working" rather than as
	// a configuration error somebody can find.
	if rec.Code != http.StatusOK {
		t.Errorf("with no configured origin the check refused a write with %d", rec.Code)
	}
}
