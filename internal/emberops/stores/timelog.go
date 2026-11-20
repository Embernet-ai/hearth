package stores

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// TimeLog keeps time entries and timers in EmberOPS: an entry is a Timesheet
// Detail row (filed by the addon into its employee's draft Timesheet for the
// day), a running timer is a Hearth Timer, and a tag is an Activity Type.
//
// Time is EmberOPS-only in this mode, as time off is: Postgres keeps no spine
// row for an entry, nothing else hangs off an entry id.
//
// Who may approve whose time is still Hearth's structure (approver, department
// managers, super admin), read through the embedded Postgres store's
// CanApprove; who may see the org's time is still Hearth's capabilities, read
// through Authz. EmberOPS enforces its own DocPerms on every write as well.
// Estimates are written to both, because the estimate also lives on the
// document spine; capacity rates are Activity Costs. Pay rates and tag admin
// follow Hearth's capabilities.
type TimeLog struct {
	timelog.Store // CanApprove, and the document estimate on the spine
	Base
	Dir   Directory
	Authz timelog.Authorizer
	// Docs answers what a document is (its kind and title) and whether the
	// caller may open it. It is the Postgres document store.
	Docs docs.Store
}

var _ timelog.Store = (*TimeLog)(nil)

const (
	capTimeAll = "time_all"
	capFinance = "finance"
	capPeople  = "people"

	detailDoctype = "Timesheet Detail"
	listCap       = 5000
)

var detailFields = []string{"name", "hearth_id", "parent", "from_time", "to_time", "hours", "description", "task",
	"maintenance_work_order", "hearth_ref_doctype", "hearth_ref_name", "hearth_tags", "hearth_approval",
	"hearth_decided_by", "hearth_decided_at", "hearth_decision_note", "hearth_deleted_at", "activity_type"}

// ---- reading ----

func (s *TimeLog) detail(ctx context.Context, filters []emberops.Filter, order string, limit int) ([]emberops.Row, error) {
	if limit <= 0 {
		limit = listCap
	}
	return s.C.ListAll(ctx, emberops.ListQuery{Doctype: detailDoctype, Parent: "Timesheet", Fields: detailFields,
		Filters: filters, OrderBy: order}, limit)
}

func (s *TimeLog) employeeName(ctx context.Context, principalID string) (string, error) {
	ids, err := s.Dir.EmployeeIDs(ctx)
	if err != nil {
		return "", err
	}
	for name, id := range ids {
		if id == principalID {
			return name, nil
		}
	}
	return "", nil
}

// ofEmployee reads one person's rows in a window: their timesheets first, then
// the rows under them.
func (s *TimeLog) ofEmployee(ctx context.Context, emp string, from, to time.Time, extra ...emberops.Filter) ([]emberops.Row, error) {
	if emp == "" {
		return nil, nil
	}
	sheets, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Timesheet", Fields: []string{"name", "employee"},
		Filters: []emberops.Filter{emberops.F("employee", emp),
			{Field: "start_date", Op: ">=", Value: from.In(s.loc()).AddDate(0, 0, -1).Format("2006-01-02")},
			{Field: "start_date", Op: "<=", Value: to.In(s.loc()).Format("2006-01-02")}}}, listCap)
	if err != nil || len(sheets) == 0 {
		return nil, err
	}
	var names []any
	for _, t := range sheets {
		names = append(names, t.S("name"))
	}
	fs := append([]emberops.Filter{{Field: "parent", Op: "in", Value: names}}, s.window(from, to)...)
	return s.detail(ctx, append(fs, extra...), "from_time desc", 0)
}

func (s *TimeLog) window(from, to time.Time) []emberops.Filter {
	return []emberops.Filter{
		{Field: "from_time", Op: ">=", Value: s.tsv(from)},
		{Field: "from_time", Op: "<", Value: s.tsv(to)},
	}
}

type docRef struct{ id, title, space string }

// entryCtx holds what one page of entries needs resolved, each read once.
type entryCtx struct {
	sheetEmp map[string]string // timesheet name -> employee name
	emps     map[string]string // employee name -> principal id
	names    map[string]string // principal id -> display name
	users    map[string]string // user name -> principal id
	refs     map[string]docRef // doctype + "\x00" + name
	tags     map[string]timelog.Tag
	spaces   map[string]string
}

func refKey(doctype, name string) string { return doctype + "\x00" + name }

// targets lists the document a row is about as (doctype, name).
func target(r emberops.Row) (string, string) {
	switch {
	case r.Has("task"):
		return "Task", r.S("task")
	case r.Has("maintenance_work_order"):
		return "Maintenance Work Order", r.S("maintenance_work_order")
	case r.Has("hearth_ref_name"):
		dt := r.S("hearth_ref_doctype")
		if dt == "" {
			dt = "Hearth Document"
		}
		return dt, r.S("hearth_ref_name")
	}
	return "", ""
}

func (s *TimeLog) resolve(ctx context.Context, rows []emberops.Row, withSpace bool) (*entryCtx, error) {
	ec := &entryCtx{sheetEmp: map[string]string{}, refs: map[string]docRef{}, tags: map[string]timelog.Tag{}, spaces: map[string]string{}}
	var parents []any
	seenP := map[string]bool{}
	byType := map[string][]string{}
	anyDecided := false
	for _, r := range rows {
		if p := r.S("parent"); p != "" && !seenP[p] {
			seenP[p] = true
			parents = append(parents, p)
		}
		if dt, n := target(r); dt != "" {
			byType[dt] = append(byType[dt], n)
		}
		anyDecided = anyDecided || r.Has("hearth_decided_by")
	}
	for len(parents) > 0 {
		n := min(len(parents), 200)
		sheets, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Timesheet", Fields: []string{"name", "employee"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: parents[:n]}}})
		if err != nil {
			return nil, err
		}
		for _, t := range sheets {
			ec.sheetEmp[t.S("name")] = t.S("employee")
		}
		parents = parents[n:]
	}
	var err error
	if ec.emps, err = s.Dir.EmployeeIDs(ctx); err != nil {
		return nil, err
	}
	if ec.names, err = s.Dir.Names(ctx); err != nil {
		return nil, err
	}
	if anyDecided {
		if ec.users, err = s.Dir.UserIDs(ctx); err != nil {
			return nil, err
		}
	}
	spaceNames := []string{}
	for dt, names := range byType {
		spec := specFor(dt)
		fields := []string{"name", "hearth_id", spec.title}
		if withSpace {
			fields = append(fields, "hearth_space")
		}
		var in []any
		seen := map[string]bool{}
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				in = append(in, n)
			}
		}
		for len(in) > 0 {
			n := min(len(in), 200)
			got, err := s.C.List(ctx, emberops.ListQuery{Doctype: dt, Fields: fields, Limit: 500,
				Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in[:n]}}})
			if err != nil {
				return nil, err
			}
			for _, g := range got {
				id := g.S("hearth_id")
				if id == "" {
					id = g.S("name")
				}
				ec.refs[refKey(dt, g.S("name"))] = docRef{id: id, title: g.S(spec.title), space: g.S("hearth_space")}
				if g.Has("hearth_space") {
					spaceNames = append(spaceNames, g.S("hearth_space"))
				}
			}
			in = in[n:]
		}
	}
	if withSpace && len(spaceNames) > 0 {
		sp, err := s.spaceNames(ctx, spaceNames)
		if err != nil {
			return nil, err
		}
		ec.spaces = sp
	}
	tags, err := s.tags(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tags {
		ec.tags[t.Name] = t
	}
	return ec, nil
}

