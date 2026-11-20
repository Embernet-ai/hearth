package authz_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// These are the highest-value tests in the codebase.
//
// Every other bug is a bug. A bug here is a data leak, so the denials are
// asserted as carefully as the grants, and each case names the SOURCE the access
// came from, because "it worked" is not the same as "it worked for the right
// reason".

type fixture struct {
	pools *pg.Pools
	svc   *authz.Service
	org   string
	alice string // owns the document
	bob   string // gets access via a channel
	carol string // gets access directly
	dave  string // gets nothing, ever
	chID  string
	docID string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping integration test")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	f := &fixture{
		pools: pools,
		svc:   authz.New(pools.Web),
		org:   uuid.NewString(),
		alice: uuid.NewString(),
		bob:   uuid.NewString(),
		carol: uuid.NewString(),
		dave:  uuid.NewString(),
		chID:  uuid.NewString(),
		docID: uuid.NewString(),
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}

	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'Test Org')`, f.org, "org-"+f.org[:8])
	for name, id := range map[string]string{"alice": f.alice, "bob": f.bob, "carol": f.carol, "dave": f.dave} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', $3)`,
			id, f.org, name)
	}
	exec(`INSERT INTO channels (id, org_id, slug, name, created_by) VALUES ($1, $2, $3, 'Eng', $4)`,
		f.chID, f.org, "eng-"+f.chID[:8], f.alice)
	// Bob is in the channel. Dave is not. That single difference is the whole
	// "channel membership IS the permission model" claim.
	exec(`INSERT INTO channel_members (channel_id, principal_id) VALUES ($1, $2)`, f.chID, f.bob)
	exec(`INSERT INTO documents (id, org_id, kind, title, owner_id, created_by, updated_by)
	      VALUES ($1, $2, 'doc', 'Line 3 rebuild', $3, $3, $3)`, f.docID, f.org, f.alice)

	t.Cleanup(func() {
		ctx := context.Background()
		// The audit log refuses an ordinary delete, so it takes the one
		// deliberate way out. That function removes a whole org and nothing
		// finer, and nothing outside a fixture calls it.
		_, _ = pools.Web.Exec(ctx, `SELECT audit_purge_org($1)`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM capability_grants WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM grants WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM documents WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM spaces WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM channel_members WHERE channel_id=$1`, f.chID)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM channels WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM principals WHERE org_id=$1`, f.org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, f.org)
	})
	return f
}

func (f *fixture) subjects(t *testing.T, principal string) core.Subjects {
	t.Helper()
	s, err := f.svc.SubjectsFor(context.Background(), f.org, principal)
	if err != nil {
		t.Fatalf("SubjectsFor: %v", err)
	}
	return s
}

func (f *fixture) doc() core.Ref { return core.Ref{Type: core.EntityDocument, ID: f.docID} }

func TestSubjectSetIncludesChannelMemberships(t *testing.T) {
	f := setup(t)

	bob := f.subjects(t, f.bob)
	var sawChannel bool
	for _, s := range bob {
		if s.Kind == core.SubjectChannel && s.ID == f.chID {
			sawChannel = true
		}
	}
	if !sawChannel {
		t.Errorf("bob is a channel member but his subject set lacks the channel: %v", bob.Keys())
	}

	dave := f.subjects(t, f.dave)
	for _, s := range dave {
		if s.Kind == core.SubjectChannel {
			t.Errorf("dave is in no channels but his subject set has %s", s.Key())
		}
	}
}

