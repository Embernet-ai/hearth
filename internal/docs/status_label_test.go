package docs

import "testing"

// Statuses read as words, and anything else passes through untouched, because
// the same label function serves every kind of grouping.
func TestStatusLabel(t *testing.T) {
	for in, want := range map[string]string{
		StatusTodo: "To do", StatusInProgress: "In progress", StatusBlocked: "Blocked",
		StatusDone: "Done", StatusCancelled: "Cancelled",
		"Engineering": "Engineering", "": "",
	} {
		if got := StatusLabel(in); got != want {
			t.Errorf("StatusLabel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, s := range Statuses {
		if StatusLabel(s) == s {
			t.Errorf("status %q has no readable label", s)
		}
	}
}
