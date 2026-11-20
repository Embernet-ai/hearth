package stores

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

// TimeOff keeps time off requests in EmberOPS (Hearth Time Off Request, or
// Leave Application when the site has HRMS; the addon hides the difference).
//
// Who may decide is still Hearth's rule (an approver, a department manager, the
// People capability), read from Hearth's own structure through PG. EmberOPS
// enforces its own DocPerms on the write as well.
type TimeOff struct {
	Base
	Dir    Directory
	PG     TimeOffStructure
	Notify timeoff.Notifier
}

// TimeOffStructure is Hearth's own approver and department structure, which
// the approval rule reads. *timeoff.Service provides it.
type TimeOffStructure interface {
	Decidable(ctx context.Context, orgID, actorID string, requesterIDs []string) (map[string]bool, error)
	Deciders(ctx context.Context, orgID, requesterID string) ([]string, error)
	DepartmentSpace(ctx context.Context, orgID, principalID string) (string, error)
	// Record writes an audit row in Hearth's own database.
	Record(ctx context.Context, e audit.Entry) error
}

var (
	_ timeoff.Store    = (*TimeOff)(nil)
	_ TimeOffStructure = (*timeoff.Service)(nil)
)

const timeOffDoctype = "Hearth Time Off Request"

// Decidable, Deciders and DepartmentSpace read Hearth's own structure.
func (s *TimeOff) Decidable(ctx context.Context, orgID, actorID string, ids []string) (map[string]bool, error) {
	return s.PG.Decidable(ctx, orgID, actorID, ids)
}

func (s *TimeOff) Deciders(ctx context.Context, orgID, requesterID string) ([]string, error) {
	return s.PG.Deciders(ctx, orgID, requesterID)
}

func (s *TimeOff) DepartmentSpace(ctx context.Context, orgID, principalID string) (string, error) {
	return s.PG.DepartmentSpace(ctx, orgID, principalID)
}

// Ask files a request from the actor.
func (s *TimeOff) Ask(ctx context.Context, orgID, actorID string, p timeoff.Params) (*timeoff.Request, error) {
	if !p.Acknowledged {
		return nil, fmt.Errorf("%w: please confirm that the request is subject to approval", timeoff.ErrInvalid)
	}
	days, err := p.Check()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	vals := map[string]any{
		"employee": emberops.Ref(actorID), "leave_kind": p.Kind, "status": "pending",
		"starts_on": p.StartsOn.Format("2006-01-02"), "ends_on": p.EndsOn.Format("2006-01-02"), "days": days,
		"reason": strings.TrimSpace(p.Reason), "comments": strings.TrimSpace(p.Comments), "half_day": boolInt(p.HalfDay),
	}
	if _, err := s.C.Upsert(ctx, timeOffDoctype, id, vals); err != nil {
		if isForbidden(err) {
			return nil, timeoff.ErrDenied
		}
		return nil, err
	}
	r, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Notify != nil {
		if ids, err := s.PG.Deciders(ctx, orgID, actorID); err == nil {
			s.Notify.TimeOffRequested(ctx, orgID, r, ids)
		}
	}
	return r, nil
}

// Mine is the actor's own requests, newest first.
func (s *TimeOff) Mine(ctx context.Context, orgID, actorID string) ([]*timeoff.Request, error) {
	return s.list(ctx, []emberops.Filter{emberops.F("employee", s.employeeName(ctx, actorID))}, 100)
}

