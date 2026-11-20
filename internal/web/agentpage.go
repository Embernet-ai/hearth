package web

// Agents, the MCP endpoint, and the canvas.
//
// The MCP endpoint is the second place on this application a machine talks to
// rather than a person, and like the first it sits outside the session
// middleware and authenticates with a bearer token.

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/canvas"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// -----------------------------------------------------------------------------
// Managing agents
// -----------------------------------------------------------------------------

func (s *Server) getAgents(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	list, err := s.agents.List(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list agents", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the agents")
		return
	}
	tokens, err := s.agents.Tokens(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list tokens", "error", err)
	}
	chans, err := s.channels.List(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load channels", "error", err)
	}
	pending, err := s.agents.Pending(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list suggestions", "error", err)
	}
	recent, err := s.agents.Recent(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list decided", "error", err)
	}

	s.render(w, r, "agents", page{
		Title: "Agents", Agents: list, AgentTokens: tokens, Channels: chans,
		Suggestions: pending, Decided: recent, Scopes: agents.Scopes,
		// The token minted by the previous request, shown once. It is carried
		// in a query parameter rather than a session, because a secret in a
		// session outlives the page it was meant for.
		NewToken: r.URL.Query().Get("token"),
		MCPURL:   strings.TrimSuffix(s.publicBase(r), "/") + "/mcp",
	})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	a, err := s.agents.Create(r.Context(), id.OrgID, id.PrincipalID,
		r.FormValue("name"), r.FormValue("handle"))
	if err != nil {
		s.agentFail(w, r, err, "could not create the agent")
		return
	}
	// Into a channel, if one was chosen. An agent shares what it makes the way
	// it is shared itself, so this is the difference between an agent whose
	// output the crew can see and one whose output nobody can.
	if ch := strings.TrimSpace(r.FormValue("channel")); ch != "" {
		if err := s.channels.Invite(r.Context(), id.OrgID, id.PrincipalID, ch, a.ID); err != nil {
			s.log.ErrorContext(r.Context(), "put the agent in a channel", "error", err)
		}
	}
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

func (s *Server) setAgentStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.agents.SetStatus(r.Context(), id.OrgID, r.PathValue("id"),
		r.FormValue("status")); err != nil {
		s.agentFail(w, r, err, "could not change the agent")
		return
	}
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

// mintToken issues a credential and shows it once.
func (s *Server) mintToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var ttl time.Duration
	if days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("days"))); err == nil && days > 0 {
		ttl = time.Duration(days) * 24 * time.Hour
	}

	t, err := s.agents.Mint(r.Context(), id.OrgID, id.PrincipalID,
		r.FormValue("agent"), r.FormValue("name"), r.Form["scopes"], ttl)
	if err != nil {
		s.agentFail(w, r, err, "could not mint a token")
		return
	}
	// Once, in the URL of the redirect, and never again. A token readable from
	// the interface is a token that leaks through a screen share.
	http.Redirect(w, r, "/agents?token="+url.QueryEscape(t.Secret), http.StatusSeeOther)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.agents.Revoke(r.Context(), id.OrgID, r.PathValue("id")); err != nil {
		s.agentFail(w, r, err, "could not revoke that token")
		return
	}
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Deciding suggestions
// -----------------------------------------------------------------------------

// decideSuggestion is where an agent's proposal becomes a change, or does not.
//
// The write happens HERE, as the person who accepted it, through the ordinary
// service call with their subject set. That is deliberate: accepting a
// suggestion is a decision somebody made, the permission that matters is
// theirs, and the history says an agent proposed it and a person did it.
func (s *Server) decideSuggestion(w http.ResponseWriter, r *http.Request) {
	// An agent's suggestion changes work in the organisation's name, so deciding one
	// is an admin's act, like minting an agent's token on this same page. Without this
	// anybody signed in could accept or reject somebody else's suggestion.
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	suggestionID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	g, err := s.agents.Get(r.Context(), id.OrgID, suggestionID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such suggestion")
		return
	}
	if r.FormValue("decision") != "accept" {
		if err := s.agents.Decide(r.Context(), id.OrgID, suggestionID,
			id.PrincipalID, "rejected", ""); err != nil {
			s.fail(w, r, http.StatusConflict, "that has already been decided")
			return
		}
		http.Redirect(w, r, backTo(r, "/agents"), http.StatusSeeOther)
		return
	}

	result, err := s.applySuggestion(r, id, g)
	status := "accepted"
	if err != nil {
		// Recorded as failed rather than left pending. A suggestion that
		// silently stays in the queue after somebody pressed accept is one
		// they press again.
		status, result = "failed", err.Error()
	}
	if derr := s.agents.Decide(r.Context(), id.OrgID, suggestionID,
		id.PrincipalID, status, result); derr != nil {
		s.fail(w, r, http.StatusConflict, "that has already been decided")
		return
	}
	if err != nil {
		s.log.WarnContext(r.Context(), "suggestion failed", "id", suggestionID, "error", err)
		s.fail(w, r, http.StatusBadRequest, "could not apply it: "+result)
		return
	}
	http.Redirect(w, r, backTo(r, "/agents"), http.StatusSeeOther)
}

