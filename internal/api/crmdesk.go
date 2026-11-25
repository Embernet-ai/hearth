package api

// The sales desk over the API: follow-ups, the forecast, the sales report, bulk change. The same calls the pages make,
// refused the same way. Everybody who works sales (the Sales team) and super admins may use them; anybody else is told
// there is nothing there. Nothing defaults to "mine".

import (
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

func (s *Service) deskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/followups", s.crmDesk(s.listFollowups))
	mux.HandleFunc("POST /v1/followups", s.crmDesk(s.createFollowup))
	mux.HandleFunc("POST /v1/followups/bulk", s.crmDesk(s.bulkFollowups))
	mux.HandleFunc("PATCH /v1/followups/{id}", s.crmDesk(s.patchFollowup))
	mux.HandleFunc("DELETE /v1/followups/{id}", s.crmDesk(s.deleteFollowup))
	mux.HandleFunc("GET /v1/crm/forecast", s.crmDesk(s.forecast))
	mux.HandleFunc("GET /v1/crm/reports", s.crmDesk(s.salesReport))
	mux.HandleFunc("POST /v1/crm/bulk", s.crmDesk(s.bulk))
	mux.HandleFunc("PATCH /v1/activities/{id}", s.crmDesk(s.patchActivity))
	mux.HandleFunc("GET /v1/crm/duplicates", s.crmDesk(s.duplicates))
	mux.HandleFunc("POST /v1/crm/merge", s.crmDesk(s.merge))
	mux.HandleFunc("GET /v1/crm/properties", s.crmDesk(s.listProperties))
	mux.HandleFunc("POST /v1/crm/properties", s.crmDesk(s.saveProperty))
	mux.HandleFunc("PATCH /v1/crm/properties/{id}", s.crmDesk(s.saveProperty))
	mux.HandleFunc("DELETE /v1/crm/properties/{id}", s.crmDesk(s.deleteProperty))
}

// crmDesk hands a handler the desk, after checking the caller works sales.
func (s *Service) crmDesk(h func(w http.ResponseWriter, r *http.Request, d crm.Desk)) http.HandlerFunc {
	return s.crm(func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.sales.(crm.Desk)
		if !ok {
			writeErr(w, http.StatusNotFound, "the CRM is not available on this deployment")
			return
		}
		c := from(r)
		has, err := s.sales.HasSalesAccess(r.Context(), c.OrgID, c.Subjects)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not check your access")
			return
		}
		if !has {
			writeErr(w, http.StatusNotFound, "no such record")
			return
		}
		h(w, r, d)
	})
}

type amtJSON struct {
	Currency string `json:"currency"`
	Cents    int64  `json:"cents"`
}

func amtsJSON(in []crm.Amt) []amtJSON {
	out := make([]amtJSON, 0, len(in))
	for _, a := range in {
		out = append(out, amtJSON{a.Currency, a.Cents})
	}
	return out
}

func followupJSON(f *crm.Followup) map[string]any {
	out := map[string]any{
		"id": f.ID, "kind": f.Kind, "title": f.Title, "body": f.Body, "status": f.Status, "overdue": f.Overdue,
		"assignee_id": nil, "assignee_name": f.AssigneeName, "due": nil, "completed_at": f.CompletedAt, "created_at": f.CreatedAt,
		"target": map[string]any{"type": string(f.Target.Type), "id": f.Target.ID, "name": f.TargetName},
	}
	if f.AssigneeID != "" {
		out["assignee_id"] = f.AssigneeID
	}
	if f.DueAt != nil {
		out["due"] = f.DueAt.Format("2006-01-02")
	}
	return out
}

