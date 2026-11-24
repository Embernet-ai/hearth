package timelog

// One person's hours, for the dashboard: this week and this month against
// their own capacity, what is waiting on somebody, and the last eight weeks.
//
// Every number is a sum over time_entries. Target comes from the capacity row
// the People page writes, and Set says whether that row exists, because the
// default of forty hours is a stand-in and a dashboard that shows a percentage
// of a number nobody chose should say so.

import (
	"context"
	"fmt"
	"time"
)

// WeekHours is one week of seven days from From (the dashboard's weeks start on Sunday).
type WeekHours struct {
	From    time.Time
	Minutes int
	Entries int
}

// PersonHours is what somebody logged, in minutes.
type PersonHours struct {
	// Week and Month are logged finished work in the current week (from
	// Monday) and the current month so far.
	Week, Month int
	// WeekEntries and MonthEntries are how many entries those sums add up.
	WeekEntries, MonthEntries int
	// Target is a week in minutes, Set is whether somebody chose it.
	Target int
	Set    bool
	// WeekTarget is the whole week and MonthTarget the working days of the
	// month up to and including today, at a fifth of the week each.
	WeekTarget, MonthTarget int
	// Waiting is finished entries nobody has decided yet, in the month, and
	// SentBack is entries a manager refused that still stand.
	Waiting, SentBack int
	// Weeks is the last eight weeks, oldest first.
	Weeks []WeekHours
}

// PersonHours reads one person's hours. Anybody may read their own; reading a
// colleague's takes the same capability the workload page asks for.
func (s *Service) PersonHours(ctx context.Context, orgID, actorID, personID string,
	weekFrom, monthFrom, today time.Time) (*PersonHours, error) {

	if actorID != personID {
		all, err := s.authz.HasCapability(ctx, orgID, actorID, capTimeAll)
		if err != nil {
			return nil, err
		}
		if !all {
			return nil, ErrNotAllowed
		}
	}
	h := &PersonHours{}

	var cap *int
	if err := s.db.QueryRow(ctx,
		`SELECT (SELECT minutes_per_week FROM capacity WHERE principal_id = p.id)
		   FROM principals p WHERE p.id = $1 AND p.org_id = $2`, personID, orgID).Scan(&cap); err != nil {
		return nil, fmt.Errorf("timelog: capacity: %w", err)
	}
	h.Target, h.Set = 2400, false
	if cap != nil {
		h.Target, h.Set = *cap, true
	}
	h.WeekTarget = h.Target
	h.MonthTarget = h.Target / 5 * workingDays(monthFrom, today)

	tomorrow := today.AddDate(0, 0, 1)
	if err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(minutes) FILTER (WHERE started_at >= $3 AND started_at < $5), 0)::int,
		       COUNT(*) FILTER (WHERE started_at >= $3 AND started_at < $5)::int,
		       COALESCE(SUM(minutes) FILTER (WHERE started_at >= $4 AND started_at < $5), 0)::int,
		       COUNT(*) FILTER (WHERE started_at >= $4 AND started_at < $5)::int,
		       COALESCE(SUM(minutes) FILTER (WHERE started_at >= $4 AND started_at < $5
		                                     AND decided_at IS NULL), 0)::int,
		       COALESCE(SUM(minutes) FILTER (WHERE started_at >= $4 AND started_at < $5
		                                     AND approved IS FALSE), 0)::int
		  FROM time_entries
		 WHERE org_id = $1 AND principal_id = $2
		   AND ended_at IS NOT NULL AND minutes IS NOT NULL AND deleted_at IS NULL
		   AND started_at >= LEAST($3, $4)`,
		orgID, personID, weekFrom, monthFrom, tomorrow).
		Scan(&h.Week, &h.WeekEntries, &h.Month, &h.MonthEntries, &h.Waiting, &h.SentBack); err != nil {
		return nil, fmt.Errorf("timelog: person hours: %w", err)
	}

	first := weekFrom.AddDate(0, 0, -7*7)
	rows, err := s.db.Query(ctx, `
		SELECT w.n, COALESCE(SUM(t.minutes), 0)::int, COUNT(t.id)::int
		  FROM generate_series(0, 7) AS w(n)
		  LEFT JOIN time_entries t
		    ON t.org_id = $1 AND t.principal_id = $2
		   AND t.ended_at IS NOT NULL AND t.minutes IS NOT NULL AND t.deleted_at IS NULL
		   AND t.started_at >= $3::timestamptz + (w.n * interval '7 days')
		   AND t.started_at <  $3::timestamptz + ((w.n + 1) * interval '7 days')
		 GROUP BY w.n ORDER BY w.n`, orgID, personID, first)
	if err != nil {
		return nil, fmt.Errorf("timelog: weekly hours: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var wk WeekHours
		if err := rows.Scan(&n, &wk.Minutes, &wk.Entries); err != nil {
			return nil, fmt.Errorf("timelog: scan weekly hours: %w", err)
		}
		wk.From = first.AddDate(0, 0, 7*n)
		h.Weeks = append(h.Weeks, wk)
	}
	return h, rows.Err()
}

// workingDays counts Monday to Friday from first to last, both included.
func workingDays(first, last time.Time) int {
	n := 0
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n++
		}
	}
	return n
}