// ToDecide is everybody else's requests the actor may decide: pending first,
// oldest first, then the last 30 days of decisions.
func (s *TimeOff) ToDecide(ctx context.Context, orgID, actorID string) ([]*timeoff.Request, error) {
	all, err := s.list(ctx, nil, 500)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range all {
		ids = append(ids, r.RequesterID)
	}
	ok, err := s.PG.Decidable(ctx, orgID, actorID, ids)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	var pending, decided []*timeoff.Request
	for _, r := range all {
		if r.RequesterID == actorID || !ok[r.RequesterID] {
			continue
		}
		switch {
		case r.Status == "pending":
			pending = append(pending, r)
		case r.DecidedAt != nil && r.DecidedAt.After(cutoff):
			decided = append(decided, r)
		case r.Status == "approved" && !r.EndsOn.Before(today):
			// Still to come or under way: somebody may need to change or cancel it.
			decided = append(decided, r)
		}
	}
	// Oldest start first within pending, as Postgres orders by starts_on.
	sortByStart(pending)
	out := append(pending, decided...)
	if len(out) > 200 {
		out = out[:200]
	}
	return out, nil
}

// PendingCount is how many requests are waiting on the actor, for the badge.
func (s *TimeOff) PendingCount(ctx context.Context, orgID, actorID string) (int, error) {
	rows, err := s.list(ctx, []emberops.Filter{emberops.F("status", "pending")}, 500)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.RequesterID)
	}
	ok, err := s.PG.Decidable(ctx, orgID, actorID, ids)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if r.RequesterID != actorID && ok[r.RequesterID] {
			n++
		}
	}
	return n, nil
}

// Decide approves or denies a pending request, and on approval puts the "Out"
// on the requester's department calendar.
func (s *TimeOff) Decide(ctx context.Context, orgID, actorID, id string, approve bool, note string) error {
	r, err := s.get(ctx, id)
	if err != nil || r.Status != "pending" || r.RequesterID == actorID {
		return timeoff.ErrDenied
	}
	ok, err := s.PG.Decidable(ctx, orgID, actorID, []string{r.RequesterID})
	if err != nil {
		return err
	}
	if !ok[r.RequesterID] {
		return timeoff.ErrDenied
	}
	status := "denied"
	if approve {
		status = "approved"
	}
	now := time.Now().UTC()
	vals := map[string]any{
		"status": status, "decided_by": emberops.Ref(actorID),
		"decided_at": s.ts(&now), "decision_note": strings.TrimSpace(note),
	}
	if approve {
		eventID, err := s.putOutEvent(ctx, orgID, r)
		if err != nil {
			return err
		}
		vals["calendar_event"] = emberops.Ref(eventID)
	}
	if _, err := s.C.Upsert(ctx, timeOffDoctype, id, vals); err != nil {
		if isForbidden(err) {
			return timeoff.ErrDenied
		}
		return err
	}
	if s.Notify != nil {
		r.Status = status
		s.Notify.TimeOffDecided(ctx, orgID, actorID, r)
	}
	return nil
}

