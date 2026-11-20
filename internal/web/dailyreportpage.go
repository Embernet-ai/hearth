package web

// Daily reports: the form everybody files from, the roll-up, and the PDF.
//
// The form asks for the three things a report is made of at once, the day's
// text, any photos, and a comment, because the old "New daily report" button
// created an empty document and sent the author to a page where the text sat
// behind an Edit popover. Two reports were made that way in October and both
// are still empty: the second step was never taken. Filing now takes one step.

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/reportpdf"
)

// reportView is one report as the roll-up page shows it.
type reportView struct {
	*docs.ReportEntry
	BodyHTML template.HTML
	Photos   []docs.Attachment
	Files    []docs.Attachment
	Comments []*channels.Message
}

const (
	// maxReportFiles bounds one submission. A phone can attach a dozen photos;
	// more than this is somebody sending a folder.
	maxReportFiles = 30
	// maxPDFPhotoBytes bounds a single photo read into memory for a PDF.
	maxPDFPhotoBytes = 25 << 20
)

func isImage(contentType, name string) bool {
	if strings.HasPrefix(contentType, "image/") {
		return true
	}
	n := strings.ToLower(name)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// getDailyReportNew shows the filing form, or opens today's report if the
// person already filed one: there is one a day.
func (s *Server) getDailyReportNew(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	day := docs.ReportDay(time.Now())
	if existing, err := s.docs.ReportOn(r.Context(), id.OrgID, id.PrincipalID, day); err != nil {
		s.log.ErrorContext(r.Context(), "find today's report", "error", err)
	} else if existing != "" {
		http.Redirect(w, r, "/documents/"+existing, http.StatusSeeOther)
		return
	}
	s.renderReportForm(w, r, page{RepDay: day})
}

func (s *Server) renderReportForm(w http.ResponseWriter, r *http.Request, p page) {
	id := auth.From(r.Context())
	// Make sure the open department exists before the picker is built, so the
	// first person to open this on a new install still has somewhere to file.
	def, err := s.docs.DefaultReportSpace(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "default report space", "error", err)
	}
	if p.RepSpace == "" {
		p.RepSpace = r.URL.Query().Get("space")
	}
	if p.RepSpace == "" {
		p.RepSpace = def
	}
	if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
		p.RepTree = tree
	} else {
		s.log.ErrorContext(r.Context(), "space tree for report", "error", err)
	}
	p.Title = "File today's report"
	s.render(w, r, "dailyreport", p)
}

