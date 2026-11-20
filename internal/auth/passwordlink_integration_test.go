package auth_test

// Single-use set-password links, and the must-change flag that now carries
// through to the identity a request is served with. Against a real Postgres.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
)

type linkFixture struct {
	svc            *auth.Service
	ctx            context.Context
	orgID          string
	owner, super   string
	person, second string
	q              func(sql string, args ...any) error
	db             *pgxpool.Pool
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	db := freshInstall(t)
	ctx := context.Background()
	f := &linkFixture{svc: auth.New(db, authz.New(db), false), ctx: ctx, db: db}
	var err error
	if f.owner, err = f.svc.Setup(ctx, "Fireball", "Owner", "owner@example.com", "a-long-enough-password"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&f.orgID); err != nil {
		t.Fatalf("org: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `SELECT audit_purge_org($1)`, f.orgID) })
	if f.super, err = f.svc.AddPerson(ctx, f.orgID, f.owner, "Super", "super@example.com", "a-long-enough-password"); err != nil {
		t.Fatalf("add super: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$3)`,
		f.orgID, f.super, f.owner); err != nil {
		t.Fatalf("grant super: %v", err)
	}
	if f.person, err = f.svc.AddPerson(ctx, f.orgID, f.owner, "Anna", "anna@example.com", "handed-over-password-1"); err != nil {
		t.Fatalf("add person: %v", err)
	}
	if f.second, err = f.svc.AddPerson(ctx, f.orgID, f.owner, "Erin", "erin@example.com", "handed-over-password-2"); err != nil {
		t.Fatalf("add second: %v", err)
	}
	f.q = func(sql string, args ...any) error { _, err := db.Exec(ctx, sql, args...); return err }
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `DELETE FROM password_links`) })
	return f
}

func TestALinkSetsThePasswordOnceAndOnlyOnce(t *testing.T) {
	f := newLinkFixture(t)
	l, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, f.person)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if l.Email != "anna@example.com" || l.Token == "" {
		t.Fatalf("unexpected link: %+v", l)
	}
	if d := time.Until(l.ExpiresAt); d < 23*time.Hour || d > 24*time.Hour+time.Minute {
		t.Errorf("expires in %v, want about 24h", d)
	}

	if email, err := f.svc.PeekPasswordLink(f.ctx, l.Token); err != nil || email != "anna@example.com" {
		t.Fatalf("peek = %q, %v", email, err)
	}
	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "short"); !errors.Is(err, auth.ErrWeak) {
		t.Fatalf("a short password was accepted: %v", err)
	}
	// A refused password must not burn the link.
	if _, err := f.svc.PeekPasswordLink(f.ctx, l.Token); err != nil {
		t.Fatalf("a weak attempt used the link up: %v", err)
	}

	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "her-own-new-password"); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// Single use.
	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "another-new-password"); !errors.Is(err, auth.ErrLinkInvalid) {
		t.Fatalf("the link worked twice: %v", err)
	}
	if _, err := f.svc.PeekPasswordLink(f.ctx, l.Token); !errors.Is(err, auth.ErrLinkInvalid) {
		t.Fatalf("a used link still peeks as good: %v", err)
	}

	// The new password signs in; the one handed over does not; the flag is off.
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := f.svc.LoginLocal(f.ctx, w, r, "anna@example.com", "her-own-new-password"); err != nil {
		t.Fatalf("sign in with the new password: %v", err)
	}
	w, r = httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := f.svc.LoginLocal(f.ctx, w, r, "anna@example.com", "handed-over-password-1"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("the old password still works: %v", err)
	}
	if must, err := f.svc.MustChangePassword(f.ctx, f.person); err != nil || must {
		t.Fatalf("must change = %v, %v after choosing their own", must, err)
	}
}

func TestTheTokenIsStoredOnlyAsAHash(t *testing.T) {
	f := newLinkFixture(t)
	l, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, f.person)
	if err != nil {
		t.Fatal(err)
	}
	db := f.db
	var n int
	if err := db.QueryRow(f.ctx, `SELECT count(*) FROM password_links`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("links stored = %d, %v", n, err)
	}
	var leaked int
	if err := db.QueryRow(f.ctx, `
		SELECT (SELECT count(*) FROM password_links WHERE encode(token_hash,'escape') LIKE '%'||$1||'%')
		     + (SELECT count(*) FROM audit_log WHERE after::text LIKE '%'||$1||'%')`, l.Token).Scan(&leaked); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if leaked != 0 {
		t.Fatal("the raw token is stored somewhere it can be read back")
	}
}