func TestAccessBySourceAndTheDenial(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// Three different grant sources, so the test proves each path independently
	// rather than proving one path three times.
	if err := f.svc.Grant(ctx, f.org, f.doc(), core.Subject{Kind: core.SubjectPrincipal, ID: f.alice}, core.AccessAdmin, f.alice); err != nil {
		t.Fatalf("grant to owner: %v", err)
	}
	if err := f.svc.Grant(ctx, f.org, f.doc(), core.Subject{Kind: core.SubjectChannel, ID: f.chID}, core.AccessEdit, f.alice); err != nil {
		t.Fatalf("grant to channel: %v", err)
	}
	if err := f.svc.Grant(ctx, f.org, f.doc(), core.Subject{Kind: core.SubjectPrincipal, ID: f.carol}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant to carol: %v", err)
	}

	cases := []struct {
		who       string
		principal string
		want      core.AccessLevel
		via       string
	}{
		{"alice", f.alice, core.AccessAdmin, "direct principal grant"},
		{"bob", f.bob, core.AccessEdit, "channel membership"},
		{"carol", f.carol, core.AccessView, "direct principal grant"},
		{"dave", f.dave, core.AccessNone, "nothing, and that is the point"},
	}
	for _, c := range cases {
		got, err := f.svc.LevelFor(ctx, f.subjects(t, c.principal), f.doc())
		if err != nil {
			t.Fatalf("%s: LevelFor: %v", c.who, err)
		}
		if got != c.want {
			t.Errorf("%s has %q via %s, want %q", c.who, got, c.via, c.want)
		}
	}

	// The denial, stated as the thing a handler actually calls.
	err := f.svc.Require(ctx, f.subjects(t, f.dave), f.doc(), core.AccessView)
	if !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("dave was not denied: %v", err)
	}
}

func TestEffectiveAccessIsTheMaximum(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// Bob gets view directly and edit through the channel. The higher wins.
	// If this took the first match instead of the maximum, sharing a document
	// with someone would silently DOWNGRADE access they already had.
	mustGrant(t, f, core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView)
	mustGrant(t, f, core.Subject{Kind: core.SubjectChannel, ID: f.chID}, core.AccessEdit)

	got, err := f.svc.LevelFor(ctx, f.subjects(t, f.bob), f.doc())
	if err != nil {
		t.Fatalf("LevelFor: %v", err)
	}
	if got != core.AccessEdit {
		t.Errorf("effective access = %q, want edit (the maximum of view and edit)", got)
	}
}

func TestLeavingAChannelRemovesAccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	mustGrant(t, f, core.Subject{Kind: core.SubjectChannel, ID: f.chID}, core.AccessEdit)

	if err := f.svc.Require(ctx, f.subjects(t, f.bob), f.doc(), core.AccessEdit); err != nil {
		t.Fatalf("bob should have edit while in the channel: %v", err)
	}

	// "Leave, lose it." No access-request theatre, and no cleanup job.
	if _, err := f.pools.Web.Exec(ctx,
		`DELETE FROM channel_members WHERE channel_id=$1 AND principal_id=$2`, f.chID, f.bob); err != nil {
		t.Fatalf("remove membership: %v", err)
	}

	err := f.svc.Require(ctx, f.subjects(t, f.bob), f.doc(), core.AccessView)
	if !errors.Is(err, authz.ErrDenied) {
		t.Errorf("bob kept access after leaving the channel: %v", err)
	}
}

func TestExpiredGrantDoesNotGrant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	mustGrant(t, f, core.Subject{Kind: core.SubjectPrincipal, ID: f.carol}, core.AccessEdit)

	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE grants SET expires_at = now() - interval '1 second'
		 WHERE entity_id=$1 AND subject_key=$2`,
		f.docID, core.Subject{Kind: core.SubjectPrincipal, ID: f.carol}.Key()); err != nil {
		t.Fatalf("expire grant: %v", err)
	}

	err := f.svc.Require(ctx, f.subjects(t, f.carol), f.doc(), core.AccessView)
	if !errors.Is(err, authz.ErrDenied) {
		t.Errorf("an expired grant still granted access: %v", err)
	}
}

func TestRevokeRemovesAccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sub := core.Subject{Kind: core.SubjectPrincipal, ID: f.carol}
	mustGrant(t, f, sub, core.AccessEdit)

	if err := f.svc.Revoke(ctx, f.org, f.doc(), sub, f.alice); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := f.svc.Require(ctx, f.subjects(t, f.carol), f.doc(), core.AccessView); !errors.Is(err, authz.ErrDenied) {
		t.Errorf("carol kept access after revoke: %v", err)
	}
	// Revoking again must not error.
	if err := f.svc.Revoke(ctx, f.org, f.doc(), sub, f.alice); err != nil {
		t.Errorf("second revoke errored: %v", err)
	}
	// And it must publish nothing the second time, because nothing changed.
	// An event per no-op reindexes the whole document every time somebody
	// clicks a button that does nothing.
	var events int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE entity_id=$1 AND event='granted'`,
		f.docID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 2 {
		t.Errorf("%d grant events for one grant and one revoke, want 2", events)
	}
}

