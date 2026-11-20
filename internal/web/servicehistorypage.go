package web

// Service history: a customer's work, day by day, as a page and as a PDF or CSV
// to send them. The customer is a department in the space tree, so the picker
// is the same department picker the daily reports use.

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/servicereport"
)

func serviceFilterFrom(r *http.Request) docs.ServiceFilter {
	q := r.URL.Query()
	f := docs.ServiceFilter{SpaceID: q.Get("space")}
	if uuid.Validate(f.SpaceID) != nil {
		f.SpaceID = ""
	}
	valid := func(v string) string {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return ""
		}
		return v
	}
	f.From, f.To = valid(q.Get("from")), valid(q.Get("to"))
	if f.From != "" && f.To != "" && f.From > f.To {
		f.From, f.To = f.To, f.From
	}
	return f
}

func (s *Server) serviceTitle(r *http.Request, id *auth.Identity, f docs.ServiceFilter) (name, sub string) {
	name = "Service history"
	if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
		for _, sp := range tree {
			if sp.ID == f.SpaceID {
				name = sp.Name
				sub = sp.Path
			}
		}
	}
	rng := "all dates"
	switch {
	case f.From != "" && f.To != "":
		rng = f.From + " to " + f.To
	case f.From != "":
		rng = "from " + f.From
	case f.To != "":
		rng = "through " + f.To
	}
	if sub != "" {
		sub += ", " + rng
	} else {
		sub = rng
	}
	return name, sub
}

// getServiceHistory is the page: pick a customer and dates, see the counts,
// download the PDF or the CSV.
func (s *Server) getServiceHistory(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	f := serviceFilterFrom(r)
	p := page{Title: "Service history", RepSpace: f.SpaceID, RepFrom: f.From, RepTo: f.To}
	if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
		p.RepTree = tree
	}
	if f.SpaceID != "" {
		recs, err := s.docs.ServiceHistory(r.Context(), id.OrgID, id.Subjects, f)
		if err != nil {
			s.log.ErrorContext(r.Context(), "service history", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "could not load the service history")
			return
		}
		p.SvcRecords = len(recs)
		days := map[string]bool{}
		for _, rec := range recs {
			for _, e := range rec.Events {
				p.SvcEvents++
				days[e.Day] = true
			}
		}
		p.SvcDays = len(days)
		p.SvcShown = recs
		q := url.Values{"space": {f.SpaceID}}
		if f.From != "" {
			q.Set("from", f.From)
		}
		if f.To != "" {
			q.Set("to", f.To)
		}
		p.SvcPDF = template.URL("/reports/service.pdf?" + q.Encode())
		p.SvcCSV = template.URL("/reports/service.csv?" + q.Encode())
	}
	s.render(w, r, "servicehistory", p)
}

func (s *Server) loadService(w http.ResponseWriter, r *http.Request) (*auth.Identity, docs.ServiceFilter, []*docs.ServiceRecord, bool) {
	id := auth.From(r.Context())
	f := serviceFilterFrom(r)
	if f.SpaceID == "" {
		s.fail(w, r, http.StatusBadRequest, "choose a customer first")
		return nil, f, nil, false
	}
	recs, err := s.docs.ServiceHistory(r.Context(), id.OrgID, id.Subjects, f)
	if err != nil {
		s.log.ErrorContext(r.Context(), "service history export", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the service history")
		return nil, f, nil, false
	}
	return id, f, recs, true
}

func (s *Server) getServiceHistoryPDF(w http.ResponseWriter, r *http.Request) {
	id, f, recs, ok := s.loadService(w, r)
	if !ok {
		return
	}
	name, sub := s.serviceTitle(r, id, f)
	var buf bytes.Buffer
	if err := reportpdfWrite(&buf, servicereport.Build(recs, name+": service history", sub)); err != nil {
		s.log.ErrorContext(r.Context(), "service history pdf", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not build the PDF")
		return
	}
	s.sendPDF(w, name+" service history", buf.Bytes())
}

func (s *Server) getServiceHistoryCSV(w http.ResponseWriter, r *http.Request) {
	id, f, recs, ok := s.loadService(w, r)
	if !ok {
		return
	}
	name, _ := s.serviceTitle(r, id, f)
	for _, r := range recs {
		r.Customer = name
	}
	var buf bytes.Buffer
	if err := servicereport.WriteCSV(&buf, recs); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not build the CSV")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(name+" service history")+`.csv"`)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(buf.Bytes())
}
