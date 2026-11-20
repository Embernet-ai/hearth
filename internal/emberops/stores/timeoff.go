package stores

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

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
	if p.StartsOn.IsZero() || p.EndsOn.IsZero() {
		return nil, fmt.Errorf("%w: a start and an end date are needed", timeoff.ErrInvalid)
	}
	if p.EndsOn.Before(p.StartsOn) {
		return nil, fmt.Errorf("%w: the end date is before the start date", timeoff.ErrInvalid)
	}
	if p.EndsOn.Sub(p.StartsOn) > 366*24*time.Hour {
		return nil, fmt.Errorf("%w: that is more than a year", timeoff.ErrInvalid)
	}
	if timeoff.KindLabel(p.Kind) == p.Kind {
		return nil, fmt.Errorf("%w: choose a type of leave", timeoff.ErrInvalid)
	}
	days := timeoff.WorkingDays(p.StartsOn, p.EndsOn)
	if days == 0 {
		return nil, fmt.Errorf("%w: those dates are all weekend", timeoff.ErrInvalid)
	}
	id := uuid.NewString()
	vals := map[string]any{
		"employee": emberops.Ref(actorID), "leave_kind": p.Kind, "status": "pending",
		"starts_on": p.StartsOn.Format("2006-01-02"), "ends_on": p.EndsOn.Format("2006-01-02"), "days": days,
		"reason": strings.TrimSpace(p.Reason), "comments": strings.TrimSpace(p.Comments),
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
			return fmt.Errorf("timeoff: calendar: %w", err)
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

var timeOffFields = []string{"name", "hearth_id", "employee", "leave_kind", "status", "starts_on", "ends_on", "days",
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
			Status: r.S("status"), Days: int(r.I("days")), Reason: r.S("reason"), Comments: r.S("comments"),
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