// applySuggestion performs the accepted write as the person who accepted it.
func (s *Server) applySuggestion(r *http.Request, id *auth.Identity, g *agents.Suggestion) (string, error) {
	ctx := r.Context()
	get := func(key string) string {
		v, _ := g.Payload[key].(string)
		return v
	}

	switch g.Action {
	case agents.ActionCreate:
		kind := get("kind")
		if kind == "" {
			kind = docs.KindTask
		}
		p := docs.CreateParams{Kind: kind, Title: get("title"), Body: get("body")}
		if docs.HasTaskFields(kind) {
			p.Task = &docs.TaskFields{Status: docs.StatusTodo}
		}
		d, err := s.docs.Create(ctx, id.OrgID, id.Subjects, p)
		if err != nil {
			return "", err
		}
		// Attributed to the agent as well as to the person, so the history
		// answers "who suggested this" a year later.
		if err := s.channels.PostAsSystem(ctx, id.OrgID, id.PrincipalID, d.Ref(),
			"Created from a suggestion by "+g.AgentName+
				noteReason(g.Reason)); err != nil {
			s.log.ErrorContext(ctx, "note the suggestion", "error", err)
		}
		return "created " + d.ID, nil

	case agents.ActionSetStatus, agents.ActionAssign:
		if g.EntityID == nil {
			return "", errors.New("the suggestion says nothing to change")
		}
		current, err := s.docs.Get(ctx, id.OrgID, id.Subjects, *g.EntityID)
		if err != nil {
			return "", err
		}
		p := docs.UpdateParams{ExpectedVersion: current.Version}
		if v := get("title"); v != "" {
			p.Title = &v
		}
		if v := get("status"); v != "" {
			p.Task = &docs.TaskPatch{Status: &v}
		}
		if _, err := s.docs.Update(ctx, id.OrgID, id.Subjects, *g.EntityID, p); err != nil {
			return "", err
		}
		if err := s.channels.PostAsSystem(ctx, id.OrgID, id.PrincipalID,
			core.Ref{Type: core.EntityDocument, ID: *g.EntityID},
			"Applied a suggestion by "+g.AgentName+noteReason(g.Reason)); err != nil {
			s.log.ErrorContext(ctx, "note the suggestion", "error", err)
		}
		return "updated " + *g.EntityID, nil

	case agents.ActionComment:
		if g.EntityID == nil {
			return "", errors.New("the suggestion says nothing to comment on")
		}
		body := get("body")
		if body == "" {
			return "", errors.New("the suggestion has no text")
		}
		if err := s.channels.PostAsSystem(ctx, id.OrgID, id.PrincipalID,
			core.Ref{Type: core.EntityDocument, ID: *g.EntityID},
			body+"\n\n(suggested by "+g.AgentName+")"); err != nil {
			return "", err
		}
		return "commented", nil

	default:
		return "", errors.New("nothing here knows how to do that")
	}
}

func noteReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "."
	}
	return ": " + reason
}

func (s *Server) agentFail(w http.ResponseWriter, r *http.Request, err error, msg string) {
	switch {
	case errors.Is(err, agents.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "agents: "))
	case errors.Is(err, agents.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such thing")
	default:
		s.log.ErrorContext(r.Context(), "agents", "error", err)
		s.fail(w, r, http.StatusInternalServerError, msg)
	}
}

// -----------------------------------------------------------------------------
// Canvas
// -----------------------------------------------------------------------------

func (s *Server) listCanvases(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	list, err := s.canvas.List(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list canvases", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the canvases")
		return
	}
	spaces, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load spaces", "error", err)
	}
	s.render(w, r, "canvases", page{Title: "Canvas", Canvases: list, Spaces: spaces})
}

