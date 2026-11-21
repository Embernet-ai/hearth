package web

// Cloud files: browse OneDrive, SharePoint and Google Drive, then link a file to a Hearth document or import a
// copy of it. Everything is done as the signed-in person, and the document service decides whether they may add to
// the document.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// CloudView is the file browser.
type CloudView struct {
	Providers []CloudProvider
	Provider  string
	Label     string
	Drives    []connect.Drive
	DriveID   string
	FolderID  string
	Items     []connect.FileItem
	DocID     string
	DocTitle  string
	Documents []CloudDoc
	Notice    string
}

// CloudProvider is a provider the person can browse.
type CloudProvider struct{ Key, Label string }

// CloudDoc is a document that can be chosen to receive a file.
type CloudDoc struct{ ID, Title string }

func (s *Server) cloudBack(w http.ResponseWriter, r *http.Request, doc, msg string) {
	if msg != "" && doc != "" {
		http.Redirect(w, r, "/documents/"+url.PathEscape(doc), http.StatusSeeOther)
		return
	}
	to := "/cloud"
	if msg != "" {
		to += "?notice=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) getCloud(w http.ResponseWriter, r *http.Request) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return
	}
	ctx := r.Context()
	id := auth.From(ctx)
	q := r.URL.Query()
	v := &CloudView{Notice: q.Get("notice"), DocID: q.Get("doc"), DriveID: q.Get("drive"), FolderID: q.Get("folder")}

	// Which providers this person can browse: Files switched on, and a live connection that covers it.
	for _, p := range connect.Providers {
		b := s.connect.Backend(p)
		if b == nil {
			continue
		}
		if on, _ := s.connect.Enabled(ctx, id.OrgID, p, connect.Files); !on {
			continue
		}
		granted, err := b.Granted(ctx, id.PrincipalID)
		if err != nil || !connect.HasScopes(granted, b.Scopes(connect.Files)) {
			continue
		}
		v.Providers = append(v.Providers, CloudProvider{Key: string(p), Label: p.Label()})
	}
	if len(v.Providers) > 0 {
		v.Provider = q.Get("provider")
		ok := false
		for _, cp := range v.Providers {
			ok = ok || cp.Key == v.Provider
		}
		if !ok {
			v.Provider = v.Providers[0].Key
		}
		p := connect.Provider(v.Provider)
		v.Label = p.Label()
		drives, err := s.connect.Drives(ctx, id.OrgID, id.PrincipalID, p)
		if err != nil {
			v.Notice = cloudErr(err)
		}
		v.Drives = drives
		if v.DriveID == "" && len(drives) > 0 {
			v.DriveID = drives[0].ID
		}
		if v.DriveID != "" && err == nil {
			items, err := s.connect.Browse(ctx, id.OrgID, id.PrincipalID, p, v.DriveID, v.FolderID)
			if err != nil {
				v.Notice = cloudErr(err)
			}
			v.Items = items
		}
	}
	if v.DocID != "" {
		if d, err := s.docs.Get(ctx, id.OrgID, id.Subjects, v.DocID); err == nil {
			v.DocTitle = d.Title
		} else {
			v.DocID = ""
		}
	}
	if v.DocID == "" {
		if ds, err := s.docs.List(ctx, id.OrgID, id.Subjects, docs.ListParams{Limit: 100}); err == nil {
			for _, d := range ds {
				v.Documents = append(v.Documents, CloudDoc{ID: d.ID, Title: d.Title})
			}
		}
	}
	s.render(w, r, "cloud", page{Title: "Cloud files", Cloud: v})
}

func cloudErr(err error) string {
	switch {
	case errors.Is(err, connect.ErrReconnect):
		return "That connection was revoked. Connect again under Settings, Connectors."
	case errors.Is(err, connect.ErrScope):
		return "Your connection does not include file access. Connect again and tick Files."
	case errors.Is(err, connect.ErrNotFound):
		return "Not found, or you may not open it."
	case errors.Is(err, connect.ErrDisabled):
		return "An administrator has not turned this on."
	}
	return strings.TrimPrefix(err.Error(), "connect: ")
}

func (s *Server) cloudForm(w http.ResponseWriter, r *http.Request) (connect.Provider, bool) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return "", false
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return "", false
	}
	p, err := connect.ParseProvider(r.FormValue("provider"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "choose a provider")
		return "", false
	}
	return p, true
}

func (s *Server) postCloudLink(w http.ResponseWriter, r *http.Request)   { s.cloudAdd(w, r, false) }
func (s *Server) postCloudImport(w http.ResponseWriter, r *http.Request) { s.cloudAdd(w, r, true) }

func (s *Server) cloudAdd(w http.ResponseWriter, r *http.Request, importIt bool) {
	p, ok := s.cloudForm(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	doc, drive, item := r.FormValue("doc"), r.FormValue("drive"), r.FormValue("item")
	if doc == "" || item == "" {
		s.fail(w, r, http.StatusBadRequest, "choose a document and a file")
		return
	}
	var err error
	if importIt {
		_, err = s.connect.Import(r.Context(), id.OrgID, id.PrincipalID, p, drive, item, doc)
	} else {
		_, err = s.connect.Link(r.Context(), id.OrgID, id.PrincipalID, p, drive, item, doc)
	}
	if err != nil {
		s.log.WarnContext(r.Context(), "cloud file", "error", err)
		q := url.Values{"provider": {string(p)}, "drive": {drive}, "doc": {doc}, "notice": {cloudErr(err)}}
		http.Redirect(w, r, "/cloud?"+q.Encode(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/documents/"+url.PathEscape(doc), http.StatusSeeOther)
}

func (s *Server) postCloudUnlink(w http.ResponseWriter, r *http.Request) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	id := auth.From(r.Context())
	if err := s.connect.Unlink(r.Context(), id.OrgID, id.PrincipalID, r.FormValue("link")); err != nil {
		s.fail(w, r, http.StatusForbidden, cloudErr(err))
		return
	}
	http.Redirect(w, r, "/documents/"+url.PathEscape(r.FormValue("doc")), http.StatusSeeOther)
}
