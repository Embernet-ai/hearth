package web

// Connecting a Google account, from the person's own account page.
//
// Per person and never for the company, so every one of these handlers works on
// the caller's own principal id, taken from their session. There is no path
// here that takes a principal from the request, which is what makes it
// impossible to connect or disconnect somebody else by editing a URL.

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/google"
)

const googleStateCookie = "hearth_google_state"

// secureCookies mirrors the session cookie's own rule: Secure only when the
// site is actually served over https, because a browser silently drops a Secure
// cookie on plain http and the state check would then fail every time locally.
func (s *Server) secureCookies() bool {
	return strings.HasPrefix(s.baseURL, "https://")
}

// startGoogle sends somebody to Google to grant access.
//
// The scopes come from the form, so connecting for a calendar never asks for a
// mailbox. Somebody who wants both ticks both.
func (s *Server) startGoogle(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		s.fail(w, r, http.StatusNotFound, "the Google integration is not configured")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	var scopes []string
	for _, v := range r.Form["scope"] {
		switch v {
		case google.ScopeCalendarRead, google.ScopeCalendarWrite,
			google.ScopeGmailRead, google.ScopeGmailSend:
			scopes = append(scopes, v)
		}
	}
	// The connectors: what an administrator has turned on and the person ticked. The scopes come from the
	// provider's own list for each, so they are only ever the ones that connector needs.
	if kinds := r.Form["connector"]; len(kinds) > 0 && s.connect != nil {
		more, err := s.kindScopes(r, connect.Google, kinds)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "unknown connector")
			return
		}
		scopes = append(scopes, more...)
	}
	if len(scopes) == 0 {
		s.fail(w, r, http.StatusBadRequest,
			"choose at least one thing to connect, or there is nothing to grant")
		return
	}

	// state, in a short-lived cookie, checked on the way back. Without it an
	// attacker can feed somebody a callback carrying their own authorisation
	// code and attach their Google account to the victim's Hearth login.
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not start that")
		return
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{
		Name: googleStateCookie, Value: state, Path: "/",
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(10 * time.Minute),
	})

	http.Redirect(w, r, s.google.AuthURLFor(state, scopes, s.googleDomain(r.Context(), auth.From(r.Context()).OrgID)), http.StatusFound)
}

// googleCallback finishes the exchange.
func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		s.fail(w, r, http.StatusNotFound, "the Google integration is not configured")
		return
	}
	id := auth.From(r.Context())

	c, err := r.Cookie(googleStateCookie)
	if err != nil || c.Value == "" {
		s.fail(w, r, http.StatusBadRequest, "no connection was in progress")
		return
	}
	// One attempt per cookie, cleared before anything else, so a replayed
	// callback cannot reuse it.
	http.SetCookie(w, &http.Cookie{
		Name: googleStateCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	if r.URL.Query().Get("state") != c.Value {
		s.fail(w, r, http.StatusBadRequest, "that connection did not start here")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		// access_denied is somebody pressing cancel, which is not a fault.
		if e == "access_denied" {
			http.Redirect(w, r, "/account", http.StatusSeeOther)
			return
		}
		s.fail(w, r, http.StatusBadRequest, "Google refused that: "+e)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		s.fail(w, r, http.StatusBadRequest, "Google returned no authorisation code")
		return
	}

	if _, err := s.google.ConnectIn(r.Context(), id.OrgID, id.PrincipalID, code, s.googleDomain(r.Context(), id.OrgID)); err != nil {
		if errors.Is(err, connect.ErrWrongTenant) {
			s.fail(w, r, http.StatusBadRequest, "that Google account is not in the connected Workspace domain")
			return
		}
		s.log.WarnContext(r.Context(), "google connect", "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "google: "))
		return
	}
	if s.connect != nil {
		s.firstSync(r, id.OrgID, id.PrincipalID, connect.Google)
		http.Redirect(w, r, "/settings/connectors", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// disconnectGoogle removes the caller's own connection.
func (s *Server) disconnectGoogle(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		s.fail(w, r, http.StatusNotFound, "the Google integration is not configured")
		return
	}
	id := auth.From(r.Context())
	if err := s.google.Disconnect(r.Context(), id.PrincipalID); err != nil {
		s.log.ErrorContext(r.Context(), "google disconnect", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not disconnect")
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// syncGoogle pulls the calendar now, rather than waiting for the next
// scheduled run, so somebody who has just connected sees something.
func (s *Server) syncGoogle(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		s.fail(w, r, http.StatusNotFound, "the Google integration is not configured")
		return
	}
	id := auth.From(r.Context())

	from := time.Now().AddDate(0, 0, -7)
	_, err := s.google.SyncCalendar(r.Context(), id.OrgID, id.PrincipalID, from, from.AddDate(0, 2, 0))
	switch {
	case errors.Is(err, google.ErrNotConnected):
		s.fail(w, r, http.StatusBadRequest, "connect a Google account first")
		return
	case errors.Is(err, google.ErrReconnect):
		s.fail(w, r, http.StatusBadRequest,
			"that connection was revoked in Google. Connect again to restore it.")
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "google sync", "error", err)
		s.fail(w, r, http.StatusBadGateway, strings.TrimPrefix(err.Error(), "google: "))
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}
