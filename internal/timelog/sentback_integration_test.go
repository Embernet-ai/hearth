package timelog_test

// Hours a manager sends back reach the person with the comment, and fixing them
// puts them back in front of the manager who sent them.

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

type seen struct {
	back, again []timelog.Decision
}

func (s *seen) HoursSentBack(_ context.Context, _ string, d timelog.Decision)    { s.back = append(s.back, d) }
func (s *seen) HoursResubmitted(_ context.Context, _ string, d timelog.Decision) { s.again = append(s.again, d) }

func TestSendBackTellsThePersonAndACorrectionResubmits(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	n := &seen{}
	f.svc.SetNotifier(n)

	e, err := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 120, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, false, "  lunch was not work  "); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if len(n.back) != 1 || n.back[0].PersonID != f.alice || n.back[0].DeciderID != f.bob ||
		n.back[0].Note != "lunch was not work" || n.back[0].Minutes != 120 {
		t.Fatalf("the person was told %+v", n.back)
	}

	if err := f.svc.Correct(ctx, f.org, f.subj(t, f.alice), e.ID, 90, nil, ""); err != nil {
		t.Fatalf("correct: %v", err)
	}
	if len(n.again) != 1 || n.again[0].DeciderID != f.bob || n.again[0].PersonID != f.alice {
		t.Fatalf("the manager was told %+v", n.again)
	}
	pending, err := f.svc.Pending(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	found := false
	for _, p := range pending {
		found = found || p.ID == e.ID
	}
	if !found {
		t.Error("corrected hours are not waiting for the manager again")
	}
}

func TestCorrectingApprovedHoursTellsNobody(t *testing.T) {
	f := setup(t)
	extra(t, f)
	ctx := context.Background()
	f.superAdmin(t, f.bob)
	n := &seen{}
	f.svc.SetNotifier(n)
	e, _ := f.svc.LogOwn(ctx, f.org, f.subj(t, f.alice), "", 60, time.Now(), "")
	if err := f.svc.Decide(ctx, f.org, f.bob, e.ID, true, ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if err := f.svc.Correct(ctx, f.org, f.subj(t, f.alice), e.ID, 45, nil, ""); err != nil {
		t.Fatalf("correct: %v", err)
	}
	if len(n.back)+len(n.again) != 0 {
		t.Errorf("approved hours produced notices: %+v", n)
	}
}
