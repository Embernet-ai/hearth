package scim_test

// The directory, against a real Postgres and over real HTTP.
//
// Driven the way Entra drives it: a bearer token, the two filters it searches
// with, and both of the shapes it sends a removal in. The criterion this is all
// for is the last test in the file, and it is about the authz service rather
// than about this package: taking somebody out of a group has to take their
// access away on their next request, not on their next sign-in.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/scim"
	"github.com/Embernet-ai/hearth/internal/teams"
)

type fx struct {
	svc    *scim.Service
	teams  *teams.Service
	auth   *auth.Service
	az     *authz.Service
	docs   *docs.Service
	pools  *pg.Pools
	server *httptest.Server
	token  string
	org    string
	owner  string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
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

	az := authz.New(pools.Web)
	authSvc := auth.New(pools.Web, az, false)
	teamsSvc := teams.New(pools.Web)
	f := &fx{
		teams: teamsSvc, auth: authSvc, az: az, docs: docs.New(pools.Web, az),
		pools: pools, org: uuid.NewString(), owner: uuid.NewString(),
		svc: scim.New(pools.Web, teamsSvc, authSvc, log),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'SCIM Test')`,
		f.org, "sc-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Owner')`,
		f.owner, f.org)

	secret, err := f.svc.Mint(ctx, f.org, f.owner, "Entra provisioning")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	f.token = secret
	f.server = httptest.NewServer(http.StripPrefix("/scim/v2", f.svc.Routes()))
	t.Cleanup(f.server.Close)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM scim_tokens WHERE org_id=$1`,
			`DELETE FROM team_members WHERE team_id IN (SELECT id FROM teams WHERE org_id=$1)`,
			`DELETE FROM teams WHERE org_id=$1`,
			`DELETE FROM sessions WHERE org_id=$1`,
			`DELETE FROM local_credentials WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

// call makes a request the way a provisioning job does.
func (f *fx) call(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.server.URL+"/scim/v2"+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/scim+json")

	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s returned HTTP %d and %q, which is not JSON",
				method, path, resp.StatusCode, string(raw))
		}
	}
	return resp.StatusCode, out
}

