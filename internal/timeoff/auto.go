package timeoff

// Vacation that approves itself.
//
// A salaried person whose time is approved by standing order (the same setting
// that approves their hours, principals.time_auto_approve_since) takes vacation
// without waiting on anybody, up to one working week in a calendar month. A
// request that fits is approved when it is filed, by the setting and not by a
// person, and everything approval does happens: the "Out" on the calendar, the
// notice to the requester, an audit row. A request that would take the month past
// the week is filed pending for the usual decision. Nothing is refused: the cap
// only decides who answers.

import (
	"time"

	"github.com/Embernet-ai/hearth/internal/audit"
)

// AutoApproveCapDays is the working days of vacation a month that approve themselves.
const AutoApproveCapDays = 5.0

// AutoApprovedNote is what an automatically approved request's decision says.
const AutoApprovedNote = "Auto-approved by the system: this person's vacation is approved automatically, up to 5 working days a month."

// AutoKind is the only kind of leave that approves itself.
const AutoKind = "vacation"

// monthDays is the working days of a request that fall inside one calendar month,
// half a day less when the request starts on a half day inside it.
func monthDays(startsOn, endsOn time.Time, half bool, year int, month time.Month) float64 {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	from, to := startsOn, endsOn
	if from.Before(first) {
		from = first
	}
	if to.After(last) {
		to = last
	}
	if to.Before(from) {
		return 0
	}
	n := float64(WorkingDays(from, to))
	if half && !startsOn.Before(first) && !startsOn.After(last) && n > 0 {
		n -= 0.5
	}
	return n
}

// WithinMonthlyCap reports whether a vacation request fits, with the vacation the
// person already has approved: in every calendar month it touches, the working
// days already approved plus the ones it asks for stay within AutoApproveCapDays.
func WithinMonthlyCap(approved []*Request, startsOn, endsOn time.Time, half bool) bool {
	for m := time.Date(startsOn.Year(), startsOn.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(endsOn); m = m.AddDate(0, 1, 0) {
		used := monthDays(startsOn, endsOn, half, m.Year(), m.Month())
		for _, a := range approved {
			if a.Status == "approved" && a.Kind == AutoKind {
				used += monthDays(a.StartsOn, a.EndsOn, a.HalfDay, m.Year(), m.Month())
			}
		}
		if used > AutoApproveCapDays {
			return false
		}
	}
	return true
}

// AutoApproveAudit is the audit row for a request approved by the setting. It names
// no actor: the decider is the setting, never a person.
func AutoApproveAudit(orgID string, r *Request) audit.Entry {
	after := SnapshotOf(r)
	after.Note = AutoApprovedNote
	return audit.Entry{OrgID: orgID, Action: audit.ActionTimeOffAutoApprove, EntityType: "time_off_request",
		EntityID: r.ID, After: after}
}
