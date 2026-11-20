package stores

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
	"github.com/Embernet-ai/hearth/internal/maint"
)

// Maint keeps preventive maintenance plans (Maintenance Plan) and meter
// readings (Hearth Meter Reading) in EmberOPS.
//
// Preventive maintenance is owned by the CMMS addon: its daily scheduler
// generates the work orders from active plans and advances each plan. Hearth
// therefore only edits plans and reads what CMMS generated: an occurrence is a
// Maintenance Work Order that names its plan (maintenance_plan) with the due
// date in planned_start, and it is done when that work order is Completed.
// Hearth's own generator is switched off in this mode (cmd/hearth/main.go does
// not register it), because both would otherwise raise the same work.
//
// Who may see or change a plan is still Hearth's access to the plan's asset,
// asked through Authz; a plan inherits its asset's visibility as before.
type Maint struct {
	Base
	Dir   Directory
	Authz maint.Authorizer
}

var _ maint.Store = (*Maint)(nil)

const planDoctype = "Maintenance Plan"

var planFields = []string{"name", "hearth_id", "asset", "hearth_title", "status", "frequency", "frequency_unit",
	"advance_notice_days", "hearth_meter_key", "meter_threshold", "hearth_next_due_at", "next_due_date",
	"hearth_last_generated_at", "hearth_last_generated_units", "hearth_template", "owner"}

type planCtx struct {
	assets map[string]docRef // Asset name -> hearth id and title
	owners map[string]string // User name -> principal id
	open   map[string]*maint.Occurrence
	trend  map[string][]reading
}

type reading struct {
	at  time.Time
	val float64
}

func (s *Maint) canView(ctx context.Context, actor core.Subjects, assetID string) error {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessView); err != nil {
		return maint.ErrNotFound
	}
	return nil
}

// visible keeps the plans whose asset the actor can view: one check per
// distinct asset, against Hearth's own grants.
func (s *Maint) visible(ctx context.Context, actor core.Subjects, plans []*maint.Schedule) []*maint.Schedule {
	ok := map[string]bool{}
	var out []*maint.Schedule
	for _, p := range plans {
		v, seen := ok[p.AssetID]
		if !seen {
			v = s.canView(ctx, actor, p.AssetID) == nil
			ok[p.AssetID] = v
		}
		if v {
			out = append(out, p)
		}
	}
	return out
}

// plans turns rows into schedules: the assets, owners, open work orders and
// meter trend of the whole page come from four reads, not four per plan.
func (s *Maint) plans(ctx context.Context, rows []emberops.Row) ([]*maint.Schedule, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	pc := &planCtx{assets: map[string]docRef{}, open: map[string]*maint.Occurrence{}, trend: map[string][]reading{}}
	var assetNames, planNames []any
	seen := map[string]bool{}
	for _, r := range rows {
		planNames = append(planNames, r.S("name"))
		if a := r.S("asset"); a != "" && !seen[a] {
			seen[a] = true
			assetNames = append(assetNames, a)
		}
	}
	for len(assetNames) > 0 {
		n := min(len(assetNames), 200)
		got, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Asset", Fields: []string{"name", "hearth_id", "asset_name"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: assetNames[:n]}}})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			id := g.S("hearth_id")
			if id == "" {
				id = g.S("name")
			}
			pc.assets[g.S("name")] = docRef{id: id, title: g.S("asset_name")}
		}
		assetNames = assetNames[n:]
	}
	var err error
	if pc.owners, err = s.Dir.UserIDs(ctx); err != nil {
		return nil, err
	}
	// The open work order of each plan: the earliest one not yet done.
	ws, err := s.workOrders(ctx, []emberops.Filter{{Field: "maintenance_plan", Op: "in", Value: planNames},
		{Field: "status", Op: "not in", Value: []any{"Completed", "Cancelled"}}}, "planned_start asc", 0)
	if err != nil {
		return nil, err
	}
	byName := map[string]string{}
	for _, r := range rows {
		byName[r.S("name")] = idOf(r)
	}
	for _, w := range ws {
		o := s.occurrence(w, byName, pc, nil)
		if _, has := pc.open[w.S("maintenance_plan")]; !has {
			pc.open[w.S("maintenance_plan")] = o
		}
	}
	// Meter trend, for the plans that have one: one read of the recent readings.
	var meterAssets []any
	seenA := map[string]bool{}
	for _, r := range rows {
		if r.Has("hearth_meter_key") && r.F("meter_threshold") > 0 && !seenA[r.S("asset")] {
			seenA[r.S("asset")] = true
			meterAssets = append(meterAssets, r.S("asset"))
		}
	}
	if len(meterAssets) > 0 {
		rd, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Hearth Meter Reading", Fields: []string{"asset", "meter_key", "value", "read_at"},
			Filters: []emberops.Filter{{Field: "asset", Op: "in", Value: meterAssets},
				{Field: "read_at", Op: ">=", Value: s.tsv(time.Now().Add(-90 * 24 * time.Hour))}},
			OrderBy: "read_at asc"}, 5000)
		if err != nil {
			return nil, err
		}
		for _, r := range rd {
			k := r.S("asset") + "\x00" + r.S("meter_key")
			pc.trend[k] = append(pc.trend[k], reading{r.T("read_at", s.loc()), r.F("value")})
		}
	}
	out := make([]*maint.Schedule, 0, len(rows))
	for _, r := range rows {
		sch := s.schedule(r, pc)
		sch.Open = pc.open[r.S("name")]
		out = append(out, sch)
	}
	return out, nil
}