func TestGrantReplacesRatherThanDuplicates(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sub := core.Subject{Kind: core.SubjectPrincipal, ID: f.carol}

	mustGrant(t, f, sub, core.AccessView)
	mustGrant(t, f, sub, core.AccessAdmin)

	var n int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM grants WHERE entity_id=$1 AND subject_key=$2`,
		f.docID, sub.Key()).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 1 {
		t.Errorf("expected exactly one grant row after re-granting, got %d", n)
	}

	got, err := f.svc.LevelFor(ctx, f.subjects(t, f.carol), f.doc())
	if err != nil {
		t.Fatalf("LevelFor: %v", err)
	}
	if got != core.AccessAdmin {
		t.Errorf("re-grant did not take effect: got %q", got)
	}
}

func TestGrantRejectsInvalidLevel(t *testing.T) {
	f := setup(t)
	err := f.svc.Grant(context.Background(), f.org, f.doc(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.carol}, core.AccessLevel("superuser"), f.alice)
	if err == nil {
		t.Fatal("an invalid access level was accepted")
	}
}

func TestNoSubjectsDenies(t *testing.T) {
	f := setup(t)
	mustGrant(t, f, core.Subject{Kind: core.SubjectOrg, ID: f.org}, core.AccessAdmin)

	// An empty subject set is what an unauthenticated request looks like if
	// something upstream forgot to populate it. It must deny, not fall through.
	err := f.svc.Require(context.Background(), core.Subjects{}, f.doc(), core.AccessView)
	if !errors.Is(err, authz.ErrDenied) {
		t.Errorf("an empty subject set was granted access: %v", err)
	}
}

// GrantSpace has to reach backward onto whatever already existed: the space
// itself, a sub-space already sitting under it, and a document already filed
// in either one. Everything created after the grant reaches the same place
// through plant.go's and docs.Create's own forward-looking inheritance, so
// this is the one path that has no other way to arrive.
func TestGrantSpaceReachesWhatAlreadyExists(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	// Alice needs to be a super admin to call GrantSpace at all -- the same
	// requirement SetDepartments carries. People (HR) is not enough.
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'super_admin',$2)`, f.org, f.alice); err != nil {
		t.Fatalf("grant super admin: %v", err)
	}

	parent := uuid.NewString()
	child := uuid.NewString()
	preexisting := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Engineering','area',$3)`,
		parent, f.org, f.alice); err != nil {
		t.Fatalf("create parent space: %v", err)
	}
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by) VALUES ($1,$2,$3,'Red Team','area',$4)`,
		child, f.org, parent, f.alice); err != nil {
		t.Fatalf("create child space: %v", err)
	}
	// Filed directly, bypassing docs.Create, so this document predates any
	// grant on the space -- exactly the case a person joining after the fact
	// has to reach.
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO documents (id, org_id, space_id, kind, title, owner_id, created_by, updated_by)
		 VALUES ($1,$2,$3,'task','Existing work',$4,$4,$4)`,
		preexisting, f.org, parent, f.alice); err != nil {
		t.Fatalf("create pre-existing document: %v", err)
	}

	if err := f.svc.GrantSpace(ctx, f.org, f.alice, f.dave, parent); err != nil {
		t.Fatalf("GrantSpace: %v", err)
	}

	dave := f.subjects(t, f.dave)
	cases := []struct {
		name string
		ref  core.Ref
	}{
		{"the space itself", core.Ref{Type: core.EntitySpace, ID: parent}},
		{"the sub-space already under it", core.Ref{Type: core.EntitySpace, ID: child}},
		{"the document already filed in it", core.Ref{Type: core.EntityDocument, ID: preexisting}},
	}
	for _, c := range cases {
		level, err := f.svc.LevelFor(ctx, dave, c.ref)
		if err != nil {
			t.Fatalf("%s: LevelFor: %v", c.name, err)
		}
		if level == core.AccessNone {
			t.Errorf("dave has no access to %s after being granted the parent department", c.name)
		}
	}
}