// Cancel withdraws the actor's own request, pending or approved and not yet
// started, and takes its "Out" off the calendar.
func (s *TimeOff) Cancel(ctx context.Context, orgID, actorID, id string) error {
	r, err := s.get(ctx, id)
	if err != nil || r.RequesterID != actorID {
		return timeoff.ErrDenied
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !(r.Status == "pending" || (r.Status == "approved" && r.StartsOn.After(today))) {
		return timeoff.ErrDenied
	}
	if _, err := s.C.Upsert(ctx, timeOffDoctype, id, map[string]any{"status": "cancelled"}); err != nil {
		return err
	}
	if row, err := s.C.GetByHearthID(ctx, timeOffDoctype, id); err == nil && row.S("calendar_event") != "" {
		_ = s.C.Delete(ctx, "Event", row.S("calendar_event"))
	}
	return nil
}

// ---- reading ----

func (s *TimeOff) employeeName(ctx context.Context, principalID string) string {
	ids, err := s.Dir.EmployeeIDs(ctx)
	if err != nil {
		return ""
	}
	for name, id := range ids {
		if id == principalID {
			return name
		}
	}
	return ""
}

var timeOffFields = []string{"name", "hearth_id", "employee", "leave_kind", "status", "starts_on", "ends_on", "days", "half_day",
	"reason", "comments", "decided_by", "decided_at", "decision_note", "creation"}

// list is one list call for the rows plus two directory reads, however many
// requests come back.
func (s *TimeOff) list(ctx context.Context, filters []emberops.Filter, limit int) ([]*timeoff.Request, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{
		Doctype: timeOffDoctype, Fields: timeOffFields, Filters: filters, OrderBy: "starts_on desc", Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return s.requests(ctx, rows)
}

func (s *TimeOff) get(ctx context.Context, hearthID string) (*timeoff.Request, error) {
	row, err := s.C.GetByHearthID(ctx, timeOffDoctype, hearthID)
	if err != nil {
		return nil, notFound(err, timeoff.ErrNotFound)
	}
	rs, err := s.requests(ctx, []emberops.Row{row})
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}

func (s *TimeOff) requests(ctx context.Context, rows []emberops.Row) ([]*timeoff.Request, error) {
	emps, err := s.Dir.EmployeeIDs(ctx)
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
	out := make([]*timeoff.Request, 0, len(rows))
	for _, r := range rows {
		req := &timeoff.Request{
			ID: r.S("hearth_id"), RequesterID: emps[r.S("employee")], Kind: r.S("leave_kind"),
			Status: r.S("status"), Days: wholeDays(r.F("days"), r.B("half_day")), HalfDay: r.B("half_day"), Reason: r.S("reason"), Comments: r.S("comments"),
			StartsOn: r.T("starts_on", time.UTC), EndsOn: r.T("ends_on", time.UTC),
			DecidedAt: r.TP("decided_at", s.loc()), DecisionNote: r.S("decision_note"),
			CreatedAt: r.T("creation", s.loc()),
		}
		req.RequesterName = names[req.RequesterID]
		req.DeciderName = names[users[r.S("decided_by")]]
		out = append(out, req)
	}
	return out, nil
}

func sortByStart(rs []*timeoff.Request) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].StartsOn.Before(rs[j].StartsOn) })
}

// ---- changing and cancelling somebody else's request ----

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// wholeDays is the working days a request spans, from the days EmberOPS holds: a
// Leave Application with a half day answers 4.5 for a request of five working days.
func wholeDays(total float64, half bool) int {
	if half && total != float64(int(total)) {
		return int(total) + 1
	}
	return int(total)
}

// putOutEvent puts the "Out" on the requester's department calendar (their own when they
// have no department) and returns the event's id.
func (s *TimeOff) putOutEvent(ctx context.Context, orgID string, r *timeoff.Request) (string, error) {
	eventID := uuid.NewString()
	ev := map[string]any{
		"subject": "Out: " + r.RequesterName, "all_day": 1, "event_type": "Public",
		"starts_on": r.StartsOn.Format("2006-01-02") + " 00:00:00",
		"ends_on":   r.EndsOn.AddDate(0, 0, 1).Format("2006-01-02") + " 00:00:00",
	}
	if dept, _ := s.PG.DepartmentSpace(ctx, orgID, r.RequesterID); dept != "" {
		ev["hearth_space"] = emberops.Ref(dept)
	} else {
		ev["event_type"] = "Private"
	}
	if _, err := s.C.Upsert(ctx, "Event", eventID, ev); err != nil {
		return "", fmt.Errorf("timeoff: calendar: %w", err)
	}
	return eventID, nil
}

// Get reads one request for its requester or anybody who may decide it.
func (s *TimeOff) Get(ctx context.Context, orgID, actorID, id string) (*timeoff.Request, error) {
	r, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.RequesterID == actorID {
		return r, nil
	}
	ok, err := s.PG.Decidable(ctx, orgID, actorID, []string{r.RequesterID})
	if err != nil {
		return nil, err
	}
	if !ok[r.RequesterID] {
		return nil, timeoff.ErrNotFound
	}
	return r, nil
}

