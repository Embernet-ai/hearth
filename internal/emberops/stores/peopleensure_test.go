package stores_test

import (
	"context"
	"testing"

	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

type persons map[string]mapping.Rec

func (p persons) Person(_ context.Context, _, id string) (mapping.Rec, error) {
	r := mapping.Rec{}
	for k, v := range p[id] {
		r[k] = v
	}
	return r, nil
}

const newbie = "44444444-4444-4444-4444-444444444444"

func TestAPersonWhoSignsInGetsAUserAndAnEmployeeWithTheirApprover(t *testing.T) {
	e := newEnv(t)
	src := persons{
		newbie: {"id": newbie, "display_name": "Nia New", "email": "nia@fireballz.ai", "kind": "user", "status": "active", "approver_id": bob},
		bob:    {"id": bob, "display_name": "Bob", "email": "bob@example.com", "kind": "user", "status": "active", "approver_id": ann},
	}
	// Bob already has his records: he is made again, not twice, and his own approver is not chased.
	p := &stores.People{Base: e.base, Source: src}
	if err := p.Ensure(e.ctx, org, newbie); err != nil {
		t.Fatal(err)
	}
	var user, emp map[string]any
	for _, u := range e.f.All("User") {
		if u["hearth_id"] == newbie {
			user = u
		}
	}
	for _, m := range e.f.All("Employee") {
		if m["hearth_id"] == newbie {
			emp = m
		}
	}
	if user == nil || emp == nil {
		t.Fatalf("user %v employee %v", user, emp)
	}
	if user["email"] != "nia@fireballz.ai" || user["first_name"] != "Nia New" || user["enabled"] != float64(1) && user["enabled"] != 1 {
		t.Fatalf("user = %v", user)
	}
	if emp["user_id"] != "nia@fireballz.ai" || emp["hearth_approver"] != "EMP-bob" {
		t.Fatalf("employee = %v", emp)
	}
	// Nothing in what was sent names a role, so nobody's roles can be granted or taken here.
	for _, k := range []string{"roles", "role_profile_name", "user_type", "block_modules"} {
		if _, has := user[k]; has {
			t.Fatalf("the User carries %s: %v", k, user)
		}
	}
	// Again changes nothing: still one of each.
	if err := p.Ensure(e.ctx, org, newbie); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, u := range e.f.All("User") {
		if u["hearth_id"] == newbie {
			n++
		}
	}
	m := 0
	for _, x := range e.f.All("Employee") {
		if x["hearth_id"] == newbie {
			m++
		}
	}
	if n != 1 || m != 1 {
		t.Fatalf("after a second run: %d users, %d employees", n, m)
	}
}

func TestAnAgentWithNoEmailGetsNoEmberOpsAccount(t *testing.T) {
	e := newEnv(t)
	before := len(e.f.All("User"))
	p := &stores.People{Base: e.base, Source: persons{newbie: {"id": newbie, "display_name": "A bot", "kind": "agent", "status": "active"}}}
	if err := p.Ensure(e.ctx, org, newbie); err != nil {
		t.Fatal(err)
	}
	if len(e.f.All("User")) != before {
		t.Fatal("a bot got a User")
	}
}

// Found live 2026-11-25: a person whose email already has an Employee under another Hearth id (made by hand, or for an
// earlier account) was refused as a duplicate and could not log time. The Employee is taken over, not made twice.
const hand = "55555555-5555-5555-5555-555555555555"

func TestAnEmployeeAlreadyOnThisEmailIsTakenOver(t *testing.T) {
	e := newEnv(t)
	e.f.Put("Employee", map[string]any{"name": "HR-EMP-00050", "hearth_id": "99999999-9999-9999-9999-999999999999", "user_id": "nia@fireballz.ai", "first_name": "Old Nia"})
	e.f.Put("Employee", map[string]any{"name": "HR-EMP-00051", "user_id": "hand@fireballz.ai", "first_name": "No id"})
	src := persons{
		newbie: {"id": newbie, "display_name": "Nia New", "email": "nia@fireballz.ai", "kind": "user", "status": "active"},
		hand:   {"id": hand, "display_name": "Hand", "email": "hand@fireballz.ai", "kind": "user", "status": "active"},
	}
	p := &stores.People{Base: e.base, Source: src}
	for i := 0; i < 2; i++ {
		for _, who := range []string{newbie, hand} {
			if err := p.Ensure(e.ctx, org, who); err != nil {
				t.Fatalf("run %d, %s: %v", i, who, err)
			}
		}
	}
	got := map[string]string{}
	for _, m := range e.f.All("Employee") {
		if m["user_id"] == "nia@fireballz.ai" || m["user_id"] == "hand@fireballz.ai" {
			got[m["name"].(string)], _ = m["hearth_id"].(string)
		}
	}
	if len(got) != 2 || got["HR-EMP-00050"] != newbie || got["HR-EMP-00051"] != hand {
		t.Fatalf("employees = %v", got)
	}
}