func (s *Service) listFollowups(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	v := r.URL.Query()
	q := crm.FollowupQuery{Assignee: v.Get("assignee"), Status: v.Get("status"), Due: v.Get("due"), Kind: v.Get("kind"),
		Limit: queryInt(r, "limit", 100, 500), Offset: queryInt(r, "offset", 0, 0)}
	if q.Assignee == "me" {
		q.Assignee = c.PrincipalID
	}
	if q.Status == "all" {
		q.Status = ""
	}
	if t := v.Get("target_type"); t != "" {
		q.Target = &core.Ref{Type: core.EntityType(t), ID: v.Get("target_id")}
	}
	rows, total, err := d.Followups(r.Context(), c.OrgID, c.Subjects, q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list the follow-ups")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, followupJSON(f))
	}
	writeJSON(w, http.StatusOK, map[string]any{"followups": out, "total": total})
}

type followupBody struct {
	Kind     *string `json:"kind"`
	Title    *string `json:"title"`
	Body     *string `json:"body"`
	Due      *string `json:"due"`
	Assignee *string `json:"assignee_id"`
	Done     *bool   `json:"done"`
	// SnoozeDays pushes the due day out by this many days (1 to 365), from the due day or from today when that has passed.
	SnoozeDays *int `json:"snooze_days"`
	Target     *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"target"`
}

func parseDue(p *string) (*time.Time, bool) {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil, true
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(*p))
	if err != nil {
		return nil, false
	}
	return &t, true
}

func (s *Service) createFollowup(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	var b followupBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	due, ok := parseDue(b.Due)
	if !ok {
		writeErr(w, http.StatusBadRequest, "due is YYYY-MM-DD")
		return
	}
	if b.Target == nil || b.Title == nil {
		writeErr(w, http.StatusBadRequest, "a follow-up needs a title and a target")
		return
	}
	p := crm.FollowupParams{Title: *b.Title, DueAt: due, Target: core.Ref{Type: core.EntityType(b.Target.Type), ID: b.Target.ID}}
	if b.Body != nil {
		p.Body = *b.Body
	}
	if b.Assignee != nil {
		p.Assignee = *b.Assignee
	}
	if b.Kind != nil {
		p.Kind = *b.Kind
	}
	f, err := d.AddFollowup(r.Context(), c.OrgID, c.Subjects, p)
	if err != nil {
		crmErr(w, err, "you cannot add a follow-up there")
		return
	}
	writeJSON(w, http.StatusCreated, followupJSON(f))
}

func (s *Service) patchFollowup(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	id := r.PathValue("id")
	var b followupBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if b.Due != nil || b.Assignee != nil {
		due, ok := parseDue(b.Due)
		if !ok {
			writeErr(w, http.StatusBadRequest, "due is YYYY-MM-DD")
			return
		}
		assignee := ""
		if b.Assignee != nil {
			assignee = *b.Assignee
		}
		// A date that is not sent keeps the date it has: only a sent empty string clears it.
		if b.Due == nil {
			rows, _, err := d.Followups(r.Context(), c.OrgID, c.Subjects, crm.FollowupQuery{Limit: 500})
			if err == nil {
				for _, f := range rows {
					if f.ID == id {
						due = f.DueAt
					}
				}
			}
		}
		if err := d.RescheduleFollowup(r.Context(), c.OrgID, c.Subjects, id, due, assignee); err != nil {
			crmErr(w, err, "you cannot change that follow-up")
			return
		}
	}
	if b.SnoozeDays != nil {
		if err := d.SnoozeFollowup(r.Context(), c.OrgID, c.Subjects, id, *b.SnoozeDays); err != nil {
			crmErr(w, err, "you cannot change that follow-up")
			return
		}
	}
	if b.Done != nil {
		if err := d.SetFollowupDone(r.Context(), c.OrgID, c.Subjects, id, *b.Done); err != nil {
			crmErr(w, err, "you cannot change that follow-up")
			return
		}
	}
	rows, _, err := d.Followups(r.Context(), c.OrgID, c.Subjects, crm.FollowupQuery{Limit: 500})
	if err == nil {
		for _, f := range rows {
			if f.ID == id {
				writeJSON(w, http.StatusOK, followupJSON(f))
				return
			}
		}
	}
	writeErr(w, http.StatusNotFound, "no such record")
}

func (s *Service) deleteFollowup(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	if err := d.DeleteFollowup(r.Context(), c.OrgID, c.Subjects, r.PathValue("id")); err != nil {
		crmErr(w, err, "you cannot remove that follow-up")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Service) forecast(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	owner := r.URL.Query().Get("owner")
	if owner == "me" {
		owner = c.PrincipalID
	}
	f, err := d.Forecast(r.Context(), c.OrgID, c.Subjects, owner, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not build the forecast")
		return
	}
	buckets := make([]map[string]any, 0, len(f.Buckets))
	for _, b := range f.Buckets {
		deals := make([]map[string]any, 0, len(b.Deals))
		for _, dl := range b.Deals {
			m := dealJSON(dl)
			deals = append(deals, m)
		}
		buckets = append(buckets, map[string]any{"key": b.Key, "label": b.Label, "count": b.Count,
			"value": amtsJSON(b.Value), "weighted": amtsJSON(b.Weighted), "deals": deals})
	}
	owners := make([]map[string]any, 0, len(f.Owners))
	for _, o := range f.Owners {
		owners = append(owners, map[string]any{"owner_id": o.OwnerID, "name": o.Name, "count": o.Count,
			"value": amtsJSON(o.Value), "weighted": amtsJSON(o.Weighted)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": f.Count, "value": amtsJSON(f.Value), "weighted": amtsJSON(f.Weighted),
		"won_this_month": map[string]any{"count": f.WonCount, "value": amtsJSON(f.Won)}, "buckets": buckets, "owners": owners})
}

func (s *Service) salesReport(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	q := r.URL.Query()
	to := time.Now()
	if t, err := time.Parse("2006-01-02", q.Get("to")); err == nil {
		to = t
	}
	fromDay := to.AddDate(0, 0, -29)
	if t, err := time.Parse("2006-01-02", q.Get("from")); err == nil {
		fromDay = t
	}
	if fromDay.After(to) {
		fromDay, to = to, fromDay
	}
	owner := q.Get("owner")
	if owner == "me" {
		owner = c.PrincipalID
	}
	rep, err := d.SalesReport(r.Context(), c.OrgID, c.Subjects, fromDay, to, owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not build the report")
		return
	}
	stages := make([]map[string]any, 0, len(rep.Pipeline))
	for _, st := range rep.Pipeline {
		stages = append(stages, map[string]any{"stage": st.Stage, "label": st.Label, "probability": st.Probability, "count": st.Count,
			"value": amtsJSON(st.Value), "weighted": amtsJSON(st.Weighted)})
	}
	owners := make([]map[string]any, 0, len(rep.Owners))
	for _, o := range rep.Owners {
		owners = append(owners, map[string]any{"owner_id": o.OwnerID, "name": o.Name, "open_count": o.OpenCount, "open": amtsJSON(o.Open),
			"weighted": amtsJSON(o.Weighted), "won_count": o.WonCount, "won": amtsJSON(o.Won), "lost_count": o.LostCount})
	}
	acts := make([]map[string]any, 0, len(rep.Activity))
	for _, a := range rep.Activity {
		acts = append(acts, map[string]any{"person_id": a.PersonID, "name": a.Name, "notes": a.Notes, "calls": a.Calls,
			"meetings": a.Meetings, "emails": a.Emails, "total": a.Total})
	}
	created := make([]map[string]any, 0, len(rep.Created))
	for _, m := range rep.Created {
		created = append(created, map[string]any{"month": m.Month, "count": m.Count, "value": amtsJSON(m.Value)})
	}
	reasons := make([]map[string]any, 0, len(rep.LostReasons))
	for _, l := range rep.LostReasons {
		reasons = append(reasons, map[string]any{"reason": l.Reason, "count": l.Count, "value": amtsJSON(l.Value)})
	}
	ages := make([]map[string]any, 0, len(rep.StageAge))
	for _, a := range rep.StageAge {
		ages = append(ages, map[string]any{"stage": a.Stage, "label": a.Label, "count": a.Count, "average_days": a.AvgDays, "oldest_days": a.OldestDays})
	}
	var winRate any
	if rep.WinRate >= 0 {
		winRate = rep.WinRate
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": fromDay.Format("2006-01-02"), "to": to.Format("2006-01-02"),
		"open": map[string]any{"count": rep.OpenCount, "value": amtsJSON(rep.Open), "weighted": amtsJSON(rep.Weighted)},
		"won":  map[string]any{"count": rep.WonCount, "value": amtsJSON(rep.Won), "average": amtsJSON(rep.AvgWon), "average_days_to_win": rep.AvgCycleDays},
		"lost": map[string]any{"count": rep.LostCount, "value": amtsJSON(rep.Lost)}, "win_rate_percent": winRate,
		"pipeline": stages, "owners": owners, "activity": acts, "deals_made": created, "lost_reasons": reasons, "stage_age": ages,
	})
}

type bulkBody struct {
	Type    string   `json:"type"`
	IDs     []string `json:"ids"`
	Owner   *string  `json:"owner_id"`
	Stage   string   `json:"stage"`
	AddTag  string   `json:"add_tag"`
	AddType string   `json:"add_type"`
}

func (s *Service) bulk(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	var b bulkBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := d.BulkApply(r.Context(), c.OrgID, c.Subjects, crm.BulkSpec{Type: core.EntityType(b.Type), IDs: b.IDs,
		Owner: b.Owner, Stage: b.Stage, AddTag: b.AddTag, AddType: b.AddType})
	if err != nil {
		crmErr(w, err, "you cannot change those")
		return
	}
	failed := make([]map[string]any, 0, len(res.Failed))
	for _, f := range res.Failed {
		failed = append(failed, map[string]any{"id": f.ID, "why": f.Why})
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": res.Done, "failed": failed})
}

func (s *Service) duplicates(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	typ := core.EntityContact
	if r.URL.Query().Get("type") == "company" {
		typ = core.EntityCompany
	}
	groups, err := d.Duplicates(r.Context(), c.OrgID, c.Subjects, typ)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not look for duplicates")
		return
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		recs := make([]map[string]any, 0, len(g.Records))
		for _, x := range g.Records {
			recs = append(recs, map[string]any{"id": x.ID, "name": x.Name, "detail": x.Detail, "owner_name": x.Owner, "deals": x.Deals, "updated_at": x.UpdatedAt})
		}
		out = append(out, map[string]any{"reason": g.Reason, "records": recs})
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": string(typ), "groups": out})
}

type mergeBody struct {
	Type     string   `json:"type"`
	Survivor string   `json:"survivor_id"`
	Merge    []string `json:"merge_ids"`
}

func (s *Service) merge(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	var b mergeBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := d.Merge(r.Context(), c.OrgID, c.Subjects, core.EntityType(b.Type), b.Survivor, b.Merge); err != nil {
		crmErr(w, err, "you cannot merge those")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"merged": len(b.Merge), "survivor_id": b.Survivor})
}

type activityPatch struct {
	Title       *string    `json:"title"`
	Body        *string    `json:"body"`
	OccurredAt  *time.Time `json:"occurred_at"`
	Direction   *string    `json:"direction"`
	Disposition *string    `json:"disposition"`
	DurationMs  *int64     `json:"duration_ms"`
}

func (s *Service) patchActivity(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	var b activityPatch
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := d.UpdateActivity(r.Context(), c.OrgID, c.Subjects, r.PathValue("id"), crm.ActivityEdit{Title: b.Title, Body: b.Body,
		OccurredAt: b.OccurredAt, Direction: b.Direction, Disposition: b.Disposition, DurationMs: b.DurationMs})
	if err != nil {
		crmErr(w, err, "you cannot change that")
		return
	}
	writeJSON(w, http.StatusOK, activityJSON(a))
}

// -----------------------------------------------------------------------------
// Custom properties
// -----------------------------------------------------------------------------

func (s *Service) props() crm.Props { p, _ := s.sales.(crm.Props); return p }

// addProps puts a record's custom fields on its answer, by key, with null for one that is not set. A failure leaves them out.
func (s *Service) addProps(r *http.Request, c *Caller, kind core.EntityType, out map[string]any) {
	p := s.props()
	id, _ := out["id"].(string)
	if p == nil || id == "" {
		return
	}
	vals, err := p.PropertiesFor(r.Context(), c.OrgID, c.Subjects, core.Ref{Type: kind, ID: id})
	if err != nil {
		return
	}
	m := make(map[string]any, len(vals))
	for _, v := range vals {
		m[v.Key] = v.Value
	}
	out["properties"] = m
}

// checkProps refuses a request whose custom field values are wrong, before it changes anything.
func (s *Service) checkProps(w http.ResponseWriter, r *http.Request, c *Caller, kind core.EntityType, values map[string]any) bool {
	if len(values) == 0 {
		return true
	}
	p := s.props()
	if p == nil {
		writeErr(w, http.StatusBadRequest, "custom fields are not available on this deployment")
		return false
	}
	if err := p.ValidateProperties(r.Context(), c.OrgID, kind, values); err != nil {
		crmErr(w, err, "you cannot set those")
		return false
	}
	return true
}

func (s *Service) setProps(r *http.Request, c *Caller, ref core.Ref, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	return s.props().SetProperties(r.Context(), c.OrgID, c.Subjects, ref, values)
}

func propertyJSON(d crm.PropertyDef) map[string]any {
	return map[string]any{"id": d.ID, "type": string(d.Kind), "key": d.Key, "label": d.Label, "data_type": d.DataType, "options": d.Options}
}

func (s *Service) listProperties(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	p := s.props()
	typ := core.EntityType(r.URL.Query().Get("type"))
	if p == nil {
		writeErr(w, http.StatusNotFound, "custom fields are not available on this deployment")
		return
	}
	defs, err := p.PropertyDefs(r.Context(), c.OrgID, typ)
	if err != nil {
		crmErr(w, err, "you cannot list those")
		return
	}
	out := make([]map[string]any, 0, len(defs))
	for _, x := range defs {
		out = append(out, propertyJSON(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"properties": out, "data_types": crm.PropertyTypes})
}

type propertyBody struct {
	Type     string   `json:"type"`
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	DataType string   `json:"data_type"`
	Options  []string `json:"options"`
}

func (s *Service) saveProperty(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	p := s.props()
	var b propertyBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, "custom fields are not available on this deployment")
		return
	}
	def, err := p.SaveProperty(r.Context(), c.OrgID, c.Subjects, crm.PropertyInput{ID: r.PathValue("id"), Kind: core.EntityType(b.Type), Key: b.Key,
		Label: b.Label, DataType: b.DataType, Options: b.Options})
	if err != nil {
		crmErr(w, err, "only an admin defines custom fields")
		return
	}
	status := http.StatusCreated
	if r.PathValue("id") != "" {
		status = http.StatusOK
	}
	writeJSON(w, status, propertyJSON(*def))
}

func (s *Service) deleteProperty(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	p := s.props()
	if p == nil {
		writeErr(w, http.StatusNotFound, "custom fields are not available on this deployment")
		return
	}
	if err := p.DeleteProperty(r.Context(), c.OrgID, c.Subjects, core.EntityType(r.URL.Query().Get("type")), r.PathValue("id")); err != nil {
		crmErr(w, err, "only an admin removes custom fields")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

type followupBulkBody struct {
	IDs  []string `json:"ids"`
	Done *bool    `json:"done"`
}

func (s *Service) bulkFollowups(w http.ResponseWriter, r *http.Request, d crm.Desk) {
	c := from(r)
	var b followupBulkBody
	if err := decode(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	done := true
	if b.Done != nil {
		done = *b.Done
	}
	res, err := d.CompleteFollowups(r.Context(), c.OrgID, c.Subjects, b.IDs, done)
	if err != nil {
		crmErr(w, err, "you cannot change those")
		return
	}
	failed := make([]map[string]any, 0, len(res.Failed))
	for _, f := range res.Failed {
		failed = append(failed, map[string]any{"id": f.ID, "why": f.Why})
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": res.Done, "failed": failed})
}