// changeable reads a request the actor may change: somebody else's, pending or approved,
// that they may decide. Anything else is ErrDenied, as in Postgres.
func (s *TimeOff) changeable(ctx context.Context, orgID, actorID, id string) (*timeoff.Request, error) {
	r, err := s.get(ctx, id)
	if err != nil || r.RequesterID == actorID || !timeoff.Changeable(r.Status) {
		return nil, timeoff.ErrDenied
	}
	ok, err := s.PG.Decidable(ctx, orgID, actorID, []string{r.RequesterID})
	if err != nil {
		return nil, err
	}
	if !ok[r.RequesterID] {
		return nil, timeoff.ErrDenied
	}
	return r, nil
}

func (s *TimeOff) nameOf(ctx context.Context, principalID string) string {
	names, err := s.Dir.Names(ctx)
	if err != nil {
		return ""
	}
	return names[principalID]
}

// Change changes a pending or approved request of somebody else's.
//
// Pending is an ordinary update. An approved one on a site without HRMS is edited where it
// is and decided again by whoever changed it. With HRMS it is a submitted Leave
// Application, which EmberOPS lets be cancelled and annotated and nothing else, so its
// change is the way EmberOPS asks for it: the old application is cancelled, its "Out"
// comes off the calendar, and a new approved one is filed with the new dates under a new
// id. The person's list then shows the old one cancelled and the new one approved. When
// the new one is refused (leave balance, an overlap) the old one is filed again as it was,
// so nobody loses time off because a change failed.
func (s *TimeOff) Change(ctx context.Context, orgID, actorID, id string, c timeoff.Change) (*timeoff.Request, error) {
	before, err := s.changeable(ctx, orgID, actorID, id)
	if err != nil {
		return nil, err
	}
	p, days, err := c.Apply(before)
	if err != nil {
		return nil, err
	}
	after := *before
	after.StartsOn, after.EndsOn, after.Days, after.Kind = p.StartsOn, p.EndsOn, days, p.Kind
	after.Reason, after.Comments, after.HalfDay = p.Reason, p.Comments, p.HalfDay
	note := strings.TrimSpace(c.Note)

	values := func(r *timeoff.Request) map[string]any {
		return map[string]any{
			"leave_kind": r.Kind, "starts_on": r.StartsOn.Format("2006-01-02"), "ends_on": r.EndsOn.Format("2006-01-02"),
			"days": r.Days, "half_day": boolInt(r.HalfDay), "reason": r.Reason, "comments": r.Comments,
		}
	}
	finalID := id
	hrms := false
	if before.Status == "approved" {
		dt, err := s.C.TimeOffBackend(ctx)
		if err != nil {
			return nil, err
		}
		hrms = dt == emberops.HRMSLeaveDoctype
	}
	switch {
	case before.Status == "pending":
		if _, err := s.C.Upsert(ctx, timeOffDoctype, id, values(&after)); err != nil {
			return nil, s.refused(err)
		}
	case !hrms:
		// Hearth Time Off Request: an approved request is edited where it is, decided again by
		// whoever changed it, with its "Out" moved.
		var oldEvent string
		if row, err := s.C.GetByHearthID(ctx, timeOffDoctype, id); err == nil {
			oldEvent = row.S("calendar_event")
		}
		eventID, err := s.putOutEvent(ctx, orgID, &after)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		v := values(&after)
		v["decided_by"], v["decided_at"], v["decision_note"] = emberops.Ref(actorID), s.ts(&now), note
		v["calendar_event"] = emberops.Ref(eventID)
		if _, err := s.C.Upsert(ctx, timeOffDoctype, id, v); err != nil {
			_ = s.C.DeleteByHearthID(ctx, "Event", eventID)
			return nil, s.refused(err)
		}
		if oldEvent != "" {
			_ = s.C.Delete(ctx, "Event", oldEvent)
		}
	default:
		// A submitted Leave Application is cancelled and annotated and nothing else, so the change
		// is the way HRMS asks for it: cancel the old one and file a new approved one.
		var oldEvent string
		if row, err := s.C.GetByHearthID(ctx, timeOffDoctype, id); err == nil {
			oldEvent = row.S("calendar_event")
		}
		who := s.nameOf(ctx, actorID)
		if _, err := s.C.Upsert(ctx, timeOffDoctype, id, map[string]any{
			"status": "cancelled", "decision_note": timeoff.ChangedNote(who, note),
		}); err != nil {
			return nil, s.refused(err)
		}
		if oldEvent != "" {
			_ = s.C.Delete(ctx, "Event", oldEvent)
		}
		finalID = uuid.NewString()
		if err := s.fileApproved(ctx, orgID, actorID, finalID, &after, values(&after), note); err != nil {
			// Put the original back, approved as before, so the failure costs nobody their time off.
			if rerr := s.fileApproved(ctx, orgID, actorID, uuid.NewString(), before, values(before), before.DecisionNote); rerr != nil {
				return nil, fmt.Errorf("timeoff: the change was refused (%v) and the original could not be filed again: %w", err, rerr)
			}
			return nil, s.refused(err)
		}
	}
	after.ID = finalID
	if err := s.PG.Record(ctx, timeoff.ChangeAudit(orgID, actorID, audit.ActionTimeOffChange, before, &after, note, c)); err != nil {
		return nil, err
	}
	out, err := s.get(ctx, finalID)
	if err != nil {
		return nil, err
	}
	if s.Notify != nil {
		s.Notify.TimeOffChanged(ctx, orgID, actorID, out, note)
	}
	return out, nil
}

