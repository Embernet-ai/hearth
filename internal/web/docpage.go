package web

// The document surface that phase six adds: a rendered body, a history that
// can be read and restored, checklists, and templates.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/markdown"
)

// renderBody turns a document body into HTML, honouring body_format.
//
// The column has said 'markdown' since the substrate landed while the page
// rendered a <pre>. This closes that, and it is the one place in the
// application where HTML from a person's typing reaches a template unescaped,
// which is safe only because the renderer escapes everything first and emits
// no tag it did not write. See internal/platform/markdown.
func (s *Server) renderBody(ctx context.Context, orgID string, actor core.Subjects, d *docs.Document) template.HTML {
	if d == nil || d.Body == "" {
		return ""
	}
	if d.BodyFormat == docs.FormatPlain {
		return template.HTML("<pre class=\"body\">" + html.EscapeString(d.Body) + "</pre>")
	}
	return template.HTML(markdown.Render(d.Body, markdown.Options{
		LinkEntity: func(kind, id string) string {
			return s.entityCard(ctx, orgID, actor, kind, id)
		},
	}))
}

// entityCard renders a live card for something embedded in a body.
//
// Permission filtered, obviously: embedding a work order you cannot see must
// not disclose its title. A card that cannot be rendered falls back to an
// ordinary link, so the person reading knows something is there.
func (s *Server) entityCard(ctx context.Context, orgID string, actor core.Subjects, kind, id string) string {
	if kind != "documents" {
		return ""
	}
	d, err := s.docs.Get(ctx, orgID, actor, id)
	if err != nil {
		return ""
	}
	status := ""
	if d.Task != nil {
		status = fmt.Sprintf(`<span class="status %s">%s</span>`,
			html.EscapeString(d.Task.Status), html.EscapeString(d.Task.Status))
	}
	title := d.Title
	if title == "" {
		title = "Untitled"
	}
	return fmt.Sprintf(
		`<a class="embed" href="/documents/%s"><span class="tag">%s</span> <strong>%s</strong> %s</a>`,
		html.EscapeString(d.ID), html.EscapeString(d.Kind), html.EscapeString(title), status)
}

// -----------------------------------------------------------------------------
// History
// -----------------------------------------------------------------------------

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")

	d, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, docID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	}
	history, err := s.docs.History(r.Context(), id.OrgID, id.Subjects, docID, 100)
	if err != nil {
		s.log.ErrorContext(r.Context(), "history", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the history")
		return
	}

	p := page{Title: "History of " + d.Title, Doc: d, History: history}

	// Comparing two versions, or a version against the one before it, which is
	// what somebody scanning a history actually wants.
	if v := r.URL.Query().Get("v"); v != "" {
		to, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "that is not a version")
			return
		}
		from := to - 1
		if f := r.URL.Query().Get("from"); f != "" {
			if n, err := strconv.ParseInt(f, 10, 64); err == nil {
				from = n
			}
		}
		p.Diff, p.DiffFrom, p.DiffTo, err = s.diffVersions(r, id, docID, from, to)
		if err != nil {
			s.fail(w, r, http.StatusNotFound, "no such version")
			return
		}
	}
	s.render(w, r, "history", p)
}

