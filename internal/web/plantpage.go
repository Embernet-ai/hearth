package web

// The plant tree on screen: spaces, the asset hierarchy with its rollups, the
// downtime log, and the label somebody scans.

import (
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/qr"
)

// getPlant is the tree with everything rolled up onto it, which is the page
// somebody looks at before deciding whether to run the line tonight.
func (s *Server) getPlant(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	tree, err := s.plant.Tree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "asset tree", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the plant")
		return
	}
	s.render(w, r, "plant", page{Title: "Plant", Assets: tree})
}

// getSpaces is the other tree: the one work is filed in rather than the one it
// happens to.
func (s *Server) getSpaces(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	tree, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "space tree", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the spaces")
		return
	}
	manageable, err := s.plant.ManageableSpaces(r.Context(), id.OrgID, id.Subjects, tree)
	if err != nil {
		s.log.ErrorContext(r.Context(), "manageable spaces", "error", err)
	}
	canAdmin, _ := s.plant.MayAdminSpaces(r.Context(), id.OrgID, id.Subjects)
	hasKids := map[string]bool{}
	for _, sp := range tree {
		if sp.ParentID != nil {
			hasKids[*sp.ParentID] = true
		}
	}
	s.render(w, r, "spaces", page{Title: "Spaces", Spaces: tree,
		SpaceParent: r.URL.Query().Get("parent"), ManageableSpaces: manageable, CanAdminSpaces: canAdmin, SpaceHasChildren: hasKids})
}

func (s *Server) createSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var parent *string
	if v := strings.TrimSpace(r.FormValue("parent")); v != "" {
		parent = &v
	}
	if _, err := s.plant.CreateSpace(r.Context(), id.OrgID, id.Subjects,
		r.FormValue("name"), r.FormValue("kind"), parent); err != nil {
		s.plantFail(w, r, err, "you cannot add a space there")
		return
	}
	http.Redirect(w, r, "/spaces", http.StatusSeeOther)
}

func (s *Server) renameSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.plant.RenameSpace(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("id"), r.FormValue("name")); err != nil {
		s.plantFail(w, r, err, "you cannot rename that space")
		return
	}
	http.Redirect(w, r, "/spaces", http.StatusSeeOther)
}

func (s *Server) moveSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var parent *string
	if v := strings.TrimSpace(r.FormValue("parent")); v != "" {
		parent = &v
	}
	if err := s.plant.MoveSpace(r.Context(), id.OrgID, id.Subjects,
		r.PathValue("id"), parent); err != nil {
		s.plantFail(w, r, err, "you cannot move that space")
		return
	}
	http.Redirect(w, r, "/spaces", http.StatusSeeOther)
}

func (s *Server) deleteSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.plant.DeleteSpace(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.plantFail(w, r, err, "you cannot remove that space")
		return
	}
	http.Redirect(w, r, "/spaces", http.StatusSeeOther)
}

