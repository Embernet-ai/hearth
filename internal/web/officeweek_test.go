package web

// The server runs in UTC. A person's week, day and month are the office's, so at 21:00 on a Saturday in New York,
// when UTC has already turned over to Sunday, the dashboard's "this week" must still be the week that ends tonight and
// must still hold the Saturday evening that person is working. Counted in UTC the week began at 19:00 on the Saturday
// before and ended a week later at 19:00 on Saturday, so every Saturday night landed in next week's total and a person
// who works evenings saw their week short by those hours.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

type askedHours struct {
	timelog.Store
	week, month, today time.Time
}

func (a *askedHours) PersonHours(_ context.Context, _, _, _ string, week, month, today time.Time) (*timelog.PersonHours, error) {
	a.week, a.month, a.today = week, month, today
	return &timelog.PersonHours{Target: 2400, Set: true, WeekTarget: 2400}, nil
}

func TestTheWeekAndDayAreTheOfficesWhateverTheServersClockSays(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone database")
	}
	// The way the server runs: its own zone is UTC.
	was, wasClock := time.Local, clock
	time.Local = time.UTC
	defer func() { time.Local, clock = was, wasClock }()
	clock = func() time.Time { return time.Date(2026, 11, 28, 21, 0, 0, 0, ny) } // Saturday evening, Sunday in UTC

	a := &askedHours{}
	s := &Server{time: a, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, status, _ := s.statsFor(context.Background(), &auth.Identity{OrgID: "o", PrincipalID: "me"}, "", false); status != 0 {
		t.Fatalf("stats: %d", status)
	}
	want := map[string]time.Time{
		"week starts":  time.Date(2026, 11, 22, 0, 0, 0, 0, ny),
		"month starts": time.Date(2026, 11, 1, 0, 0, 0, 0, ny),
		"today":        time.Date(2026, 11, 28, 0, 0, 0, 0, ny),
	}
	got := map[string]time.Time{"week starts": a.week, "month starts": a.month, "today": a.today}
	for k, w := range want {
		if !got[k].Equal(w) {
			t.Errorf("%s at %s, want %s", k, got[k].In(ny).Format("Mon 2 Jan 15:04 MST"), w.Format("Mon 2 Jan 15:04 MST"))
		}
	}

	// The time page's and approvals' Monday week is the office's too.
	if from := startOfWeek(clock()); !from.Equal(time.Date(2026, 11, 23, 0, 0, 0, 0, ny)) {
		t.Errorf("monday week starts %s", from.In(ny))
	}
}
