package web

import (
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
)

// A due date is entered as a date and stored as midnight UTC. The database hands it
// back in the server's own zone, which west of UTC is the evening before. The
// calendar must put it on the day somebody entered.
func TestCalendarPutsADueDateOnTheDayItWasEntered(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone database")
	}
	was := time.Local
	time.Local = ny
	defer func() { time.Local = was }()

	due := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).In(ny)
	row := &docs.Row{Document: &docs.Document{ID: "d1", Task: &docs.TaskFields{DueAt: &due}}}
	_, weeks, _, _ := calendar("2026-11", []*docs.Row{row})
	for _, w := range weeks {
		for _, d := range w {
			if len(d.Rows) > 0 {
				if got := d.Date.Format("2006-01-02"); got != "2026-11-05" {
					t.Fatalf("due 5 Nov showed on %s", got)
				}
				return
			}
		}
	}
	t.Fatal("the due date is not on the calendar at all")
}

func TestABoardOrTableGroupsByStatusUnlessNothingWasChosen(t *testing.T) {
	if g := withDefaultGroup(docs.Query{}, docs.ViewBoard).GroupBy; g != docs.Col("status") {
		t.Fatalf("an untouched board groups by status, got %v", g)
	}
	if g := withDefaultGroup(docs.Query{Ungrouped: true}, docs.ViewTable).GroupBy; !g.Zero() {
		t.Fatalf("a table where Nothing was chosen stays flat, got %v", g)
	}
	if g := withDefaultGroup(docs.Query{}, docs.ViewCalendar).GroupBy; !g.Zero() {
		t.Fatalf("other views are not defaulted, got %v", g)
	}
}
