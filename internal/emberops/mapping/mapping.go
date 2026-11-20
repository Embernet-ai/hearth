// Package mapping turns Hearth rows into the documents the emberops_hearth
// addon stores. It is the Go side of the addon's mapping.py and follows it
// field for field: the README's table is the contract, and a field renamed
// there is renamed here.
//
// A Rec is one Hearth row as JSON (Postgres to_jsonb), so the same functions
// serve the migration, which reads rows from Postgres, and the stores, which
// build rows from domain values. References to other Hearth records are written
// "hearth:<id>"; the addon resolves them through each target's hearth_id.
package mapping

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Rec is one Hearth row.
type Rec map[string]any

// Doc is a Frappe document payload for api.upsert.
type Doc = map[string]any

// Order is the addon's MIGRATION_ORDER: a record may only reference what an
// earlier entry created.
var Order = []string{
	"User", "Employee", "Employee Group", "Hearth Space", "Department", "Activity Type",
	"Hearth Property Def", "Customer", "Supplier", "Contact", "Item", "Asset",
	"Hearth Document", "Maintenance Plan", "Task", "Maintenance Work Order",
	"Hearth Meter Reading", "Asset Failure Log", "Timesheet", "Stock Entry",
	"Purchase Order", "Event", "Hearth View", "Hearth Rule", "Hearth Time Off Request",
	"Hearth Department Request", "Hearth Entity Link",
}

// Status and priority vocabularies.
var (
	TaskStatus   = map[string]string{"todo": "Open", "in_progress": "Working", "blocked": "Blocked", "done": "Completed", "cancelled": "Cancelled"}
	WOStatus     = map[string]string{"todo": "Open", "in_progress": "In Progress", "blocked": "On Hold", "done": "Completed", "cancelled": "Cancelled"}
	TaskPriority = []string{"Low", "Medium", "High", "Urgent"}
	WOPriority   = []string{"Low", "Medium", "High", "Critical"}
)

