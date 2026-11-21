// Package fake is an in-memory stand-in for EmberOPS: /api/resource plus the
// emberops_hearth.api methods Hearth uses, behind an httptest server.
//
// It exists so the stores can be tested without a bench. It is deliberately
// small: documents are maps, child tables ride inline, filters support the
// operators Hearth sends, and "hearth:<id>" references are resolved by looking
// for a document with that hearth_id. It does not enforce Frappe's validation
// or permissions beyond checking the bearer token.
package fake

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Frappe is the fake site.
type Frappe struct {
	*httptest.Server

	mu   sync.Mutex
	docs map[string]map[string]map[string]any // doctype -> name -> doc
	seq  int
	// Tokens maps an accepted bearer token to the Frappe user it acts as.
	Tokens map[string]string
	// Calls records "METHOD path?query" for every request, for N+1 assertions.
	Calls []string
	// Seen records the bearer token of every request.
	Seen []string
	// Users records the acting user of every request, parallel to Calls.
	Users []string
	// HRMS makes the site store time off as HRMS Leave Applications, which can be cancelled
	// and annotated once decided and nothing else (see decidedTimeOff).
	HRMS bool
	// RejectNew, when set, may refuse a record about to be created, as EmberOPS does for an
	// overlap or a leave balance. It returns the reason, or "" to accept.
	RejectNew func(doctype string, vals map[string]any) string
}

// New starts a fake accepting the given tokens (token -> user).
func New(tokens map[string]string) *Frappe {
	f := &Frappe{docs: map[string]map[string]map[string]any{}, Tokens: tokens}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Put stores a document directly, returning its name.
func (f *Frappe) Put(doctype string, doc map[string]any) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.put(doctype, doc)
}

// autoname is the field some doctypes are named by (autoname "field:...").
var autoname = map[string]string{"Activity Type": "activity_type", "Hearth Space": "space_name"}

func (f *Frappe) put(doctype string, doc map[string]any) string {
	if f.docs[doctype] == nil {
		f.docs[doctype] = map[string]map[string]any{}
	}
	name, _ := doc["name"].(string)
	if name == "" {
		if field, ok := autoname[doctype]; ok {
			name, _ = doc[field].(string)
		}
	}
	if name == "" {
		f.seq++
		name = fmt.Sprintf("%s-%04d", strings.ReplaceAll(doctype, " ", ""), f.seq)
	}
	doc["name"] = name
	doc["doctype"] = doctype
	f.docs[doctype][name] = doc
	return name
}

// All returns the documents of a doctype ordered by name.
func (f *Frappe) All(doctype string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sorted(doctype)
}

