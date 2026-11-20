package web

import (
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
)

// mustChangeGate sends somebody whose account is still on a password another
// person chose to their account page, before any other page.
//
// The flag used to be a banner on the account page and nothing else, so an
// account on a handed-over password carried on exactly like any other. Now the
// only places a person in that state can reach are the account page, the form
// that changes the password, and signing out (which is outside this gate).
func (s *Server) mustChangeGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auth.From(r.Context())
		if id == nil || !id.MustChangePassword || mustChangeAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/account", http.StatusSeeOther)
	})
}

// mustChangeAllowed is the short list of requests a person who has to change
// their password may still make.
func mustChangeAllowed(r *http.Request) bool {
	switch r.URL.Path {
	case "/account", "/account/password":
		return true
	}
	// The account page shows the person's own picture.
	return r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/people/") &&
		strings.HasSuffix(r.URL.Path, "/avatar")
}
