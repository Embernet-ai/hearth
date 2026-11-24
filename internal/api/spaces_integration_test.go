package api_test

// The space routes, against a real Postgres: a super admin makes and lists spaces, asking twice for one name returns the
// same space, and a person with no space admin rights, a read token and a token with no such route switched on are all
// refused, exactly as the Spaces page refuses them.

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/plant"
)

func TestSpacesOverTheAPIAreRBACGated(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	admin := uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Ada Admin')`, admin, f.org)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, admin)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(ctx, `DELETE FROM spaces WHERE org_id=$1`, f.org)
		_, _ = f.pools.Web.Exec(ctx, `DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})

	// Not switched on: the routes answer 404, never a half answer.
	adminTok := f.token(t, admin, "write")
	if code, _, _ := f.call(t, adminTok, "GET", "/v1/spaces", nil); code != http.StatusNotFound {
		t.Fatalf("spaces before they are switched on: %d", code)
	}
	f.svc.WithSpaces(plant.New(f.pools.Web, f.az))
	f.handler = f.svc.Routes()

	// A super admin makes one.
	code, got, _ := f.call(t, adminTok, "POST", "/v1/spaces", map[string]any{"name": "Codex beads", "kind": "project"})
	if code != http.StatusCreated || got["name"] != "Codex beads" || got["kind"] != "project" || got["id"] == "" {
		t.Fatalf("create = %d %v", code, got)
	}
	id := got["id"].(string)

	// Asking again for the same name returns it, not a second space.
	code, got, _ = f.call(t, adminTok, "POST", "/v1/spaces", map[string]any{"name": " codex BEADS "})
	if code != http.StatusCreated || got["id"] != id {
		t.Fatalf("second create = %d %v, want the same id %s", code, got, id)
	}
	if code, got, _ := f.call(t, adminTok, "GET", "/v1/spaces", nil); code != http.StatusOK || len(got["spaces"].([]any)) != 1 {
		t.Fatalf("admin's list = %d %v", code, got)
	}

	// A person with no space admin rights cannot make a top-level space, and does not see the admin's.
	aliceTok := f.token(t, f.alice, "write")
	if code, _, _ := f.call(t, aliceTok, "POST", "/v1/spaces", map[string]any{"name": "Alice's own"}); code != http.StatusForbidden {
		t.Errorf("a person without space admin rights made a space: %d", code)
	}
	if code, got, _ := f.call(t, aliceTok, "GET", "/v1/spaces", nil); code != http.StatusOK || len(got["spaces"].([]any)) != 0 {
		t.Errorf("alice saw a space she has no grant on: %d %v", code, got)
	}

	// A read token cannot write, even for a super admin.
	if code, _, _ := f.call(t, f.token(t, admin, "read"), "POST", "/v1/spaces", map[string]any{"name": "Nope"}); code != http.StatusForbidden {
		t.Errorf("a read token made a space: %d", code)
	}
	// A name and a kind are checked.
	if code, _, _ := f.call(t, adminTok, "POST", "/v1/spaces", map[string]any{"name": "  "}); code != http.StatusBadRequest {
		t.Errorf("a blank name: %d", code)
	}
	if code, _, _ := f.call(t, adminTok, "POST", "/v1/spaces", map[string]any{"name": "X", "kind": "galaxy"}); code != http.StatusBadRequest {
		t.Errorf("a bad kind: %d", code)
	}
}