// fileApproved files an approved request under id, decided now by the actor, with its "Out".
func (s *TimeOff) fileApproved(ctx context.Context, orgID, actorID, id string, r *timeoff.Request, vals map[string]any, note string) error {
	eventID, err := s.putOutEvent(ctx, orgID, r)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	vals["employee"] = emberops.Ref(r.RequesterID)
	vals["status"] = "approved"
	vals["decided_by"] = emberops.Ref(actorID)
	vals["decided_at"] = s.ts(&now)
	vals["decision_note"] = note
	vals["calendar_event"] = emberops.Ref(eventID)
	if _, err := s.C.Upsert(ctx, timeOffDoctype, id, vals); err != nil {
		_ = s.C.DeleteByHearthID(ctx, "Event", eventID)
		return err
	}
	return nil
}

// refused is EmberOPS refusing a write: the person's reason when it said one, else the error.
func (s *TimeOff) refused(err error) error {
	if isForbidden(err) {
		return timeoff.ErrDenied
	}
	if errors.Is(err, emberops.ErrInvalid) {
		return fmt.Errorf("%w: %s", timeoff.ErrInvalid, err)
	}
	return err
}

// CancelFor cancels a pending or approved request of somebody else's. EmberOPS cancels a
// submitted Leave Application rather than deleting it, so the request stays on the person's
// list as cancelled, with who cancelled it and why.
func (s *TimeOff) CancelFor(ctx context.Context, orgID, actorID, id, note string) error {
	before, err := s.changeable(ctx, orgID, actorID, id)
	if err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	now := time.Now().UTC()
	var oldEvent string
	if row, err := s.C.GetByHearthID(ctx, timeOffDoctype, id); err == nil {
		oldEvent = row.S("calendar_event")
	}
	if _, err := s.C.Upsert(ctx, timeOffDoctype, id, map[string]any{
		"status": "cancelled", "decided_at": s.ts(&now), "decision_note": timeoff.CancelledNote(s.nameOf(ctx, actorID), note),
	}); err != nil {
		return s.refused(err)
	}
	if oldEvent != "" {
		_ = s.C.Delete(ctx, "Event", oldEvent)
	}
	after := *before
	after.Status = "cancelled"
	if err := s.PG.Record(ctx, timeoff.ChangeAudit(orgID, actorID, audit.ActionTimeOffCancel, before, &after, note, timeoff.Change{})); err != nil {
		return err
	}
	if s.Notify != nil {
		after.DecisionNote = timeoff.CancelledNote(s.nameOf(ctx, actorID), note)
		s.Notify.TimeOffChanged(ctx, orgID, actorID, &after, note)
	}
	return nil
}
