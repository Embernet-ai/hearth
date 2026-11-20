package notify

import (
	"testing"
	"time"
)

func TestDueExcerptNamesTheDayItWasEntered(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone database")
	}
	at := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).In(ny)
	if got := dueExcerpt(at); got != "due 5 Nov" {
		t.Errorf("a job due the 5th read %q", got)
	}
}
