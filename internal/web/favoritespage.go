package web

import (
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

// FavoriteItem is one starred record as the favorites page lists it.
type FavoriteItem struct {
	Kind, Name, Detail, Href string
}

// markFavorite says whether the record on this page is one the person has starred.
func (s *Server) markFavorite(r *http.Request, ref core.Ref, p *page) {
	if s.sales == nil {
		return
	}
	id := auth.From(r.Context())
	p.FavoriteType = string(ref.Type)
	on, err := s.sales.IsFavorite(r.Context(), id.OrgID, id.Subjects, ref)
	if err != nil {
		s.log.WarnContext(r.Context(), "is favorite", "error", err)
	}
	p.IsFavorite = on
}

// setFavorite stars or unstars a record for the person asking, and goes back to the page they were on.
func (s *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ref := core.Ref{Type: core.EntityType(r.FormValue("type")), ID: r.FormValue("id")}
	if err := s.sales.SetFavorite(r.Context(), id.OrgID, id.Subjects, ref, r.FormValue("on") == "1"); err != nil {
		s.crmFail(w, r, err, "you cannot star that")
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/crm/favorites"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// getFavorites lists the person's starred companies, contacts and deals.
func (s *Server) getFavorites(w http.ResponseWriter, r *http.Request) {
	if !s.salesOn(w, r) {
		return
	}
	id := auth.From(r.Context())
	favs, err := s.sales.Favorites(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list favorites", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load your favorites")
		return
	}
	var items []FavoriteItem
	for _, f := range favs {
		switch f.Ref.Type {
		case core.EntityCompany:
			if c, err := s.crm.GetCompany(r.Context(), id.OrgID, id.Subjects, f.Ref.ID); err == nil {
				items = append(items, FavoriteItem{Kind: "Company", Name: c.Name, Detail: c.Kind, Href: "/companies/" + c.ID})
			}
		case core.EntityContact:
			if c, err := s.crm.GetContact(r.Context(), id.OrgID, id.Subjects, f.Ref.ID); err == nil {
				items = append(items, FavoriteItem{Kind: "Contact", Name: c.Name, Detail: c.CompanyName, Href: "/contacts/" + c.ID})
			}
		case core.EntityDeal:
			if d, err := s.sales.GetDeal(r.Context(), id.OrgID, id.Subjects, f.Ref.ID); err == nil {
				items = append(items, FavoriteItem{Kind: "Deal", Name: d.Name, Detail: crm.StageLabel(d.Stage) + " " + d.Amount(), Href: "/deals/" + d.ID})
			}
		}
	}
	s.render(w, r, "crmfavorites", page{Title: "Favorites", SalesOn: true, FavoriteItems: items})
}
