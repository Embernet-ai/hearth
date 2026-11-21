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