func (s *TimeLog) spaceNames(ctx context.Context, names []string) (map[string]string, error) {
	var in []any
	seen := map[string]bool{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			in = append(in, n)
		}
	}
	out := map[string]string{}
	for len(in) > 0 {
		n := min(len(in), 200)
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Hearth Space", Fields: []string{"name", "space_name"}, Limit: 500, Reference: true,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in[:n]}}})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.S("name")] = r.S("space_name")
		}
		in = in[n:]
	}
	return out, nil
}

func minutesOf(r emberops.Row) int { return int(math.Round(r.F("hours") * 60)) }

func (s *TimeLog) entry(ec *entryCtx, r emberops.Row) timelog.Entry {
	e := timelog.Entry{
		ID: r.S("hearth_id"), StartedAt: r.T("from_time", s.loc()), EndedAt: r.TP("to_time", s.loc()),
		Note: r.S("description"), DecidedAt: r.TP("hearth_decided_at", s.loc()), DecidedNote: r.S("hearth_decision_note"),
		DeletedAt: r.TP("hearth_deleted_at", s.loc()),
	}
	if e.ID == "" {
		e.ID = r.S("name")
	}
	if e.EndedAt != nil {
		m := minutesOf(r)
		e.Minutes = &m
	}
	e.PrincipalID = ec.emps[ec.sheetEmp[r.S("parent")]]
	e.PersonName = ec.names[e.PrincipalID]
	if e.PersonName == "" {
		e.PersonName = e.PrincipalID
	}
	if dt, n := target(r); dt != "" {
		if ref, ok := ec.refs[refKey(dt, n)]; ok {
			e.DocumentID, e.DocTitle = ref.id, ref.title
		} else {
			e.DocumentID = n
		}
	}
	switch r.S("hearth_approval") {
	case "approved":
		t := true
		e.Approved = &t
	case "denied":
		f := false
		e.Approved = &f
	}
	if r.Has("hearth_decided_by") {
		e.DecidedBy = ec.users[r.S("hearth_decided_by")]
		e.DeciderName = ec.names[e.DecidedBy]
	}
	for _, n := range splitTags(r.S("hearth_tags")) {
		if t, ok := ec.tags[n]; ok {
			e.Tags = append(e.Tags, t)
		} else {
			e.Tags = append(e.Tags, timelog.Tag{ID: n, Name: n})
		}
	}
	sort.SliceStable(e.Tags, func(i, j int) bool { return e.Tags[i].Name < e.Tags[j].Name })
	return e
}

func splitTags(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *TimeLog) entries(ctx context.Context, rows []emberops.Row) ([]timelog.Entry, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ec, err := s.resolve(ctx, rows, false)
	if err != nil {
		return nil, err
	}
	out := make([]timelog.Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.entry(ec, r))
	}
	return out, nil
}

func (s *TimeLog) row(ctx context.Context, entryID string) (emberops.Row, error) {
	r, err := s.C.GetByHearthID(ctx, detailDoctype, entryID)
	if err != nil {
		return nil, notFound(err, timelog.ErrNotFound)
	}
	return r, nil
}

// owner is the principal an existing row belongs to, and the employee's Frappe name.
func (s *TimeLog) owner(ctx context.Context, r emberops.Row) (principal, employee string, err error) {
	sheets, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Timesheet", Fields: []string{"name", "employee"}, Limit: 1,
		Filters: []emberops.Filter{emberops.F("name", r.S("parent"))}})
	if err != nil || len(sheets) == 0 {
		return "", "", err
	}
	emps, err := s.Dir.EmployeeIDs(ctx)
	if err != nil {
		return "", "", err
	}
	employee = sheets[0].S("employee")
	return emps[employee], employee, nil
}

func (s *TimeLog) Mine(ctx context.Context, orgID, principalID string, from, to time.Time, showRemoved bool) ([]timelog.Entry, error) {
	if to.Before(from) {
		from, to = to, from
	}
	emp, err := s.employeeName(ctx, principalID)
	if err != nil {
		return nil, err
	}
	var extra []emberops.Filter
	if !showRemoved {
		extra = append(extra, emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"})
	}
	rows, err := s.ofEmployee(ctx, emp, from, to, extra...)
	if err != nil {
		return nil, err
	}
	return s.entries(ctx, rows)
}

func (s *TimeLog) MineTotal(ctx context.Context, orgID, principalID string, from, to time.Time) (total, pending int, err error) {
	if to.Before(from) {
		from, to = to, from
	}
	emp, err := s.employeeName(ctx, principalID)
	if err != nil {
		return 0, 0, err
	}
	rows, err := s.ofEmployee(ctx, emp, from, to, emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
		emberops.Filter{Field: "to_time", Op: "is", Value: "set"})
	if err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		m := minutesOf(r)
		total += m
		if r.S("hearth_approval") == "" || r.S("hearth_approval") == "pending" {
			pending += m
		}
	}
	return total, pending, nil
}

// docFilter selects the rows filed against one document.
func docFilter(kind, name string) []emberops.Filter {
	switch kind {
	case docs.KindTask:
		return []emberops.Filter{emberops.F("task", name)}
	case docs.KindWorkOrder:
		return []emberops.Filter{emberops.F("maintenance_work_order", name)}
	}
	dt := doctypeFor(kind)
	if dt == "" {
		dt = "Hearth Document"
	}
	return []emberops.Filter{emberops.F("hearth_ref_doctype", dt), emberops.F("hearth_ref_name", name)}
}