func TestAProvisioningTokenIsTheOnlyWayIn(t *testing.T) {
	f := setup(t)

	// No token at all.
	resp, err := f.server.Client().Get(f.server.URL + "/scim/v2/Users")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("HTTP %d with no token, want 401", resp.StatusCode)
	}
	// The header matters: without it a provisioning job retries the same bad
	// credential forever instead of reporting a configuration problem.
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("a 401 with no WWW-Authenticate tells the directory nothing")
	}

	// A revoked one stops working straight away rather than at the next
	// restart.
	ctx := context.Background()
	var id string
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT id::text FROM scim_tokens WHERE org_id=$1`, f.org).Scan(&id); err != nil {
		t.Fatalf("read token: %v", err)
	}
	if err := f.svc.Revoke(ctx, f.org, f.owner, id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if code, _ := f.call(t, http.MethodGet, "/Users", ""); code != http.StatusUnauthorized {
		t.Errorf("a revoked token got HTTP %d", code)
	}
}

func TestAPersonIsCreatedFoundAndDeactivated(t *testing.T) {
	f := setup(t)

	code, created := f.call(t, http.MethodPost, "/Users", `{
	  "schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
	  "externalId":"aad-1001",
	  "userName":"dana@example.invalid",
	  "name":{"givenName":"Dana","familyName":"Okafor"},
	  "emails":[{"value":"dana@example.invalid","primary":true}],
	  "active":true}`)
	if code != http.StatusCreated {
		t.Fatalf("HTTP %d creating a user: %v", code, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("the created user has no id, so the directory has nothing to come back to")
	}
	if created["displayName"] != "Dana Okafor" {
		t.Errorf("the name came out as %v", created["displayName"])
	}
	if created["active"] != true {
		t.Error("a user created active is not active")
	}

	// The one search a provisioning job makes, deciding between creating and
	// updating.
	code, found := f.call(t, http.MethodGet,
		`/Users?filter=userName+eq+%22dana@example.invalid%22`, "")
	if code != http.StatusOK {
		t.Fatalf("HTTP %d searching: %v", code, found)
	}
	if n, _ := found["totalResults"].(float64); n != 1 {
		t.Fatalf("the search found %v users, want exactly one", found["totalResults"])
	}

	// A filter that matches nobody has to return nobody rather than everybody,
	// which is the way this usually breaks and the way it silently reconciles
	// a whole tenant.
	_, none := f.call(t, http.MethodGet,
		`/Users?filter=userName+eq+%22nobody@example.invalid%22`, "")
	if n, _ := none["totalResults"].(float64); n != 0 {
		t.Errorf("a filter matching nobody returned %v users", none["totalResults"])
	}

	// Deactivation, in both shapes Entra sends it. The string "False" is not a
	// mistake: some provisioning jobs send it because their internal
	// representation is a string.
	code, patched := f.call(t, http.MethodPatch, "/Users/"+id, `{
	  "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
	  "Operations":[{"op":"replace","path":"active","value":"False"}]}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d deactivating: %v", code, patched)
	}
	if patched["active"] != false {
		t.Error(`a patch with the string "False" did not deactivate the account`)
	}

	// And a DELETE suspends rather than deleting, because a principal is the
	// foreign key on every "who did this" in the database.
	if code, _ := f.call(t, http.MethodPatch, "/Users/"+id, `{
	  "Operations":[{"op":"replace","path":"active","value":true}]}`); code != http.StatusOK {
		t.Fatalf("could not reinstate: HTTP %d", code)
	}
	if code, _ := f.call(t, http.MethodDelete, "/Users/"+id, ""); code != http.StatusNoContent {
		t.Errorf("HTTP %d deleting", code)
	}
	var status string
	if err := f.pools.Web.QueryRow(context.Background(),
		`SELECT status FROM principals WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatalf("the principal was actually deleted, taking its history: %v", err)
	}
	if status != "suspended" {
		t.Errorf("after a delete the account is %q", status)
	}
}

// The criterion the whole phase is judged on.
//
// Removing somebody from a group in the directory takes their access away, and
// it takes it away on their next request rather than on their next sign-in,
// because a session lasts thirty days and the day somebody leaves is exactly
// the day they do not sign in again.
func TestRemovingSomebodyFromAGroupRevokesTheirAccess(t *testing.T) {
	f, ctx := setup(t), context.Background()

	_, dana := f.call(t, http.MethodPost, "/Users", `{
	  "externalId":"aad-2001","userName":"dana@example.invalid",
	  "displayName":"Dana","active":true}`)
	danaID, _ := dana["id"].(string)
	if danaID == "" {
		t.Fatal("no user was created")
	}

	code, group := f.call(t, http.MethodPost, "/Groups", `{
	  "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
	  "externalId":"aad-group-3001","displayName":"Line 3 maintenance",
	  "members":[{"value":"`+danaID+`"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("HTTP %d creating a group: %v", code, group)
	}
	groupID, _ := group["id"].(string)

	// Something only that team can see.
	owner, err := f.az.SubjectsFor(ctx, f.org, f.owner)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	machine, err := f.docs.Create(ctx, f.org, owner, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Conveyor 3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.docs.Share(ctx, f.org, owner, machine.ID,
		core.Subject{Kind: core.SubjectTeam, ID: groupID}, core.AccessEdit, f.owner); err != nil {
		t.Fatalf("share: %v", err)
	}

	reads := func() bool {
		subs, err := f.az.SubjectsFor(ctx, f.org, danaID)
		if err != nil {
			t.Fatalf("subjects: %v", err)
		}
		_, err = f.docs.Get(ctx, f.org, subs, machine.ID)
		return err == nil
	}
	if !reads() {
		t.Fatal("somebody in the group cannot see what the group was granted")
	}

	// The removal, in the shape Entra sends when it is provisioning a single
	// member change: a filtered path and no value at all.
	code, _ = f.call(t, http.MethodPatch, "/Groups/"+groupID, `{
	  "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
	  "Operations":[{"op":"remove","path":"members[value eq \"`+danaID+`\"]"}]}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d removing a member", code)
	}
	if reads() {
		t.Fatal("removing somebody from a group in the directory left their " +
			"access behind, which is the whole thing this endpoint is for")
	}

	// And the other shape, a plain path with a value array, has to work too:
	// which one arrives depends on the tenant's provisioning mode rather than
	// on anything here.
	if code, _ := f.call(t, http.MethodPatch, "/Groups/"+groupID, `{
	  "Operations":[{"op":"add","path":"members","value":[{"value":"`+danaID+`"}]}]}`); code != http.StatusOK {
		t.Fatalf("HTTP %d adding a member back", code)
	}
	if !reads() {
		t.Fatal("adding somebody back did not restore their access")
	}
	if code, _ := f.call(t, http.MethodPatch, "/Groups/"+groupID, `{
	  "Operations":[{"op":"remove","path":"members","value":[{"value":"`+danaID+`"}]}]}`); code != http.StatusOK {
		t.Fatalf("HTTP %d removing with a value array", code)
	}
	if reads() {
		t.Fatal("a removal with a value array did not take effect")
	}
}

// A directory that finds the owner's address must not adopt that account. The
// next thing it does is deactivate everybody who is not in a group it knows
// about, and there is nobody left who could undo that.
func TestTheOwnerIsNeverTakenOverByTheDirectory(t *testing.T) {
	f := setup(t)

	// Give the owner a local credential, which is what an installation set up
	// before the directory was connected looks like.
	if _, err := f.pools.Web.Exec(context.Background(), `
		INSERT INTO local_credentials (principal_id, org_id, email, password_hash)
		VALUES ($1,$2,$3,'x')`, f.owner, f.org, "owner@example.invalid"); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	code, body := f.call(t, http.MethodPost, "/Users", `{
	  "externalId":"aad-9001","userName":"owner@example.invalid",
	  "displayName":"Owner","active":true}`)
	if code == http.StatusCreated {
		t.Fatal("the directory took over the account that claimed this installation")
	}
	if code != http.StatusInternalServerError && code != http.StatusBadRequest {
		t.Errorf("HTTP %d: %v", code, body)
	}

	var status string
	if err := f.pools.Web.QueryRow(context.Background(),
		`SELECT status FROM principals WHERE id=$1`, f.owner).Scan(&status); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if status != "active" {
		t.Errorf("the owner is %q", status)
	}
}
