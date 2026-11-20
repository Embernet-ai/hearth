package web

// One search box.
//
// It answers from the better engine when there is one and from Postgres when
// there is not, and it SAYS which. When the cluster is down the results get
// narrower, and somebody looking at a shorter list than they expected deserves
// to know why rather than concluding their document has been deleted.

import (
	"github.com/Embernet-ai/hearth/internal/core"
	"html/template"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/search"
)

func (s *Server) getSearch(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")

	p := page{Title: "Search", Query: docs.Query{Kind: kind}, SearchText: text, Kind: kind}

	if text == "" {
		s.render(w, r, "search", p)
		return
	}

	res, err := s.search.Search(r.Context(), id.OrgID, id.Subjects, search.Query{
		Text: text, Kind: kind,
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "search failed", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "search could not answer")
		return
	}

	// The primary engine failing is not an error for the person searching, but
	// it is one for whoever runs this, so it goes in the log with the fact that
	// the fallback answered.
	if primary := s.search.Primary(); primary != nil && res.Engine != primary.Name() {
		s.log.WarnContext(r.Context(), "search fell back to postgres",
			"wanted", primary.Name(), "used", res.Engine)
	}

	p.Results = res
	p.Hits = renderHits(res.Hits)
	s.render(w, r, "search", p)
}

// RenderedHit is a hit with its snippet already marked safe.
type RenderedHit struct {
	search.Hit
	// HTML is the snippet with its <mark> tags intact. Both engines escape the
	// text and insert the marks themselves, which is why this is the one place
	// raw HTML is trusted: it was produced by Postgres or by OpenSearch, not by
	// a person typing into a field.
	HTML template.HTML
	// Href is where clicking goes. A file leads to the document it hangs off,
	// because a file has no page of its own and its permissions live there.
	Href string
}

func renderHits(hits []search.Hit) []RenderedHit {
	out := make([]RenderedHit, 0, len(hits))
	for _, h := range hits {
		rh := RenderedHit{Hit: h, HTML: template.HTML(h.Snippet)}
		// Where a hit takes you. Everything used to be a document, and sending
		// a contact to /documents/<id> is a search that finds people and then
		// four-oh-fours on them, which is worse than not finding them.
		switch {
		case h.EntityType == string(core.EntityFile) && h.ParentID != "":
			rh.Href = "/documents/" + h.ParentID
		case h.EntityType == string(core.EntityContact):
			rh.Href = "/contacts/" + h.EntityID
		case h.EntityType == string(core.EntityCompany):
			rh.Href = "/companies/" + h.EntityID
		case h.EntityType == string(core.EntityDeal):
			rh.Href = "/deals/" + h.EntityID
		case h.EntityType == search.EntityActivity:
			// A note or a call has no page; it lives on the timeline of the record it is about.
			rh.Href = "/search"
			if kind, id, ok := strings.Cut(h.ParentID, ":"); ok {
				rh.Href = map[string]string{"company": "/companies/", "contact": "/contacts/", "deal": "/deals/"}[kind] + id + "#timeline"
			}
		default:
			rh.Href = "/documents/" + h.EntityID
		}
		out = append(out, rh)
	}
	return out
}