// forDoc finds the rows filed against a document the actor may open.
func (s *TimeLog) forDoc(ctx context.Context, orgID string, actor core.Subjects, docID string) (kind string, rows []emberops.Row, err error) {
	d, err := s.Docs.Get(ctx, orgID, actor, docID)
	if err != nil {
		return "", nil, timelog.ErrNotFound
	}
	dt := doctypeFor(d.Kind)
	if dt == "" {
		return d.Kind, nil, nil
	}
	name, err := s.C.Resolve(ctx, dt, docID)
	if err != nil || name == "" {
		return d.Kind, nil, err
	}
	rows, err = s.detail(ctx, append(docFilter(d.Kind, name), emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"}), "from_time desc", 0)
	return d.Kind, rows, err
}

func (s *TimeLog) Entries(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]timelog.Entry, error) {
	_, rows, err := s.forDoc(ctx, orgID, actor, docID)
	if err != nil {
		return nil, err
	}
	return s.entries(ctx, rows)
}

// ---- writing ----

func targetKind(kind string) string {
	switch kind {
	case docs.KindTask:
		return "task"
	case docs.KindWorkOrder:
		return "work_order"
	case docs.KindAsset:
		return "asset"
	case docs.KindPurchaseOrder:
		return "purchase_order"
	}
	return "doc"
}

func (s *TimeLog) record(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes int, on time.Time, note string, requireDoc bool) (*timelog.Entry, error) {
	kind := ""
	if docID != "" {
		if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
			return nil, err
		}
		d, err := s.Docs.Get(ctx, orgID, actor, docID)
		if err != nil {
			return nil, timelog.ErrNotFound
		}
		kind = d.Kind
	} else if requireDoc {
		return nil, errors.New("timelog: choose a document")
	}
	if minutes <= 0 {
		return nil, errors.New("timelog: how long did it take?")
	}
	if minutes > 100000 {
		return nil, errors.New("timelog: that is more than two months of work")
	}
	if on.IsZero() {
		on = time.Now()
	}
	end := on.Add(time.Duration(minutes) * time.Minute)
	id := uuid.NewString()
	rec := mapping.Rec{"id": id, "principal_id": actor.Principal(), "started_at": on.UTC().Format(time.RFC3339),
		"ended_at": end.UTC().Format(time.RFC3339), "minutes": float64(minutes), "note": strings.TrimSpace(note)}
	if docID != "" {
		rec["document_id"], rec["target_kind"] = docID, targetKind(kind)
	}
	row := mapping.TZ{Loc: s.loc()}.TimeEntryRow(rec, nil, "")
	delete(row, "doctype")
	if _, err := s.C.UpsertTimeEntry(ctx, row); err != nil {
		if isForbidden(err) {
			return nil, timelog.ErrForbidden
		}
		return nil, err
	}
	return &timelog.Entry{ID: id, DocumentID: docID, PrincipalID: actor.Principal(), StartedAt: on, EndedAt: &end,
		Minutes: &minutes, Note: strings.TrimSpace(note)}, nil
}

func (s *TimeLog) Log(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes int, on time.Time, note string) (*timelog.Entry, error) {
	return s.record(ctx, orgID, actor, docID, minutes, on, note, true)
}

func (s *TimeLog) LogOwn(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes int, on time.Time, note string) (*timelog.Entry, error) {
	return s.record(ctx, orgID, actor, docID, minutes, on, note, false)
}

// authority is the permission Correct, Remove and Restore share: the entry's
// author, or an admin of the document it is filed against.
func (s *TimeLog) authority(ctx context.Context, orgID string, actor core.Subjects, entryID string) (emberops.Row, string, error) {
	r, err := s.row(ctx, entryID)
	if err != nil {
		return nil, "", err
	}
	author, emp, err := s.owner(ctx, r)
	if err != nil {
		return nil, "", err
	}
	if author == actor.Principal() {
		return r, emp, nil
	}
	dt, name := target(r)
	if dt == "" {
		return nil, "", timelog.ErrForbidden
	}
	id := name
	if ref, err := s.Dir.HearthIDs(ctx, dt, []string{name}); err == nil {
		id = ref[name]
	}
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: id}, core.AccessAdmin); err != nil {
		return nil, "", err
	}
	return r, emp, nil
}

func (s *TimeLog) patch(ctx context.Context, entryID, emp string, vals map[string]any) error {
	vals["hearth_id"] = entryID
	vals["employee"] = emp
	_, err := s.C.UpsertTimeEntry(ctx, vals)
	if isForbidden(err) {
		return timelog.ErrForbidden
	}
	return err
}

func (s *TimeLog) Correct(ctx context.Context, orgID string, actor core.Subjects, entryID string, minutes int, on *time.Time, note string) error {
	r, emp, err := s.authority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if r.Has("hearth_deleted_at") {
		return timelog.ErrDeleted
	}
	if minutes < 0 || minutes > 100000 {
		return errors.New("timelog: that duration is not plausible")
	}
	start := r.T("from_time", s.loc())
	if on != nil {
		start = *on
	}
	end := start.Add(time.Duration(minutes) * time.Minute)
	vals := map[string]any{"from_time": s.tsv(start), "to_time": s.tsv(end), "hours": math.Round(float64(minutes)/60*10000) / 10000}
	if n := strings.TrimSpace(note); n != "" {
		vals["description"] = n
	}
	return s.patch(ctx, entryID, emp, vals)
}

func (s *TimeLog) Remove(ctx context.Context, orgID string, actor core.Subjects, entryID string) error {
	r, emp, err := s.authority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if r.Has("hearth_deleted_at") {
		return nil
	}
	now := time.Now()
	if err := s.patch(ctx, entryID, emp, map[string]any{"hearth_deleted_at": s.tsv(now)}); err != nil {
		return err
	}
	return s.auditRemoval(ctx, orgID, actor.Principal(), entryID, "remove", "", r)
}

// auditRemoval writes the audit row in Hearth's own database: the audit log
// stays in Postgres whichever back end holds the entry.
func (s *TimeLog) auditRemoval(ctx context.Context, orgID, actorID, entryID, via, reason string, r emberops.Row) error {
	b := timelog.RemovedEntry{StartedAt: r.T("from_time", s.loc()), Note: r.S("description")}
	if r.TP("to_time", s.loc()) != nil {
		m := minutesOf(r)
		b.Minutes = &m
	}
	if p, _, err := s.owner(ctx, r); err == nil {
		b.Person = p
	}
	return s.Store.AuditRemoval(ctx, orgID, actorID, entryID, via, reason, b)
}

