package web

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// postOnlyParent answers a GET for an address that only a form posts to, such as /documents/{id}/time. Such an address is not a
// page: asking for it with GET (a refresh, a bookmark, the back button after a form left it in the address bar) used to
// land on the catch-all and answer 404 "no such page". Instead it says where the person belongs, the nearest page above it
// (/documents/{id}), and the caller redirects there. ok is false for an address that is not a form's target at all, which stays a 404.
func postOnlyParent(mux *http.ServeMux, host string, u *url.URL) (to string, ok bool) {
	probe := func(method, p string) string {
		x := *u
		x.Path, x.RawPath = p, ""
		_, pattern := mux.Handler(&http.Request{Method: method, URL: &x, Host: host})
		return pattern
	}
	p := u.Path
	if p == "/" || !strings.HasPrefix(probe("POST", p), "POST ") {
		return "", false
	}
	if pat := probe("GET", p); strings.HasPrefix(pat, "GET ") && pat != "GET /" {
		return "", false // it is a page as well as a form's address
	}
	for q := path.Dir(p); q != "/" && q != "."; q = path.Dir(q) {
		if pat := probe("GET", q); strings.HasPrefix(pat, "GET ") && pat != "GET /" {
			return q, true
		}
	}
	return "/", true
}
