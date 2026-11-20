package web

// The set-password link: an administrator issues it, the person opens it and
// chooses their own password. See auth/passwordlink.go for the rules.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
)

// linkBase is the address people reach this installation at.
func (s *Server) linkBase(r *http.Request) string {
	if s.baseURL != "" {
		return strings.TrimRight(s.baseURL, "/")
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// showLink renders the page that hands an administrator a freshly issued link,
// and sends it by email first when this installation has somewhere to send from.
// The link is on this page once; it is stored only as a hash.
func (s *Server) showLink(w http.ResponseWriter, r *http.Request, l *auth.PasswordLink, name string) {
	url := s.linkBase(r) + "/set-password?t=" + l.Token
	p := page{
		Title: "Set-password link", LinkURL: url, LinkEmail: l.Email, LinkName: name,
		LinkExpires: l.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"),
	}
	id := auth.From(r.Context())
	if s.orgMail != nil && s.orgMail.Configured(r.Context(), id.OrgID) {
		body := fmt.Sprintf("Hello %s,\n\nUse this link to choose your password for Hearth. It works once and "+
			"expires in 24 hours:\n\n%s\n\nIf you were not expecting this, ignore this email.\n", name, url)
		if err := s.orgMail.Send(r.Context(), id.OrgID, l.Email, "Choose your Hearth password", body); err != nil {
			s.log.WarnContext(r.Context(), "password link email failed", "error", err)
		} else {
			p.LinkEmailed = true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "passwordlink", p)
}

// postPasswordLink issues a link for an existing account.
func (s *Server) postPasswordLink(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	target := r.PathValue("id")
	l, err := s.auth.IssuePasswordLink(r.Context(), id.OrgID, id.PrincipalID, target)
	switch {
	case errors.Is(err, auth.ErrNotOwner), errors.Is(err, auth.ErrIsOwner):
		s.fail(w, r, http.StatusForbidden, err.Error())
		return
	case err != nil:
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "auth: "))
		return
	}
	name := l.Email
	if list, lerr := s.auth.People(r.Context(), id.OrgID); lerr == nil {
		for _, p := range list {
			if p.PrincipalID == target && p.DisplayName != "" {
				name = p.DisplayName
			}
		}
	}
	s.showLink(w, r, l, name)
}

// getSetPassword is the page the link opens. Outside the session wall: the
// person has no session yet. The token travels in the query string, which the
// request log does not record, and the page tells browsers not to send it on.
func (s *Server) getSetPassword(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	email, err := s.auth.PeekPasswordLink(r.Context(), token)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusGone)
		s.render(w, r, "setpassword", page{Title: "Choose your password", Message: auth.ErrLinkInvalid.Error()})
		return
	}
	s.render(w, r, "setpassword", page{Title: "Choose your password", LinkEmail: email, Token: token})
}

func (s *Server) postSetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	token := r.FormValue("t")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	pw, again := r.FormValue("password"), r.FormValue("again")
	if pw != again {
		email, _ := s.auth.PeekPasswordLink(r.Context(), token)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "setpassword", page{Title: "Choose your password", LinkEmail: email, Token: token,
			Message: "The two passwords do not match."})
		return
	}
	switch err := s.auth.RedeemPasswordLink(r.Context(), token, pw); {
	case errors.Is(err, auth.ErrLinkInvalid):
		w.WriteHeader(http.StatusGone)
		s.render(w, r, "setpassword", page{Title: "Choose your password", Message: err.Error()})
	case errors.Is(err, auth.ErrWeak):
		email, _ := s.auth.PeekPasswordLink(r.Context(), token)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "setpassword", page{Title: "Choose your password", LinkEmail: email, Token: token,
			Message: err.Error()})
	case err != nil:
		s.log.ErrorContext(r.Context(), "set password by link", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not set the password")
	default:
		s.render(w, r, "setpassword", page{Title: "Choose your password", Done: true})
	}
}