func TestALinkExpiresAfterADay(t *testing.T) {
	f := newLinkFixture(t)
	l, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, f.person)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q(`UPDATE password_links SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PeekPasswordLink(f.ctx, l.Token); !errors.Is(err, auth.ErrLinkInvalid) {
		t.Fatalf("an expired link peeks as good: %v", err)
	}
	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "her-own-new-password"); !errors.Is(err, auth.ErrLinkInvalid) {
		t.Fatalf("an expired link worked: %v", err)
	}
	// And a made-up token is the same answer, not a different one.
	if err := f.svc.RedeemPasswordLink(f.ctx, "not-a-token", "her-own-new-password"); !errors.Is(err, auth.ErrLinkInvalid) {
		t.Fatalf("a made-up token: %v", err)
	}
}

func TestANewLinkRetiresTheOldOne(t *testing.T) {
	f := newLinkFixture(t)
	first, _ := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, f.person)
	second, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.super, f.person)
	if err != nil {
		t.Fatalf("a super admin could not issue: %v", err)
	}
	if _, err := f.svc.PeekPasswordLink(f.ctx, first.Token); !errors.Is(err, auth.ErrLinkInvalid) {
		t.Fatalf("the first link survived a second: %v", err)
	}
	if err := f.svc.RedeemPasswordLink(f.ctx, second.Token, "her-own-new-password"); err != nil {
		t.Fatalf("the second link: %v", err)
	}
}

func TestWhoMayIssueALink(t *testing.T) {
	f := newLinkFixture(t)
	// An ordinary account cannot.
	if _, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.person, f.second); !errors.Is(err, auth.ErrNotOwner) {
		t.Fatalf("an ordinary account issued a link: %v", err)
	}
	// A super admin cannot reach the owner, the same line every account action draws.
	if _, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.super, f.owner); !errors.Is(err, auth.ErrIsOwner) {
		t.Fatalf("a super admin issued a link for the owner: %v", err)
	}
	// Nobody issues one for themselves.
	if _, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.super, f.super); err == nil {
		t.Fatal("issued a link for oneself")
	}
	// An unknown person.
	if _, err := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("issued a link for nobody")
	}
}

func TestIssuingAndUsingALinkAreAudited(t *testing.T) {
	f := newLinkFixture(t)
	db := f.db
	l, _ := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, f.person)
	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "her-own-new-password"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"person.password_link", "person.password_set"} {
		var n int
		if err := db.QueryRow(f.ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND entity_id = $2`,
			action, f.person).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s audit rows = %d, %v", action, n, err)
		}
	}
}

func TestAddingSomebodyWithALinkNeedsNoPassword(t *testing.T) {
	f := newLinkFixture(t)
	l, err := f.svc.AddPersonWithLink(f.ctx, f.orgID, f.owner, "Dana", "dana@example.com")
	if err != nil {
		t.Fatalf("add with link: %v", err)
	}
	// Nobody was told a password, so nothing but the link gets them in.
	if err := f.svc.LoginLocal(f.ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil),
		"dana@example.com", ""); err == nil {
		t.Fatal("signed in with an empty password")
	}
	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "danas-own-password"); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if err := f.svc.LoginLocal(f.ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil),
		"dana@example.com", "danas-own-password"); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if _, err := f.svc.AddPersonWithLink(f.ctx, f.orgID, f.person, "Mal", "mal@example.com"); !errors.Is(err, auth.ErrNotOwner) {
		t.Fatalf("an ordinary account added somebody: %v", err)
	}
}

func TestChoosingAPasswordEndsTheirOtherSessions(t *testing.T) {
	f := newLinkFixture(t)
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := f.svc.LoginLocal(f.ctx, w, r, "anna@example.com", "handed-over-password-1"); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	l, _ := f.svc.IssuePasswordLink(f.ctx, f.orgID, f.owner, f.person)
	if err := f.svc.RedeemPasswordLink(f.ctx, l.Token, "her-own-new-password"); err != nil {
		t.Fatal(err)
	}
	got := identityFor(f, cookie)
	if got != nil {
		t.Fatal("a session from before the new password survived it")
	}
}

// identityFor runs a request carrying the cookie through the real middleware.
func identityFor(f *linkFixture, c *http.Cookie) *auth.Identity {
	var got *auth.Identity
	h := f.svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = auth.From(r.Context()) }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	h.ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestTheMustChangeFlagReachesTheIdentity(t *testing.T) {
	f := newLinkFixture(t)
	w, r := httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := f.svc.LoginLocal(f.ctx, w, r, "anna@example.com", "handed-over-password-1"); err != nil {
		t.Fatal(err)
	}
	id := identityFor(f, w.Result().Cookies()[0])
	if id == nil || !id.MustChangePassword {
		t.Fatalf("an account on a handed-over password reads as %+v", id)
	}
	// Changing it clears the flag on the very next request.
	if err := f.svc.ChangeOwnPassword(f.ctx, f.person, "handed-over-password-1", "chosen-by-anna-herself"); err != nil {
		t.Fatal(err)
	}
	id = identityFor(f, w.Result().Cookies()[0])
	if id == nil || id.MustChangePassword {
		t.Fatalf("after changing it the flag is still %+v", id)
	}
	// The owner, who chose their own password at setup, is never gated.
	w2, r2 := httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := f.svc.LoginLocal(f.ctx, w2, r2, "owner@example.com", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	if id := identityFor(f, w2.Result().Cookies()[0]); id == nil || id.MustChangePassword {
		t.Fatalf("the owner reads as %+v", id)
	}
}
