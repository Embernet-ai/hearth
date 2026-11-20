package timelog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

func monday(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// The dashboard's hours are sums over what was recorded, against a capacity
// somebody set, and a colleague's are behind the everyone's time permission.
func TestPersonHoursAreSumsOfWhatWasLogged(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	now := time.Now()
	noon := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.Local) }
	weekFrom := monday(now)
	monthFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

	logs := []struct {
		on   time.Time
		mins int
	}{
		{noon(today), 60},
		{noon(weekFrom), 90},
		{noon(weekFrom.AddDate(0, 0, -3)), 120},   // last week
		{noon(weekFrom.AddDate(0, 0, -20)), 30},   // three weeks back
		{noon(weekFrom.AddDate(0, 0, -400)), 500}, // outside the eight weeks
	}
	var entries []string
	for _, l := range logs {
		e, err := f.svc.LogOwn(ctx, f.org, alice, "", l.mins, l.on, "")
		if err != nil {
			t.Fatalf("log: %v", err)
		}
		entries = append(entries, e.ID)
	}
	// A removed entry counts for nothing.
	gone, err := f.svc.LogOwn(ctx, f.org, alice, "", 999, noon(today), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Remove(ctx, f.org, alice, gone.ID); err != nil {
		t.Fatal(err)
	}
	// One entry sent back by somebody who may decide it.
	f.superAdmin(t, f.bob)
	if err := f.svc.Decide(ctx, f.org, f.bob, entries[1], false, "wrong job"); err != nil {
		t.Fatalf("send back: %v", err)
	}

	wantWeek, wantMonth := 0, 0
	for _, l := range logs {
		if !l.on.Before(weekFrom) {
			wantWeek += l.mins
		}
		if !l.on.Before(monthFrom) {
			wantMonth += l.mins
		}
	}

	h, err := f.svc.PersonHours(ctx, f.org, f.alice, f.alice, weekFrom, monthFrom, today)
	if err != nil {
		t.Fatalf("person hours: %v", err)
	}
	if h.Week != wantWeek || h.Month != wantMonth {
		t.Errorf("week %d month %d, want %d and %d", h.Week, h.Month, wantWeek, wantMonth)
	}
	if h.Set || h.Target != 2400 {
		t.Errorf("no capacity row: Set=%v Target=%d, want false and the 2400 default", h.Set, h.Target)
	}
	if len(h.Weeks) != 8 {
		t.Fatalf("weeks = %d, want 8", len(h.Weeks))
	}
	if last := h.Weeks[7]; !last.From.Equal(weekFrom) || last.Minutes != wantWeek {
		t.Errorf("current week bar = %v %d, want %v %d", last.From, last.Minutes, weekFrom, wantWeek)
	}
	if prev := h.Weeks[6]; prev.Minutes != 120 {
		t.Errorf("last week bar = %d, want 120", prev.Minutes)
	}
	if h.Weeks[4].Minutes != 30 {
		t.Errorf("three weeks back = %d, want 30", h.Weeks[4].Minutes)
	}
	for i, w := range h.Weeks {
		if w.Minutes > 120 && i != 7 {
			t.Errorf("bar %d holds %d minutes: the removed or distant entries leaked in", i, w.Minutes)
		}
	}
	wantBack := 0
	if !logs[1].on.Before(monthFrom) {
		wantBack = 90
	}
	if h.SentBack != wantBack || h.Waiting != wantMonth-wantBack {
		t.Errorf("sent back %d waiting %d, want %d and %d", h.SentBack, h.Waiting, wantBack, wantMonth-wantBack)
	}

	if err := f.svc.SetCapacity(ctx, f.org, f.alice, 1200, nil); err != nil {
		t.Fatal(err)
	}
	h, err = f.svc.PersonHours(ctx, f.org, f.alice, f.alice, weekFrom, monthFrom, today)
	if err != nil || !h.Set || h.WeekTarget != 1200 {
		t.Errorf("after setting capacity: %+v err=%v", h, err)
	}
	if want := 240 * workingDaysForTest(monthFrom, today); h.MonthTarget != want {
		t.Errorf("month target = %d, want %d (a fifth of the week per working day so far)", h.MonthTarget, want)
	}

	// A colleague may not read hers; somebody who holds everyone's time may.
	carol := f.person(t, "Carol")
	if _, err := f.svc.PersonHours(ctx, f.org, carol, f.alice, weekFrom, monthFrom, today); !errors.Is(err, timelog.ErrNotAllowed) {
		t.Errorf("carol reading alice's hours: %v, want ErrNotAllowed", err)
	}
	if got, err := f.svc.PersonHours(ctx, f.org, f.bob, f.alice, weekFrom, monthFrom, today); err != nil || got.Week != wantWeek {
		t.Errorf("a super admin reading alice's hours: %+v %v", got, err)
	}
}

func workingDaysForTest(first, last time.Time) int {
	n := 0
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			n++
		}
	}
	return n
}