// moveDocument files a document in a space, which is the write that finally
// spends a column the substrate has carried unused since the beginning.
func (s *Server) moveDocumentToSpace(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var space *string
	if v := strings.TrimSpace(r.FormValue("space")); v != "" {
		space = &v
	}
	if err := s.plant.MoveDocument(r.Context(), id.OrgID, id.Subjects, docID, space); err != nil {
		s.plantFail(w, r, err, "you cannot file this there")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// setAssetFields writes an asset's place in the plant tree and its code.
func (s *Server) setAssetFields(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	assetID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	f := plant.Fields{Code: r.FormValue("code")}
	if v := strings.TrimSpace(r.FormValue("parent")); v != "" {
		f.ParentAssetID = &v
	}
	f.InstalledAt = parseDay(r.FormValue("installed"))
	f.RetiredAt = parseDay(r.FormValue("retired"))

	if err := s.plant.SetFields(r.Context(), id.OrgID, id.Subjects, assetID, f); err != nil {
		s.plantFail(w, r, err, "you cannot change where that asset sits")
		return
	}
	http.Redirect(w, r, "/documents/"+assetID, http.StatusSeeOther)
}

// scan is the whole point of putting a label on a machine.
//
// A short path, because it is printed under the code and somebody will type it
// by hand when the label is too oily to read.
func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID, err := s.plant.ByCode(r.Context(), id.OrgID, id.Subjects, r.PathValue("code"))
	if err != nil {
		// Not-found whether the code is wrong or the caller has no grant. A
		// scanner that says "that machine exists but is not yours" is a
		// directory of the estate.
		s.fail(w, r, http.StatusNotFound, "no asset carries that code")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// getLabel is the printable page: the code, the symbol, and the address under
// it in case the symbol is unreadable.
func (s *Server) getLabel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	assetID := r.PathValue("id")

	d, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, assetID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such asset")
		return
	}
	fields, err := s.plant.FieldsFor(r.Context(), id.OrgID, assetID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "asset fields", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the asset")
		return
	}
	if fields.Code == "" {
		s.fail(w, r, http.StatusBadRequest,
			"give this asset a code first, then print its label")
		return
	}

	target := strings.TrimSuffix(s.publicBase(r), "/") + "/a/" + fields.Code
	svg, err := qr.SVG(target, 5)
	if err != nil {
		s.log.ErrorContext(r.Context(), "qr", "code", fields.Code, "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not draw the label")
		return
	}

	s.render(w, r, "label", page{
		Title: d.Title, Doc: d, AssetCode: fields.Code,
		// The SVG is written by this application from a code this application
		// validated, so it goes to the page unescaped. That is the same
		// argument the rendered markdown body makes, and it holds for the same
		// reason: nothing in it came from typed text.
		ScanURL: target, QR: template.HTML(svg),
	})
}

// publicBase is where a phone will reach this deployment.
//
// The configured public base when there is one, and the request's own host
// otherwise, because a label printed from a development machine that points at
// the production hostname is a label that scans to the wrong estate.
func (s *Server) publicBase(r *http.Request) string {
	if s.baseURL != "" {
		return s.baseURL
	}
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// -----------------------------------------------------------------------------
// Downtime
// -----------------------------------------------------------------------------

// recordDowntime handles all three shapes from one form: the line has stopped,
// the line is back, and here is an outage that finished earlier.
func (s *Server) recordDowntime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	assetID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	back := "/documents/" + assetID
	planned := r.FormValue("planned") == "on"
	var workOrder *string
	if v := strings.TrimSpace(r.FormValue("work_order")); v != "" {
		workOrder = &v
	}

	var err error
	switch r.FormValue("action") {
	case "down":
		_, err = s.plant.GoDown(r.Context(), id.OrgID, id.Subjects, assetID,
			parseMoment(r.FormValue("from")), planned, workOrder, r.FormValue("note"))
	case "up":
		_, err = s.plant.ComeUp(r.Context(), id.OrgID, id.Subjects, assetID,
			parseMoment(r.FormValue("to")))
	case "log":
		from, to := parseMoment(r.FormValue("from")), parseMoment(r.FormValue("to"))
		_, err = s.plant.LogDowntime(r.Context(), id.OrgID, id.Subjects, assetID,
			from, to, planned, workOrder, r.FormValue("note"))
	default:
		s.fail(w, r, http.StatusBadRequest, "that is not something to do with downtime")
		return
	}

	switch {
	case errors.Is(err, plant.ErrAlreadyDown):
		s.fail(w, r, http.StatusConflict,
			"that machine is already recorded as down. Bring it up first, so the "+
				"same outage is not counted twice.")
		return
	case errors.Is(err, plant.ErrNotDown):
		s.fail(w, r, http.StatusConflict, "that machine is not recorded as down")
		return
	case err != nil:
		s.plantFail(w, r, err, "you cannot record downtime against this")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) removeDowntime(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	assetID := r.PathValue("id")
	if err := s.plant.RemoveDowntime(r.Context(), id.OrgID, id.Subjects, r.PathValue("entry")); err != nil {
		s.plantFail(w, r, err, "you cannot remove that record")
		return
	}
	http.Redirect(w, r, "/documents/"+assetID, http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Shared
// -----------------------------------------------------------------------------

// plantFail turns a service error into the right status and a message somebody
// can act on. A cycle in particular deserves its own explanation: "forbidden"
// tells a person nothing about why the machine will not go where they put it.
func (s *Server) plantFail(w http.ResponseWriter, r *http.Request, err error, forbidden string) {
	switch {
	case errors.Is(err, plant.ErrCycle):
		s.fail(w, r, http.StatusBadRequest,
			"that would put something inside itself. A machine cannot hang off "+
				"one of its own parts.")
	case errors.Is(err, plant.ErrInvalid):
		s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "plant: "))
	case errors.Is(err, plant.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such thing")
	case errors.Is(err, plant.ErrDenied):
		s.fail(w, r, http.StatusForbidden,
			"only a manager of this department or an admin can change the folders here")
	default:
		s.log.WarnContext(r.Context(), "plant refused", "error", err)
		s.fail(w, r, http.StatusForbidden, forbidden)
	}
}

// parseDay reads a date field, returning nil for an empty one so that clearing
// a date clears it rather than setting the year to one.
func parseDay(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil
	}
	return &d
}

// parseMoment reads a datetime-local field, falling back to now. An outage form
// submitted with an empty time means "right now", which is what somebody
// standing next to a stopped machine means.
func parseMoment(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Now()
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t
		}
	}
	return time.Now()
}

// assetOptions is the parent picker: every asset the caller can see, except the
// one being edited and everything below it, because offering those is offering
// a cycle.
func (s *Server) assetOptions(r *http.Request, exclude string) []*plant.Asset {
	id := auth.From(r.Context())
	tree, err := s.plant.Tree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "asset options", "error", err)
		return nil
	}
	if exclude == "" {
		return tree
	}
	// Everything below the excluded node, by walking the paths the tree
	// already carries. The service refuses a cycle anyway; this keeps the
	// picker from offering one in the first place.
	below := map[string]bool{exclude: true}
	for _, a := range tree {
		if a.ParentID != nil && below[*a.ParentID] {
			below[a.ID] = true
		}
	}
	out := make([]*plant.Asset, 0, len(tree))
	for _, a := range tree {
		if !below[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

// reorderSpaces sets the order of one parent's children from a dragged list.
func (s *Server) reorderSpaces(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.plant.ReorderSpaces(r.Context(), id.OrgID, id.Subjects,
		r.FormValue("parent"), r.Form["id"]); err != nil {
		s.plantFail(w, r, err, "you cannot reorder those")
		return
	}
	http.Redirect(w, r, "/spaces", http.StatusSeeOther)
}

// saveSpaceOrder persists a whole rearrangement from the Spaces page. Returns
// 204 so the page can clear its unsaved state without reloading; a refusal is
// an ordinary status the page turns back into the saved order by reloading.
func (s *Server) saveSpaceOrder(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.plant.SaveSpaceOrder(r.Context(), id.OrgID, id.Subjects, r.Form["id"]); err != nil {
		s.plantFail(w, r, err, "you cannot reorder those")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
