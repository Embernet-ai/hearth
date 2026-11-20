package web

// The four ways a list renders, and the saved views that name one.
//
// All four are the SAME query. A board is not a different data source from a
// table, it is the same rows arranged into columns, which is why switching
// between them keeps every filter and why a saved view can be opened as any of
// them. The predecessor had a separate endpoint per layout and they disagreed
// about what "overdue" meant, because two queries written a month apart always
// do.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/views"
)

// Cell is one rendered value in a table row.
type Cell struct {
	Text string
	// Class carries the styling hook for values that mean something: a status
	// chip, a priority word. Empty for plain text.
	Class string
	// Link makes the cell an anchor to the document. Exactly one column gets
	// this, and it is whichever one is showing the title.
	Link bool
	// Muted is for values that are context rather than content, like a date.
	Muted bool
}

// TableRow is a document with its cells already rendered, so the template does
// no lookups and cannot introduce an N+1 by accident.
type TableRow struct {
	Doc   *docs.Document
	Cells []Cell
	// Depth is how far inside another row this one sits: 0 for a top-level
	// row, 1 for a subtask whose parent is in the same list, and so on. A
	// nested row starts folded away under its parent.
	Depth int
	// Children is how many subtasks of this row follow it, for the toggle.
	Children int
	// Subtask marks a row with a parent the list does not show -- hidden by a
	// filter, in another group, or not the reader's to see. It stands on its
	// own, labelled, rather than vanishing.
	Subtask bool
	// Parent is the id of the row this one is folded under, empty at depth 0.
	Parent string
}

// keepWithParents moves each subtask into the group its top-most listed
// ancestor is in, so a course's subtasks stay under the course even when one
// person is done, one in progress and one not started. Grouped by status, they
// used to scatter across three sections and the parent showed one of three.
// Each row still shows its own status; only where it sits changes.
func keepWithParents(groups []TableGroup) []TableGroup {
	home := map[string]int{}
	parentOf := map[string]string{}
	for gi, g := range groups {
		for _, r := range g.Rows {
			home[r.Doc.ID] = gi
			if r.Doc.ParentID != nil {
				parentOf[r.Doc.ID] = *r.Doc.ParentID
			}
		}
	}
	// The group of the highest ancestor that is in the list, walked with a
	// bound so a loop in the data cannot hang the page.
	target := func(id string) int {
		g := home[id]
		for i, cur := 0, id; i < 50; i++ {
			p, ok := parentOf[cur]
			if !ok {
				break
			}
			pg, listed := home[p]
			if !listed {
				break
			}
			g, cur = pg, p
		}
		return g
	}
	out := make([]TableGroup, len(groups))
	for gi, g := range groups {
		out[gi] = TableGroup{Value: g.Value, Label: g.Label}
	}
	for gi, g := range groups {
		for _, r := range g.Rows {
			to := gi
			if r.Doc.ParentID != nil {
				to = target(r.Doc.ID)
			}
			out[to].Rows = append(out[to].Rows, r)
		}
	}
	return out
}

// nestRows puts each subtask straight after its parent, depth first, when the
// parent is in the same set of rows. Everything else keeps its order. A row
// whose parent is elsewhere stays top-level, marked Subtask.
//
// Bounded by the rows given: a parent is only ever a row already in the list,
// and each row is placed once, so a loop in the data (which SetParent refuses,
// but this does not depend on) cannot make it recurse forever.
func nestRows(rows []TableRow) []TableRow {
	if len(rows) == 0 {
		return rows
	}
	index := make(map[string]int, len(rows))
	for i, r := range rows {
		index[r.Doc.ID] = i
	}
	kids := map[string][]int{}
	for i, r := range rows {
		if p := r.Doc.ParentID; p != nil {
			if _, ok := index[*p]; ok && *p != r.Doc.ID {
				kids[*p] = append(kids[*p], i)
			}
		}
	}
	out := make([]TableRow, 0, len(rows))
	placed := make([]bool, len(rows))
	var place func(i, depth int)
	place = func(i, depth int) {
		if placed[i] {
			return
		}
		placed[i] = true
		r := rows[i]
		r.Depth = depth
		r.Children = len(kids[r.Doc.ID])
		r.Subtask = depth == 0 && r.Doc.ParentID != nil
		if depth > 0 {
			r.Parent = *r.Doc.ParentID
		}
		out = append(out, r)
		for _, k := range kids[r.Doc.ID] {
			place(k, depth+1)
		}
	}
	for i, r := range rows {
		top := r.Doc.ParentID == nil
		if !top {
			_, parentHere := index[*r.Doc.ParentID]
			top = !parentHere
		}
		if top {
			place(i, 0)
		}
	}
	// Anything left belongs to a loop of parents within the list; show it
	// flat rather than drop it.
	for i := range rows {
		place(i, 0)
	}
	return out
}