// RemoveMany is the admin removal against EmberOPS: a super admin names entries
// and a reason, and apply false reports without changing anything.
func (s *TimeLog) RemoveMany(ctx context.Context, orgID string, actor core.Subjects, ids []string, reason string, apply bool) ([]timelog.RemovalLine, error) {
	ok, err := s.Authz.HasCapability(ctx, orgID, actor.Principal(), "super_admin")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, timelog.ErrNotAllowed
	}
	reason = strings.TrimSpace(reason)
	if apply && reason == "" {
		return nil, timelog.ErrNeedsReason
	}
	var out []timelog.RemovalLine
	for _, id := range ids {
		id = strings.TrimSpace(id)
		r, err := s.row(ctx, id)
		if err != nil {
			return out, fmt.Errorf("%s: %w", id, err)
		}
		principal, emp, err := s.owner(ctx, r)
		if err != nil {
			return out, err
		}
		l := timelog.RemovalLine{ID: id, Person: principal, StartedAt: r.T("from_time", s.loc()), Note: r.S("description"),
			Minutes: minutesOf(r), AlreadyRemoved: r.Has("hearth_deleted_at")}
		if apply && !l.AlreadyRemoved {
			if err := s.patch(ctx, id, emp, map[string]any{"hearth_deleted_at": s.tsv(time.Now())}); err != nil {
				return out, err
			}
			if err := s.auditRemoval(ctx, orgID, actor.Principal(), id, "admin remove", reason, r); err != nil {
				return out, err
			}
			l.Removed = true
		}
		out = append(out, l)
	}
	return out, nil
}

func (s *TimeLog) Restore(ctx context.Context, orgID string, actor core.Subjects, entryID string) error {
	r, emp, err := s.authority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if !r.Has("hearth_deleted_at") {
		return nil
	}
	return s.patch(ctx, entryID, emp, map[string]any{"hearth_deleted_at": nil})
}

// ---- timers ----

func (s *TimeLog) Start(ctx context.Context, orgID string, actor core.Subjects, docID, note string) (*timelog.Entry, error) {
	var task, wo string
	if docID != "" {
		if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
			return nil, err
		}
		d, err := s.Docs.Get(ctx, orgID, actor, docID)
		if err != nil {
			return nil, timelog.ErrNotFound
		}
		// A timer can point at a task or a work order. Another kind of
		// document starts an unattached timer: Hearth Timer has no field for it.
		if dt := doctypeFor(d.Kind); d.Kind == docs.KindTask || d.Kind == docs.KindWorkOrder {
			name, err := s.C.Resolve(ctx, dt, docID)
			if err != nil {
				return nil, err
			}
			if d.Kind == docs.KindTask {
				task = name
			} else {
				wo = name
			}
		}
	}
	if err := s.C.TimerStart(ctx, task, wo, strings.TrimSpace(note), nil); err != nil {
		if errors.Is(err, emberops.ErrDuplicate) || errors.Is(err, emberops.ErrConflict) {
			return nil, timelog.ErrAlreadyRunning
		}
		return nil, err
	}
	return s.Running(ctx, orgID, actor.Principal())
}

func (s *TimeLog) Stop(ctx context.Context, orgID, principalID string) (*timelog.Entry, error) {
	id := uuid.NewString()
	if _, err := s.C.TimerStop(ctx, id, ""); err != nil {
		if errors.Is(err, emberops.ErrNotFound) {
			return nil, timelog.ErrNotRunning
		}
		return nil, err
	}
	r, err := s.row(ctx, id)
	if err != nil {
		return nil, err
	}
	es, err := s.entries(ctx, []emberops.Row{r})
	if err != nil || len(es) == 0 {
		return nil, err
	}
	es[0].PrincipalID = principalID
	return &es[0], nil
}

func (s *TimeLog) Running(ctx context.Context, orgID, principalID string) (*timelog.Entry, error) {
	emp, err := s.employeeName(ctx, principalID)
	if err != nil || emp == "" {
		return nil, err
	}
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Hearth Timer", Limit: 1,
		Fields:  []string{"name", "started_at", "task", "maintenance_work_order", "note"},
		Filters: []emberops.Filter{emberops.F("employee", emp)}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	t := rows[0]
	e := &timelog.Entry{ID: t.S("name"), PrincipalID: principalID, StartedAt: t.T("started_at", s.loc()), Note: t.S("note")}
	if dt, n := target(t); dt != "" {
		if ref, err := s.Dir.HearthIDs(ctx, dt, []string{n}); err == nil {
			e.DocumentID = ref[n]
		}
		if g, err := s.C.List(ctx, emberops.ListQuery{Doctype: dt, Fields: []string{"name", specFor(dt).title}, Limit: 1,
			Filters: []emberops.Filter{emberops.F("name", n)}}); err == nil && len(g) == 1 {
			e.DocTitle = g[0].S(specFor(dt).title)
		}
	}
	return e, nil
}

// ---- approval ----

func (s *TimeLog) Pending(ctx context.Context, orgID, actorID string) ([]timelog.Entry, error) {
	es, err := s.pendingAll(ctx)
	if err != nil {
		return nil, err
	}
	return s.mayDecide(ctx, orgID, actorID, es)
}

func (s *TimeLog) PendingCount(ctx context.Context, orgID, actorID string) (int, error) {
	es, err := s.Pending(ctx, orgID, actorID)
	return len(es), err
}

