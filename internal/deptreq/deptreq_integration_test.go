package deptreq_test

// Asking for departments and answering. Every permission is tested from both
// sides: the person who may decide, beside the person who may not, because a
// check that always says yes passes every happy-path test.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fx struct {
	svc   *deptreq.Service
	exec  func(q string, args ...any)
	org   string
	nora  string // new, in no department
	mia   string // manages White Team
	ed    string // manages Engineering
	hal   string // HR: holds People, which is management, not admin
	sam   string // a super admin
	white string
	eng   string
	sub   string // a project under White Team, not a department
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_deptreq_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	f := &fx{svc: deptreq.New(pools.Web, nil), org: uuid.NewString(),
		nora: uuid.NewString(), mia: uuid.NewString(), ed: uuid.NewString(), hal: uuid.NewString(), sam: uuid.NewString(),
		white: uuid.NewString(), eng: uuid.NewString(), sub: uuid.NewString()}
	f.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	f.exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Dept Co')`, f.org, "dr-"+f.org[:8])
	for id, name := range map[string]string{f.nora: "Nora", f.mia: "Mia", f.ed: "Ed", f.hal: "Hal", f.sam: "Sam"} {
		f.exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`, id, f.org, name)
	}
	f.exec(`INSERT INTO spaces (id, org_id, name, created_by) VALUES ($1, $2, 'White Team', $3)`, f.white, f.org, f.hal)
	f.exec(`INSERT INTO spaces (id, org_id, name, created_by) VALUES ($1, $2, 'Engineering', $3)`, f.eng, f.org, f.hal)
	f.exec(`INSERT INTO spaces (id, org_id, parent_id, name, created_by) VALUES ($1, $2, $3, 'Line 3', $4)`, f.sub, f.org, f.white, f.hal)
	f.exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1, $2, $3)`, f.org, f.white, f.mia)
	f.exec(`INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1, $2, $3)`, f.org, f.eng, f.ed)
	f.exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'people', $2)`, f.org, f.hal)
	f.exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'super_admin', $2)`, f.org, f.sam)
	return f
}

func (f *fx) departmentsOf(t *testing.T, who string) []string {
	t.Helper()
	cur, err := f.svc.Current(context.Background(), f.org, who)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range cur {
		ids = append(ids, d.ID)
	}
	return ids
}

func sees(t *testing.T, f *fx, who, requestID string) bool {
	t.Helper()
	list, err := f.svc.ToDecide(context.Background(), f.org, who)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(list, func(r *deptreq.Request) bool { return r.ID == requestID })
}

func TestAManagerAnswersForTheirOwnDepartment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r, err := f.svc.Ask(ctx, f.org, f.nora, []string{f.white}, "hired for White Team")
	if err != nil {
		t.Fatal(err)
	}

	// The White Team manager and HR see it; the Engineering manager and Nora
	// herself do not.
	if !sees(t, f, f.mia, r.ID) || !sees(t, f, f.sam, r.ID) {
		t.Fatal("the department's manager and a super admin should both see the request")
	}
	// HR is management, not admin: HR neither sees nor decides it.
	if sees(t, f, f.hal, r.ID) {
		t.Fatal("HR was offered a department request")
	}
	if err := f.svc.Decide(ctx, f.org, f.hal, r.ID, true, []string{f.white}, ""); !errors.Is(err, deptreq.ErrDenied) {
		t.Fatalf("HR moved somebody into a department: %v", err)
	}
	if sees(t, f, f.ed, r.ID) {
		t.Fatal("a manager of another department saw the request")
	}
	if sees(t, f, f.nora, r.ID) {
		t.Fatal("the requester was offered her own request to decide")
	}
	if err := f.svc.Decide(ctx, f.org, f.ed, r.ID, true, []string{f.white}, ""); !errors.Is(err, deptreq.ErrDenied) {
		t.Fatalf("the Engineering manager approved a White Team request: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, f.nora, r.ID, true, []string{f.white}, ""); !errors.Is(err, deptreq.ErrDenied) {
		t.Fatalf("Nora approved her own request: %v", err)
	}

	if err := f.svc.Decide(ctx, f.org, f.mia, r.ID, true, []string{f.white}, ""); err != nil {
		t.Fatalf("the manager could not approve: %v", err)
	}
	if got := f.departmentsOf(t, f.nora); !slices.Equal(got, []string{f.white}) {
		t.Fatalf("Nora is in %v, want White Team", got)
	}
	// Answered once; a second answer is refused.
	if err := f.svc.Decide(ctx, f.org, f.sam, r.ID, false, nil, "changed my mind"); !errors.Is(err, deptreq.ErrDenied) {
		t.Fatalf("a decided request was decided again: %v", err)
	}
	latest, err := f.svc.Latest(ctx, f.org, f.nora)
	if err != nil || latest.Status != "approved" || len(latest.Granted) != 1 || latest.DeciderName != "Mia" {
		t.Fatalf("the record does not say what was granted and by whom: %+v %v", latest, err)
	}
}