func (f *Frappe) sorted(doctype string) []map[string]any {
	var out []map[string]any
	for _, d := range f.docs[doctype] {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

// CallCount counts recorded requests whose line contains substr.
func (f *Frappe) CallCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.Calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func (f *Frappe) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	user, ok := f.Tokens[tok]
	f.Calls = append(f.Calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.Seen = append(f.Seen, tok)
	f.Users = append(f.Users, user)
	if !ok {
		fail(w, 401, "AuthenticationError", "invalid token")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/")
	switch {
	case strings.HasPrefix(path, "resource/"):
		f.resource(w, r, strings.TrimPrefix(path, "resource/"), user)
	case strings.HasPrefix(path, "method/"):
		f.method(w, r, strings.TrimPrefix(path, "method/"), user)
	default:
		fail(w, 404, "", "no such route")
	}
}

func fail(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	sm, _ := json.Marshal([]string{mustJSON(map[string]string{"message": msg})})
	_ = json.NewEncoder(w).Encode(map[string]any{"exc_type": typ, "_server_messages": string(sm)})
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func reply(w http.ResponseWriter, key string, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{key: v})
}

func (f *Frappe) resource(w http.ResponseWriter, r *http.Request, rest, user string) {
	doctype, name, _ := strings.Cut(rest, "/")
	switch {
	case name == "" && r.Method == http.MethodGet:
		rows := f.list(doctype, r)
		reply(w, "data", rows)
	case name == "" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.resolveRefs(body)
		body["owner"] = user
		body["modified"] = stamp()
		n := f.put(doctype, body)
		reply(w, "data", f.docs[doctype][n])
	case r.Method == http.MethodGet:
		d, ok := f.docs[doctype][name]
		if !ok {
			fail(w, 404, "DoesNotExistError", doctype+" "+name+" not found")
			return
		}
		reply(w, "data", d)
	case r.Method == http.MethodPut:
		d, ok := f.docs[doctype][name]
		if !ok {
			fail(w, 404, "DoesNotExistError", doctype+" "+name+" not found")
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, has := body["modified"]; has && m != d["modified"] {
			fail(w, 409, "TimestampMismatchError", "changed since")
			return
		}
		f.resolveRefs(body)
		for k, v := range body {
			d[k] = v
		}
		d["modified"] = stamp()
		reply(w, "data", d)
	case r.Method == http.MethodDelete:
		d, ok := f.docs[doctype][name]
		if !ok {
			fail(w, 404, "DoesNotExistError", doctype+" "+name+" not found")
			return
		}
		if doctype == "Event" && strings.HasPrefix(fmt.Sprint(d["subject"]), "Out: ") {
			// What live EmberOPS does with a time off "Out" (found on live 2026-11-20): a person has no delete on
			// an Event, even their own, and a request that still links it keeps it.
			if user != "svc@example.com" {
				fail(w, 403, "PermissionError", "User not allowed to delete Event: "+name)
				return
			}
			// A Private event answers only to its owner, the service account included (live: "does not have access to
			// this document").
			if d["event_type"] == "Private" && d["owner"] != user {
				fail(w, 403, "PermissionError", "User does not have access to this document: Event - "+name)
				return
			}
			for _, req := range f.docs["Hearth Time Off Request"] {
				if fmt.Sprint(req["calendar_event"]) == name {
					fail(w, 417, "LinkExistsError", "Cannot delete Event "+name+": it is linked with a Hearth Time Off Request")
					return
				}
			}
		}
		delete(f.docs[doctype], name)
		reply(w, "message", "ok")
	default:
		fail(w, 405, "", "method not allowed")
	}
}

var stampN int

func stamp() string {
	stampN++
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(stampN) * time.Second).Format("2006-01-02 15:04:05.000000")
}

func (f *Frappe) method(w http.ResponseWriter, r *http.Request, name, user string) {
	args := map[string]any{}
	if r.Method == http.MethodGet {
		for k, v := range r.URL.Query() {
			args[k] = v[0]
		}
	} else if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&args)
	}
	if r.Method != http.MethodGet && (name == "frappe.client.get_list" || name == "frappe.client.get_count") {
		// A read whose arguments are too long for a URL arrives as a JSON body. Frappe merges the body into the same form
		// the query string fills, so the fake does the same.
		q := url.Values{}
		for k, v := range args {
			if str, ok := v.(string); ok {
				q.Set(k, str)
				continue
			}
			if v == nil {
				continue
			}
			if b, err := json.Marshal(v); err == nil {
				q.Set(k, string(b))
			}
		}
		r = r.Clone(r.Context())
		r.URL.RawQuery = q.Encode()
		r.Method = http.MethodGet
	}
	switch name {
	case "frappe.client.get_list":
		dt, _ := args["doctype"].(string)
		reply(w, "message", f.list(dt, r))
	case "frappe.client.get_count":
		dt, _ := args["doctype"].(string)
		rr := r.Clone(r.Context())
		q := rr.URL.Query()
		q.Del("limit_page_length")
		rr.URL.RawQuery = q.Encode()
		reply(w, "message", len(f.list(dt, rr)))
	case "emberops_hearth.api.resolve":
		dt, _ := args["doctype"].(string)
		hid, _ := args["hearth_id"].(string)
		if d := f.byHearthID(dt, hid); d != nil {
			reply(w, "message", d["name"])
			return
		}
		reply(w, "message", nil)
	case "emberops_hearth.api.time_off_backend":
		if f.HRMS {
			reply(w, "message", "Leave Application")
		} else {
			reply(w, "message", "Hearth Time Off Request")
		}
	case "emberops_hearth.api.get_by_hearth_id":
		dt, _ := args["doctype"].(string)
		hid, _ := args["hearth_id"].(string)
		reply(w, "message", f.byHearthID(dt, hid))
	case "emberops_hearth.api.upsert":
		dt, _ := args["doctype"].(string)
		hid, _ := args["hearth_id"].(string)
		vals, _ := args["values"].(map[string]any)
		res, err := f.upsert(dt, hid, vals, user)
		var bad invalid
		if errors.As(err, &bad) {
			fail(w, 417, "ValidationError", err.Error())
			return
		}
		if err != nil {
			fail(w, 404, "DoesNotExistError", err.Error())
			return
		}
		reply(w, "message", res)
	case "emberops_hearth.api.bulk_upsert":
		items, _ := args["items"].([]any)
		if len(items) > 500 {
			fail(w, 417, "ValidationError", "Send at most 500 items at a time")
			return
		}
		out := map[string]any{"ok": 0, "failed": []any{}, "done": []any{}}
		ok, failed, done := 0, []any{}, []any{}
		for _, it := range items {
			m, _ := it.(map[string]any)
			dt, _ := m["doctype"].(string)
			hid, _ := m["hearth_id"].(string)
			vals, _ := m["values"].(map[string]any)
			res, err := f.upsert(dt, hid, vals, user)
			if err != nil {
				failed = append(failed, map[string]any{"doctype": dt, "hearth_id": hid, "error": err.Error()})
				continue
			}
			ok++
			done = append(done, map[string]any{"doctype": dt, "hearth_id": hid, "name": res["name"], "created": res["created"]})
		}
		out["ok"], out["failed"], out["done"] = ok, failed, done
		reply(w, "message", out)
	case "emberops_hearth.api.upsert_time_entry":
		vals, _ := args["values"].(map[string]any)
		ts, row := f.timeEntry(vals, user)
		reply(w, "message", map[string]any{"timesheet": ts, "row": row["name"], "modified": row["modified"]})
	case "emberops_hearth.api.timer_start":
		emp := f.employeeOf(user)
		for _, t := range f.docs["Hearth Timer"] {
			if t["employee"] == emp {
				fail(w, 409, "DuplicateEntryError", "A timer is already running")
				return
			}
		}
		doc := map[string]any{"owner": user, "employee": emp, "started_at": time.Now().UTC().Format("2006-01-02 15:04:05")}
		for _, k := range []string{"task", "maintenance_work_order", "project", "note", "tags"} {
			if v, ok := args[k]; ok {
				doc[k] = v
			}
		}
		n := f.put("Hearth Timer", doc)
		reply(w, "message", map[string]any{"name": n, "started_at": doc["started_at"]})
	case "emberops_hearth.api.timer_stop":
		emp := f.employeeOf(user)
		var timer map[string]any
		for _, t := range f.docs["Hearth Timer"] {
			if t["employee"] == emp {
				timer = t
			}
		}
		if timer == nil {
			fail(w, 404, "DoesNotExistError", "No timer is running")
			return
		}
		delete(f.docs["Hearth Timer"], timer["name"].(string))
		end := time.Now().UTC().Format("2006-01-02 15:04:05")
		row := map[string]any{
			"hearth_id": args["hearth_id"], "employee": emp, "from_time": timer["started_at"], "to_time": end,
			"task": timer["task"], "maintenance_work_order": timer["maintenance_work_order"],
			"description": timer["note"], "hearth_tags": timer["tags"],
		}
		if n, _ := args["note"].(string); n != "" {
			row["description"] = n
		}
		ts, r := f.timeEntry(row, user)
		reply(w, "message", map[string]any{"timesheet": ts, "row": r["name"]})
	case "emberops_hearth.api.week_load":
		emp, _ := args["employee"].(string)
		if emp == "" {
			emp = f.employeeOf(user)
		}
		start, _ := args["week_start"].(string)
		st, err := time.Parse("2006-01-02", start)
		if err != nil {
			now := time.Now().UTC()
			st = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			st = st.AddDate(0, 0, -int((st.Weekday()+6)%7))
		}
		end := st.AddDate(0, 0, 7)
		hours := 0.0
		for _, ts := range f.docs["Timesheet"] {
			if ts["employee"] != emp {
				continue
			}
			for _, d := range f.docs["Timesheet Detail"] {
				ft, _ := d["from_time"].(string)
				if d["parent"] != ts["name"] || d["hearth_deleted_at"] != nil || ft < st.Format("2006-01-02") || ft >= end.Format("2006-01-02") {
					continue
				}
				h, _ := d["hours"].(float64)
				hours += h
			}
		}
		capMin := 2400.0
		for _, e := range f.docs["Employee"] {
			if e["name"] == emp {
				if c, ok := e["hearth_minutes_per_week"].(float64); ok && c > 0 {
					capMin = c
				}
			}
		}
		reply(w, "message", map[string]any{"employee": emp, "week_start": st.Format("2006-01-02"),
			"minutes": int(hours*60 + 0.5), "capacity_minutes": int(capMin)})
	case "frappe.client.cancel":
		dt, _ := args["doctype"].(string)
		n, _ := args["name"].(string)
		if d, ok := f.docs[dt][n]; ok {
			d["docstatus"] = 2.0
			reply(w, "message", d)
			return
		}
		fail(w, 404, "DoesNotExistError", dt+" "+n+" not found")
	default:
		fail(w, 404, "", "no such method "+name)
	}
}

// employeeOf is the Employee a Frappe user is, falling back to the user.
func (f *Frappe) employeeOf(user string) string {
	for _, e := range f.docs["Employee"] {
		if e["user_id"] == user {
			return e["name"].(string)
		}
	}
	return user
}

// timeEntry writes one Timesheet Detail row into the draft Timesheet of its
// employee and day, as the addon does. The row does not carry the employee:
// its parent does.
func (f *Frappe) timeEntry(vals map[string]any, user string) (string, map[string]any) {
	if vals == nil {
		vals = map[string]any{}
	}
	f.resolveRefs(vals)
	emp, _ := vals["employee"].(string)
	delete(vals, "employee")
	hid, _ := vals["hearth_id"].(string)
	vals["hearth_id"] = strings.ToLower(hid)
	if d := f.byHearthID("Timesheet Detail", hid); d != nil {
		for k, v := range vals {
			d[k] = v
		}
		d["modified"] = stamp()
		return d["parent"].(string), d
	}
	ft, _ := vals["from_time"].(string)
	day := ft
	if len(day) >= 10 {
		day = day[:10]
	}
	var ts map[string]any
	for _, t := range f.docs["Timesheet"] {
		if t["employee"] == emp && t["start_date"] == day {
			ts = t
		}
	}
	if ts == nil {
		ts = map[string]any{"employee": emp, "start_date": day, "docstatus": 0, "owner": user}
		f.put("Timesheet", ts)
	}
	row := map[string]any{"parent": ts["name"], "owner": user, "modified": stamp()}
	for k, v := range vals {
		row[k] = v
	}
	f.put("Timesheet Detail", row)
	return ts["name"].(string), row
}

func (f *Frappe) byHearthID(doctype, hid string) map[string]any {
	hid = strings.ToLower(hid)
	for _, d := range f.docs[doctype] {
		if s, _ := d["hearth_id"].(string); strings.ToLower(s) == hid {
			return d
		}
	}
	return nil
}

// LinkTargets says which doctype a link field points at, for fields where more
// than one doctype carries the same hearth_id (a person is both a User and an
// Employee). Fields not listed search every doctype.
var LinkTargets = map[string]string{
	"view_user": "User", "decided_by": "User", "read_by": "User", "done_by": "User",
	"user": "User", "employee": "Employee", "owner": "User", "reported_by": "Employee", "hearth_approver": "Employee",
	"recorded_by": "User", "requester": "Employee", "assigned_technician": "Employee",
	"hearth_decided_by": "User",
}

// resolveRefs replaces "hearth:<id>" with the name of the document holding
// that hearth_id.
func (f *Frappe) resolveRefs(v map[string]any) {
	for k, val := range v {
		switch x := val.(type) {
		case string:
			if id, ok := strings.CutPrefix(x, "hearth:"); ok {
				v[k] = id
				for dt, byName := range f.docs {
					if want, ok := LinkTargets[k]; ok && want != dt {
						continue
					}
					for _, d := range byName {
						if s, _ := d["hearth_id"].(string); strings.EqualFold(s, id) {
							v[k] = d["name"]
						}
					}
				}
			}
		case []any:
			for _, e := range x {
				if m, ok := e.(map[string]any); ok {
					f.resolveRefs(m)
				}
			}
		}
	}
}

func (f *Frappe) upsert(doctype, hid string, vals map[string]any, user string) (map[string]any, error) {
	if vals == nil {
		vals = map[string]any{}
	}
	delete(vals, "doctype")
	f.resolveRefs(vals)
	vals["hearth_id"] = strings.ToLower(hid)
	d := f.byHearthID(doctype, hid)
	created := d == nil
	if created && f.RejectNew != nil {
		if why := f.RejectNew(doctype, vals); why != "" {
			return nil, invalid(why)
		}
	}
	if created {
		d = map[string]any{"owner": user}
	} else if err := f.decidedTimeOff(doctype, d, vals); err != nil {
		return nil, err
	}
	for k, v := range vals {
		d[k] = v
	}
	d["modified"] = stamp()
	name := f.put(doctype, d)
	f.children(doctype, name, d)
	return map[string]any{"name": name, "created": created, "modified": d["modified"]}, nil
}

// children keeps the child tables some doctypes are read through (Stock Entry
// Detail, Dynamic Link) as documents of their own with a parent, as Frappe's
// get_list on a child table sees them, and the top-level mirrors Frappe sets.
func (f *Frappe) children(doctype, name string, d map[string]any) {
	child := func(dt, key string) {
		rows, _ := d[key].([]any)
		for n, existing := range f.docs[dt] {
			if existing["parent"] == name {
				delete(f.docs[dt], n)
			}
		}
		for _, r := range rows {
			if m, ok := r.(map[string]any); ok {
				c := map[string]any{"parent": name}
				for k, v := range m {
					c[k] = v
				}
				f.put(dt, c)
			}
		}
	}
	switch doctype {
	case "Stock Entry":
		// The addon fills the default warehouse on the side the entry type needs.
		rows, _ := d["items"].([]any)
		for _, r := range rows {
			if m, ok := r.(map[string]any); ok {
				if d["stock_entry_type"] == "Material Issue" && m["s_warehouse"] == nil {
					m["s_warehouse"] = "Stores"
				}
				if d["stock_entry_type"] == "Material Receipt" && m["t_warehouse"] == nil {
					m["t_warehouse"] = "Stores"
				}
			}
		}
		child("Stock Entry Detail", "items")
	case "Contact":
		child("Dynamic Link", "links")
		if mail, _ := d["email_ids"].([]any); len(mail) > 0 {
			d["email_id"] = mail[0].(map[string]any)["email_id"]
		}
		if ph, _ := d["phone_nos"].([]any); len(ph) > 0 {
			d["phone"] = ph[0].(map[string]any)["phone"]
		}
	}
}

// list applies fields, filters, or_filters, order_by and limit.
func (f *Frappe) list(doctype string, r *http.Request) []map[string]any {
	q := r.URL.Query()
	var fields []string
	_ = json.Unmarshal([]byte(q.Get("fields")), &fields)
	var filters, orFilters [][]any
	_ = json.Unmarshal([]byte(q.Get("filters")), &filters)
	_ = json.Unmarshal([]byte(q.Get("or_filters")), &orFilters)

	// Child rows (Timesheet Detail) are stored as documents of their own
	// doctype, so a parent query reads the same set.
	rows := f.sorted(doctype)

	var out []map[string]any
	for _, d := range rows {
		if !matchAll(d, filters) {
			continue
		}
		if len(orFilters) > 0 && !matchAny(d, orFilters) {
			continue
		}
		out = append(out, d)
	}
	if ob := q.Get("order_by"); ob != "" {
		field, dir, _ := strings.Cut(strings.TrimSpace(ob), " ")
		field = strings.Trim(field, "`")
		if i := strings.LastIndex(field, "."); i >= 0 {
			field = field[i+1:]
		}
		desc := strings.EqualFold(dir, "desc")
		sort.SliceStable(out, func(i, j int) bool {
			a, b := fmt.Sprint(out[i][field]), fmt.Sprint(out[j][field])
			if desc {
				return a > b
			}
			return a < b
		})
	}
	start, _ := strconv.Atoi(q.Get("limit_start"))
	if start > len(out) {
		start = len(out)
	}
	out = out[start:]
	if lim, err := strconv.Atoi(q.Get("limit_page_length")); err == nil && lim > 0 && lim < len(out) {
		out = out[:lim]
	}
	if len(fields) == 0 {
		fields = []string{"name"}
	}
	proj := make([]map[string]any, 0, len(out))
	for _, d := range out {
		p := map[string]any{}
		for _, fl := range fields {
			if fl == "*" {
				for k, v := range d {
					p[k] = v
				}
				continue
			}
			p[fl] = d[fl]
			if fl == "_user_tags" && d[fl] == nil {
				// Frappe keeps tags as a comma separated column; the fake keeps the list that was sent.
				if tags, ok := d["_tags"].([]any); ok {
					parts := make([]string, 0, len(tags))
					for _, t := range tags {
						parts = append(parts, fmt.Sprint(t))
					}
					p[fl] = strings.Join(parts, ",")
				}
			}
		}
		proj = append(proj, p)
	}
	return proj
}

func matchAll(d map[string]any, fs [][]any) bool {
	for _, f := range fs {
		if !match(d, f) {
			return false
		}
	}
	return true
}

func matchAny(d map[string]any, fs [][]any) bool {
	for _, f := range fs {
		if match(d, f) {
			return true
		}
	}
	return false
}

func match(d map[string]any, f []any) bool {
	if len(f) == 4 { // [doctype, field, op, value]
		f = f[1:]
	}
	if len(f) != 3 {
		return true
	}
	field, _ := f[0].(string)
	op, _ := f[1].(string)
	have := d[field]
	hs := fmt.Sprint(have)
	if have == nil {
		hs = ""
	}
	switch strings.ToLower(op) {
	case "=":
		return hs == fmt.Sprint(f[2])
	case "!=":
		return hs != fmt.Sprint(f[2])
	case "in", "not in":
		list, _ := f[2].([]any)
		in := false
		for _, e := range list {
			if hs == fmt.Sprint(e) {
				in = true
			}
		}
		return in == (strings.ToLower(op) == "in")
	case "like":
		pat := strings.ToLower(fmt.Sprint(f[2]))
		pat = strings.Trim(pat, "%")
		return strings.Contains(strings.ToLower(hs), pat)
	case ">=":
		return hs >= fmt.Sprint(f[2])
	case "<=":
		return hs <= fmt.Sprint(f[2])
	case ">":
		return hs > fmt.Sprint(f[2])
	case "<":
		return hs < fmt.Sprint(f[2])
	case "is":
		set := fmt.Sprint(f[2]) == "set"
		return (hs != "") == set
	}
	return true
}

// invalid is a refusal the addon makes as a validation error (HTTP 417), not a missing record.
type invalid string

func (e invalid) Error() string { return string(e) }

// decidedTimeOff is the addon's rule for a decided request on a site with HRMS (emberops_hearth/api.py
// _amend_decided_leave): once approved, denied or cancelled it can be cancelled and
// annotated and nothing else. Changing dates, kind or days means cancelling it and
// filing a new request.
func (f *Frappe) decidedTimeOff(doctype string, d, vals map[string]any) error {
	if doctype != "Hearth Time Off Request" || !f.HRMS {
		return nil
	}
	cur, _ := d["status"].(string)
	if cur != "approved" && cur != "denied" && cur != "cancelled" {
		return nil
	}
	if want, ok := vals["status"].(string); ok && want != cur && !(want == "cancelled" && cur != "cancelled") {
		return invalid(fmt.Sprintf("already %s; cancel it and file a new request", cur))
	}
	if cur == "cancelled" {
		if want, ok := vals["status"].(string); ok && want != "cancelled" {
			return invalid("is cancelled and cannot change")
		}
	}
	for _, k := range []string{"starts_on", "ends_on", "leave_kind", "days", "half_day", "reason", "employee"} {
		if v, ok := vals[k]; ok && fmt.Sprint(v) != fmt.Sprint(d[k]) {
			return invalid(fmt.Sprintf("already decided; change %s by cancelling it and filing a new request", k))
		}
	}
	return nil
}