func (s *TimeLog) pendingAll(ctx context.Context) ([]timelog.Entry, error) {
	rows, err := s.detail(ctx, []emberops.Filter{emberops.F("hearth_approval", "pending"),
		{Field: "to_time", Op: "is", Value: "set"}, {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}, "from_time asc", 0)
	if err != nil {
		return nil, err
	}
	return s.entries(ctx, rows)
}

// mayDecide keeps the entries whose author the actor may approve: one
// CanApprove per distinct author, read from Hearth's structure.
func (s *TimeLog) mayDecide(ctx context.Context, orgID, actorID string, es []timelog.Entry) ([]timelog.Entry, error) {
	ok := map[string]bool{}
	var out []timelog.Entry
	for _, e := range es {
		if e.PrincipalID == "" || e.PrincipalID == actorID {
			continue
		}
		allowed, seen := ok[e.PrincipalID]
		if !seen {
			var err error
			if allowed, err = s.Store.CanApprove(ctx, orgID, actorID, e.PrincipalID); err != nil {
				return nil, err
			}
			ok[e.PrincipalID] = allowed
		}
		if allowed {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *TimeLog) decide(ctx context.Context, orgID, actorID string, ids []string, approve bool, note string) (int, error) {
	var want []any
	for _, id := range ids {
		want = append(want, id)
	}
	rows, err := s.detail(ctx, []emberops.Filter{{Field: "hearth_id", Op: "in", Value: want}}, "", 0)
	if err != nil {
		return 0, err
	}
	es, err := s.entries(ctx, rows)
	if err != nil {
		return 0, err
	}
	byID := map[string]emberops.Row{}
	for _, r := range rows {
		byID[strings.ToLower(r.S("hearth_id"))] = r
	}
	ok, err := s.mayDecide(ctx, orgID, actorID, es)
	if err != nil {
		return 0, err
	}
	allowed := map[string]bool{}
	for _, e := range ok {
		if e.EndedAt != nil && e.DeletedAt == nil && e.DecidedAt == nil {
			allowed[strings.ToLower(e.ID)] = true
		}
	}
	// All or nothing on authority, checked before any write.
	for _, id := range ids {
		if !allowed[strings.ToLower(id)] {
			return 0, timelog.ErrNotYours
		}
	}
	status := "denied"
	if approve {
		status = "approved"
	}
	now := time.Now()
	for _, id := range ids {
		_, emp, err := s.owner(ctx, byID[strings.ToLower(id)])
		if err != nil {
			return 0, err
		}
		if err := s.patch(ctx, id, emp, map[string]any{"hearth_approval": status, "hearth_decided_by": emberops.Ref(actorID),
			"hearth_decided_at": s.tsv(now), "hearth_decision_note": strings.TrimSpace(note)}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *TimeLog) Decide(ctx context.Context, orgID, actorID, entryID string, approve bool, note string) error {
	_, err := s.decide(ctx, orgID, actorID, []string{entryID}, approve, note)
	return err
}

func (s *TimeLog) ApproveMany(ctx context.Context, orgID, actorID string, entryIDs []string) (int, error) {
	seen := map[string]bool{}
	var ids []string
	for _, id := range entryIDs {
		if _, err := uuid.Parse(id); err != nil {
			return 0, errors.New("timelog: that is not an entry")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, errors.New("timelog: choose at least one entry")
	}
	n, err := s.decide(ctx, orgID, actorID, ids, true, "")
	if errors.Is(err, timelog.ErrNotYours) {
		return 0, timelog.ErrBatchRefused
	}
	return n, err
}

func (s *TimeLog) Reopen(ctx context.Context, orgID, actorID, entryID string) error {
	r, err := s.row(ctx, entryID)
	if err != nil {
		return timelog.ErrNotYours
	}
	author, emp, err := s.owner(ctx, r)
	if err != nil || !r.Has("hearth_decided_at") || author == actorID {
		return timelog.ErrNotYours
	}
	if ok, err := s.Store.CanApprove(ctx, orgID, actorID, author); err != nil || !ok {
		return timelog.ErrNotYours
	}
	return s.patch(ctx, entryID, emp, map[string]any{"hearth_approval": "pending", "hearth_decided_by": nil,
		"hearth_decided_at": nil, "hearth_decision_note": nil})
}

func (s *TimeLog) Decided(ctx context.Context, orgID, actorID string, from, to time.Time, seeAll bool) ([]timelog.Entry, error) {
	if to.Before(from) {
		from, to = to, from
	}
	fs := append([]emberops.Filter{{Field: "hearth_decided_at", Op: "is", Value: "set"}}, s.window(from, to)...)
	if !seeAll {
		email, err := s.userName(ctx, actorID)
		if err != nil {
			return nil, err
		}
		fs = append(fs, emberops.F("hearth_decided_by", email))
	}
	rows, err := s.detail(ctx, fs, "hearth_decided_at desc, from_time desc, hearth_id", 200)
	if err != nil {
		return nil, err
	}
	if len(rows) > 200 {
		rows = rows[:200]
	}
	return s.entries(ctx, rows)
}

func (s *TimeLog) userName(ctx context.Context, principalID string) (string, error) {
	m, err := s.Dir.Emails(ctx)
	return m[principalID], err
}

func (s *TimeLog) CanApprove(ctx context.Context, orgID, actorID, subjectID string) (bool, error) {
	return s.Store.CanApprove(ctx, orgID, actorID, subjectID)
}

// ---- tags ----

func (s *TimeLog) tags(ctx context.Context) ([]timelog.Tag, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Activity Type", Reference: true, OrderBy: "name asc",
		// Only Hearth's tags: ERPNext ships Activity Types of its own (Execution, Planning...) that are not Hearth's.
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}},
		Fields:  []string{"name", "hearth_id", "hearth_description"}}, listCap)
	if err != nil {
		return nil, err
	}
	out := make([]timelog.Tag, 0, len(rows))
	for _, r := range rows {
		id := r.S("hearth_id")
		if id == "" {
			id = r.S("name")
		}
		out = append(out, timelog.Tag{ID: id, Name: r.S("name"), Description: r.S("hearth_description")})
	}
	return out, nil
}

func (s *TimeLog) ListTags(ctx context.Context, orgID string) ([]timelog.Tag, error) {
	return s.tags(ctx)
}

func (s *TimeLog) requireTagAdmin(ctx context.Context, orgID, actorID string) error {
	ok, err := s.Authz.HasCapability(ctx, orgID, actorID, capPeople)
	if err != nil {
		return err
	}
	if !ok {
		return timelog.ErrNotAllowed
	}
	return nil
}

func (s *TimeLog) CreateTag(ctx context.Context, orgID, actorID, name, description string) (*timelog.Tag, error) {
	if err := s.requireTagAdmin(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ",") {
		return nil, errors.New("timelog: a tag needs a name without commas")
	}
	id := uuid.NewString()
	if _, err := s.C.Upsert(ctx, "Activity Type", id, map[string]any{"activity_type": name, "hearth_description": strings.TrimSpace(description)}); err != nil {
		if errors.Is(err, emberops.ErrDuplicate) {
			return nil, fmt.Errorf("timelog: there is already a tag called %q", name)
		}
		return nil, err
	}
	return &timelog.Tag{ID: id, Name: name, Description: strings.TrimSpace(description)}, nil
}

func (s *TimeLog) UpdateTag(ctx context.Context, orgID, actorID, tagID, name, description string) error {
	if err := s.requireTagAdmin(ctx, orgID, actorID); err != nil {
		return err
	}
	cur, err := s.C.GetByHearthID(ctx, "Activity Type", tagID)
	if err != nil {
		return notFound(err, timelog.ErrNotFound)
	}
	name = strings.TrimSpace(name)
	// A tag is named by its Activity Type, which Frappe renames in place; the
	// tags already written on entries keep the old text until they are edited.
	if name != "" && name != cur.S("name") {
		if err := s.C.Call(ctx, "frappe.client.rename_doc", map[string]any{"doctype": "Activity Type",
			"old_name": cur.S("name"), "new_name": name}, nil); err != nil {
			return err
		}
	}
	_, err = s.C.Upsert(ctx, "Activity Type", tagID, map[string]any{"hearth_description": strings.TrimSpace(description)})
	return err
}

func (s *TimeLog) DeleteTag(ctx context.Context, orgID, actorID, tagID string) error {
	if err := s.requireTagAdmin(ctx, orgID, actorID); err != nil {
		return err
	}
	return notFound(s.C.DeleteByHearthID(ctx, "Activity Type", tagID), timelog.ErrNotFound)
}

func (s *TimeLog) tagName(ctx context.Context, tagID string) (string, error) {
	ts, err := s.tags(ctx)
	if err != nil {
		return "", err
	}
	for _, t := range ts {
		if t.ID == tagID {
			return t.Name, nil
		}
	}
	return "", timelog.ErrNotFound
}

func (s *TimeLog) EntryTags(ctx context.Context, orgID, entryID string) ([]timelog.Tag, error) {
	r, err := s.row(ctx, entryID)
	if err != nil {
		return nil, err
	}
	all, err := s.tags(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]timelog.Tag{}
	for _, t := range all {
		by[t.Name] = t
	}
	var out []timelog.Tag
	for _, n := range splitTags(r.S("hearth_tags")) {
		if t, ok := by[n]; ok {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *TimeLog) setTags(ctx context.Context, entryID, emp string, tags []string) error {
	vals := map[string]any{"hearth_tags": strings.Join(tags, ",")}
	if len(tags) > 0 {
		vals["activity_type"] = tags[0]
	}
	return s.patch(ctx, entryID, emp, vals)
}

func (s *TimeLog) TagEntry(ctx context.Context, orgID string, actor core.Subjects, entryID, tagID string) error {
	r, emp, err := s.authority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if r.Has("hearth_deleted_at") {
		return timelog.ErrDeleted
	}
	name, err := s.tagName(ctx, tagID)
	if err != nil {
		return err
	}
	have := splitTags(r.S("hearth_tags"))
	for _, h := range have {
		if h == name {
			return nil
		}
	}
	return s.setTags(ctx, entryID, emp, append(have, name))
}

func (s *TimeLog) UntagEntry(ctx context.Context, orgID string, actor core.Subjects, entryID, tagID string) error {
	r, emp, err := s.authority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if r.Has("hearth_deleted_at") {
		return timelog.ErrDeleted
	}
	name, err := s.tagName(ctx, tagID)
	if err != nil {
		return err
	}
	var keep []string
	for _, h := range splitTags(r.S("hearth_tags")) {
		if h != name {
			keep = append(keep, h)
		}
	}
	return s.setTags(ctx, entryID, emp, keep)
}

func (s *TimeLog) BatchTag(ctx context.Context, orgID string, actor core.Subjects, principalID, tagID string, from, to time.Time) (int, error) {
	if actor.Principal() != principalID {
		return 0, timelog.ErrForbidden
	}
	if to.Before(from) {
		from, to = to, from
	}
	name, err := s.tagName(ctx, tagID)
	if err != nil {
		return 0, err
	}
	emp, err := s.employeeName(ctx, principalID)
	if err != nil {
		return 0, err
	}
	rows, err := s.ofEmployee(ctx, emp, from, to, emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
		emberops.Filter{Field: "to_time", Op: "is", Value: "set"})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		have := splitTags(r.S("hearth_tags"))
		if contains(have, name) {
			continue
		}
		if err := s.setTags(ctx, r.S("hearth_id"), emp, append(have, name)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- estimates, effort, capacity, workload, reports ----

// estimateField is where a document's estimate lives, with its unit (hours).
func estimateField(kind string) string {
	switch kind {
	case docs.KindTask:
		return "expected_time"
	case docs.KindWorkOrder:
		return "estimated_hours"
	}
	return ""
}

func (s *TimeLog) SetEstimate(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes *int) error {
	// The spine keeps the estimate too (rolling back to Postgres needs it), and
	// its write enforces Hearth's own edit permission first.
	if err := s.Store.SetEstimate(ctx, orgID, actor, docID, minutes); err != nil {
		return err
	}
	d, err := s.Docs.Get(ctx, orgID, actor, docID)
	if err != nil {
		return timelog.ErrNotFound
	}
	field := estimateField(d.Kind)
	if field == "" {
		return nil
	}
	var v any
	if minutes != nil {
		v = math.Round(float64(*minutes)/60*10000) / 10000
	}
	_, err = s.C.Upsert(ctx, doctypeFor(d.Kind), docID, map[string]any{field: v})
	return err
}

// rates is Employee name -> cents per hour, from Activity Cost.
func (s *TimeLog) rates(ctx context.Context) (map[string]int, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Activity Cost", Reference: true,
		Fields: []string{"employee", "costing_rate"}}, listCap)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		if r.Has("employee") && r.F("costing_rate") > 0 {
			out[r.S("employee")] = int(math.Round(r.F("costing_rate") * 100))
		}
	}
	return out, nil
}

func (s *TimeLog) EffortFor(ctx context.Context, orgID string, actor core.Subjects, docID string) (*timelog.Effort, error) {
	d, err := s.Docs.Get(ctx, orgID, actor, docID)
	if err != nil {
		return nil, timelog.ErrNotFound
	}
	out := &timelog.Effort{}
	dt := doctypeFor(d.Kind)
	if dt == "" {
		return out, nil
	}
	doc, err := s.C.GetByHearthID(ctx, dt, docID)
	if err != nil {
		return nil, notFound(err, timelog.ErrNotFound)
	}
	if f := estimateField(d.Kind); f != "" && doc.F(f) > 0 {
		m := int(math.Round(doc.F(f) * 60))
		out.EstimateMinutes = &m
	}
	rows, err := s.detail(ctx, append(docFilter(d.Kind, doc.S("name")),
		emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
		emberops.Filter{Field: "to_time", Op: "is", Value: "set"}), "", 0)
	if err != nil {
		return nil, err
	}
	cost, err := s.cost(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.ActualMinutes += minutesOf(r)
		out.Entries++
	}
	out.CostCents = cost
	return out, nil
}

// cost prices rows by their employee's rate; nil when nobody involved has one.
func (s *TimeLog) cost(ctx context.Context, rows []emberops.Row) (*int, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	rates, err := s.rates(ctx)
	if err != nil || len(rates) == 0 {
		return nil, err
	}
	ec, err := s.resolve(ctx, rows, false)
	if err != nil {
		return nil, err
	}
	var total float64
	priced := false
	for _, r := range rows {
		if rate, ok := rates[ec.sheetEmp[r.S("parent")]]; ok {
			total += float64(minutesOf(r)) * float64(rate) / 60
			priced = true
		}
	}
	if !priced {
		return nil, nil
	}
	c := int(math.Round(total))
	return &c, nil
}

func (s *TimeLog) ForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string) (*timelog.AssetEffort, error) {
	a, err := s.Docs.Get(ctx, orgID, actor, assetID)
	if err != nil {
		return nil, timelog.ErrNotFound
	}
	out := &timelog.AssetEffort{AssetID: assetID, AssetTitle: a.Title}
	if out.AssetTitle == "" {
		out.AssetTitle = "Untitled"
	}
	name, err := s.C.Resolve(ctx, "Asset", assetID)
	if err != nil || name == "" {
		return out, err
	}
	// Work orders that name the asset, plus every task or work order linked to it.
	wos := map[string]bool{}
	wrows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Maintenance Work Order", Fields: []string{"name"},
		Filters: []emberops.Filter{emberops.F("asset", name)}}, listCap)
	if err != nil {
		return nil, err
	}
	for _, r := range wrows {
		wos[r.S("name")] = true
	}
	tasks := map[string]bool{}
	linked, err := s.Docs.Linked(ctx, orgID, actor, assetID)
	if err != nil {
		return nil, err
	}
	byKind := map[string][]any{}
	for _, l := range linked {
		if l.Kind == docs.KindTask || l.Kind == docs.KindWorkOrder {
			byKind[doctypeFor(l.Kind)] = append(byKind[doctypeFor(l.Kind)], l.ID)
		}
	}
	for dt, ids := range byKind {
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: dt, Fields: []string{"name"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids}}})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if dt == "Task" {
				tasks[r.S("name")] = true
			} else {
				wos[r.S("name")] = true
			}
		}
	}
	var rows []emberops.Row
	for _, q := range []struct {
		field string
		set   map[string]bool
	}{{"maintenance_work_order", wos}, {"task", tasks}} {
		if len(q.set) == 0 {
			continue
		}
		var in []any
		for n := range q.set {
			in = append(in, n)
		}
		got, err := s.detail(ctx, []emberops.Filter{{Field: q.field, Op: "in", Value: in},
			{Field: "hearth_deleted_at", Op: "is", Value: "not set"}, {Field: "to_time", Op: "is", Value: "set"}}, "", 0)
		if err != nil {
			return nil, err
		}
		rows = append(rows, got...)
	}
	docsSeen := map[string]bool{}
	for _, r := range rows {
		out.Minutes += minutesOf(r)
		_, n := target(r)
		docsSeen[n] = true
	}
	out.WorkOrders = len(docsSeen)
	out.CostCents, err = s.cost(ctx, rows)
	return out, err
}

