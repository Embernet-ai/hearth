package web

// The governance pages: what happened, who is in which team, and what the
// directory is allowed to push.
//
// Owner only, all of them. The audit log names what everybody has done and team
// membership decides what everybody can see, and neither is a thing to leave
// open to whoever has a session.

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/scim"
	"github.com/Embernet-ai/hearth/internal/teams"
)

// ---------------------------------------------------------------------------
// The audit log
// ---------------------------------------------------------------------------

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	q := r.URL.Query()

	f := audit.Filter{
		Action:   strings.TrimSpace(q.Get("action")),
		ActorID:  strings.TrimSpace(q.Get("actor")),
		EntityID: strings.TrimSpace(q.Get("entity")),
		Limit:    100,
	}
	// A uuid or nothing. A malformed one reaching the query is a database error
	// on a page, and the honest answer to "actor=banana" is the unfiltered log
	// rather than a stack trace.
	if !isUUID(f.ActorID) {
		f.ActorID = ""
	}
	if !isUUID(f.EntityID) {
		f.EntityID = ""
	}
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
			f.Since = &t
		}
	}
	if v := strings.TrimSpace(q.Get("before")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.Before = n
		}
	}

	rows, err := s.audit.List(r.Context(), id.OrgID, f)
	if err != nil {
		s.log.ErrorContext(r.Context(), "read audit log", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the log")
		return
	}
	kinds, err := s.audit.Actions(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "read audit actions", "error", err)
	}
	total, err := s.audit.Count(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "count audit log", "error", err)
	}

	p := page{
		Title: "Audit", Heading: "Audit log", IsAdmin: true,
		Audit: rows, AuditKinds: kinds, AuditCount: total, AuditFilter: f,
	}
	// Paging by id rather than by offset, so reading to the end of a long log
	// does not skip rows as new ones arrive at the top.
	if len(rows) == f.Limit {
		p.AuditMore = rows[len(rows)-1].ID
	}
	s.render(w, r, "audit", p)
}

// ---------------------------------------------------------------------------
// Teams
// ---------------------------------------------------------------------------

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	all, err := s.teams.List(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list teams", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the teams")
		return
	}
	people, err := s.auth.People(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list people", "error", err)
	}
	s.render(w, r, "teams", page{
		Title: "Teams", Heading: "Teams", IsAdmin: true,
		Teams: all, Persons: people,
	})
}

func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	team, err := s.teams.Get(r.Context(), id.OrgID, r.PathValue("id"))
	if errors.Is(err, teams.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such team")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "load team", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the team")
		return
	}
	members, err := s.teams.Members(r.Context(), id.OrgID, team.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load team members", "error", err)
	}
	people, err := s.auth.People(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list people", "error", err)
	}
	// What this team can already see, asked as the team rather than as the
	// person reading the page. A subject set of exactly one team subject is
	// what makes this the team's own answer: anything in the list is in it
	// because of the team, not because the owner can see everything.
	visible, err := s.docs.List(r.Context(), id.OrgID,
		core.Subjects{team.Subject()}, docs.ListParams{Limit: 50})
	if err != nil {
		s.log.ErrorContext(r.Context(), "list team documents", "error", err)
	}
	// And everything the owner could share with them, for the picker.
	mine, err := s.docs.List(r.Context(), id.OrgID, id.Subjects, docs.ListParams{Limit: 200})
	if err != nil {
		s.log.ErrorContext(r.Context(), "list documents", "error", err)
	}
	s.render(w, r, "team", page{
		Title: team.Name, Heading: team.Name, IsAdmin: true,
		Team: team, TeamMembers: members, Persons: people,
		Documents: visible, Assignable: mine,
	})
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	_, err := s.teams.Create(r.Context(), id.OrgID, id.PrincipalID,
		r.FormValue("name"), r.FormValue("description"))
	if errors.Is(err, teams.ErrInvalid) {
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "teams: "))
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "create team", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not create the team")
		return
	}
	http.Redirect(w, r, "/teams", http.StatusSeeOther)
}

func (s *Server) renameTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.teams.Rename(r.Context(), id.OrgID, id.PrincipalID,
		r.PathValue("id"), r.FormValue("name"))
	if s.teamProblem(w, r, err) {
		return
	}
	http.Redirect(w, r, "/teams/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	err := s.teams.Delete(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	if s.teamProblem(w, r, err) {
		return
	}
	http.Redirect(w, r, "/teams", http.StatusSeeOther)
}

func (s *Server) addTeamMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.teams.Add(r.Context(), id.OrgID, id.PrincipalID,
		r.PathValue("id"), strings.TrimSpace(r.FormValue("principal")))
	if s.teamProblem(w, r, err) {
		return
	}
	http.Redirect(w, r, "/teams/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	err := s.teams.Remove(r.Context(), id.OrgID, id.PrincipalID,
		r.PathValue("id"), r.PathValue("principal"))
	if s.teamProblem(w, r, err) {
		return
	}
	http.Redirect(w, r, "/teams/"+r.PathValue("id"), http.StatusSeeOther)
}

// grantTeam gives a team access to something.
//
// The same authz call a person's grant goes through, with a team subject
// instead of a principal one. That is the entire feature, and it is why teams
// took two tables rather than a permission system.
func (s *Server) grantTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	team, err := s.teams.Get(r.Context(), id.OrgID, r.PathValue("id"))
	if s.teamProblem(w, r, err) {
		return
	}

	target := strings.TrimSpace(r.FormValue("document"))
	if !isUUID(target) {
		s.fail(w, r, http.StatusBadRequest, "that is not a document id")
		return
	}
	level := core.AccessLevel(strings.TrimSpace(r.FormValue("level")))
	if !level.Valid() {
		s.fail(w, r, http.StatusBadRequest, "that is not an access level")
		return
	}

	// The caller has to be able to administer the thing they are sharing.
	// Being the owner is not the same as being allowed to grant on somebody
	// else's private document, and the docs service is where that is decided.
	err = s.docs.Share(r.Context(), id.OrgID, id.Subjects, target,
		team.Subject(), level, id.PrincipalID)
	switch {
	case errors.Is(err, docs.ErrNotFound), errors.Is(err, authz.ErrDenied):
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "grant to team", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not share that")
		return
	}
	http.Redirect(w, r, "/teams/"+team.ID, http.StatusSeeOther)
}

func (s *Server) teamProblem(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, teams.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such team")
	case errors.Is(err, teams.ErrManaged):
		s.fail(w, r, http.StatusBadRequest,
			"that team mirrors a group in your directory, so it is changed there")
	case errors.Is(err, teams.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "teams: "))
	default:
		s.log.ErrorContext(r.Context(), "team", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "that did not work")
	}
	return true
}

// ---------------------------------------------------------------------------
// Provisioning tokens
// ---------------------------------------------------------------------------

func (s *Server) mintSCIMToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	secret, err := s.scim.Mint(r.Context(), id.OrgID, id.PrincipalID, r.FormValue("name"))
	if err != nil {
		s.log.ErrorContext(r.Context(), "mint provisioning token", "error", err)
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "scim: "))
		return
	}
	// Shown once, on the page that asked for it. There is nowhere to read it
	// back from, which is the point.
	http.Redirect(w, r, "/integrations?token="+url.QueryEscape(secret), http.StatusSeeOther)
}

func (s *Server) revokeSCIMToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	err := s.scim.Revoke(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	if errors.Is(err, scim.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such token")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "revoke provisioning token", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not revoke it")
		return
	}
	http.Redirect(w, r, "/integrations", http.StatusSeeOther)
}

func isUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
