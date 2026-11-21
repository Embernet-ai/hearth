package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
)

// A signed in contractor account is known to be one on every request, so the web
// layer can refuse it every page that is not about its folder. A super admin is
// never narrowed, whatever their account says, and an ordinary account is not
// one.
func TestAContractorIsKnownOnEveryRequestAndASuperAdminNever(t *testing.T) {
	db := freshInstall(t)
	ctx := context.Background()
	svc := auth.New(db, authz.New(db), false)
	owner, err := svc.Setup(ctx, "Fireball", "Pat", "owner@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	var orgID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `SELECT audit_purge_org($1)`, orgID) })

	add := func(name, email string) string {
		id, err := svc.AddPerson(ctx, orgID, owner, name, email, "a-long-enough-password")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	contractor, plain, super := add("Carol", "carol@example.com"), add("Ana", "ana@example.com"), add("Sam", "sam@example.com")
	_ = contractor
	if _, err := db.Exec(ctx, `INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$3)`, orgID, super, owner); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{contractor, super} {
		if err := svc.SetContractor(ctx, orgID, owner, id, true); err != nil {
			t.Fatal(err)
		}
	}

	seen := func(email string) *auth.Identity {
		t.Helper()
		w := httptest.NewRecorder()
		if err := svc.LoginLocal(ctx, w, httptest.NewRequest(http.MethodPost, "/login", nil), email, "a-long-enough-password"); err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range w.Result().Cookies() {
			r.AddCookie(c)
		}
		var got *auth.Identity
		svc.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = auth.From(r.Context()) })).
			ServeHTTP(httptest.NewRecorder(), r)
		if got == nil {
			t.Fatalf("no identity for %s", email)
		}
		return got
	}
	if !seen("carol@example.com").Contractor {
		t.Error("a contractor account is not known as one")
	}
	if seen("ana@example.com").Contractor {
		t.Error("an ordinary account is known as a contractor")
	}
	if seen("sam@example.com").Contractor {
		t.Error("a super admin marked as a contractor is narrowed")
	}
	_ = plain
}
