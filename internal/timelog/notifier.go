package timelog

import (
	"context"
	"time"
)

// Decision is what a notification about somebody's hours needs: whose hours, who
// decided, which day and how long, and the comment the manager left.
type Decision struct {
	EntryID   string
	PersonID  string
	DeciderID string
	StartedAt time.Time
	Minutes   int
	Note      string
}

// Notifier tells people what happened to hours that were sent back. A nil one is
// allowed and means nobody is told, which is how the tests and hearthctl run.
type Notifier interface {
	// HoursSentBack tells the person their hours came back with the manager's comment.
	HoursSentBack(ctx context.Context, orgID string, d Decision)
	// HoursResubmitted tells the manager the person corrected hours they sent back,
	// which are waiting for a decision again.
	HoursResubmitted(ctx context.Context, orgID string, d Decision)
}
