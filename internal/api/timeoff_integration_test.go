package api_test

// Time off over the API and as connector tools, against a real Postgres: the same people may
// change and cancel a request here as on the page, a read token and the person themselves may
// not, and each tool is the route run through its own handler.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

func TestTimeOffCanBeChangedAndCancelledOverTheAPIAndAsTools(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	boss, admin := uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Boss'), ($3,$2,'user','Ada Admin')`, boss, f.org, admin)
	exec(`UPDATE principals SET approver_id = $2 WHERE id = $1`, f.alice, boss)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'super_admin',$2)`, f.org, admin)
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(ctx, `DELETE FROM calendar_events WHERE org_id=$1`, f.org)
		_, _ = f.pools.Web.Exec(ctx, `DELETE FROM time_off_requests WHERE org_id=$1`, f.org)
		_, _ = f.pools.Web.Exec(ctx, `DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})
	store := timeoff.New(f.pools.Web, nil)
	f.svc.WithTimeOff(store)
	handler := f.svc.Routes()
	f.handler = handler

	mon := time.Date(2030, 4, 1, 0, 0, 0, 0, time.UTC)
	ask := func() string {
		r, err := store.Ask(ctx, f.org, f.alice, timeoff.Params{StartsOn: mon, EndsOn: mon.AddDate(0, 0, 4), Kind: "vacation", Acknowledged: true})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	id := ask()
	bossTok, adminTok, aliceTok, bobTok := f.token(t, boss, "write"), f.token(t, admin, "write"), f.token(t, f.alice, "write"), f.token(t, f.bob, "write")

	// Who sees it, and who does not.
	if code, got, _ := f.call(t, bossTok, "GET", "/v1/time-off", nil); code != http.StatusOK || len(got["requests"].([]any)) != 1 {
		t.Fatalf("the approver's list: %d %v", code, got)
	}
	if code, got, _ := f.call(t, bobTok, "GET", "/v1/time-off", nil); code != http.StatusOK || len(got["requests"].([]any)) != 0 {
		t.Fatalf("a colleague's list: %d %v", code, got)
	}
	if code, _, _ := f.call(t, bobTok, "GET", "/v1/time-off/"+id, nil); code != http.StatusNotFound {
		t.Fatalf("a colleague read the request: %d", code)
	}

	// Refusals: a read token, the person, a colleague.
	if code, _, _ := f.call(t, f.token(t, boss, "read"), "PATCH", "/v1/time-off/"+id, map[string]any{"kind": "sick"}); code != http.StatusForbidden {
		t.Errorf("a read token changed it: %d", code)
	}
	for name, tok := range map[string]string{"the person": aliceTok, "a colleague": bobTok} {
		if code, _, _ := f.call(t, tok, "PATCH", "/v1/time-off/"+id, map[string]any{"kind": "sick"}); code != http.StatusForbidden {
			t.Errorf("%s changed it: %d", name, code)
		}
		if code, _, _ := f.call(t, tok, "POST", "/v1/time-off/"+id+"/cancel", map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%s cancelled it: %d", name, code)
		}
	}
	if code, _, _ := f.call(t, bossTok, "PATCH", "/v1/time-off/"+id, map[string]any{"starts_on": "next week"}); code != http.StatusBadRequest {
		t.Errorf("a bad date: %d", code)
	}
	if code, _, _ := f.call(t, bossTok, "PATCH", "/v1/time-off/"+id, map[string]any{"nmae": "typo"}); code != http.StatusBadRequest {
		t.Errorf("a misspelled field: %d", code)
	}

	// The approver changes it.
	code, got, _ := f.call(t, bossTok, "PATCH", "/v1/time-off/"+id, map[string]any{"ends_on": "2030-04-03", "half_day": true, "note": "short week"})
	if code != http.StatusOK || got["ends_on"] != "2030-04-03" || got["days"].(float64) != 2.5 || got["half_day"] != true {
		t.Fatalf("change = %d %v", code, got)
	}

	// An agent has the same four tools, offered by what its token may do, run as the same routes.
	tools := f.svc.TimeOffTools()
	if tools == nil {
		t.Fatal("no time off tools with time off switched on")
	}
	caller := func(p string) *agents.Caller {
		return &agents.Caller{OrgID: f.org, AgentID: p, AgentName: "test", Scopes: []string{agents.ScopeRead, agents.ScopeWrite}, Subjects: f.subj(t, p)}
	}
	has := map[string]bool{}
	for _, x := range tools.Tools(caller(boss), false) {
		has[x.Name] = true
	}
	if !has["time_off_list"] || !has["time_off_get"] || has["time_off_change"] || has["time_off_cancel"] {
		t.Errorf("read-only tools: %v", has)
	}
	for _, x := range tools.Tools(caller(boss), true) {
		has[x.Name] = true
	}
	if !has["time_off_change"] || !has["time_off_cancel"] {
		t.Errorf("write tools: %v", has)
	}
	if r, ok := tools.Call(ctx, caller(f.alice), "time_off_change", map[string]any{"id": id, "kind": "sick"}, true); !ok || !r.IsError {
		t.Errorf("the person changed it through a tool: %v", r)
	}
	if r, ok := tools.Call(ctx, caller(boss), "time_off_change", map[string]any{"id": id, "kind": "sick"}, false); !ok || !r.IsError || !strings.Contains(r.Content[0].Text, "cannot change time off") {
		t.Errorf("a write without write rights: %v", r)
	}
	r, ok := tools.Call(ctx, caller(admin), "time_off_change", map[string]any{"id": id, "kind": "sick", "note": "HR"}, true)
	if !ok || r.IsError || !strings.Contains(r.Content[0].Text, `"kind":"sick"`) {
		t.Fatalf("an admin's change through a tool: %v", r)
	}
	r, ok = tools.Call(ctx, caller(admin), "time_off_cancel", map[string]any{"id": id, "note": "off"}, true)
	if !ok || r.IsError || !strings.Contains(r.Content[0].Text, `"status":"cancelled"`) {
		t.Fatalf("an admin's cancel through a tool: %v", r)
	}
	if r, ok := tools.Call(ctx, caller(admin), "time_off_cancel", map[string]any{"id": id}, true); !ok || !r.IsError {
		t.Errorf("cancelled twice: %v", r)
	}

	// The API cancel, on a fresh one, by the admin.
	id2 := ask()
	if code, got, _ := f.call(t, adminTok, "POST", "/v1/time-off/"+id2+"/cancel", map[string]any{"note": "gone"}); code != http.StatusOK || got["status"] != "cancelled" {
		t.Fatalf("cancel = %d %v", code, got)
	}
	var rows int
	if err := f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE org_id = $1 AND action IN ('time_off.change','time_off.cancel')`, f.org).Scan(&rows); err != nil || rows != 4 {
		t.Fatalf("audit rows = %d, %v (two changes and two cancels)", rows, err)
	}
}