func idOf(r emberops.Row) string {
	if id := r.S("hearth_id"); id != "" {
		return id
	}
	return r.S("name")
}

func (s *Maint) schedule(r emberops.Row, pc *planCtx) *maint.Schedule {
	a := pc.assets[r.S("asset")]
	sch := &maint.Schedule{
		ID: idOf(r), AssetID: a.id, AssetTitle: a.title, Name: r.S("hearth_title"),
		LeadDays: int(r.I("advance_notice_days")), Enabled: r.S("status") == "Active",
		CreatedBy: pc.owners[r.S("owner")],
	}
	if sch.AssetTitle == "" {
		sch.AssetTitle = "Untitled"
	}
	if sch.Name == "" {
		sch.Name = r.S("name")
	}
	if t := r.S("hearth_template"); t != "" {
		sch.TemplateID = &t
	}
	if r.S("frequency_unit") == "Days" && r.I("frequency") > 0 {
		d := int(r.I("frequency"))
		sch.EveryDays = &d
	}
	if r.Has("hearth_meter_key") && r.F("meter_threshold") > 0 {
		k, u := r.S("hearth_meter_key"), r.F("meter_threshold")
		sch.MeterKey, sch.EveryUnits = &k, &u
	}
	if t := r.TP("hearth_next_due_at", s.loc()); t != nil {
		sch.NextDueAt = *t
	} else if t := r.TP("next_due_date", s.loc()); t != nil {
		sch.NextDueAt = *t
	}
	sch.LastGeneratedAt = r.TP("hearth_last_generated_at", s.loc())
	if r.Has("hearth_last_generated_units") {
		u := r.F("hearth_last_generated_units")
		sch.LastGeneratedUnits = &u
	}
	if sch.MeterKey != nil {
		sch.ProjectedMeterDue = projectMeterDue(pc.trend[r.S("asset")+"\x00"+*sch.MeterKey], sch, time.Now())
	}
	return sch
}

// projectMeterDue is the trend projection maint.Service does in SQL, over the
// readings of the last 90 days: units per day since the earliest reading,
// applied to the distance left to the next threshold.
func projectMeterDue(rs []reading, sch *maint.Schedule, now time.Time) *time.Time {
	if len(rs) < 2 || sch.EveryUnits == nil || *sch.EveryUnits <= 0 {
		return nil
	}
	first, last := rs[0], rs[len(rs)-1]
	if !last.at.After(first.at) {
		return nil
	}
	base := first.val
	if sch.LastGeneratedUnits != nil {
		base = *sch.LastGeneratedUnits
	}
	threshold := base + *sch.EveryUnits
	if last.val >= threshold {
		return &now
	}
	days := last.at.Sub(first.at).Hours() / 24
	perDay := (last.val - first.val) / days
	if perDay <= 0 {
		return nil
	}
	remaining := (threshold - last.val) / perDay
	if remaining > 3650 {
		return nil
	}
	due := last.at.Add(time.Duration(remaining * float64(24*time.Hour)))
	return &due
}

var woFields = []string{"name", "hearth_id", "title", "status", "maintenance_plan", "planned_start", "actual_end", "creation", "asset"}

func (s *Maint) workOrders(ctx context.Context, filters []emberops.Filter, order string, limit int) ([]emberops.Row, error) {
	filters = append(filters, emberops.Filter{Field: "hearth_deleted_at", Op: "is", Value: "not set"})
	if limit <= 0 {
		limit = 5000
	}
	return s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Maintenance Work Order", Fields: woFields, Filters: filters, OrderBy: order}, limit)
}

