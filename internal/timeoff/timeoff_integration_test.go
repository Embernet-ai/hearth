package timeoff_test

// Time off against a real Postgres. The checks that matter are about who may
// decide and who may see: every refusal is tested beside its permission,
// because a check that always says yes passes every happy-path test.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

func TestTimeOffIsAskedDecidedAndPrivate(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_timeoff_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	db := pools.Web
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	org := uuid.NewString()
	ana, boss, hr, colleague, manager := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	dept := uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Time Off Co')`, org, "to-"+org[:8])
	for id, name := range map[string]string{ana: "Ana", boss: "Boss", hr: "Hilda HR", colleague: "Colin", manager: "Mara"} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`, id, org, name)
	}
	exec(`UPDATE principals SET approver_id = $2 WHERE id = $1`, ana, boss)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'people', $2)`, org, hr)
	exec(`INSERT INTO spaces (id, org_id, kind, name, created_by) VALUES ($1, $2, 'folder', 'Engineering', $3)`, dept, org, boss)
	exec(`INSERT INTO principal_departments (org_id, principal_id, space_id) VALUES ($1, $2, $3)`, org, ana, dept)
	exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1, $2, $3)`, org, dept, manager)

	svc := timeoff.New(db, nil)
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Refused without the acknowledgement, the dates backwards, or all weekend.
	for name, p := range map[string]timeoff.Params{
		"unacknowledged": {StartsOn: day("2030-03-04"), EndsOn: day("2030-03-08"), Kind: "vacation"},
		"backwards":      {StartsOn: day("2030-03-08"), EndsOn: day("2030-03-04"), Kind: "vacation", Acknowledged: true},
		"weekend":        {StartsOn: day("2030-03-09"), EndsOn: day("2030-03-10"), Kind: "vacation", Acknowledged: true},
		"no kind":        {StartsOn: day("2030-03-04"), EndsOn: day("2030-03-08"), Kind: "beach", Acknowledged: true},
	} {
		if _, err := svc.Ask(ctx, org, ana, p); !errors.Is(err, timeoff.ErrInvalid) {
			t.Errorf("%s request was accepted: %v", name, err)
		}
	}

	// Mon 4 to Mon 11 March 2030 is six working days.
	r, err := svc.Ask(ctx, org, ana, timeoff.Params{StartsOn: day("2030-03-04"), EndsOn: day("2030-03-11"),
		Kind: "medical", Reason: "Surgery", Acknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Days != 6 {
		t.Fatalf("counted %d working days, want 6", r.Days)
	}

	// Who sees it: the approver, the department manager and HR. Not a colleague.
	for who, want := range map[string]int{boss: 1, manager: 1, hr: 1, colleague: 0, ana: 0} {
		list, err := svc.ToDecide(ctx, org, who)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != want {
			t.Errorf("%s sees %d requests to decide, want %d", who, len(list), want)
		}
	}

	// Who may decide: never the requester, never a colleague.
	if err := svc.Decide(ctx, org, ana, r.ID, true, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("the requester approved their own time off: %v", err)
	}
	if err := svc.Decide(ctx, org, colleague, r.ID, true, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("a colleague approved somebody's time off: %v", err)
	}
	if err := svc.Decide(ctx, org, boss, r.ID, true, "Get well"); err != nil {
		t.Fatalf("the approver could not approve: %v", err)
	}
	if err := svc.Decide(ctx, org, hr, r.ID, false, ""); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("an already decided request was decided again: %v", err)
	}

	// The team's calendar says Out, with the name and never the reason.
	var title, space string
	if err := db.QueryRow(ctx, `SELECT title, COALESCE(space_id::text, '') FROM calendar_events WHERE org_id = $1`, org).Scan(&title, &space); err != nil {
		t.Fatalf("no calendar entry after approval: %v", err)
	}
	if title != "Out: Ana" || space != dept {
		t.Fatalf("calendar says %q on %q, want \"Out: Ana\" on the department", title, space)
	}

	mine, err := svc.Mine(ctx, org, ana)
	if err != nil || len(mine) != 1 || mine[0].Status != "approved" || mine[0].DeciderName != "Boss" {
		t.Fatalf("Ana's own view is wrong: %+v %v", mine, err)
	}

	// Cancelling a future approved request takes it off the calendar; only
	// the requester can.
	if err := svc.Cancel(ctx, org, boss, r.ID); !errors.Is(err, timeoff.ErrDenied) {
		t.Fatalf("somebody else cancelled Ana's time off: %v", err)
	}
	if err := svc.Cancel(ctx, org, ana, r.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM calendar_events WHERE org_id = $1`, org).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the calendar still shows the cancelled time off (%d, %v)", n, err)
	}
}