// TableGroup is Table's own version of a board column -- same shape as
// docs.Group, just holding rendered TableRow instead of raw *docs.Row, since
// a table's rows need columns/properties resolved the same way an ungrouped
// table's always have.
type TableGroup struct {
	Value string
	Label string
	Rows  []TableRow
}

// CalDay is one square of the month grid.
type CalDay struct {
	Date    time.Time
	InMonth bool
	Today   bool
	Rows    []*docs.Row
}

// Bar is one row of the timeline, positioned as percentages of the visible
// window so the template needs no arithmetic and no fixed pixel width.
type Bar struct {
	Doc      *docs.Document
	LeftPct  float64
	WidthPct float64
	Status   string
}

// ViewTab is one of the four layout links across the top of a list.
type ViewTab struct {
	Kind  string
	Label string
	Href  string
	On    bool
}

// FieldOption is one entry in a group-by or column picker.
type FieldOption struct {
	Key   string
	Label string
	On    bool
}

var viewTabLabels = map[string]string{
	docs.ViewTable:    "Table",
	docs.ViewBoard:    "Board",
	docs.ViewCalendar: "Calendar",
	docs.ViewTimeline: "Timeline",
}

// listPath is the address a kind lives at, so a filtered list of work orders is
// /work-orders and not /documents with an argument.
func listPath(kind string) string {
	switch kind {
	case docs.KindWorkOrder:
		return "/work-orders"
	case docs.KindAsset:
		return "/assets"
	case docs.KindTask:
		return "/tasks"
	default:
		return "/documents"
	}
}

