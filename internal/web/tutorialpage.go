package web

// Settings -> Tutorial: the way into somebody's personal playground, back out,
// and Reset. See internal/playground for what a playground is and why it is
// an organisation of its own.

import (
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
)

func (s *Server) getTutorial(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	s.render(w, r, "tutorial", page{
		Title:   "Tutorial",
		Message: r.URL.Query().Get("done"),
		// In it already: the page offers Back and Reset rather than Enter.
		InPlayground: id.ActingFor != "",
	})
}

// postEnterPlayground switches a real session to the person's own playground
// self, building the playground on the first visit. Only from a real session
// in a real organisation: nobody enters a playground from inside one.
func (s *Server) postEnterPlayground(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if id.ActingFor != "" || s.playground.IsPlayground(r.Context(), id.OrgID) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	orgID, self, err := s.playground.Enter(r.Context(), id.PrincipalID, id.DisplayName)
	if err != nil {
		s.log.ErrorContext(r.Context(), "enter playground", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not open your playground")
		return
	}
	if err := s.auth.Switch(r.Context(), w, r, orgID, self, id.PrincipalID); err != nil {
		s.log.ErrorContext(r.Context(), "switch to playground", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not open your playground")
		return
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

// postLeavePlayground puts the real person's own session back. Their status is
// checked again on the way out, as it is on any sign-in: somebody suspended
// while practising does not get a session back.
func (s *Server) postLeavePlayground(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if id.ActingFor == "" {
		http.Redirect(w, r, "/settings/tutorial", http.StatusSeeOther)
		return
	}
	if !s.backToReal(w, r, id.ActingFor) {
		return
	}
	http.Redirect(w, r, "/settings/tutorial", http.StatusSeeOther)
}

// postResetPlayground throws the playground away. From inside it, that means
// stepping out first (the session belongs to the playground being deleted),
// then straight back into a fresh one: a clean slate, not a trip to Settings.
func (s *Server) postResetPlayground(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	real := id.PrincipalID
	inside := id.ActingFor != ""
	if inside {
		real = id.ActingFor
		if !s.backToReal(w, r, real) {
			return
		}
	} else if s.playground.IsPlayground(r.Context(), id.OrgID) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := s.playground.Reset(r.Context(), real); err != nil {
		s.log.ErrorContext(r.Context(), "reset playground", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not reset your playground")
		return
	}
	if !inside {
		http.Redirect(w, r, "/settings/tutorial?done=Your+playground+was+reset.", http.StatusSeeOther)
		return
	}
	name, err := s.auth.DisplayNameOf(r.Context(), real)
	if err != nil {
		s.log.ErrorContext(r.Context(), "reset playground", "error", err)
		http.Redirect(w, r, "/settings/tutorial", http.StatusSeeOther)
		return
	}
	orgID, self, err := s.playground.Enter(r.Context(), real, name)
	if err == nil {
		err = s.auth.Switch(r.Context(), w, r, orgID, self, real)
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "re-enter playground", "error", err)
		http.Redirect(w, r, "/settings/tutorial", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

// backToReal switches the browser back to the real person's own session, or
// signs it out if they may no longer sign in. Reports whether to carry on.
func (s *Server) backToReal(w http.ResponseWriter, r *http.Request, real string) bool {
	ok, err := s.auth.SignInAllowed(r.Context(), real)
	if err != nil {
		s.log.ErrorContext(r.Context(), "leave playground", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not leave the playground")
		return false
	}
	org, err := s.auth.OrgOf(r.Context(), real)
	if !ok || err != nil {
		_ = s.auth.Destroy(r.Context(), w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return false
	}
	if err := s.auth.Switch(r.Context(), w, r, org, real, ""); err != nil {
		s.log.ErrorContext(r.Context(), "leave playground", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not leave the playground")
		return false
	}
	return true
}

// playgroundBlocked is everything that reaches outside Hearth, or creates
// something real, that a playground must not do: email, webhooks, tokens,
// Google, imports, real accounts, capabilities. Prefixes of POST paths.
var playgroundBlocked = []string{
	"/integrations/", "/agents", "/settings/mail", "/calendar/feed", "/import/",
	"/rules", "/account/password", "/settings/connectors", "/cloud/",
}

// playgroundBlocks is the guard's whole decision: whether a request with this
// method and path must be refused inside a playground. A function on its own
// so the test can put every route the server has through it
// (tutorialpage_test.go).
func playgroundBlocks(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	if path == "/people" || // a new person is a real login
		strings.HasSuffix(path, "/capability") || strings.HasSuffix(path, "/reply") ||
		strings.HasSuffix(path, "/password-link") { // it can send a real email
		return true
	}
	for _, prefix := range playgroundBlocked {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// playgroundGuard refuses those inside a playground, before any handler runs,
// so a new handler under one of these paths is covered the day it is added.
func (s *Server) playgroundGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auth.From(r.Context())
		if id != nil && playgroundBlocks(r.Method, r.URL.Path) && s.playground.IsPlayground(r.Context(), id.OrgID) {
			s.fail(w, r, http.StatusForbidden,
				"That reaches outside Hearth, so it is switched off in the playground. Everything else works here.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