// occurrence reads a CMMS-generated work order as one coming-round of a plan.
func (s *Maint) occurrence(w emberops.Row, planIDs map[string]string, pc *planCtx, plans map[string]*maint.Schedule) *maint.Occurrence {
	id := idOf(w)
	o := &maint.Occurrence{
		ID: id, ScheduleID: planIDs[w.S("maintenance_plan")], WorkOrder: w.S("title"),
		DueAt: w.T("planned_start", s.loc()), GeneratedAt: w.T("creation", s.loc()), Reason: maint.ReasonCalendar,
	}
	o.WorkOrderID = &id
	if o.DueAt.IsZero() {
		o.DueAt = o.GeneratedAt
	}
	if w.S("status") == "Completed" {
		t := w.T("actual_end", s.loc())
		if t.IsZero() {
			t = o.DueAt
		}
		o.CompletedAt = &t
	}
	if a, ok := pc.assets[w.S("asset")]; ok {
		o.AssetID = a.id
	}
	if p := plans[o.ScheduleID]; p != nil {
		o.Name, o.AssetID = p.Name, p.AssetID
		if p.MeterKey != nil && p.EveryDays == nil {
			o.Reason = maint.ReasonMeter
		}
	}
	return o
}

func (s *Maint) planRow(ctx context.Context, id string) (emberops.Row, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: planDoctype, Fields: planFields, Limit: 1,
		Filters: []emberops.Filter{emberops.F("hearth_id", id)}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, maint.ErrNotFound
	}
	return rows[0], nil
}

func (s *Maint) one(ctx context.Context, id string) (*maint.Schedule, error) {
	r, err := s.planRow(ctx, id)
	if err != nil {
		return nil, err
	}
	ps, err := s.plans(ctx, []emberops.Row{r})
	if err != nil || len(ps) == 0 {
		return nil, err
	}
	return ps[0], nil
}

func (s *Maint) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*maint.Schedule, error) {
	sch, err := s.one(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.canView(ctx, actor, sch.AssetID); err != nil {
		return nil, err
	}
	return sch, nil
}

func (s *Maint) List(ctx context.Context, orgID string, actor core.Subjects) ([]*maint.Schedule, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: planDoctype, Fields: planFields}, 5000)
	if err != nil {
		return nil, err
	}
	ps, err := s.plans(ctx, rows)
	if err != nil {
		return nil, err
	}
	ps = s.visible(ctx, actor, ps)
	// A plan on a deleted asset is not listed, as in Postgres, where the list joins only live assets.
	if gone, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Asset", Fields: []string{"hearth_id"},
		Filters: []emberops.Filter{{Field: "hearth_deleted_at", Op: "is", Value: "set"}}}, 0); err == nil && len(gone) > 0 {
		deleted := map[string]bool{}
		for _, g := range gone {
			deleted[idOf(g)] = true
		}
		kept := ps[:0]
		for _, p := range ps {
			if p.AssetID == "" || !deleted[p.AssetID] {
				kept = append(kept, p)
			}
		}
		ps = kept
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Enabled != ps[j].Enabled {
			return ps[i].Enabled
		}
		return ps[i].NextDueAt.Before(ps[j].NextDueAt)
	})
	return ps, nil
}

func (s *Maint) assetName(ctx context.Context, assetID string) (string, error) {
	name, err := s.C.Resolve(ctx, "Asset", assetID)
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", maint.ErrNotFound
	}
	return name, nil
}

func (s *Maint) ForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string) ([]*maint.Schedule, error) {
	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	name, err := s.assetName(ctx, assetID)
	if err != nil {
		if errors.Is(err, maint.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: planDoctype, Fields: planFields,
		Filters: []emberops.Filter{emberops.F("asset", name)}}, 5000)
	if err != nil {
		return nil, err
	}
	ps, err := s.plans(ctx, rows)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].NextDueAt.Before(ps[j].NextDueAt) })
	return ps, err
}