// listURL renders a query back into an address.
//
// The kind is dropped when the path already says it, so switching a filter on a
// work order list does not turn a clean /work-orders into one carrying a
// redundant argument that then disagrees with the path if either is edited.
func listURL(base string, q docs.Query, viewKind string) string {
	v := q.Encode()
	switch base {
	case "/work-orders", "/assets", "/tasks":
		v.Del("kind")
	}
	if viewKind != "" && viewKind != docs.ViewTable {
		v.Set("view", viewKind)
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

// listRequest is everything a list page needs, gathered once.
type listRequest struct {
	query    docs.Query
	viewKind string
	// saved is the named view this was opened from, if any.
	saved *views.View
	// base is the address the page's own links point at.
	base string
}

// getList serves /documents and its per-kind addresses.
func (s *Server) getList(w http.ResponseWriter, r *http.Request) {
	q := docs.ParseQuery(r.URL.Query())
	// The path wins over the query string, so /assets is always assets even if
	// somebody pastes a kind argument onto it.
	if k := kindFor(r); k != "" {
		q.Kind = k
	}
	s.renderList(w, r, listRequest{
		query:    q,
		viewKind: viewKindFrom(r.URL.Query().Get("view")),
		base:     listPath(q.Kind),
	})
}

// getSavedView opens a named view.
//
// The query comes from the stored row, not from the address, so the link keeps
// working when somebody trims the query string off it. Anything still in the
// address is applied on top, which is what makes "open the view, then narrow
// it" produce a URL that can itself be sent on.
func (s *Server) getSavedView(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	v, err := s.views.Get(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	switch {
	case errors.Is(err, views.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such view")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "load view", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load that view")
		return
	}

	q := v.Query
	// Extra filters from the address narrow the saved view rather than
	// replacing it.
	if extra := docs.ParseQuery(r.URL.Query()); len(extra.Filters) > 0 {
		q.Filters = append(append([]docs.Filter{}, q.Filters...), extra.Filters...)
	}
	kind := v.Kind
	if requested := r.URL.Query().Get("view"); requested != "" {
		kind = viewKindFrom(requested)
	}

	s.renderList(w, r, listRequest{
		query: q, viewKind: kind, saved: v, base: "/views/" + v.ID,
	})
}

func viewKindFrom(s string) string {
	if docs.ValidViewKind(s) {
		return s
	}
	return docs.ViewTable
}

// withDefaultGroup groups a board or a table by status when nobody said how to
// group it, and leaves it flat when somebody chose "Nothing".
func withDefaultGroup(q docs.Query, viewKind string) docs.Query {
	if (viewKind == docs.ViewBoard || viewKind == docs.ViewTable) && q.GroupBy.Zero() && !q.Ungrouped {
		q.GroupBy = docs.Col("status")
	}
	return q
}

// renderList runs the query once and arranges the result four possible ways.
func (s *Server) renderList(w http.ResponseWriter, r *http.Request, req listRequest) {
	id := auth.From(r.Context())
	ctx := r.Context()

	// A board needs something to group by, and status is what a board is for.
	// Table gets the same default now -- an ungrouped table was the biggest
	// gap between the two views (Board already grouped, Table just listed
	// everything flat), and status is exactly as much "what a list of tasks
	// is for" as it is "what a board is for".
	req.query = withDefaultGroup(req.query, req.viewKind)

	// Closed work stays out of a list until somebody asks for it -- a done six
	// months ago sitting in the same view as this week's work is the
	// structure-is-invisible problem in reverse, noise instead of missing
	// structure. Built here rather than folded into req.query.Filters so the
	// Filter popover's own status chips still read as untouched by default;
	// picking a specific status there is a narrower question than this.
	searchQuery := req.query
	if !req.query.Closed && !hasStatusFilter(req.query.Filters) {
		searchQuery.Filters = append(append([]docs.Filter{}, req.query.Filters...),
			docs.Filter{Field: docs.Col("status"), Op: docs.OpIsNot, Values: []string{docs.StatusDone}},
			docs.Filter{Field: docs.Col("status"), Op: docs.OpIsNot, Values: []string{docs.StatusCancelled}},
		)
	}

	rows, err := s.docs.Search(ctx, id.OrgID, id.Subjects, searchQuery)
	if err != nil {
		s.log.ErrorContext(ctx, "search", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load that list")
		return
	}
	// The count comes from the same compiled WHERE as the rows, so "showing 50
	// of 214" cannot be lying about either half.
	total, err := s.docs.Count(ctx, id.OrgID, id.Subjects, searchQuery)
	if err != nil {
		s.log.ErrorContext(ctx, "count", "error", err)
		total = len(rows)
	}

	people, err := s.docs.Assignable(ctx, id.OrgID)
	if err != nil {
		s.log.ErrorContext(ctx, "load assignable", "error", err)
	}
	nameOf := make(map[string]string, len(people))
	for _, p := range people {
		nameOf[p.ID] = p.Name
	}

	// Custom fields that apply here, for the filter and group controls and for
	// any property columns the table is showing.
	if req.query.Kind == docs.KindAsset {
		if err := s.docs.EnsureAssetProperties(ctx, id.OrgID); err != nil {
			s.log.ErrorContext(ctx, "seed asset properties", "error", err)
		}
	}
	defs, err := s.docs.PropertyDefsFor(ctx, id.OrgID, req.query.Kind)
	if err != nil {
		s.log.ErrorContext(ctx, "load property defs", "error", err)
	}

	cols := req.query.Columns
	if len(cols) == 0 {
		cols = docs.TableColumns
	}

	// Property values for the whole page in one query, and only when a property
	// is actually being shown or grouped on.
	var propValues map[string]map[string]string
	if needsProperties(cols, req.query.GroupBy) {
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		if propValues, err = s.docs.PropertyValuesFor(ctx, id.OrgID, ids); err != nil {
			s.log.ErrorContext(ctx, "load property values", "error", err)
		}
	}

	p := page{
		Title:      s.listTitle(req),
		Heading:    s.listTitle(req),
		Wide:       true,
		Kind:       req.query.Kind,
		Query:      req.query,
		ViewKind:   req.viewKind,
		SavedView:  req.saved,
		Base:       req.base,
		Total:      total,
		Shown:      len(rows),
		Statuses:   docs.Statuses,
		People:     people,
		PropDefs:   defs,
		Columns:    cols,
		Tabs:       s.viewTabs(req),
		GroupBy:    req.query.GroupBy.Key(),
		GroupOpts:  groupOptions(req.query, defs),
		ColumnOpts: columnOptions(cols, defs),
		SaveTarget: listURL(listPath(req.query.Kind), req.query, req.viewKind),
		// CurrentURL is exactly the view being looked at right now -- same
		// base (plain kind or a saved view), same filters, same grouping --
		// so an action that wants to return here (the grouped table's
		// inline Add row) lands back on the same view rather than resetting
		// to the kind's bare default.
		CurrentURL: listURL(req.base, req.query, req.viewKind),
	}

	// Daily reports for this space, a dedicated section rather than a filter
	// on "All documents" -- Gabriel's actual complaint was findability, and a
	// kind=daily_report filter you have to know to type does not fix that.
	// Only shown when browsing inside a space and not already viewing daily
	// reports themselves (which would just be duplicating the list below).
	if req.query.SpaceID != nil && req.query.Kind != docs.KindDailyReport {
		if reports, err := s.docs.Search(ctx, id.OrgID, id.Subjects, docs.Query{
			Kind: docs.KindDailyReport, SpaceID: req.query.SpaceID, Limit: 10,
		}); err == nil {
			p.DailyReports = reports
		} else {
			s.log.ErrorContext(ctx, "load daily reports", "error", err)
		}
	} else if req.query.SpaceID == nil && req.query.Kind == "" {
		// All documents: the same section, for everybody whose department
		// is not set up yet and so never browses into one. Every report the
		// reader can see, newest first, narrowed by a department picker.
		q := docs.Query{Kind: docs.KindDailyReport, Limit: 20}
		if sp := r.URL.Query().Get("reports"); uuid.Validate(sp) == nil {
			q.SpaceID = &sp
			p.ReportSpace = sp
		}
		if reports, err := s.docs.Search(ctx, id.OrgID, id.Subjects, q); err == nil {
			p.DailyReports = reports
		} else {
			s.log.ErrorContext(ctx, "load daily reports", "error", err)
		}
		if tree, err := s.plant.SpaceTree(ctx, id.OrgID, id.Subjects); err == nil {
			p.ReportTree = tree
			p.ReportSpaceNames = map[string]string{}
			for _, sp := range tree {
				p.ReportSpaceNames[sp.ID] = sp.Path
				if sp.Depth == 0 {
					p.ReportSpaces = append(p.ReportSpaces, sp)
				}
			}
		}
	}

	switch req.viewKind {
	case docs.ViewBoard:
		p.Groups = s.boardGroups(ctx, id.OrgID, req.query, rows, nameOf)
	case docs.ViewCalendar:
		p.Month, p.Weeks, p.PrevMonth, p.NextMonth = calendar(r.URL.Query().Get("month"), rows)
	case docs.ViewTimeline:
		p.Bars, p.TimelineFrom, p.TimelineTo = timeline(rows)
	default:
		// Grouped the same way Board is -- same boardGroups helper, the name
		// predates Table getting groups too. Each group's own rows go through
		// the same tableRows rendering a flat table already used, just once
		// per group instead of once for everything.
		var groups []TableGroup
		for _, g := range s.boardGroups(ctx, id.OrgID, req.query, rows, nameOf) {
			groups = append(groups, TableGroup{
				Value: g.Value, Label: g.Label,
				Rows: tableRows(g.Rows, cols, propValues, nameOf),
			})
		}
		// Subtasks follow their parent's group, then fold under it.
		for _, g := range keepWithParents(groups) {
			g.Rows = nestRows(g.Rows)
			p.GroupedTable = append(p.GroupedTable, g)
		}
	}

	if saved, err := s.views.List(ctx, id.OrgID, id.PrincipalID); err == nil {
		p.SavedViews = saved
	} else {
		s.log.ErrorContext(ctx, "list saved views", "error", err)
	}

	s.render(w, r, "documents", p)
}

func (s *Server) listTitle(req listRequest) string {
	if req.saved != nil {
		return req.saved.Name
	}
	return heading(req.query.Kind)
}

func (s *Server) viewTabs(req listRequest) []ViewTab {
	out := make([]ViewTab, 0, len(docs.ViewKinds))
	for _, k := range docs.ViewKinds {
		out = append(out, ViewTab{
			Kind:  k,
			Label: viewTabLabels[k],
			Href:  tabHref(req.base, req.query, k),
			On:    k == req.viewKind,
		})
	}
	return out
}

// tabHref is listURL, but it always states the view kind, table included.
// listURL omits view= for table because that is the right default for a
// plain list address. A saved view has a different default: no view= means
// "whatever kind this was saved as" (getSavedView), not table. Leaving it
// out of the Table tab's own link on a view saved as Board made clicking
// Table silently reopen Board, since the link carried no information to
// override it. The tabs are the one place actually choosing a kind, so they
// cannot rely on an omission whose meaning changes depending on the page.
func tabHref(base string, q docs.Query, viewKind string) string {
	v := q.Encode()
	switch base {
	case "/work-orders", "/assets", "/tasks":
		v.Del("kind")
	}
	v.Set("view", viewKind)
	return base + "?" + v.Encode()
}

func needsProperties(cols []docs.Field, group docs.Field) bool {
	if group.IsProperty() {
		return true
	}
	for _, c := range cols {
		if c.IsProperty() {
			return true
		}
	}
	return false
}

func groupOptions(q docs.Query, defs []docs.PropertyDef) []FieldOption {
	out := []FieldOption{{Key: "", Label: "Nothing", On: q.GroupBy.Zero()}}
	for _, name := range docs.GroupableColumns {
		out = append(out, FieldOption{
			Key: name, Label: docs.ColumnLabel(name), On: q.GroupBy == docs.Col(name),
		})
	}
	// Only select properties make sensible board columns. Grouping by a free
	// text field gives one column per distinct value, which on a serial number
	// is one column per row.
	for _, d := range defs {
		if d.DataType != docs.PropSelect {
			continue
		}
		f := docs.Prop(d.Key)
		out = append(out, FieldOption{Key: f.Key(), Label: d.Label, On: q.GroupBy == f})
	}
	return out
}

func columnOptions(chosen []docs.Field, defs []docs.PropertyDef) []FieldOption {
	on := make(map[docs.Field]bool, len(chosen))
	for _, c := range chosen {
		on[c] = true
	}
	out := make([]FieldOption, 0, len(docs.SelectableColumns)+len(defs))
	for _, name := range docs.SelectableColumns {
		f := docs.Col(name)
		out = append(out, FieldOption{Key: name, Label: docs.ColumnLabel(name), On: on[f]})
	}
	for _, d := range defs {
		f := docs.Prop(d.Key)
		out = append(out, FieldOption{Key: f.Key(), Label: d.Label, On: on[f]})
	}
	return out
}

// -----------------------------------------------------------------------------
// Template helpers
// -----------------------------------------------------------------------------
//
// Every control on a list page is a link that carries the whole query, so
// changing one thing never silently drops another. Building those links in Go
// rather than in the template is what makes that true: a template that
// assembled query strings by concatenation would lose a filter the first time
// somebody added a second one.

// hasStatusFilter reports whether the query already names a status, so the
// default closed-exclusion does not fight a filter chip that asked for one
// specific status (including Done or Cancelled themselves).
func hasStatusFilter(filters []docs.Filter) bool {
	for _, f := range filters {
		if f.Field == docs.Col("status") {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Table
// -----------------------------------------------------------------------------

func tableRows(rows []*docs.Row, cols []docs.Field,
	props map[string]map[string]string, nameOf map[string]string) []TableRow {

	out := make([]TableRow, 0, len(rows))
	for _, row := range rows {
		tr := TableRow{Doc: row.Document, Cells: make([]Cell, 0, len(cols))}
		for _, c := range cols {
			tr.Cells = append(tr.Cells, renderCell(row.Document, c, props[row.ID], nameOf))
		}
		out = append(out, tr)
	}
	return out
}

// renderCell turns one field of one document into something a table shows.
//
// A dash rather than a blank for anything unset. An empty cell reads as a
// rendering fault; a dash reads as "there is nothing here", which is the truth.
func renderCell(d *docs.Document, f docs.Field, props map[string]string, nameOf map[string]string) Cell {
	const none = "—"

	if f.IsProperty() {
		if v, ok := props[f.Property]; ok && v != "" {
			return Cell{Text: v}
		}
		return Cell{Text: none, Muted: true}
	}

	switch f.Column {
	case "title":
		if d.Title == "" {
			return Cell{Text: "Untitled", Link: true, Muted: true}
		}
		return Cell{Text: d.Title, Link: true}
	case "kind":
		return Cell{Text: d.Kind, Class: "tag"}
	case "owner":
		return Cell{Text: personName(nameOf, &d.OwnerID)}
	case "created":
		return Cell{Text: humanAgo(d.CreatedAt), Muted: true}
	case "updated":
		return Cell{Text: humanAgo(d.UpdatedAt), Muted: true}
	}

	if d.Task == nil {
		return Cell{Text: none, Muted: true}
	}
	switch f.Column {
	case "status":
		return Cell{Text: docs.StatusLabel(d.Task.Status), Class: "status " + d.Task.Status}
	case "priority":
		return Cell{Text: priorityWord(d.Task.Priority), Class: fmt.Sprintf("prio p%d", d.Task.Priority)}
	case "assignee":
		if d.Task.AssigneeID == nil {
			return Cell{Text: "Nobody", Muted: true}
		}
		return Cell{Text: personName(nameOf, d.Task.AssigneeID)}
	case "due":
		return dateCell(d.Task.DueAt)
	case "started":
		return dateCell(d.Task.StartedAt)
	case "completed":
		return dateCell(d.Task.CompletedAt)
	}
	return Cell{Text: none, Muted: true}
}

func dateCell(t *time.Time) Cell {
	if t == nil {
		return Cell{Text: "—", Muted: true}
	}
	return Cell{Text: t.Format("2 Jan 2006"), Muted: true}
}

func personName(nameOf map[string]string, id *string) string {
	if id == nil {
		return "—"
	}
	if n, ok := nameOf[*id]; ok && n != "" {
		return n
	}
	if len(*id) > 8 {
		return (*id)[:8]
	}
	return *id
}

func priorityWord(p int16) string {
	switch {
	case p >= 3:
		return "urgent"
	case p == 2:
		return "high"
	case p == 1:
		return "normal"
	default:
		return "low"
	}
}

// -----------------------------------------------------------------------------
// Board
// -----------------------------------------------------------------------------

// boardGroups arranges rows into columns, in an order that means something.
//
// Status reads todo to cancelled because that is the lifecycle. A select
// property reads in the order its options were defined, because somebody chose
// that order and alphabetising it throws away the information. Everything else
// falls back to whatever order the rows arrived in, which is the sort asked for.
func (s *Server) boardGroups(ctx context.Context, orgID string, q docs.Query,
	rows []*docs.Row, nameOf map[string]string) []docs.Group {

	var order []string
	labels := map[string]string{}

	switch {
	case q.GroupBy == docs.Col("status"):
		order = docs.Statuses
	case q.GroupBy == docs.Col("priority"):
		order = []string{"3", "2", "1", "0"}
		labels = map[string]string{"3": "urgent", "2": "high", "1": "normal", "0": "low"}
	case q.GroupBy == docs.Col("assignee"), q.GroupBy == docs.Col("owner"):
		for id, name := range nameOf {
			labels[id] = name
		}
		labels[""] = "Nobody"
	case q.GroupBy.IsProperty():
		if def, err := s.docs.PropertyDefByKey(ctx, orgID, q.GroupBy.Property); err == nil {
			order = def.Options
		} else if !errors.Is(err, docs.ErrNotFound) {
			s.log.ErrorContext(ctx, "load group property", "error", err)
		}
	}
	return docs.GroupRows(rows, order, labels)
}

// moveCard applies a drag: the card landed in a different column, so the field
// the board groups by takes that column's value.
func (s *Server) moveCard(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}
	field := docs.ParseField(r.FormValue("field"))
	if field.Zero() {
		http.Error(w, "no field", http.StatusBadRequest)
		return
	}

	ctx, closed := docs.WithClosedSink(r.Context())
	err := s.docs.SetField(ctx, id.OrgID, id.Subjects, docID, field, r.FormValue("value"))
	switch {
	case errors.Is(err, docs.ErrField):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, docs.ErrNotFound):
		http.Error(w, "no such document", http.StatusNotFound)
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "move card refused", "doc", docID, "error", err)
		http.Error(w, "you cannot change that", http.StatusForbidden)
		return
	}
	s.notify(id.OrgID, "documents")
	// The browser already moved the card optimistically. Confirming with no
	// content rather than re-rendering the board keeps the drag from flickering
	// back through its old position on every drop. A card dropped into done that
	// took subtasks with it says so in a header the board shows.
	if n := len(closed.IDs); n > 0 {
		w.Header().Set("X-Hearth-Notice", CascadeMessage(n, closed.Status))
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// Calendar
// -----------------------------------------------------------------------------

// calendar lays out a month grid and drops each row on its due date.
//
// Weeks start on Monday. Rows with no due date do not appear at all, which is
// correct for a calendar: an undated job is not happening on any particular
// day, and putting it on today would be an invention.
func calendar(monthParam string, rows []*docs.Row) (time.Time, [][]CalDay, string, string) {
	now := time.Now()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if monthParam != "" {
		if t, err := time.ParseInLocation("2006-01", monthParam, time.Local); err == nil {
			first = t
		}
	}

	byDay := map[string][]*docs.Row{}
	for _, row := range rows {
		if row.Task == nil || row.Task.DueAt == nil {
			continue
		}
		// A due date is a date, stored as midnight UTC (see updateDocument), so it is
		// read as one. Converting it to the server's zone first put it on the evening
		// before for anybody west of UTC.
		key := row.Task.DueAt.UTC().Format("2006-01-02")
		byDay[key] = append(byDay[key], row)
	}

	// Back up to the Monday on or before the first of the month.
	start := first
	for start.Weekday() != time.Monday {
		start = start.AddDate(0, 0, -1)
	}
	today := now.Format("2006-01-02")

	var weeks [][]CalDay
	for day := start; ; {
		week := make([]CalDay, 0, 7)
		for i := 0; i < 7; i++ {
			key := day.Format("2006-01-02")
			week = append(week, CalDay{
				Date:    day,
				InMonth: day.Month() == first.Month(),
				Today:   key == today,
				Rows:    byDay[key],
			})
			day = day.AddDate(0, 0, 1)
		}
		weeks = append(weeks, week)
		// Stop once the grid has passed the end of the month. Six rows is the
		// most any month needs and five is the most it usually takes.
		if day.After(first.AddDate(0, 1, -1)) {
			break
		}
	}
	return first, weeks,
		first.AddDate(0, -1, 0).Format("2006-01"),
		first.AddDate(0, 1, 0).Format("2006-01")
}

// -----------------------------------------------------------------------------
// Timeline
// -----------------------------------------------------------------------------

// timeline positions each row as a bar across the window the rows themselves
// define.
//
// A row runs from when it started, or was created if it has not, to when it is
// due, or when it was completed if it is finished. Something with neither a
// start nor an end is left out rather than drawn as a full-width bar, which
// would read as a job that takes the whole quarter.
func timeline(rows []*docs.Row) ([]Bar, time.Time, time.Time) {
	type span struct {
		row        *docs.Row
		start, end time.Time
	}
	var spans []span
	for _, row := range rows {
		if row.Task == nil {
			continue
		}
		start := row.CreatedAt
		if row.Task.StartedAt != nil {
			start = *row.Task.StartedAt
		}
		end := time.Time{}
		switch {
		case row.Task.CompletedAt != nil:
			end = *row.Task.CompletedAt
		case row.Task.DueAt != nil:
			end = *row.Task.DueAt
		}
		if end.IsZero() {
			continue
		}
		if end.Before(start) {
			// Completed before it was started, which happens when somebody
			// back-dates. Drawn as a point rather than a negative bar.
			end = start
		}
		spans = append(spans, span{row: row, start: start, end: end})
	}
	if len(spans) == 0 {
		now := time.Now()
		return nil, now, now.AddDate(0, 0, 30)
	}

	from, to := spans[0].start, spans[0].end
	for _, s := range spans {
		if s.start.Before(from) {
			from = s.start
		}
		if s.end.After(to) {
			to = s.end
		}
	}
	// A day of padding at each end, so a bar that ends on the last day is not
	// flush against the edge and unreadable.
	from = from.AddDate(0, 0, -1)
	to = to.AddDate(0, 0, 1)

	total := to.Sub(from).Seconds()
	if total <= 0 {
		total = 1
	}

	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })

	bars := make([]Bar, 0, len(spans))
	for _, s := range spans {
		left := s.start.Sub(from).Seconds() / total * 100
		width := s.end.Sub(s.start).Seconds() / total * 100
		// A minimum width, so a job that started and finished the same hour is
		// still something you can see and click.
		if width < 1 {
			width = 1
		}
		if left+width > 100 {
			width = 100 - left
		}
		status := ""
		if s.row.Task != nil {
			status = s.row.Task.Status
		}
		bars = append(bars, Bar{Doc: s.row.Document, LeftPct: left, WidthPct: width, Status: status})
	}
	return bars, from, to
}

// -----------------------------------------------------------------------------
// Saved views
// -----------------------------------------------------------------------------

func (s *Server) listViews(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	saved, err := s.views.List(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list views", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load your views")
		return
	}
	s.render(w, r, "views", page{Title: "Views", SavedViews: saved, Me: id.PrincipalID})
}

// saveView names the list somebody is looking at.
//
// The query is parsed from the submitted address rather than rebuilt from form
// fields, so what gets saved is exactly what is on screen. Rebuilding it would
// be a second encoder to keep in step with the first.
func (s *Server) saveView(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	from := r.FormValue("from")
	parsed, err := url.Parse(from)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the list that was being saved")
		return
	}
	q := docs.ParseQuery(parsed.Query())
	if q.Kind == "" {
		switch parsed.Path {
		case "/work-orders":
			q.Kind = docs.KindWorkOrder
		case "/assets":
			q.Kind = docs.KindAsset
		case "/tasks":
			q.Kind = docs.KindTask
		}
	}

	v, err := s.views.Save(r.Context(), id.OrgID, id.PrincipalID, views.View{
		Name:   r.FormValue("name"),
		Kind:   viewKindFrom(parsed.Query().Get("view")),
		Query:  q,
		Shared: r.FormValue("shared") != "",
	})
	switch {
	case errors.Is(err, views.ErrName):
		s.fail(w, r, http.StatusBadRequest, "give the view a name first")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "save view", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save that view")
		return
	}
	http.Redirect(w, r, v.Path(), http.StatusSeeOther)
}

func (s *Server) deleteView(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	err := s.views.Delete(r.Context(), id.OrgID, id.PrincipalID, r.PathValue("id"))
	switch {
	case errors.Is(err, views.ErrNotFound):
		// Either it is gone or it was never yours. Both are "no".
		s.fail(w, r, http.StatusNotFound, "no such view")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "delete view", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not remove that view")
		return
	}
	http.Redirect(w, r, "/views", http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// People
// -----------------------------------------------------------------------------

func (s *Server) addPerson(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	// No password typed means the person chooses their own through a link.
	var err error
	if strings.TrimSpace(r.FormValue("password")) == "" {
		var l *auth.PasswordLink
		if l, err = s.auth.AddPersonWithLink(r.Context(), id.OrgID, id.PrincipalID,
			r.FormValue("name"), r.FormValue("email")); err == nil {
			name := strings.TrimSpace(r.FormValue("name"))
			if name == "" {
				name = l.Email
			}
			s.showLink(w, r, l, name)
			return
		}
	} else {
		_, err = s.auth.AddPerson(r.Context(), id.OrgID, id.PrincipalID,
			r.FormValue("name"), r.FormValue("email"), r.FormValue("password"))
	}
	if err != nil {
		if errors.Is(err, auth.ErrNotOwner) {
			s.fail(w, r, http.StatusForbidden, "only the owner or a super admin can add accounts")
			return
		}
		// Everything else is something the owner can fix by retyping, so it is
		// shown rather than logged and hidden.
		list, _ := s.auth.People(r.Context(), id.OrgID)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "people", page{
			Title: "People", Persons: list, IsAdmin: true,
			Message: strings.TrimPrefix(err.Error(), "auth: "),
		})
		return
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

// setPersonStatus suspends or reinstates somebody.
func (s *Server) setPersonStatus(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.auth.SetPersonStatus(r.Context(), id.OrgID, id.PrincipalID,
		r.PathValue("id"), r.FormValue("status"))
	if err != nil {
		if errors.Is(err, auth.ErrNotOwner) {
			s.fail(w, r, http.StatusForbidden, "only the owner or a super admin can change an account")
			return
		}
		if errors.Is(err, auth.ErrIsOwner) {
			s.fail(w, r, http.StatusForbidden, "the owner's account cannot be suspended")
			return
		}
		list, _ := s.auth.People(r.Context(), id.OrgID)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "people", page{
			Title: "People", Persons: list, IsAdmin: true,
			Message: strings.TrimPrefix(err.Error(), "auth: "),
		})
		return
	}
	http.Redirect(w, r, backTo(r, "/people"), http.StatusSeeOther)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	must, err := s.auth.MustChangePassword(r.Context(), id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "read password state", "error", err)
	}
	// The Google connection, if the integration is configured at all. Not
	// connected is the ordinary state and not an error, so it renders the
	// offer rather than a failure.
	p := page{Title: "Your account", MustChange: must}
	if s.google != nil {
		p.GoogleOn = true
		if conn, err := s.google.Get(r.Context(), id.PrincipalID); err == nil {
			p.Google = conn
		}
	}
	s.render(w, r, "account", p)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.auth.ChangeOwnPassword(r.Context(), id.PrincipalID,
		r.FormValue("current"), r.FormValue("next"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "account", page{
			Title: "Your account", Message: strings.TrimPrefix(err.Error(), "auth: "),
			MustChange: id.MustChangePassword,
		})
		return
	}
	s.render(w, r, "account", page{Title: "Your account", Message: "Password changed."})
}