// Invert reverses a vocabulary.
func Invert(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// PriorityName maps Hearth's integer priority onto a scale, clamped.
func PriorityName(p int, scale []string) string {
	return scale[max(0, min(p, len(scale)-1))]
}

// PriorityNumber is the inverse; an unknown name is 0.
func PriorityNumber(name string, scale []string) int {
	for i, n := range scale {
		if n == name {
			return i
		}
	}
	return 0
}

// ---- accessors ----

// S reads a string ("" for null).
func (r Rec) S(k string) string {
	switch v := r[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// F reads a number.
func (r Rec) F(k string) (float64, bool) {
	switch v := r[k].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// I reads an integer.
func (r Rec) I(k string) int { f, _ := r.F(k); return int(f) }

// B reads a boolean.
func (r Rec) B(k string) bool { b, _ := r[k].(bool); return b }

// Has reports a non-null, non-empty value.
func (r Rec) Has(k string) bool {
	switch v := r[k].(type) {
	case nil:
		return false
	case string:
		return v != ""
	}
	return true
}

// List reads an array of rows.
func (r Rec) List(k string) []Rec {
	list, _ := r[k].([]any)
	out := make([]Rec, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, Rec(m))
		}
	}
	return out
}

// Strings reads an array of strings.
func (r Rec) Strings(k string) []string {
	list, _ := r[k].([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Ref writes "hearth:<id>", or nil for an empty id.
func Ref(id string) any {
	if id == "" {
		return nil
	}
	return "hearth:" + id
}

func refOf(r Rec, k string) any { return Ref(r.S(k)) }

// TZ converts Hearth instants to the site's naive local time.
type TZ struct{ Loc *time.Location }

func (z TZ) loc() *time.Location {
	if z.Loc == nil {
		return time.UTC
	}
	return z.Loc
}

func parse(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, !x.IsZero()
	case string:
		if x == "" {
			return time.Time{}, false
		}
		for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-07:00", "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(l, x); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// DT is a timestamp as Frappe's "YYYY-MM-DD HH:MM:SS", or nil.
func (z TZ) DT(v any) any {
	t, ok := parse(v)
	if !ok {
		return nil
	}
	return t.In(z.loc()).Format("2006-01-02 15:04:05")
}

// Date is a timestamp's date in the site's zone, or nil.
func (z TZ) Date(v any) any {
	t, ok := parse(v)
	if !ok {
		return nil
	}
	return t.In(z.loc()).Format("2006-01-02")
}

// PlainDate passes a date column through, since it has no zone.
func PlainDate(v any) any {
	t, ok := parse(v)
	if !ok {
		return nil
	}
	return t.Format("2006-01-02")
}

func hours(minutes Rec, k string) any {
	f, ok := minutes.F(k)
	if !ok {
		return nil
	}
	return round(f/60.0, 4)
}

func round(f float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int64(f*p+0.5*sign(f))) / p
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// Dumps is JSON with sorted keys, which is what the addon stores.
func Dumps(v any) string {
	b, _ := json.Marshal(v) // encoding/json sorts map keys
	return string(b)
}

// Clean drops nil so an upsert never blanks a field the caller did not mean to.
func Clean(d Doc) Doc {
	for k, v := range d {
		if v == nil {
			delete(d, k)
		}
	}
	return d
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- checklist, shared bits ----

// ChecklistRows maps checklist items (ordered by position).
func (z TZ) ChecklistRows(items []Rec) []any {
	sorted := append([]Rec(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].I("position") < sorted[j].I("position") })
	rows := make([]any, 0, len(sorted))
	for _, it := range sorted {
		rows = append(rows, Clean(Doc{
			"doctype": "Hearth Checklist Item", "hearth_id": orNil(it.S("id")),
			"item_text": it.S("text"), "description": orNil(it.S("description")),
			"done": flag(it.Has("done_at")), "done_at": z.DT(it["done_at"]), "done_by": orNil(it.S("done_by_email")),
		}))
	}
	return rows
}

func (z TZ) common(d Rec) Doc {
	format := d.S("body_format")
	if format == "" {
		format = "markdown"
	}
	out := Doc{
		"hearth_id": d.S("id"), "hearth_space": refOf(d, "space_id"),
		"hearth_body": orNil(d.S("body")), "hearth_body_format": format,
		"hearth_deleted_at": z.DT(d["deleted_at"]),
	}
	if p := d["properties"]; p != nil {
		if m, ok := p.(map[string]any); ok && len(m) > 0 {
			out["hearth_properties"] = Dumps(m)
		}
	}
	return out
}

func title(d Rec) string {
	if t := d.S("title"); t != "" {
		return t
	}
	return "Untitled"
}

func emails(d Rec, k string) []string {
	if l := d.Strings(k); l != nil {
		return l
	}
	return []string{}
}

// ---- documents ----

// TaskDoc maps documents(kind=task) + task_fields to a Task.
func (z TZ) TaskDoc(d Rec) Doc {
	out := z.common(d)
	status := d.S("status")
	if status == "" {
		status = "todo"
	}
	out["doctype"] = "Task"
	out["subject"] = title(d)
	out["status"] = TaskStatus[status]
	out["priority"] = PriorityName(d.I("priority"), TaskPriority)
	out["parent_task"] = refOf(d, "parent_id")
	out["exp_end_date"] = z.Date(d["due_at"])
	out["hearth_due_at"] = z.DT(d["due_at"])
	out["hearth_started_at"] = z.DT(d["started_at"])
	out["hearth_completed_at"] = z.DT(d["completed_at"])
	out["completed_on"] = z.Date(d["completed_at"])
	out["expected_time"] = hours(d, "estimate_minutes")
	out["hearth_primary_assignee"] = orNil(d.S("primary_assignee_email"))
	out["hearth_checklist"] = z.ChecklistRows(d.List("checklist"))
	out["_assign"] = emails(d, "assignee_emails")
	return Clean(out)
}

// WorkOrderDoc maps documents(kind=work_order) to a Maintenance Work Order.
func (z TZ) WorkOrderDoc(d Rec) Doc {
	out := z.common(d)
	status := d.S("status")
	if status == "" {
		status = "todo"
	}
	preventive := d.Has("pm_reason") || d.Has("schedule_id")
	woType, cat := "Reactive", "Corrective"
	if preventive {
		woType, cat = "Planned", "Preventive"
	}
	out["doctype"] = "Maintenance Work Order"
	out["title"] = title(d)
	out["wo_type"] = woType
	out["maintenance_category"] = cat
	out["status"] = WOStatus[status]
	out["priority"] = PriorityName(d.I("priority"), WOPriority)
	out["asset"] = refOf(d, "asset_id")
	out["maintenance_plan"] = refOf(d, "schedule_id")
	out["hearth_parent"] = refOf(d, "parent_id")
	out["planned_start"] = z.DT(d["pm_due_at"])
	out["planned_end"] = z.DT(d["due_at"])
	out["actual_start"] = z.DT(d["started_at"])
	out["actual_end"] = z.DT(d["completed_at"])
	out["estimated_hours"] = hours(d, "estimate_minutes")
	out["hearth_primary_assignee"] = orNil(d.S("primary_assignee_email"))
	out["hearth_checklist"] = z.ChecklistRows(d.List("checklist"))
	out["_assign"] = emails(d, "assignee_emails")
	return Clean(out)
}

// AssetDoc maps documents(kind=asset) + asset_fields to an Asset.
func (z TZ) AssetDoc(d Rec) Doc {
	out := z.common(d)
	name := d.S("title")
	if name == "" {
		name = d.S("code")
	}
	if name == "" {
		name = "Untitled"
	}
	out["doctype"] = "Asset"
	out["asset_name"] = name
	out["hearth_code"] = orNil(strings.TrimSpace(d.S("code")))
	out["hearth_parent_asset"] = refOf(d, "parent_asset_id")
	out["hearth_installed_at"] = z.DT(d["installed_at"])
	out["hearth_retired_at"] = z.DT(d["retired_at"])
	out["hearth_checklist"] = z.ChecklistRows(d.List("checklist"))
	out["is_existing_asset"] = 1
	return Clean(out)
}

// DocumentDoc maps doc, wiki, note, template and daily_report documents to a
// Hearth Document.
func (z TZ) DocumentDoc(d Rec) Doc {
	kind := d.S("kind")
	if kind == "" {
		kind = "doc"
	}
	format := d.S("body_format")
	if format == "" {
		format = "markdown"
	}
	out := Doc{
		"doctype": "Hearth Document", "hearth_id": d.S("id"), "title": title(d), "kind": kind,
		"hearth_space": refOf(d, "space_id"), "parent_hearth_document": refOf(d, "parent_id"),
		"body": orNil(d.S("body")), "body_format": format, "template_kind": orNil(d.S("template_kind")),
		"checklist": z.ChecklistRows(d.List("checklist")), "deleted_at": z.DT(d["deleted_at"]),
	}
	if m, ok := d["properties"].(map[string]any); ok && len(m) > 0 {
		out["properties"] = Dumps(m)
	}
	return Clean(out)
}

// CanvasDoc maps a canvas with its nodes and edges to a Hearth Document of
// kind canvas, the graph kept as JSON in content.
func (z TZ) CanvasDoc(c Rec, nodes, edges []Rec) Doc {
	ns := make([]any, 0, len(nodes))
	for _, n := range nodes {
		node := Doc{"id": n.S("id"), "text": n.S("text"), "x": n.I("x"), "y": n.I("y"), "w": dflt(n, "w", 220), "h": dflt(n, "h", 120)}
		if n.Has("entity_type") {
			node["entity_type"] = n.S("entity_type")
		}
		if n.Has("entity_id") {
			node["entity_id"] = n.S("entity_id")
		}
		if st, ok := n["style"].(map[string]any); ok {
			node["style"] = st
		} else {
			node["style"] = map[string]any{}
		}
		ns = append(ns, node)
	}
	es := make([]any, 0, len(edges))
	for _, e := range edges {
		es = append(es, Doc{"id": e.S("id"), "from": e.S("from_node"), "to": e.S("to_node"), "label": e.S("label")})
	}
	return Clean(Doc{
		"doctype": "Hearth Document", "hearth_id": c.S("id"), "title": c.S("name"), "kind": "canvas",
		"hearth_space": refOf(c, "space_id"), "content": Dumps(Doc{"nodes": ns, "edges": es}),
		"deleted_at": z.DT(c["deleted_at"]),
	})
}

func dflt(r Rec, k string, d int) int {
	if f, ok := r.F(k); ok {
		return int(f)
	}
	return d
}

// PurchaseOrderDoc maps documents(kind=purchase_order).
func (z TZ) PurchaseOrderDoc(d Rec) Doc {
	out := z.common(d)
	out["doctype"] = "Purchase Order"
	out["hearth_title"] = orNil(d.S("title"))
	out["supplier"] = refOf(d, "supplier_id")
	out["schedule_date"] = z.Date(d["due_at"])
	return Clean(out)
}

// ---- maintenance ----

// PMScheduleDoc maps pm_schedules to a Maintenance Plan.
func (z TZ) PMScheduleDoc(s Rec) Doc {
	every, hasEvery := s.F("every_days")
	hasEvery = hasEvery && every != 0
	schedule, status := "Meter", "Paused"
	if hasEvery {
		schedule = "Calendar"
	}
	if s.B("enabled") {
		status = "Active"
	}
	lead := 7
	if f, ok := s.F("lead_days"); ok {
		lead = int(f)
	}
	out := Doc{
		"doctype": "Maintenance Plan", "hearth_id": s.S("id"), "asset": refOf(s, "asset_id"),
		"hearth_title": orNil(s.S("name")), "hearth_template": refOf(s, "template_id"),
		"schedule_type": schedule, "status": status, "advance_notice_days": lead,
		"hearth_meter_key":   orNil(s.S("meter_key")),
		"hearth_next_due_at": z.DT(s["next_due_at"]), "next_due_date": z.Date(s["next_due_at"]),
		"hearth_last_generated_at": z.DT(s["last_generated_at"]), "last_generated_date": z.Date(s["last_generated_at"]),
	}
	if hasEvery {
		out["frequency"], out["frequency_unit"] = int(every), "Days"
	}
	if s.Has("meter_key") {
		out["meter_doctype"] = "Hearth Meter Reading"
	}
	if f, ok := s.F("every_units"); ok {
		out["meter_threshold"] = f
	}
	if f, ok := s.F("last_generated_units"); ok {
		out["hearth_last_generated_units"] = f
	}
	return Clean(out)
}

// OccurrencePatch maps pm_occurrences onto the work order CMMS generated.
func (z TZ) OccurrencePatch(o Rec) Doc {
	return Clean(Doc{
		"doctype": "Maintenance Work Order", "hearth_id": o.S("work_order_id"),
		"planned_start": z.DT(o["due_at"]), "maintenance_plan": refOf(o, "schedule_id"),
	})
}

// MeterReadingDoc maps meter_readings.
func (z TZ) MeterReadingDoc(r Rec) Doc {
	v, _ := r.F("value")
	return Clean(Doc{
		"doctype": "Hearth Meter Reading", "hearth_id": r.S("id"), "asset": refOf(r, "asset_id"),
		"meter_key": r.S("key"), "value": v, "read_at": z.DT(r["read_at"]),
		"read_by": orNil(r.S("read_by_email")), "note": orNil(r.S("note")),
	})
}

// DowntimeDoc maps asset_downtime to an Asset Failure Log, left as a draft:
// submitting one creates a reactive work order.
func (z TZ) DowntimeDoc(r Rec) Doc {
	note := strings.TrimSpace(r.S("note"))
	desc := note
	if desc == "" {
		desc = "Downtime"
		if r.B("planned") {
			desc = "Planned downtime"
		}
	}
	out := Doc{
		"doctype": "Asset Failure Log", "hearth_id": r.S("id"), "asset": refOf(r, "asset_id"),
		"failure_date": z.DT(r["started_at"]), "downtime_start": z.DT(r["started_at"]),
		"downtime_end": z.DT(r["ended_at"]), "hearth_planned": flag(r.B("planned")),
		"hearth_note": orNil(note), "failure_description": desc,
		"maintenance_work_order": refOf(r, "work_order_id"),
	}
	if f, ok := r.F("minutes"); ok {
		out["downtime_minutes"] = f
	}
	return Clean(out)
}

// ---- time ----

// TimeEntryRow maps one finished time entry to a Timesheet Detail row.
func (z TZ) TimeEntryRow(e Rec, tags []string, defaultActivity string) Doc {
	activity := defaultActivity
	if len(tags) > 0 {
		activity = tags[0]
	}
	approval := "pending"
	if e.Has("decided_at") {
		approval = "denied"
		if e.B("approved") {
			approval = "approved"
		}
	}
	row := Doc{
		"doctype": "Timesheet Detail", "hearth_id": e.S("id"), "employee": refOf(e, "principal_id"),
		"activity_type": orNil(activity), "hearth_tags": orNil(strings.Join(tags, ",")),
		"from_time": z.DT(e["started_at"]), "to_time": z.DT(e["ended_at"]), "hours": hours(e, "minutes"),
		"description": orNil(e.S("note")), "hearth_deleted_at": z.DT(e["deleted_at"]),
		"hearth_approval": approval, "hearth_decided_by": orNil(e.S("decided_by_email")),
		"hearth_decided_at": z.DT(e["decided_at"]), "hearth_decision_note": orNil(e.S("decided_note")),
	}
	switch target := e.S("target_kind"); {
	case target == "task":
		row["task"] = refOf(e, "document_id")
	case target == "work_order":
		row["maintenance_work_order"] = refOf(e, "document_id")
	case e.Has("document_id"):
		dt := "Hearth Document"
		switch target {
		case "asset":
			dt = "Asset"
		case "purchase_order":
			dt = "Purchase Order"
		}
		row["hearth_ref_doctype"] = dt
		row["hearth_ref_name"] = refOf(e, "document_id")
	}
	return Clean(row)
}

// TimerDoc maps a running time entry (no end) to a Hearth Timer.
func (z TZ) TimerDoc(e Rec) Doc {
	out := Doc{
		"doctype": "Hearth Timer", "employee": refOf(e, "principal_id"),
		"started_at": z.DT(e["started_at"]), "note": orNil(e.S("note")),
	}
	switch e.S("target_kind") {
	case "task":
		out["task"] = refOf(e, "document_id")
	case "work_order":
		out["maintenance_work_order"] = refOf(e, "document_id")
	}
	return Clean(out)
}

// TimeTagDoc maps a time entry tag to an Activity Type.
func TimeTagDoc(t Rec) Doc {
	return Clean(Doc{"doctype": "Activity Type", "activity_type": t.S("name"), "hearth_id": t.S("id"),
		"hearth_description": orNil(t.S("description"))})
}

// ---- people, spaces ----

// SpaceDoc maps a space with its managers (emails). Departments are top-level
// spaces.
func SpaceDoc(s Rec, managerEmails []string, department bool) Doc {
	mem := make([]any, 0, len(managerEmails))
	for _, e := range managerEmails {
		mem = append(mem, Doc{"doctype": "Hearth Space Member", "user": e, "member_role": "Manager"})
	}
	kind := s.S("kind")
	if kind == "" {
		kind = "folder"
	}
	return Clean(Doc{
		"doctype": "Hearth Space", "hearth_id": s.S("id"), "space_name": s.S("name"),
		"parent_hearth_space": refOf(s, "parent_id"), "kind": kind, "position": s.I("position"),
		"is_department": flag(department), "members": mem,
	})
}

// SpaceAccessDoc is SpaceDoc with the rest of what decides who reaches a space:
// the department's members and the space's grants. Both the live copy of
// Hearth's structure and the migration build their spaces with it, so they
// cannot disagree.
//
// grants are Recs with "kind" (principal, team or org), "email" (a principal's
// address), "team_id", "level" and an optional "expires_at". A principal
// without an email, a channel and an unknown kind have no EmberOPS subject and
// are left out. Everyone in the org (kind org) is the Employee role.
func (z TZ) SpaceAccessDoc(s Rec, managerEmails, memberEmails []string, grants []Rec, department bool) Doc {
	doc := SpaceDoc(s, managerEmails, department)
	rows, _ := doc["members"].([]any)
	isManager := map[string]bool{}
	for _, e := range managerEmails {
		isManager[e] = true
	}
	for _, e := range memberEmails {
		if !isManager[e] {
			rows = append(rows, Doc{"doctype": "Hearth Space Member", "user": e, "member_role": "Member"})
		}
	}
	gs := make([]any, 0, len(grants))
	for _, g := range grants {
		row := Doc{"doctype": "Hearth Space Grant", "level": g.S("level"), "expires_on": z.DT(g["expires_at"])}
		switch g.S("kind") {
		case "principal":
			if !g.Has("email") {
				continue
			}
			row["subject_type"], row["user"] = "User", g.S("email")
		case "team":
			row["subject_type"], row["employee_group"] = "Employee Group", Ref(g.S("team_id"))
		case "org":
			row["subject_type"], row["role"] = "Role", "Employee"
		default:
			continue
		}
		gs = append(gs, Clean(row))
	}
	doc["members"] = rows
	doc["grants"] = gs
	return doc
}

// TeamDoc maps a team to an Employee Group.
func TeamDoc(t Rec, memberIDs []string) Doc {
	src := t.S("source")
	if src == "" {
		src = "local"
	}
	list := make([]any, 0, len(memberIDs))
	for _, m := range memberIDs {
		list = append(list, Doc{"employee": Ref(m)})
	}
	return Clean(Doc{
		"doctype": "Employee Group", "employee_group_name": t.S("name"), "hearth_id": t.S("id"),
		"hearth_source": src, "hearth_external_id": orNil(t.S("external_id")), "employee_list": list,
	})
}

// UserDoc maps a principal with an email to a User.
func UserDoc(p Rec) Doc {
	kind := p.S("kind")
	if kind == "" {
		kind = "user"
	}
	enabled := 1
	if s := p.S("status"); s != "" && s != "active" {
		enabled = 0
	}
	return Clean(Doc{
		"doctype": "User", "email": p.S("email"), "first_name": p.S("display_name"), "hearth_id": p.S("id"),
		"hearth_kind": kind, "hearth_handle": orNil(p.S("handle")), "enabled": enabled, "send_welcome_email": 0,
	})
}

// EmployeeDoc maps a user principal (and its capacity) to an Employee.
func EmployeeDoc(p Rec) Doc {
	out := Doc{
		"doctype": "Employee", "hearth_id": p.S("id"), "first_name": p.S("display_name"),
		"user_id": p.S("email"), "hearth_handle": orNil(p.S("handle")),
		"hearth_approver": refOf(p, "approver_id"),
		// ERPNext makes a User Permission for the person on their own Employee, which limits them to their own Timesheets: a
		// Hearth admin or approver then saw 99 of 638 timesheets and the time report, workload and approvals showed one person.
		// Hearth decides who sees whose time.
		"create_user_permission": 0,
		// A suspended or deleted person is an inactive Employee, so the workload and capacity pages list only the people
		// Hearth lists.
		"status": employeeStatus(p.S("status")),
	}
	if f, ok := p.F("minutes_per_week"); ok {
		out["hearth_minutes_per_week"] = f
	}
	return Clean(out)
}

func employeeStatus(s string) string {
	if s == "" || s == "active" {
		return "Active"
	}
	return "Inactive"
}

// TimeOffDoc maps time_off_requests.
func (z TZ) TimeOffDoc(r Rec) Doc {
	status := r.S("status")
	if status == "" {
		status = "pending"
	}
	return Clean(Doc{
		"doctype": "Hearth Time Off Request", "hearth_id": r.S("id"), "employee": refOf(r, "requester_id"),
		"leave_kind": r.S("kind"), "status": status, "starts_on": PlainDate(r["starts_on"]), "ends_on": PlainDate(r["ends_on"]),
		"days": r.I("days"), "reason": orNil(r.S("reason")), "comments": orNil(r.S("comments")),
		"decided_by": orNil(r.S("decided_by_email")), "decided_at": z.DT(r["decided_at"]),
		"decision_note": orNil(r.S("decision_note")),
	})
}

// DepartmentRequestDoc maps department_requests with their spaces.
func (z TZ) DepartmentRequestDoc(r Rec, spaces []Rec) Doc {
	status := r.S("status")
	if status == "" {
		status = "pending"
	}
	rows := make([]any, 0, len(spaces))
	for _, s := range spaces {
		rows = append(rows, Doc{"doctype": "Hearth Department Request Space",
			"hearth_space": Ref(s.S("space_id")), "request_kind": s.S("kind")})
	}
	return Clean(Doc{
		"doctype": "Hearth Department Request", "hearth_id": r.S("id"), "employee": refOf(r, "requester_id"),
		"status": status, "note": orNil(r.S("note")), "decided_by": orNil(r.S("decided_by_email")),
		"decided_at": z.DT(r["decided_at"]), "decision_note": orNil(r.S("decision_note")), "spaces": rows,
	})
}

// ---- views, rules, properties, links ----

// ViewDoc maps saved_views.
func ViewDoc(v Rec) Doc {
	kind := v.S("kind")
	if kind == "" {
		kind = "table"
	}
	q := v["query"]
	if q == nil {
		q = map[string]any{}
	}
	return Clean(Doc{
		"doctype": "Hearth View", "hearth_id": v.S("id"), "view_name": v.S("name"),
		"view_user": orNil(v.S("owner_email")), "view_kind": kind, "hearth_space": refOf(v, "space_id"),
		"shared": flag(v.B("shared")), "query": Dumps(q),
	})
}

// RuleDoc maps rules.
func RuleDoc(r Rec) Doc {
	trig, _ := r["trigger"].(map[string]any)
	conds, acts := r["conditions"], r["actions"]
	if conds == nil {
		conds = []any{}
	}
	if acts == nil {
		acts = []any{}
	}
	ev, _ := trig["event"].(string)
	kind, _ := trig["kind"].(string)
	return Clean(Doc{
		"doctype": "Hearth Rule", "hearth_id": r.S("id"), "rule_name": r.S("name"), "enabled": flag(r.B("enabled")),
		"hearth_space": refOf(r, "space_id"), "trigger_event": orNil(ev), "trigger_kind": orNil(kind),
		"conditions": Dumps(conds), "actions": Dumps(acts),
	})
}

// PropertyDefDoc maps property_defs.
func PropertyDefDoc(p Rec) Doc {
	opts := p["options"]
	if opts == nil {
		opts = []any{}
	}
	return Clean(Doc{
		"doctype": "Hearth Property Def", "hearth_id": p.S("id"), "prop_key": p.S("key"),
		"display_name": p.S("display_name"), "data_type": p.S("data_type"), "is_multi": flag(p.B("is_multi")),
		"applies_to": orNil(strings.Join(p.Strings("applies_to"), ",")), "source": orNil(p.S("source")),
		"external_id": orNil(p.S("external_id")), "options_json": Dumps(opts),
	})
}

// LinkDoctype names the doctype for one end of an entity link, or "" when
// EmberOPS holds no such thing (a file or a message, say).
func LinkDoctype(entityType, kind string) string {
	switch entityType {
	case "document":
		switch kind {
		case "task":
			return "Task"
		case "work_order":
			return "Maintenance Work Order"
		case "asset":
			return "Asset"
		case "purchase_order":
			return "Purchase Order"
		case "":
			return ""
		default:
			return "Hearth Document"
		}
	case "contact":
		return "Contact"
	case "canvas":
		return "Hearth Document"
	}
	return ""
}

// entityLinkNS namespaces the ids of entity links.
var entityLinkNS = uuid.MustParse("6f1c7a7e-2b0a-4e3e-9a55-6a1d2c1f0002")

// EntityLinkID is the Hearth id of the Hearth Entity Link for one relationship.
// It is derived from the relationship, so the live back end and the migration
// name the same link the same way and neither can write it twice.
func EntityLinkID(fromType, fromID, toType, toID, kind string) string {
	if kind == "" {
		kind = "mention"
	}
	return uuid.NewSHA1(entityLinkNS, []byte(fromType+":"+strings.ToLower(fromID)+"|"+toType+":"+strings.ToLower(toID)+"|"+kind)).String()
}

// EntityLinkDoc maps entity_links. doctypeFor maps a Hearth entity type or
// document kind to its doctype.
func EntityLinkDoc(l Rec, doctypeFor func(entityType, kind string) string) Doc {
	kind := l.S("kind")
	if kind == "" {
		kind = "mention"
	}
	return Clean(Doc{
		"doctype": "Hearth Entity Link", "hearth_id": l.S("id"),
		"from_doctype": doctypeFor(l.S("from_type"), l.S("from_kind")), "from_name": refOf(l, "from_id"),
		"to_doctype": doctypeFor(l.S("to_type"), l.S("to_kind")), "to_name": refOf(l, "to_id"), "link_kind": kind,
	})
}

// EventDoc maps calendar_events: personal events are Private and owned by the
// person, space events are Public.
func (z TZ) EventDoc(e Rec) Doc {
	personal := e.Has("principal_id")
	et, owner := "Public", any(nil)
	if personal {
		et, owner = "Private", orNil(e.S("owner_email"))
	}
	return Clean(Doc{
		"doctype": "Event", "hearth_id": e.S("id"), "subject": e.S("title"), "description": orNil(e.S("notes")),
		"hearth_location": orNil(e.S("location")), "starts_on": z.DT(e["starts_at"]), "ends_on": z.DT(e["ends_at"]),
		"all_day": flag(e.B("all_day")), "event_type": et, "hearth_space": refOf(e, "space_id"), "owner": owner,
	})
}

// ---- companies, contacts, parts ----

// CompanyDoc maps companies to a Customer or, for any other kind, a Supplier.
func (z TZ) CompanyDoc(c Rec) Doc {
	kind := c.S("kind")
	if kind == "" {
		kind = "vendor"
	}
	common := Doc{
		"hearth_id": c.S("id"), "hearth_company_kind": kind,
		"hearth_domain": orNil(strings.ToLower(strings.TrimSpace(c.S("domain")))),
		"hearth_notes":  orNil(c.S("notes")), "hearth_deleted_at": z.DT(c["deleted_at"]),
	}
	if kind == "customer" {
		common["doctype"], common["customer_name"], common["customer_type"] = "Customer", c.S("name"), "Company"
	} else {
		common["doctype"], common["supplier_name"], common["supplier_type"] = "Supplier", c.S("name"), "Company"
	}
	return Clean(common)
}

// ContactDoc maps contacts.
func (z TZ) ContactDoc(c Rec) Doc {
	parts := strings.SplitN(strings.TrimSpace(c.S("name")), " ", 2)
	var last any
	if len(parts) > 1 {
		last = strings.TrimSpace(parts[1])
	}
	links := []any{}
	if c.Has("company_id") {
		dt := "Supplier"
		if c.S("company_kind") == "customer" {
			dt = "Customer"
		}
		links = append(links, Doc{"link_doctype": dt, "link_name": refOf(c, "company_id")})
	}
	mail, phone := []any{}, []any{}
	if c.Has("email") {
		mail = append(mail, Doc{"email_id": c.S("email"), "is_primary": 1})
	}
	if c.Has("phone") {
		phone = append(phone, Doc{"phone": c.S("phone"), "is_primary_phone": 1})
	}
	return Clean(Doc{
		"doctype": "Contact", "hearth_id": c.S("id"), "first_name": parts[0], "last_name": last,
		"designation": orNil(c.S("title")), "hearth_notes": orNil(c.S("notes")),
		"email_ids": mail, "phone_nos": phone, "links": links, "hearth_deleted_at": z.DT(c["deleted_at"]),
	})
}

// PartDoc maps parts to an Item. currencyPerCent is the currency value of one
// cent (0.01 for dollars).
func (z TZ) PartDoc(p Rec, currencyPerCent float64) Doc {
	code := strings.TrimSpace(p.S("sku"))
	if code == "" {
		code = p.S("name")
	}
	cost, _ := p.F("unit_cost_cents")
	out := Doc{
		"doctype": "Item", "hearth_id": p.S("id"), "item_code": code, "item_name": p.S("name"),
		"item_group": "Spare Parts", "stock_uom": "Nos", "is_stock_item": 1,
		"valuation_rate": round(cost*currencyPerCent, 2), "hearth_supplier_id": orNil(p.S("supplier_id")),
		"hearth_deleted_at": z.DT(p["deleted_at"]),
	}
	if f, ok := p.F("reorder_at"); ok {
		out["hearth_reorder_at"] = f
	}
	return Clean(out)
}

// MovementEntryType: Hearth keeps the sign in quantity; negative leaves stock.
func MovementEntryType(reason string, quantity float64) string {
	if reason == "consumed" || (reason == "adjusted" && quantity < 0) {
		return "Material Issue"
	}
	return "Material Receipt"
}

// MovementRemarks is what a Stock Entry's remarks hold for a movement. A
// Material Receipt does not say whether stock was received or returned, nor
// whether an adjustment was a count, so those are written in front of the note.
func MovementRemarks(reason, note string) string {
	note = strings.TrimSpace(note)
	if reason == "returned" || reason == "adjusted" {
		return strings.TrimSpace("[" + reason + "] " + note)
	}
	return note
}

// ParseMovementRemarks reads it back: the reason the entry type implies, unless
// a marker says otherwise, and the note.
func ParseMovementRemarks(entryType, remarks string) (reason, note string) {
	reason = "received"
	if entryType == "Material Issue" {
		reason = "consumed"
	}
	for _, r := range []string{"returned", "adjusted"} {
		if rest, ok := strings.CutPrefix(remarks, "["+r+"]"); ok {
			return r, strings.TrimSpace(rest)
		}
	}
	return reason, strings.TrimSpace(remarks)
}

// PartMovementDoc maps part_movements to a Stock Entry.
func (z TZ) PartMovementDoc(m Rec, itemCode, warehouse string, currencyPerCent float64) Doc {
	q, _ := m.F("quantity")
	if q < 0 {
		q = -q
	}
	cost, _ := m.F("unit_cost_cents")
	row := Doc{"item_code": itemCode, "qty": q, "basic_rate": round(cost*currencyPerCent, 2)}
	entry := MovementEntryType(m.S("reason"), mustF(m, "quantity"))
	// With no warehouse the addon fills Hearth Settings' default one.
	if warehouse != "" {
		if entry == "Material Issue" {
			row["s_warehouse"] = warehouse
		} else {
			row["t_warehouse"] = warehouse
		}
	}
	out := Doc{
		"doctype": "Stock Entry", "hearth_id": m.S("id"), "stock_entry_type": entry,
		"posting_date": z.Date(m["moved_at"]), "remarks": orNil(MovementRemarks(m.S("reason"), m.S("note"))), "items": []any{row},
	}
	if m.S("target_kind") == "work_order" {
		out["hearth_maintenance_work_order"] = refOf(m, "document_id")
	}
	return Clean(out)
}

func mustF(r Rec, k string) float64 { f, _ := r.F(k); return f }
