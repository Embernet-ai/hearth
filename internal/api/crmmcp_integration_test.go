package api_test

// The CRM as MCP tools, against a real Postgres: each tool is a route run through
// its own handler, so it sees what the caller's subject set sees, is idempotent on
// external_id, and is only offered to a caller whose writes land.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/agents"
)

func TestTheCRMToolsAreTheCRMAPIRunThroughItsHandlers(t *testing.T) {
	f := crmSetup(t)
	ctx := context.Background()
	tools := f.svc.CRMTools()
	if tools == nil {
		t.Fatal("no CRM tools with a CRM switched on")
	}
	caller := func(principal string, scopes ...string) *agents.Caller {
		subj, err := f.az.SubjectsFor(ctx, f.org, principal)
		if err != nil {
			t.Fatal(err)
		}
		return &agents.Caller{OrgID: f.org, AgentID: principal, AgentName: "test", Scopes: scopes, Subjects: subj}
	}
	names := func(ts []agents.Tool) map[string]bool {
		m := map[string]bool{}
		for _, x := range ts {
			m[x.Name] = true
		}
		return m
	}
	alice, carol := caller(f.alice, agents.ScopeRead, agents.ScopeWrite), caller(f.carol, agents.ScopeRead, agents.ScopeWrite)

	t.Run("a caller whose writes do not land is not offered a write tool", func(t *testing.T) {
		ro := names(tools.Tools(alice, false))
		if !ro["crm_find_contacts"] || !ro["crm_stats"] || ro["crm_create_contact"] || ro["crm_add_activity"] {
			t.Errorf("read-only tools: %v", ro)
		}
		rw := names(tools.Tools(alice, true))
		for _, n := range []string{"crm_create_contact", "crm_update_deal", "crm_add_activity", "crm_create_company", "crm_create_deal"} {
			if !rw[n] {
				t.Errorf("%s missing from the write tools", n)
			}
		}
		if r, ok := tools.Call(ctx, alice, "crm_create_contact", map[string]any{"name": "X"}, false); !ok || !r.IsError {
			t.Errorf("a write without write rights: %v %v", r, ok)
		}
	})

	call := func(c *agents.Caller, name string, args map[string]any) (string, bool) {
		t.Helper()
		r, ok := tools.Call(ctx, c, name, args, true)
		if !ok {
			t.Fatalf("%s is not a tool", name)
		}
		var b strings.Builder
		for _, part := range r.Content {
			b.WriteString(part.Text)
		}
		return b.String(), r.IsError
	}

	co, isErr := call(alice, "crm_create_company", map[string]any{"name": "Acme", "domain": "acme.example", "external_id": "co-1", "source": "mcp-test"})
	if isErr {
		t.Fatalf("create company: %s", co)
	}
	var company map[string]any
	_ = json.Unmarshal([]byte(co), &company)
	companyID, _ := company["id"].(string)

	first, _ := call(alice, "crm_create_contact", map[string]any{"name": "Dana", "email": "dana@acme.example", "company_id": companyID, "external_id": "chat-9", "source": "mcp-test"})
	again, isErr := call(alice, "crm_create_contact", map[string]any{"name": "Dana", "email": "dana@acme.example", "company_id": companyID, "external_id": "chat-9", "source": "mcp-test"})
	const replay = "Already existed, nothing changed: "
	var a, b map[string]any
	_ = json.Unmarshal([]byte(first), &a)
	_ = json.Unmarshal([]byte(strings.TrimPrefix(again, replay)), &b)
	if isErr || !strings.HasPrefix(again, replay) || a["id"] == nil || a["id"] != b["id"] {
		t.Errorf("a create sent twice: first %q, again %q", first, again)
	}

	found, isErr := call(alice, "crm_find_contacts", map[string]any{"email": "DANA@acme.example"})
	if isErr || !strings.Contains(found, "dana@acme.example") {
		t.Errorf("find by email: %v %s", isErr, found)
	}
	dup, isErr := call(alice, "crm_create_contact", map[string]any{"name": "Dana Two", "email": "dana@acme.example"})
	if !isErr || !strings.Contains(dup, "existing_id") {
		t.Errorf("a second holder of an email should be refused and name the first: %v %s", isErr, dup)
	}

	deal, isErr := call(alice, "crm_create_deal", map[string]any{"name": "Rewind line", "company_id": companyID, "amount_cents": 120000})
	if isErr || !strings.Contains(deal, `"stage":"new"`) {
		t.Fatalf("create deal: %v %s", isErr, deal)
	}
	var d map[string]any
	_ = json.Unmarshal([]byte(deal), &d)
	moved, isErr := call(alice, "crm_update_deal", map[string]any{"id": d["id"], "stage": "proposal"})
	if isErr || !strings.Contains(moved, `"stage":"proposal"`) {
		t.Errorf("move a deal: %v %s", isErr, moved)
	}
	note, isErr := call(alice, "crm_add_activity", map[string]any{"kind": "note", "title": "Chat", "body": "asked for a quote",
		"targets": []map[string]any{{"type": "company", "id": companyID}}})
	if isErr {
		t.Errorf("add a note: %s", note)
	}
	tl, isErr := call(alice, "crm_timeline", map[string]any{"kind": "companies", "id": companyID})
	if isErr || !strings.Contains(tl, "asked for a quote") {
		t.Errorf("timeline: %v %s", isErr, tl)
	}
	stats, isErr := call(alice, "crm_stats", map[string]any{})
	if isErr || !strings.Contains(stats, `"deals":1`) {
		t.Errorf("stats: %v %s", isErr, stats)
	}

	// Carol is not on the Sales team: the tools show her what the pages show her.
	if out, _ := call(carol, "crm_find_contacts", map[string]any{"email": "dana@acme.example"}); strings.Contains(out, "dana@acme.example") {
		t.Errorf("somebody outside the sales team found a contact: %s", out)
	}
	if out, isErr := call(carol, "crm_get_company", map[string]any{"id": companyID}); !isErr {
		t.Errorf("somebody outside the sales team read a company: %s", out)
	}
	if out, isErr := call(alice, "crm_get_company", map[string]any{"id": "../x"}); !isErr {
		t.Errorf("a path-shaped id was accepted: %s", out)
	}
}