func (s *Server) diffVersions(r *http.Request, id *auth.Identity, docID string, from, to int64) ([]docs.DiffOp, *docs.Version, *docs.Version, error) {
	newer, err := s.docs.VersionAt(r.Context(), id.OrgID, id.Subjects, docID, to)
	if err != nil {
		return nil, nil, nil, err
	}
	// Version 1 has nothing before it, so it compares against nothing and the
	// whole thing reads as added, which is exactly what happened.
	older := &docs.Version{}
	if from >= 1 {
		older, err = s.docs.VersionAt(r.Context(), id.OrgID, id.Subjects, docID, from)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return docs.Diff(older.Body, newer.Body), older, newer, nil
}

func (s *Server) restoreVersion(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a version")
		return
	}

	if _, err := s.docs.Restore(r.Context(), id.OrgID, id.Subjects, docID, version); err != nil {
		if errors.Is(err, docs.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "no such version")
			return
		}
		s.log.WarnContext(r.Context(), "restore refused", "doc", docID, "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot restore this")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Checklists
// -----------------------------------------------------------------------------

func (s *Server) addChecklistItem(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if _, err := s.docs.AddChecklistItem(r.Context(), id.OrgID, id.Subjects,
		docID, r.FormValue("text"), r.FormValue("description")); err != nil {
		s.log.WarnContext(r.Context(), "add checklist item refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot add to this checklist")
		return
	}
	s.notify(id.OrgID, "document")
	// Reopens the Add-a-step popover on the way back, so adding several steps
	// in a row does not mean re-clicking Add each time.
	http.Redirect(w, r, "/documents/"+docID+"?checklist=open", http.StatusSeeOther)
}

func (s *Server) setChecklistItem(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	done := r.FormValue("done") == "1"
	if err := s.docs.SetChecklistItemDone(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("item"), done); err != nil {
		s.log.WarnContext(r.Context(), "tick refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot change this checklist")
		return
	}
	s.notify(id.OrgID, "document")
	http.Redirect(w, r, docPage(r, docID), http.StatusSeeOther)
}

func (s *Server) removeChecklistItem(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := s.docs.RemoveChecklistItem(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("item")); err != nil {
		s.log.WarnContext(r.Context(), "remove checklist item refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot change this checklist")
		return
	}
	s.notify(id.OrgID, "document")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Templates
// -----------------------------------------------------------------------------

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.docs.EnsureTemplateProperties(r.Context(), id.OrgID); err != nil {
		s.log.ErrorContext(r.Context(), "seed template properties", "error", err)
	}
	list, err := s.docs.Templates(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "templates", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the templates")
		return
	}
	s.render(w, r, "templates", page{Title: "Templates", Templates: list})
}

func (s *Server) useTemplate(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	d, err := s.docs.Instantiate(r.Context(), id.OrgID, id.Subjects, docs.InstantiateParams{
		TemplateID: r.PathValue("id"),
		Title:      strings.TrimSpace(r.FormValue("title")),
		Kind:       r.FormValue("kind"),
	})
	if err != nil {
		s.log.WarnContext(r.Context(), "instantiate refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "could not create that from the template")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+d.ID, http.StatusSeeOther)
}

// removeAttachment takes a file off its document without deleting it, and
// restoreAttachment puts it back. Both return to the document.
func (s *Server) removeAttachment(w http.ResponseWriter, r *http.Request) {
	s.setAttachment(w, r, s.docs.RemoveAttachment)
}

func (s *Server) restoreAttachment(w http.ResponseWriter, r *http.Request) {
	s.setAttachment(w, r, s.docs.RestoreAttachment)
}

func (s *Server) setAttachment(w http.ResponseWriter, r *http.Request,
	change func(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error) {

	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := change(r.Context(), id.OrgID, id.Subjects, docID, r.PathValue("fid")); err != nil {
		if errors.Is(err, authz.ErrDenied) || errors.Is(err, docs.ErrNotFound) {
			s.fail(w, r, http.StatusForbidden, "you cannot change the files on this")
			return
		}
		s.log.ErrorContext(r.Context(), "change attachment", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not change that file")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// postPreview renders what somebody is typing, as they type, through exactly
// the renderer that will show it once saved -- so "**bold**" is seen as bold
// before posting, and what the preview shows is what the page will show.
// cards=1 is for a document body, where a link to something in Hearth becomes
// a card; a comment renders without them, as comments do.
func (s *Server) postPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "too long to preview", http.StatusRequestEntityTooLarge)
		return
	}
	body := r.FormValue("body")
	opts := markdown.Options{}
	if r.FormValue("cards") != "" {
		id := auth.From(r.Context())
		opts.LinkEntity = func(kind, entityID string) string {
			return s.entityCard(r.Context(), id.OrgID, id.Subjects, kind, entityID)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if strings.TrimSpace(body) == "" {
		return
	}
	_, _ = io.WriteString(w, `<div class="muted small mdpreview-label">Preview</div>`+markdown.Render(body, opts))
}

// docPage is where a form on a document goes back to: the page itself, or the
// quick view when it was posted from there (peek=1), so a tick or a comment
// in a list's peek panel keeps the list where it was.
func docPage(r *http.Request, docID string) string {
	if r.FormValue("peek") == "1" {
		return "/documents/" + docID + "?peek=1"
	}
	return "/documents/" + docID
}
