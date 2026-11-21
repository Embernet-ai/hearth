package web

// What a contractor account may reach.
//
// A contractor account exists to work in the folder it was given. Everything it
// may open is already cut by its grants, but a page that lists the company's
// contacts, deals, channels, calendar, people or time is a page about
// everybody, and the grants were never meant to be the only thing standing
// between a contractor and it. So the account is let into a short list of pages
// (the folder and the work in it, search, its own inbox and account) and every
// other address answers not found, whatever a handler would have done. A page
// added tomorrow is closed to contractors until it is put on the list.
//
// A super admin is never narrowed (auth.Identity.Contractor is never set for
// one), and an account that is not a contractor's goes straight through.

import (
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
)

// contractorPrefixes are the paths a contractor account may use, with the
// handlers behind them left to decide what its grants cover.
var contractorPrefixes = []string{
	"/documents", "/tasks", "/work-orders", "/projects", "/spaces", "/views",
	"/search", "/inbox", "/account", "/events", "/push/", "/preview",
	"/offline/",
}

// contractorMayReach reports whether a contractor account may use this request.
func contractorMayReach(r *http.Request) bool {
	path := r.URL.Path
	for _, prefix := range contractorPrefixes {
		if path == prefix || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/") ||
			(strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix)) {
			return true
		}
	}
	// Somebody's picture, on a comment or the top bar.
	return r.Method == http.MethodGet && strings.HasPrefix(path, "/people/") && strings.HasSuffix(path, "/avatar")
}

func (s *Server) contractorGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auth.From(r.Context())
		if id == nil || !id.Contractor {
			next.ServeHTTP(w, r)
			return
		}
		// Where the company's own front page would have been.
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			http.Redirect(w, r, "/spaces", http.StatusSeeOther)
			return
		}
		if !contractorMayReach(r) {
			s.fail(w, r, http.StatusNotFound, "no such page")
			return
		}
		next.ServeHTTP(w, r)
	})
}