// Revoking a department takes back the space grant. It deliberately does not
// chase down every document that grant already reached -- SetDepartments
// does not unwind its own consequences elsewhere either, and a department
// revoked by mistake is re-granted, not reconstructed row by row.
func TestRevokeSpaceLeavesAlreadyPropagatedDocumentGrants(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,'super_admin',$2)`, f.org, f.alice); err != nil {
		t.Fatalf("grant super admin: %v", err)
	}
	space := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Engineering','area',$3)`,
		space, f.org, f.alice); err != nil {
		t.Fatalf("create space: %v", err)
	}

	if err := f.svc.GrantSpace(ctx, f.org, f.alice, f.dave, space); err != nil {
		t.Fatalf("GrantSpace: %v", err)
	}
	if err := f.svc.RevokeSpace(ctx, f.org, f.alice, f.dave, space); err != nil {
		t.Fatalf("RevokeSpace: %v", err)
	}

	level, err := f.svc.LevelFor(ctx, f.subjects(t, f.dave), core.Ref{Type: core.EntitySpace, ID: space})
	if err != nil {
		t.Fatalf("LevelFor: %v", err)
	}
	if level != core.AccessNone {
		t.Errorf("dave still has %q on the space after it was revoked", level)
	}
}

func mustGrant(t *testing.T, f *fixture, sub core.Subject, level core.AccessLevel) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.svc.Grant(ctx, f.org, f.doc(), sub, level, f.alice); err != nil {
		t.Fatalf("grant %s to %s: %v", level, sub.Key(), err)
	}
}

// Moving people around -- departments, approver, managers, department access --
// is an admin's act. HR holds People and is management, not admin: every one
// of these is refused to them, and allowed to a super admin. Asserted for all
// six, because the one left out is the one somebody reaches for.
func TestOnlyAnAdminMovesPeopleAround(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Carol is HR; Alice is a super admin; Dave is moved around.
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'people',$3)`, f.org, f.carol, f.alice)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, f.alice)
	dept := uuid.NewString()
	exec(`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'White Team','folder',$3)`, dept, f.org, f.alice)

	moves := map[string]func(actor string) error{
		"set departments": func(a string) error { return f.svc.SetDepartments(ctx, f.org, a, f.dave, []string{dept}) },
		"set approver":    func(a string) error { return f.svc.SetApprover(ctx, f.org, a, f.dave, f.bob) },
		"add manager":     func(a string) error { return f.svc.AddManager(ctx, f.org, a, dept, f.dave) },
		"remove manager":  func(a string) error { return f.svc.RemoveManager(ctx, f.org, a, dept, f.dave) },
		"grant access":    func(a string) error { return f.svc.GrantSpace(ctx, f.org, a, f.dave, dept) },
		"revoke access":   func(a string) error { return f.svc.RevokeSpace(ctx, f.org, a, f.dave, dept) },
	}
	for name, move := range moves {
		if err := move(f.carol); !errors.Is(err, authz.ErrDenied) {
			t.Errorf("HR could %s: %v", name, err)
		}
		if err := move(f.alice); err != nil {
			t.Errorf("a super admin could not %s: %v", name, err)
		}
	}
}

// The one exception: a playground is a made-up company its own user runs, and
// moving its made-up colleagues is a lesson. People is enough there -- and only
// there; the same holder is refused in the real org (TestOnlyAnAdminMovesPeopleAround).
func TestPeopleMovesPeopleOnlyInsideAPlayground(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	sandbox, self, colleague, dept := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO orgs (id, slug, name, playground_of) VALUES ($1, $2, 'Sandbox', $3)`, sandbox, "pg-"+sandbox[:8], f.alice)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{`DELETE FROM principal_departments WHERE org_id=$1`, `DELETE FROM capability_grants WHERE org_id=$1`,
			`DELETE FROM spaces WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = f.pools.Web.Exec(ctx, q, sandbox)
		}
	})
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','You'), ($3,$2,'user','Al')`, self, sandbox, colleague)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'people',$2)`, sandbox, self)
	exec(`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Engineering','folder',$3)`, dept, sandbox, self)

	if err := f.svc.SetDepartments(ctx, sandbox, self, colleague, []string{dept}); err != nil {
		t.Fatalf("the playground's own user could not move a made-up colleague: %v", err)
	}
	// Without People, not even there.
	exec(`DELETE FROM capability_grants WHERE org_id=$1`, sandbox)
	if err := f.svc.SetDepartments(ctx, sandbox, self, colleague, nil); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("a playground user without People moved somebody: %v", err)
	}
}
