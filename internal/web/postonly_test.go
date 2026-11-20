package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// registeredRoutes reads every route the server registers, from the source, the way the playground guard test does, so a
// form route added next year is checked too.
func registeredRoutes(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`(?:authed|mux)\.HandleFunc\("((?:GET|POST|PUT|PATCH|DELETE) [^"]+)"`)
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			out = append(out, m[1])
		}
	}
	if len(out) < 100 {
		t.Fatalf("found only %d routes; the pattern has stopped matching", len(out))
	}
	return out
}

// A form's own address is not a page. A refresh, a bookmark or the back button on one used to land on the catch-all and say 404 "no such
// page", which is what contractors saw after logging time with a duration that was refused. Every address that only a form posts to must go
// back to a real page above it.
func TestEveryFormOnlyAddressGoesBackToAPage(t *testing.T) {
	routes := registeredRoutes(t)
	mux := http.NewServeMux()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	seen := map[string]bool{}
	for _, r := range routes {
		if !seen[r] {
			seen[r] = true
			mux.Handle(r, noop)
		}
	}
	sample := regexp.MustCompile(`\{[^}]+\}`)
	checked, toRoot := 0, []string{}
	for _, r := range routes {
		method, pattern, _ := strings.Cut(r, " ")
		if method != "POST" || seen["GET "+pattern] {
			continue
		}
		p := sample.ReplaceAllString(strings.TrimSuffix(pattern, "..."), "x")
		to, ok := postOnlyParent(mux, "h", &url.URL{Path: p})
		if !ok {
			t.Errorf("%s is a form's address and has no way back", r)
			continue
		}
		checked++
		if to == "/" {
			toRoot = append(toRoot, r)
		}
	}
	if checked < 80 {
		t.Fatalf("only %d form addresses checked", checked)
	}
	t.Logf("%d form-only addresses checked; %d have no page above them and go to the dashboard: %v", checked, len(toRoot), toRoot)
	// the case that was reported
	if to, ok := postOnlyParent(mux, "h", &url.URL{Path: "/documents/dfec564a-9dce-4ec6-a96e-b2f1b3ad8bc1/time"}); !ok || to != "/documents/dfec564a-9dce-4ec6-a96e-b2f1b3ad8bc1" {
		t.Errorf("log time goes back to %q", to)
	}
	// a page that exists, and an address that is nothing, are left alone
	if _, ok := postOnlyParent(mux, "h", &url.URL{Path: "/documents/abc"}); ok {
		t.Error("a page was treated as a form address")
	}
	if _, ok := postOnlyParent(mux, "h", &url.URL{Path: "/no/such/page"}); ok {
		t.Error("an address nothing posts to was treated as a form address")
	}
}