func (s *TimeLog) SetCapacity(ctx context.Context, orgID, principalID string, minutesPerWeek int, rateCents *int) error {
	if minutesPerWeek < 0 || minutesPerWeek > 10080 {
		return errors.New("timelog: a week has 10080 minutes")
	}
	if _, err := s.C.Upsert(ctx, "Employee", principalID, map[string]any{"hearth_minutes_per_week": minutesPerWeek}); err != nil {
		return err
	}
	if rateCents == nil {
		return nil
	}
	emp, err := s.employeeName(ctx, principalID)
	if err != nil || emp == "" {
		return err
	}
	settings, err := s.C.Get(ctx, "Hearth Settings", "Hearth Settings")
	if err != nil {
		return err
	}
	at := settings.S("default_activity_type")
	if at == "" {
		return errors.New("timelog: set the default Activity Type in Hearth Settings before setting pay rates")
	}
	rate := float64(*rateCents) / 100
	cur, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Activity Cost", Fields: []string{"name"}, Limit: 1,
		Filters: []emberops.Filter{emberops.F("employee", emp), emberops.F("activity_type", at)}})
	if err != nil {
		return err
	}
	if len(cur) == 1 {
		_, err = s.C.Update(ctx, "Activity Cost", cur[0].S("name"), map[string]any{"costing_rate": rate})
		return err
	}
	_, err = s.C.Insert(ctx, "Activity Cost", map[string]any{"employee": emp, "activity_type": at, "costing_rate": rate})
	return err
}

