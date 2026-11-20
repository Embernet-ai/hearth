package web

import "testing"

// What a status change that moved subtasks says: the first version's words for
// a close, and where they went for anything else.
func TestCascadeMessageSaysWhereSubtasksWent(t *testing.T) {
	for _, tc := range []struct {
		n      int
		status string
		want   string
	}{
		{1, "done", "Closed with 1 subtask"},
		{3, "", "Closed with 3 subtasks"},
		{1, "in_progress", "Moved 1 subtask to In progress"},
		{4, "cancelled", "Moved 4 subtasks to Cancelled"},
		{2, "blocked", "Moved 2 subtasks to Blocked"},
	} {
		if got := CascadeMessage(tc.n, tc.status); got != tc.want {
			t.Errorf("CascadeMessage(%d, %q) = %q, want %q", tc.n, tc.status, got, tc.want)
		}
	}
	if closedFlash("0", "cancelled") != "" || closedFlash("x", "done") != "" || closedFlash("2", "cancelled") != "Moved 2 subtasks to Cancelled" {
		t.Error("the flash should show only a small positive count")
	}
}
