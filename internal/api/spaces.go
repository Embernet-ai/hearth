package api

// Spaces over the API: list the ones the token's person can see, and make one. Making a top-level space is a space
// admin's or a super admin's, and a folder inside a space is its manager's, exactly as on the Spaces page, because the call
// is the page's own (plant.Store.CreateSpace). A space that already has the name is returned, so a tool can ask for its
// space on every run.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// SpaceStore is the slice of the plant service the space routes reach.
type SpaceStore interface {
	CreateSpace(ctx context.Context, orgID string, actor core.Subjects, name, kind string, parentID *string) (*plant.Space, error)
	SpaceTree(ctx context.Context, orgID string, actor core.Subjects) ([]*plant.Space, error)
}

// WithSpaces switches the space routes on. Nil leaves them answering 404.
func (s *Service) WithSpaces(st SpaceStore) *Service {
	s.spaces = st
	return s
}

func (s *Service) spaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/spaces", s.listSpaces)
	mux.HandleFunc("POST /v1/spaces", s.createSpace)
}

func renderSpace(sp *plant.Space) map[string]any {
	out := map[string]any{"id": sp.ID, "name": sp.Name, "kind": sp.Kind, "path": sp.Path, "depth": sp.Depth, "documents": sp.Documents}
	if sp.ParentID != nil {
		out["parent_id"] = *sp.ParentID
	}
	return out
}

func (s *Service) listSpaces(w http.ResponseWriter, r *http.Request) {
	if s.spaces == nil {
		writeErr(w, http.StatusNotFound, "no such route")
		return
	}
	c := from(r)
	tree, err := s.spaces.SpaceTree(r.Context(), c.OrgID, c.Subjects)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list")
		return
	}
	out := make([]map[string]any, 0, len(tree))
	for _, sp := range tree {
		out = append(out, renderSpace(sp))
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": out})
}

func (s *Service) createSpace(w http.ResponseWriter, r *http.Request) {
	if s.spaces == nil {
		writeErr(w, http.StatusNotFound, "no such route")
		return
	}
	c := from(r)
	var body struct {
		Name   string  `json:"name"`
		Kind   string  `json:"kind"`
		Parent *string `json:"parent_id"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "a space needs a name")
		return
	}
	sp, err := s.spaces.CreateSpace(r.Context(), c.OrgID, c.Subjects, body.Name, body.Kind, body.Parent)
	switch {
	case errors.Is(err, plant.ErrDenied):
		writeErr(w, http.StatusForbidden, "you cannot add a space there")
		return
	case errors.Is(err, plant.ErrInvalid), errors.Is(err, plant.ErrNotFound):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "plant: "))
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not make the space")
		return
	}
	writeJSON(w, http.StatusCreated, renderSpace(sp))
}