// A manager cannot reach past their own departments: not by approving one they
// do not manage, and not by approving a set that drops one they do not manage.
func TestAManagerCannotChangeDepartmentsTheyDoNotManage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	r, _ := f.svc.Ask(ctx, f.org, f.nora, []string{f.white, f.eng}, "")
	if err := f.svc.Decide(ctx, f.org, f.mia, r.ID, true, []string{f.white, f.eng}, ""); !errors.Is(err, deptreq.ErrDenied) {
		t.Fatalf("Mia put Nora in Engineering: %v", err)
	}
	// Narrowed to her own department, she may.
	if err := f.svc.Decide(ctx, f.org, f.mia, r.ID, true, []string{f.white}, ""); err != nil {
		t.Fatalf("Mia could not approve White Team alone: %v", err)
	}

	// Now a super admin puts Nora in both, and she asks for White Team only.
	r2, _ := f.svc.Ask(ctx, f.org, f.nora, []string{f.eng}, "")
	if err := f.svc.Decide(ctx, f.org, f.sam, r2.ID, true, []string{f.white, f.eng}, ""); err != nil {
		t.Fatalf("a super admin could not set two departments: %v", err)
	}
	r3, _ := f.svc.Ask(ctx, f.org, f.nora, []string{f.white}, "only White Team now")
	// Approving as asked would take her out of Engineering, which is not Mia's.
	if err := f.svc.Decide(ctx, f.org, f.mia, r3.ID, true, []string{f.white}, ""); !errors.Is(err, deptreq.ErrDenied) {
		t.Fatalf("Mia removed Nora from Engineering: %v", err)
	}
	if got := f.departmentsOf(t, f.nora); len(got) != 2 {
		t.Fatalf("a refused decision changed Nora's departments: %v", got)
	}
}

func TestAskingIsCheckedAndReplacesTheLastQuestion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.svc.Ask(ctx, f.org, f.nora, nil, ""); !errors.Is(err, deptreq.ErrInvalid) {
		t.Fatalf("an empty request was accepted: %v", err)
	}
	if _, err := f.svc.Ask(ctx, f.org, f.nora, []string{f.sub}, ""); !errors.Is(err, deptreq.ErrInvalid) {
		t.Fatalf("a project was accepted as a department: %v", err)
	}
	if _, err := f.svc.Ask(ctx, f.org, f.nora, []string{uuid.NewString()}, ""); !errors.Is(err, deptreq.ErrInvalid) {
		t.Fatalf("a department from nowhere was accepted: %v", err)
	}

	first, _ := f.svc.Ask(ctx, f.org, f.nora, []string{f.eng}, "")
	second, err := f.svc.Ask(ctx, f.org, f.nora, []string{f.white}, "")
	if err != nil {
		t.Fatal(err)
	}
	if sees(t, f, f.sam, first.ID) || !sees(t, f, f.sam, second.ID) {
		t.Fatal("a second request should replace the first")
	}

	// No without a reason is refused; with one, it stands and changes nothing.
	if err := f.svc.Decide(ctx, f.org, f.mia, second.ID, false, nil, ""); !errors.Is(err, deptreq.ErrInvalid) {
		t.Fatalf("a no without a reason was accepted: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, f.mia, second.ID, false, nil, "you are in Engineering"); err != nil {
		t.Fatal(err)
	}
	if got := f.departmentsOf(t, f.nora); len(got) != 0 {
		t.Fatalf("a no changed Nora's departments: %v", got)
	}

	third, _ := f.svc.Ask(ctx, f.org, f.nora, []string{f.white}, "")
	if err := f.svc.Cancel(ctx, f.org, f.nora); err != nil {
		t.Fatal(err)
	}
	if sees(t, f, f.sam, third.ID) {
		t.Fatal("a cancelled request still waits")
	}
}