func (s *Server) createCanvas(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var space *string
	if v := strings.TrimSpace(r.FormValue("space")); v != "" {
		space = &v
	}
	c, err := s.canvas.Create(r.Context(), id.OrgID, id.Subjects, r.FormValue("name"), space)
	if err != nil {
		s.canvasFail(w, r, err, "could not create the canvas")
		return
	}
	http.Redirect(w, r, "/canvas/"+c.ID, http.StatusSeeOther)
}

func (s *Server) getCanvas(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	canvasID := r.PathValue("id")

	c, err := s.canvas.Get(r.Context(), id.OrgID, id.Subjects, canvasID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such canvas")
		return
	}
	nodes, err := s.canvas.Nodes(r.Context(), id.OrgID, id.Subjects, canvasID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load nodes", "error", err)
	}
	edges, err := s.canvas.Edges(r.Context(), id.OrgID, id.Subjects, canvasID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load edges", "error", err)
	}
	candidates, err := s.docs.List(r.Context(), id.OrgID, id.Subjects, docs.ListParams{Limit: 200})
	if err != nil {
		s.log.ErrorContext(r.Context(), "load candidates", "error", err)
	}

	s.render(w, r, "canvas", page{
		Title: c.Name, Canvas: c, Nodes: nodes, Edges: edges, Assignable: candidates,
	})
}

func (s *Server) deleteCanvas(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.canvas.Delete(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.canvasFail(w, r, err, "could not remove the canvas")
		return
	}
	http.Redirect(w, r, "/canvas", http.StatusSeeOther)
}

func (s *Server) addCanvasNode(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	canvasID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	n := canvas.Node{
		Text: r.FormValue("text"),
		X:    atoiOr(r.FormValue("x"), 40), Y: atoiOr(r.FormValue("y"), 40),
	}
	if v := strings.TrimSpace(r.FormValue("entity")); v != "" {
		t := string(core.EntityDocument)
		n.EntityID, n.EntityType = &v, &t
	}
	if _, err := s.canvas.AddNode(r.Context(), id.OrgID, id.Subjects, canvasID, n); err != nil {
		s.canvasFail(w, r, err, "could not add that card")
		return
	}
	http.Redirect(w, r, "/canvas/"+canvasID, http.StatusSeeOther)
}

func (s *Server) moveCanvasNode(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	canvasID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.canvas.MoveNode(r.Context(), id.OrgID, id.Subjects, canvasID, r.PathValue("node"),
		atoiOr(r.FormValue("x"), 0), atoiOr(r.FormValue("y"), 0),
		atoiOr(r.FormValue("w"), 220), atoiOr(r.FormValue("h"), 120))
	if err != nil {
		s.canvasFail(w, r, err, "could not move that card")
		return
	}
	// Dragged by script, so the answer is a status rather than a page.
	if r.FormValue("quiet") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/canvas/"+canvasID, http.StatusSeeOther)
}

func (s *Server) removeCanvasNode(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	canvasID := r.PathValue("id")
	if err := s.canvas.RemoveNode(r.Context(), id.OrgID, id.Subjects, canvasID,
		r.PathValue("node")); err != nil {
		s.canvasFail(w, r, err, "could not remove that card")
		return
	}
	http.Redirect(w, r, "/canvas/"+canvasID, http.StatusSeeOther)
}

func (s *Server) addCanvasEdge(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	canvasID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.canvas.AddEdge(r.Context(), id.OrgID, id.Subjects, canvasID,
		r.FormValue("from"), r.FormValue("to"), r.FormValue("label")); err != nil {
		s.canvasFail(w, r, err, "could not draw that line")
		return
	}
	http.Redirect(w, r, "/canvas/"+canvasID, http.StatusSeeOther)
}

func (s *Server) removeCanvasEdge(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	canvasID := r.PathValue("id")
	if err := s.canvas.RemoveEdge(r.Context(), id.OrgID, id.Subjects, canvasID,
		r.PathValue("edge")); err != nil {
		s.canvasFail(w, r, err, "could not remove that line")
		return
	}
	http.Redirect(w, r, "/canvas/"+canvasID, http.StatusSeeOther)
}

func (s *Server) canvasFail(w http.ResponseWriter, r *http.Request, err error, msg string) {
	switch {
	case errors.Is(err, canvas.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "canvas: "))
	case errors.Is(err, canvas.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such canvas")
	default:
		s.log.WarnContext(r.Context(), "canvas refused", "error", err)
		s.fail(w, r, http.StatusForbidden, msg)
	}
}

func atoiOr(v string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}