// Save creates or replaces a plan. CMMS advances next-due and the last
// generated stamps itself, so a save sends them only when the person set them.
func (s *Maint) Save(ctx context.Context, orgID string, actor core.Subjects, sch maint.Schedule) (*maint.Schedule, error) {
	if sch.ID != "" {
		cur, err := s.one(ctx, sch.ID)
		if err != nil {
			return nil, err
		}
		sch.AssetID = cur.AssetID
	}
	if sch.AssetID == "" {
		return nil, fmt.Errorf("%w: it needs an asset", maint.ErrInvalid)
	}
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: sch.AssetID}, core.AccessEdit); err != nil {
		return nil, err
	}
	sch.Name = strings.TrimSpace(sch.Name)
	if sch.Name == "" {
		return nil, fmt.Errorf("%w: it needs a name", maint.ErrInvalid)
	}
	hasCalendar := sch.EveryDays != nil && *sch.EveryDays > 0
	hasMeter := sch.MeterKey != nil && *sch.MeterKey != "" && sch.EveryUnits != nil && *sch.EveryUnits > 0
	if !hasCalendar && !hasMeter {
		return nil, fmt.Errorf("%w: give it an interval in days, in meter units, or both", maint.ErrInvalid)
	}
	if !hasCalendar {
		sch.EveryDays = nil
	}
	if !hasMeter {
		sch.MeterKey, sch.EveryUnits = nil, nil
	}
	if sch.LeadDays < 0 || sch.LeadDays > 365 {
		sch.LeadDays = 7
	}
	create := sch.ID == ""
	if create {
		sch.ID, sch.Enabled = uuid.NewString(), true
		if sch.NextDueAt.IsZero() {
			sch.NextDueAt = time.Now()
			if hasCalendar {
				sch.NextDueAt = sch.NextDueAt.AddDate(0, 0, *sch.EveryDays)
			}
		}
	}
	rec := mapping.Rec{"id": sch.ID, "asset_id": sch.AssetID, "name": sch.Name, "lead_days": float64(sch.LeadDays),
		"enabled": sch.Enabled}
	if sch.TemplateID != nil {
		rec["template_id"] = *sch.TemplateID
	}
	if sch.EveryDays != nil {
		rec["every_days"] = float64(*sch.EveryDays)
	}
	if sch.MeterKey != nil {
		rec["meter_key"], rec["every_units"] = *sch.MeterKey, *sch.EveryUnits
	}
	if !sch.NextDueAt.IsZero() {
		rec["next_due_at"] = sch.NextDueAt.UTC().Format(time.RFC3339)
	}
	doc := mapping.TZ{Loc: s.loc()}.PMScheduleDoc(rec)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if !hasCalendar && !create {
		// Switching a plan to meter-only must clear the calendar clock.
		doc["frequency"], doc["frequency_unit"] = 0, nil
	}
	if !hasMeter && !create {
		doc["hearth_meter_key"], doc["meter_threshold"], doc["meter_doctype"] = nil, 0, nil
	}
	if _, err := s.C.Upsert(ctx, planDoctype, sch.ID, doc); err != nil {
		if isForbidden(err) {
			return nil, maint.ErrNotFound
		}
		return nil, err
	}
	sch.OrgID = orgID
	if create {
		sch.CreatedBy = actor.Principal()
	}
	return &sch, nil
}

func (s *Maint) mayChange(ctx context.Context, actor core.Subjects, id string) (*maint.Schedule, error) {
	sch, err := s.one(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.canView(ctx, actor, sch.AssetID); err != nil {
		return nil, err
	}
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: sch.AssetID}, core.AccessEdit); err != nil {
		return nil, err
	}
	return sch, nil
}

func (s *Maint) SetEnabled(ctx context.Context, orgID string, actor core.Subjects, id string, on bool) error {
	if _, err := s.mayChange(ctx, actor, id); err != nil {
		return err
	}
	status := "Paused"
	if on {
		status = "Active"
	}
	_, err := s.C.Upsert(ctx, planDoctype, id, map[string]any{"status": status})
	return err
}

func (s *Maint) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if _, err := s.mayChange(ctx, actor, id); err != nil {
		return err
	}
	return notFound(s.C.DeleteByHearthID(ctx, planDoctype, id), maint.ErrNotFound)
}

// ---- occurrences ----

// occurrences reads the work orders CMMS generated for the given plans.
func (s *Maint) occurrences(ctx context.Context, plans []*maint.Schedule, extra []emberops.Filter, limit int) ([]maint.Occurrence, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	names, err := s.C.List(ctx, emberops.ListQuery{Doctype: planDoctype, Fields: []string{"name", "hearth_id"}, Limit: 500,
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: planIDs(plans)}}})
	if err != nil {
		return nil, err
	}
	var in []any
	byName := map[string]string{}
	for _, n := range names {
		in = append(in, n.S("name"))
		byName[n.S("name")] = idOf(n)
	}
	ws, err := s.workOrders(ctx, append([]emberops.Filter{{Field: "maintenance_plan", Op: "in", Value: in}}, extra...), "planned_start desc", limit)
	if err != nil {
		return nil, err
	}
	pm := map[string]*maint.Schedule{}
	for _, p := range plans {
		pm[p.ID] = p
	}
	pc := &planCtx{assets: map[string]docRef{}}
	out := make([]maint.Occurrence, 0, len(ws))
	for _, w := range ws {
		out = append(out, *s.occurrence(w, byName, pc, pm))
	}
	return out, nil
}

