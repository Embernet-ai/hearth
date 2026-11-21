package web

// The integrations page: API tokens, webhooks, and their delivery log.

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/api"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/importer"
)

func (s *Server) getIntegrations(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	tokens, err := s.api.Tokens(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list api tokens", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the tokens")
		return
	}
	hooks, err := s.api.Webhooks(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list webhooks", "error", err)
	}
	people, err := s.assignable(r)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load people", "error", err)
	}
	provisioning, err := s.scim.Tokens(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list provisioning tokens", "error", err)
	}

	s.render(w, r, "integrations", page{
		Title: "Integrations", APITokens: tokens, Hooks: hooks, People: people,
		APIScopes: api.Scopes, ImportSources: importer.Sources,
		NewToken:   r.URL.Query().Get("token"),
		APIBase:    strings.TrimSuffix(s.publicBase(r), "/") + "/api/v1",
		SCIMTokens: provisioning,
		SCIMBase:   strings.TrimSuffix(s.publicBase(r), "/") + "/scim/v2",
	})
}

// mintAPIToken issues a credential that acts as one person.
func (s *Server) mintAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	// Defaults to the person minting it. A token acts AS somebody, and the
	// commonest case by far is that somebody is you.
	owner := strings.TrimSpace(r.FormValue("principal"))
	if owner == "" {
		owner = id.PrincipalID
	}
	var ttl time.Duration
	if days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("days"))); err == nil && days > 0 {
		ttl = time.Duration(days) * 24 * time.Hour
	}

	t, err := s.api.Mint(r.Context(), id.OrgID, owner, r.FormValue("name"),
		r.Form["scopes"], ttl)
	if err != nil {
		if errors.Is(err, api.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "api: "))
			return
		}
		s.log.ErrorContext(r.Context(), "mint api token", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not mint a token")
		return
	}
	http.Redirect(w, r, "/integrations?token="+url.QueryEscape(t.Secret), http.StatusSeeOther)
}

func (s *Server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.api.Revoke(r.Context(), id.OrgID, r.PathValue("id")); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such token")
		return
	}
	http.Redirect(w, r, "/integrations", http.StatusSeeOther)
}

func (s *Server) saveWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	h := api.Webhook{
		ID:  strings.TrimSpace(r.FormValue("id")),
		URL: r.FormValue("url"), Events: r.Form["events"],
		Enabled: r.FormValue("enabled") != "off",
	}
	if _, err := s.api.SaveWebhook(r.Context(), id.OrgID, id.PrincipalID, h); err != nil {
		if errors.Is(err, api.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "api: "))
			return
		}
		s.log.ErrorContext(r.Context(), "save webhook", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the webhook")
		return
	}
	http.Redirect(w, r, "/integrations", http.StatusSeeOther)
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.api.DeleteWebhook(r.Context(), id.OrgID, r.PathValue("id")); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such webhook")
		return
	}
	http.Redirect(w, r, "/integrations", http.StatusSeeOther)
}