func (s *TimeLog) Workload(ctx context.Context, orgID, actorID string, from, to time.Time) ([]timelog.Load, error) {
	seeAll, err := s.Authz.HasCapability(ctx, orgID, actorID, capTimeAll)
	if err != nil {
		return nil, err
	}
	if !seeAll {
		return nil, timelog.ErrNotAllowed
	}
	seeMoney, err := s.Authz.HasCapability(ctx, orgID, actorID, capFinance)
	if err != nil {
		return nil, err
	}
	if to.Before(from) {
		from, to = to, from
	}
	emps, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Employee", Reference: true,
		Fields:  []string{"name", "hearth_id", "hearth_minutes_per_week", "status"},
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}}, listCap)
	if err != nil {
		return nil, err
	}
	names, err := s.Dir.Names(ctx)
	if err != nil {
		return nil, err
	}
	loads := map[string]*timelog.Load{} // by Employee name
	var order []*timelog.Load
	for _, e := range emps {
		if st := e.S("status"); st != "" && st != "Active" {
			continue
		}
		c := 2400
		if e.F("hearth_minutes_per_week") > 0 {
			c = int(e.F("hearth_minutes_per_week"))
		}
		l := &timelog.Load{PrincipalID: e.S("hearth_id"), Name: names[e.S("hearth_id")], Capacity: c}
		if l.Name == "" {
			l.Name = l.PrincipalID
		}
		loads[e.S("name")] = l
		order = append(order, l)
	}
	// Logged: the window's rows in one read, their timesheets in another.
	rows, err := s.detail(ctx, append(s.window(from, to),
		emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
		emberops.Filter{Field: "to_time", Op: "is", Value: "set"}), "", 0)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		ec, err := s.resolve(ctx, rows, false)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if l := loads[ec.sheetEmp[r.S("parent")]]; l != nil {
				l.Logged += minutesOf(r)
			}
		}
	}
	// Assigned: open tasks and work orders by primary assignee, from EmberOPS.
	users, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	byPrincipal := map[string]*timelog.Load{}
	for _, l := range order {
		byPrincipal[l.PrincipalID] = l
	}
	for _, q := range []struct{ dt, est, due string }{{"Task", "expected_time", "hearth_due_at"}, {"Maintenance Work Order", "estimated_hours", "planned_end"}} {
		open, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: q.dt,
			Fields: []string{"hearth_primary_assignee", q.est, q.due},
			Filters: []emberops.Filter{{Field: "status", Op: "not in", Value: []any{"Completed", "Cancelled"}},
				{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
				{Field: "hearth_primary_assignee", Op: "is", Value: "set"}}}, listCap)
		if err != nil {
			return nil, err
		}
		for _, r := range open {
			if due := r.TP(q.due, s.loc()); due != nil && (due.Before(from) || !due.Before(to)) {
				continue
			}
			if l := byPrincipal[users[r.S("hearth_primary_assignee")]]; l != nil {
				l.Open++
				l.Assigned += int(math.Round(r.F(q.est) * 60))
			}
		}
	}
	if seeMoney {
		rates, err := s.rates(ctx)
		if err != nil {
			return nil, err
		}
		for emp, l := range loads {
			if c, ok := rates[emp]; ok {
				c := c
				l.RateCents = &c
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Assigned != order[j].Assigned {
			return order[i].Assigned > order[j].Assigned
		}
		return order[i].Name < order[j].Name
	})
	out := make([]timelog.Load, 0, len(order))
	for _, l := range order {
		out = append(out, *l)
	}
	return out, nil
}