func planIDs(ps []*maint.Schedule) []any {
	out := make([]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func (s *Maint) HistoryForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string, limit int) ([]maint.Occurrence, error) {
	ps, err := s.ForAsset(ctx, orgID, actor, assetID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.occurrences(ctx, ps, nil, limit)
}

func (s *Maint) Report(ctx context.Context, orgID string, actor core.Subjects, from, to time.Time) (*maint.Compliance, error) {
	if to.Before(from) {
		from, to = to, from
	}
	c := &maint.Compliance{From: from, To: to}
	ps, err := s.List(ctx, orgID, actor)
	if err != nil {
		return nil, err
	}
	occ, err := s.occurrences(ctx, ps, []emberops.Filter{
		{Field: "planned_start", Op: ">=", Value: s.tsv(from)}, {Field: "planned_start", Op: "<", Value: s.tsv(to)}}, 0)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, o := range occ {
		c.Due++
		switch {
		case o.CompletedAt != nil && !o.Late():
			c.OnTime++
		case o.CompletedAt != nil:
			c.Late++
		case o.Deadline().Before(now):
			c.Missed++
		default:
			c.Open++
		}
		c.Rows = append(c.Rows, o)
	}
	return c, nil
}

// ---- meters ----

const readingDoctype = "Hearth Meter Reading"

var readingFields = []string{"name", "hearth_id", "meter_key", "value", "read_at", "read_by", "note"}

func (s *Maint) RecordReading(ctx context.Context, orgID string, actor core.Subjects, assetID, key string, value float64, note string) (*maint.Reading, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: a reading needs to say which meter", maint.ErrInvalid)
	}
	if value < 0 {
		return nil, fmt.Errorf("%w: a meter does not go backwards", maint.ErrInvalid)
	}
	now := time.Now()
	r := &maint.Reading{ID: uuid.NewString(), AssetID: assetID, Key: key, Value: value, ReadAt: now,
		ReadBy: actor.Principal(), Note: strings.TrimSpace(note)}
	if _, err := s.C.Upsert(ctx, readingDoctype, r.ID, map[string]any{"asset": emberops.Ref(assetID), "meter_key": key,
		"value": value, "read_at": s.tsv(now), "read_by": emberops.Ref(r.ReadBy), "note": r.Note}); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Maint) readings(ctx context.Context, assetID string, filters []emberops.Filter, limit int) ([]maint.Reading, error) {
	name, err := s.assetName(ctx, assetID)
	if err != nil {
		if errors.Is(err, maint.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: readingDoctype, Fields: readingFields, Limit: limit, OrderBy: "read_at desc",
		Filters: append([]emberops.Filter{emberops.F("asset", name)}, filters...)})
	if err != nil {
		return nil, err
	}
	users, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.Dir.Names(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]maint.Reading, 0, len(rows))
	for _, r := range rows {
		by := users[r.S("read_by")]
		out = append(out, maint.Reading{ID: idOf(r), AssetID: assetID, Key: r.S("meter_key"), Value: r.F("value"),
			ReadAt: r.T("read_at", s.loc()), ReadBy: by, ReadName: names[by], Note: r.S("note")})
	}
	return out, nil
}

func (s *Maint) Readings(ctx context.Context, orgID string, actor core.Subjects, assetID string, limit int) ([]maint.Reading, error) {
	if err := s.canView(ctx, actor, assetID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.readings(ctx, assetID, nil, limit)
}

func (s *Maint) LatestReading(ctx context.Context, assetID, key string) (*maint.Reading, error) {
	rs, err := s.readings(ctx, assetID, []emberops.Filter{emberops.F("meter_key", key)}, 1)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

func (s *Maint) MeterKeys(ctx context.Context, assetID string) ([]string, error) {
	name, err := s.assetName(ctx, assetID)
	if err != nil {
		if errors.Is(err, maint.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: readingDoctype, Fields: []string{"meter_key"},
		Filters: []emberops.Filter{emberops.F("asset", name)}}, 5000)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if k := r.S("meter_key"); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}
