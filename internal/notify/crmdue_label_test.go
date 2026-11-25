package notify

import "testing"

func TestAReminderStartsWithWhatKindOfFollowupItIs(t *testing.T) {
	for kind, want := range map[string]string{"call": "Call: ", "email": "Email: ", "meeting": "Meeting: ", "todo": "Follow up: ", "": "Follow up: "} {
		if got := followupLabel(kind); got != want {
			t.Errorf("%q reminder starts %q, want %q", kind, got, want)
		}
	}
}
