package crmsync

import (
	"strings"
	"testing"
	"time"
)

// A meeting and an email are sent to EmberOps under their own names, not as notes.
func TestMeetingsAndEmailsAreNamedInTheComment(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		a    activity
		want string
	}{
		{activity{Kind: "meeting", Title: "Kickoff", DurationMS: 1800000, At: at}, "Hearth meeting, 30 min"},
		{activity{Kind: "email", Title: "Quote", Direction: "outbound", At: at}, "Hearth email, outbound"},
		{activity{Kind: "note", Title: "x", At: at}, "Hearth note"},
	} {
		if got := commentHTML(c.a); !strings.Contains(got, c.want) {
			t.Errorf("%s comment = %q, want %q in it", c.a.Kind, got, c.want)
		}
	}
}