// postDailyReport files the report: text, photos and a first comment, together.
func (s *Server) postDailyReport(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	ctx := r.Context()
	if err := r.ParseMultipartForm(maxUploadForm); err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	day := docs.ReportDay(time.Now())
	body := strings.TrimSpace(r.FormValue("body"))
	comment := strings.TrimSpace(r.FormValue("comment"))
	space := r.FormValue("space")

	var headers []*multipart.FileHeader
	for _, field := range []string{"camera", "photos", "files"} {
		headers = append(headers, r.MultipartForm.File[field]...)
	}
	// Browsers send an empty part for a file input nobody touched.
	kept := headers[:0]
	for _, h := range headers {
		if h.Size > 0 && h.Filename != "" {
			kept = append(kept, h)
		}
	}
	headers = kept

	again := func(msg string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.renderReportForm(w, r, page{RepDay: day, RepSpace: space, RepBody: body, RepComment: comment, RepError: msg})
	}
	if body == "" && len(headers) == 0 {
		again("Write what you did today, or add a photo.")
		return
	}
	if len(headers) > maxReportFiles {
		again(fmt.Sprintf("That is %d files. Add up to %d at a time.", len(headers), maxReportFiles))
		return
	}

	// The department has to be one this person can see. Create itself only
	// checks that the space is in the org, and filing copies the space's
	// grants onto the report, so the choice is checked here.
	tree, err := s.plant.SpaceTree(ctx, id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(ctx, "space tree for report", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not check the department")
		return
	}
	allowed := false
	for _, sp := range tree {
		if sp.ID == space {
			allowed = true
			break
		}
	}
	if !allowed {
		again("Choose a department from the list.")
		return
	}

	d, err := s.docs.Create(ctx, id.OrgID, id.Subjects, docs.CreateParams{
		Kind: docs.KindDailyReport, Title: "Daily report — " + day, Body: body, SpaceID: &space,
	})
	if errors.Is(err, docs.ErrReportExists) {
		if existing, e2 := s.docs.ReportOn(ctx, id.OrgID, id.PrincipalID, day); e2 == nil && existing != "" {
			http.Redirect(w, r, "/documents/"+existing, http.StatusSeeOther)
			return
		}
	}
	if err != nil {
		s.log.ErrorContext(ctx, "create daily report", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not file the report")
		return
	}

	var failed []string
	for _, h := range headers {
		if err := s.attachUpload(r, id, d.ID, h); err != nil {
			s.log.ErrorContext(ctx, "daily report upload", "doc", d.ID, "file", h.Filename, "error", err)
			failed = append(failed, h.Filename)
		}
	}
	if comment != "" {
		if _, err := s.channels.Post(ctx, id.OrgID, id.Subjects, channels.PostParams{
			Entity: core.Ref{Type: core.EntityDocument, ID: d.ID}, Body: comment,
		}); err != nil {
			s.log.ErrorContext(ctx, "daily report comment", "doc", d.ID, "error", err)
			failed = append(failed, "your comment")
		}
	}
	s.notify(id.OrgID, "documents")
	if len(failed) > 0 {
		s.fail(w, r, http.StatusBadGateway, fmt.Sprintf(
			"Your report was saved, but this did not come through: %s. Open today's report from the dashboard and add it there.",
			strings.Join(failed, ", ")))
		return
	}
	http.Redirect(w, r, "/documents/"+d.ID, http.StatusSeeOther)
}

// attachUpload stores one uploaded file and attaches it to the document.
func (s *Server) attachUpload(r *http.Request, id *auth.Identity, docID string, h *multipart.FileHeader) error {
	f, err := h.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	up, err := s.files.Upload(r.Context(), id.OrgID, id.PrincipalID,
		h.Filename, h.Header.Get("Content-Type"), h.Size, f)
	if err != nil {
		return err
	}
	return s.docs.Attach(r.Context(), id.OrgID, id.Subjects, docID, up.ID)
}

// reportFilterFrom reads the roll-up's filter from the query string. The dates
// are office days; the default is the last seven, today included.
func reportFilterFrom(r *http.Request) docs.ReportFilter {
	q := r.URL.Query()
	f := docs.ReportFilter{SpaceID: q.Get("space"), PersonID: q.Get("person")}
	if uuid.Validate(f.SpaceID) != nil {
		f.SpaceID = ""
	}
	if uuid.Validate(f.PersonID) != nil {
		f.PersonID = ""
	}
	valid := func(v string) string {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return ""
		}
		return v
	}
	f.From, f.To = valid(q.Get("from")), valid(q.Get("to"))
	now := time.Now()
	if f.To == "" {
		f.To = docs.ReportDay(now)
	}
	if f.From == "" {
		f.From = docs.ReportDay(now.AddDate(0, 0, -6))
	}
	if f.From > f.To {
		f.From, f.To = f.To, f.From
	}
	return f
}

// reportViews adds what a reader needs beside each report's own row.
func (s *Server) reportViews(r *http.Request, id *auth.Identity, entries []*docs.ReportEntry) []*reportView {
	out := make([]*reportView, 0, len(entries))
	for _, e := range entries {
		v := &reportView{ReportEntry: e}
		v.BodyHTML = s.renderBody(r.Context(), id.OrgID, id.Subjects,
			&docs.Document{ID: e.ID, Body: e.Body, BodyFormat: e.BodyFormat})
		if atts, err := s.docs.Attachments(r.Context(), id.OrgID, id.Subjects, e.ID); err == nil {
			for _, a := range atts {
				if isImage(a.ContentType, a.Filename) {
					v.Photos = append(v.Photos, a)
				} else {
					v.Files = append(v.Files, a)
				}
			}
		}
		if cs, err := s.channels.Comments(r.Context(), id.OrgID, id.Subjects,
			core.Ref{Type: core.EntityDocument, ID: e.ID}, 100); err == nil {
			v.Comments = cs
		}
		out = append(out, v)
	}
	return out
}

// getDailyReports is the roll-up: reports by department and day.
func (s *Server) getDailyReports(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	f := reportFilterFrom(r)
	entries, err := s.docs.Reports(r.Context(), id.OrgID, id.Subjects, f)
	if err != nil {
		s.log.ErrorContext(r.Context(), "daily report roll-up", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the reports")
		return
	}
	p := page{Title: "Daily reports", RepSpace: f.SpaceID, RepFrom: f.From, RepTo: f.To, RepPerson: f.PersonID}
	p.RepEntries = s.reportViews(r, id, entries)
	if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
		p.RepTree = tree
	}
	if people, err := s.docs.Assignable(r.Context(), id.OrgID); err == nil {
		p.RepPeople = people
	}
	if mine, err := s.docs.ReportOn(r.Context(), id.OrgID, id.PrincipalID, docs.ReportDay(time.Now())); err == nil {
		p.RepMineToday = mine
	}
	// The PDF link carries the same filter as the page, dates included, so
	// what is downloaded is what is on screen. Built here as a whole URL:
	// html/template would percent-encode a query string passed in pieces.
	q := url.Values{}
	q.Set("from", f.From)
	q.Set("to", f.To)
	if f.SpaceID != "" {
		q.Set("space", f.SpaceID)
	}
	if f.PersonID != "" {
		q.Set("person", f.PersonID)
	}
	p.RepPDFHref = template.URL("/reports/daily.pdf?" + q.Encode())
	s.render(w, r, "dailyreports", p)
}

// buildReportPDF draws the entries the caller may read into a PDF.
func (s *Server) buildReportPDF(r *http.Request, id *auth.Identity, entries []*docs.ReportEntry, heading, sub string) ([]byte, error) {
	doc := reportpdf.Doc{Heading: heading, Subheading: sub, Generated: time.Now()}
	photos := 0
	for _, e := range entries {
		rep := reportpdf.Report{
			Day: e.Day, Author: e.Author, Department: e.SpacePath, Body: e.Body, Title: e.Title,
		}
		atts, err := s.docs.Attachments(r.Context(), id.OrgID, id.Subjects, e.ID)
		if err != nil {
			return nil, fmt.Errorf("attachments for %s: %w", e.ID, err)
		}
		for _, a := range atts {
			if !isImage(a.ContentType, a.Filename) || photos >= reportpdf.MaxPhotos || a.Size > maxPDFPhotoBytes {
				rep.Files = append(rep.Files, a.Filename)
				continue
			}
			_, rc, err := s.files.Open(r.Context(), id.OrgID, a.FileID)
			if err != nil {
				rep.Files = append(rep.Files, a.Filename)
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rc, maxPDFPhotoBytes+1))
			rc.Close()
			if err != nil || len(data) > maxPDFPhotoBytes {
				rep.Files = append(rep.Files, a.Filename)
				continue
			}
			rep.Photos = append(rep.Photos, reportpdf.Photo{Name: a.Filename, Data: data})
			photos++
		}
		if cs, err := s.channels.Comments(r.Context(), id.OrgID, id.Subjects,
			core.Ref{Type: core.EntityDocument, ID: e.ID}, 100); err == nil {
			for _, c := range cs {
				rep.Comments = append(rep.Comments, reportpdf.Comment{Author: c.AuthorName, Body: c.Body, At: c.CreatedAt})
			}
		}
		doc.Reports = append(doc.Reports, rep)
	}
	var buf bytes.Buffer
	if err := reportpdf.Write(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) sendPDF(w http.ResponseWriter, name string, b []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(name)+`.pdf"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// getDailyReportsPDF downloads the roll-up the filter describes.
func (s *Server) getDailyReportsPDF(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	f := reportFilterFrom(r)
	entries, err := s.docs.Reports(r.Context(), id.OrgID, id.Subjects, f)
	if err != nil {
		s.log.ErrorContext(r.Context(), "daily report pdf", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the reports")
		return
	}
	where := "All departments"
	if f.SpaceID != "" {
		if tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects); err == nil {
			for _, sp := range tree {
				if sp.ID == f.SpaceID {
					where = sp.Path
				}
			}
		}
	}
	sub := where + ", " + f.From + " to " + f.To
	b, err := s.buildReportPDF(r, id, entries, "Daily reports", sub)
	if err != nil {
		s.log.ErrorContext(r.Context(), "build daily report pdf", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not build the PDF")
		return
	}
	s.sendPDF(w, "daily reports "+f.From+" to "+f.To, b)
}

// getReportPDF downloads one report.
func (s *Server) getReportPDF(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	entries, err := s.docs.Reports(r.Context(), id.OrgID, id.Subjects, docs.ReportFilter{DocID: docID})
	if err != nil {
		s.log.ErrorContext(r.Context(), "single report pdf", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the report")
		return
	}
	if len(entries) != 1 {
		s.fail(w, r, http.StatusNotFound, "no such daily report")
		return
	}
	e := entries[0]
	b, err := s.buildReportPDF(r, id, entries, "Daily report", e.Day+", "+e.Author)
	if err != nil {
		s.log.ErrorContext(r.Context(), "build report pdf", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not build the PDF")
		return
	}
	s.sendPDF(w, "daily report "+e.Day+" "+e.Author, b)
}

// reportpdfWrite draws a document; a seam so the service history and the daily
// reports share one writer.
func reportpdfWrite(w io.Writer, d reportpdf.Doc) error { return reportpdf.Write(w, d) }
