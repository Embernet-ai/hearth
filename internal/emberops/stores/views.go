package stores

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/views"
)

// Views keeps saved views as Hearth View records.
type Views struct {
	Base
	Dir Directory
}

var _ views.Store = (*Views)(nil)

const viewDoctype = "Hearth View"

var viewFields = []string{"name", "hearth_id", "view_name", "view_user", "view_kind",
	"hearth_space", "shared", "query", "creation", "modified"}

// Save creates or replaces a view by name for one person, as the Postgres
// store does: saving the same name again updates it.
func (s *Views) Save(ctx context.Context, orgID, ownerID string, v views.View) (*views.View, error) {
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return nil, views.ErrName
	}
	if v.Kind == "" {
		v.Kind = docs.ViewTable
	}
	if !docs.ValidViewKind(v.Kind) {
		return nil, fmt.Errorf("%w: %q", views.ErrKind, v.Kind)
	}
	payload, err := json.Marshal(v.Query)
	if err != nil {
		return nil, fmt.Errorf("views: encode query: %w", err)
	}

	// An existing view of this person's with this name keeps its id.
	id := ""
	owners, err := s.Dir.Emails(ctx)
	if err != nil {
		return nil, err
	}
	if email := owners[ownerID]; email != "" {
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: viewDoctype, Fields: []string{"hearth_id"}, Limit: 1,
			Filters: []emberops.Filter{emberops.F("view_user", email), emberops.F("view_name", v.Name)}})
		if err != nil {
			return nil, err
		}
		if len(rows) == 1 {
			id = rows[0].S("hearth_id")
		}
	}
	if id == "" {
		id = uuid.NewString()
	}
	vals := map[string]any{
		"view_name": v.Name, "view_user": emberops.Ref(ownerID), "view_kind": v.Kind,
		"hearth_space": emberops.Ref(deref(v.SpaceID)), "shared": b2i(v.Shared), "query": string(payload),
	}
	if _, err := s.C.Upsert(ctx, viewDoctype, id, vals); err != nil {
		return nil, err
	}
	return s.Get(ctx, orgID, ownerID, id)
}

// Get returns a view the caller may open. EmberOPS filters by the caller's own
// permissions, so a view that is neither theirs nor shared is simply not found.
func (s *Views) Get(ctx context.Context, orgID, principalID, id string) (*views.View, error) {
	row, err := s.C.GetByHearthID(ctx, viewDoctype, id)
	if err != nil {
		return nil, notFound(err, views.ErrNotFound)
	}
	owners, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	spaces, err := s.Dir.HearthIDs(ctx, "Hearth Space", []string{row.S("hearth_space")})
	if err != nil {
		return nil, err
	}
	v, err := s.view(orgID, row, owners, spaces)
	if err != nil {
		return nil, err
	}
	if v.OwnerID != principalID && !v.Shared {
		return nil, views.ErrNotFound
	}
	return v, nil
}

// List returns the caller's own views and everything shared, their own first.
// One list call and one directory read, however many views there are.
func (s *Views) List(ctx context.Context, orgID, principalID string) ([]*views.View, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: viewDoctype, Fields: viewFields, OrderBy: "view_name asc"}, 0)
	if err != nil {
		return nil, err
	}
	owners, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	var spaceNames []string
	for _, r := range rows {
		spaceNames = append(spaceNames, r.S("hearth_space"))
	}
	spaces, err := s.Dir.HearthIDs(ctx, "Hearth Space", spaceNames)
	if err != nil {
		return nil, err
	}
	var mine, theirs []*views.View
	for _, r := range rows {
		v, err := s.view(orgID, r, owners, spaces)
		if err != nil {
			return nil, err
		}
		switch {
		case v.OwnerID == principalID:
			mine = append(mine, v)
		case v.Shared:
			theirs = append(theirs, v)
		}
	}
	return append(mine, theirs...), nil
}

// Delete removes a view; only its owner can.
func (s *Views) Delete(ctx context.Context, orgID, principalID, id string) error {
	v, err := s.Get(ctx, orgID, principalID, id)
	if err != nil {
		return err
	}
	if v.OwnerID != principalID {
		return views.ErrNotFound
	}
	return notFound(s.C.DeleteByHearthID(ctx, viewDoctype, id), views.ErrNotFound)
}

func (s *Views) view(orgID string, r emberops.Row, owners, spaces map[string]string) (*views.View, error) {
	v := &views.View{
		ID: r.S("hearth_id"), OrgID: orgID, OwnerID: owners[r.S("view_user")],
		Name: r.S("view_name"), Kind: r.S("view_kind"), Shared: r.B("shared"),
		CreatedAt: r.T("creation", s.loc()), UpdatedAt: r.T("modified", s.loc()),
	}
	if sp := spaces[r.S("hearth_space")]; sp != "" {
		v.SpaceID = &sp
	}
	if q := r.S("query"); q != "" {
		if err := json.Unmarshal([]byte(q), &v.Query); err != nil {
			return nil, fmt.Errorf("views: decode query for %s: %w", v.ID, err)
		}
	}
	return v, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
