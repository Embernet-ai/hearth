package web

// Automations. Owner only, because a rule acts on everybody's work.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/rules"
)

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	list, err := s.rules.List(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list rules", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the rules")
		return
	}
	runs, err := s.rules.Runs(r.Context(), id.OrgID, r.URL.Query().Get("rule"), 50)
	if err != nil {
		s.log.ErrorContext(r.Context(), "rule runs", "error", err)
	}
	people, err := s.docs.Assignable(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load people", "error", err)
	}

	s.render(w, r, "rules", page{
		Title: "Automations", IsAdmin: true,
		Rules: list, RuleRuns: runs, People: people,
		Statuses: docs.Statuses,
	})
}

func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	rule := rules.Rule{
		ID:      r.FormValue("id"),
		Name:    r.FormValue("name"),
		Trigger: rules.Trigger{Event: r.FormValue("event"), Kind: r.FormValue("kind")},
	}
	// One condition and one action from the form. The stored shape holds any
	// number, so a second control is a template change rather than a schema
	// one, and the first version of a rules interface that asks for six of
	// each is a form nobody finishes.
	if field := r.FormValue("cond_field"); field != "" {
		rule.Conditions = append(rule.Conditions, rules.Condition{
			Field:  docs.ParseField(field),
			Op:     orDefaultString(r.FormValue("cond_op"), docs.OpIs),
			Values: []string{r.FormValue("cond_value")},
		})
	}
	action := rules.Action{
		Kind:        r.FormValue("action"),
		Value:       r.FormValue("action_value"),
		Text:        r.FormValue("action_text"),
		PrincipalID: r.FormValue("action_principal"),
		Title:       r.FormValue("action_title"),
		URL:         r.FormValue("action_url"),
		Secret:      r.FormValue("action_secret"),
	}
	if f := r.FormValue("action_field"); f != "" {
		action.Field = docs.ParseField(f)
	}
	rule.Actions = []rules.Action{action}

	if _, err := s.rules.Save(r.Context(), id.OrgID, id.PrincipalID, rule); err != nil {
		list, _ := s.rules.List(r.Context(), id.OrgID)
		people, _ := s.docs.Assignable(r.Context(), id.OrgID)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "rules", page{
			Title: "Automations", IsAdmin: true, Rules: list, People: people,
			Statuses: docs.Statuses,
			Message:  strings.TrimPrefix(err.Error(), "rules: "),
		})
		return
	}
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}

func orDefaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func (s *Server) toggleRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.rules.SetEnabled(r.Context(), id.OrgID, r.PathValue("id"), r.FormValue("on") == "1")
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such rule")
		return
	}
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.rules.Delete(r.Context(), id.OrgID, r.PathValue("id")); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such rule")
		return
	}
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}

// dryRunRule replays a rule against real history and reports what it would
// have done.
//
// Against history rather than invented input, because the useful question is
// not "does this work" but "how often would this have fired last week, and on
// what". A rule that looks sensible and would have touched four hundred work
// orders is one nobody should enable.
func (s *Server) dryRunRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	if _, err := s.engine.DryRun(r.Context(), id.OrgID, r.PathValue("id"), 100); err != nil {
		if errors.Is(err, rules.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "no such rule")
			return
		}
		s.log.ErrorContext(r.Context(), "dry run", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "the dry run did not finish")
		return
	}
	http.Redirect(w, r, "/rules?rule="+r.PathValue("id"), http.StatusSeeOther)
}