func (s *TimeLog) requireBilling(ctx context.Context, orgID, actorID string) error {
	ok, err := s.Authz.HasCapability(ctx, orgID, actorID, capFinance)
	if err != nil {
		return err
	}
	if !ok {
		return timelog.ErrNotAllowed
	}
	return nil
}

func (s *TimeLog) finished(ctx context.Context, from, to time.Time) ([]emberops.Row, error) {
	if to.Before(from) {
		from, to = to, from
	}
	return s.detail(ctx, append(s.window(from, to),
		emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"},
		emberops.Filter{Field: "to_time", Op: "is", Value: "set"}), "from_time asc", 0)
}

func (s *TimeLog) TimeReportSummary(ctx context.Context, orgID, actorID string, from, to time.Time) ([]timelog.ReportTotal, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.finished(ctx, from, to)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ec, err := s.resolve(ctx, rows, false)
	if err != nil {
		return nil, err
	}
	tot := map[string]*timelog.ReportTotal{}
	for _, r := range rows {
		pid := ec.emps[ec.sheetEmp[r.S("parent")]]
		t := tot[pid]
		if t == nil {
			t = &timelog.ReportTotal{PrincipalID: pid, PersonName: ec.names[pid]}
			tot[pid] = t
		}
		t.Minutes += minutesOf(r)
	}
	out := make([]timelog.ReportTotal, 0, len(tot))
	for _, t := range tot {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		return out[i].PersonName < out[j].PersonName
	})
	return out, nil
}

func (s *TimeLog) TimeReport(ctx context.Context, orgID, actorID, principalID string, from, to time.Time) ([]timelog.ReportRow, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.finished(ctx, from, to)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ec, err := s.resolve(ctx, rows, true)
	if err != nil {
		return nil, err
	}
	var out []timelog.ReportRow
	for _, r := range rows {
		e := s.entry(ec, r)
		if principalID != "" && e.PrincipalID != principalID {
			continue
		}
		rr := timelog.ReportRow{ID: e.ID, PrincipalID: e.PrincipalID, PersonName: e.PersonName, DocTitle: e.DocTitle,
			StartedAt: e.StartedAt, Minutes: minutesOf(r), Note: e.Note}
		if dt, n := target(r); dt != "" {
			rr.SpaceName = ec.spaces[ec.refs[refKey(dt, n)].space]
		}
		for _, t := range e.Tags {
			rr.Tags = append(rr.Tags, t.Name)
		}
		out = append(out, rr)
	}
	return out, nil
}

// ---- imported time ----

func (s *TimeLog) Imported(ctx context.Context, orgID, actorID, note string) ([]timelog.ImportedEntry, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	rows, err := s.detail(ctx, []emberops.Filter{emberops.F("description", note), emberops.F("hearth_approval", "pending"),
		{Field: "to_time", Op: "is", Value: "set"}, {Field: "hearth_deleted_at", Op: "is", Value: "not set"}}, "", 0)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	es, err := s.entries(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]timelog.ImportedEntry, 0, len(es))
	for _, e := range es {
		if e.DocumentID == "" || e.Minutes == nil {
			continue
		}
		ie := timelog.ImportedEntry{ID: e.ID, DocumentID: e.DocumentID, DocTitle: e.DocTitle, PrincipalID: e.PrincipalID,
			PersonName: e.PersonName, StartedAt: e.StartedAt, Minutes: *e.Minutes}
		if ids, _, err := s.Docs.AssignedTo(ctx, e.DocumentID); err == nil {
			names, _ := s.Dir.Names(ctx)
			for _, id := range ids {
				ie.Assignees = append(ie.Assignees, names[id])
			}
			if len(ids) == 1 {
				ie.SuggestedID, ie.SuggestedName = ids[0], names[ids[0]]
			}
		}
		out = append(out, ie)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DocTitle != out[j].DocTitle {
			return out[i].DocTitle < out[j].DocTitle
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out, nil
}

// Reassign moves imported entries onto other people. A time entry belongs to
// its person's Timesheet, which EmberOPS does not let a row leave, so each move
// writes the entry again under the new person (with a new id) and removes the
// old one.
func (s *TimeLog) Reassign(ctx context.Context, orgID, actorID, note string, moves []timelog.Move) (int, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return 0, err
	}
	n := 0
	now := time.Now()
	for _, m := range moves {
		r, err := s.C.GetByHearthID(ctx, detailDoctype, m.EntryID)
		if err != nil {
			if errors.Is(err, emberops.ErrNotFound) {
				continue
			}
			return n, err
		}
		_, oldEmp, err := s.owner(ctx, r)
		if err != nil {
			return n, err
		}
		newEmp, err := s.employeeName(ctx, m.PrincipalID)
		if err != nil {
			return n, err
		}
		if newEmp == "" || newEmp == oldEmp || r.S("description") != note || r.S("hearth_approval") == "approved" ||
			r.S("hearth_approval") == "denied" || r.Has("hearth_deleted_at") {
			continue
		}
		vals := map[string]any{"hearth_id": uuid.NewString(), "employee": emberops.Ref(m.PrincipalID),
			"from_time": r["from_time"], "to_time": r["to_time"], "hours": r["hours"], "description": r["description"],
			"task": r["task"], "maintenance_work_order": r["maintenance_work_order"], "hearth_ref_doctype": r["hearth_ref_doctype"],
			"hearth_ref_name": r["hearth_ref_name"], "hearth_tags": r["hearth_tags"], "activity_type": r["activity_type"]}
		for k, v := range vals {
			if v == nil || v == "" {
				delete(vals, k)
			}
		}
		if _, err := s.C.UpsertTimeEntry(ctx, vals); err != nil {
			return n, err
		}
		if err := s.patch(ctx, m.EntryID, oldEmp, map[string]any{"hearth_deleted_at": s.tsv(now)}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *TimeLog) DeleteImported(ctx context.Context, orgID, docID, note string) error {
	d, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Task", Fields: []string{"name"}, Limit: 1,
		Filters: []emberops.Filter{emberops.F("hearth_id", docID)}})
	kind, dt := docs.KindTask, "Task"
	if err != nil || len(d) == 0 {
		kind, dt = docs.KindWorkOrder, "Maintenance Work Order"
		if d, err = s.C.List(ctx, emberops.ListQuery{Doctype: dt, Fields: []string{"name"}, Limit: 1,
			Filters: []emberops.Filter{emberops.F("hearth_id", docID)}}); err != nil || len(d) == 0 {
			return err
		}
	}
	rows, err := s.detail(ctx, append(docFilter(kind, d[0].S("name")), emberops.F("description", note),
		emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"}), "", 0)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, r := range rows {
		_, emp, err := s.owner(ctx, r)
		if err != nil {
			return err
		}
		if err := s.patch(ctx, r.S("hearth_id"), emp, map[string]any{"hearth_deleted_at": s.tsv(now)}); err != nil {
			return err
		}
	}
	return nil
}
