package auth_test

// Who may administer accounts, against a real Postgres.
//
// The owner is one person: whoever claimed the installation. A super admin sits
// one step below and the owner delegates to them, so they may add, suspend and
// reinstate anybody -- except the owner, who is the account that can undo
// everything else and so must never be the one locked out. Before this, every
// one of these was the owner's alone, and a super admin got a 403 on Suspend.

import (
	"context"
	"errors"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
)

func TestASuperAdminAdministersEverybodyButTheOwner(t *testing.T) {
	db := freshInstall(t)
	ctx := context.Background()
	svc := auth.New(db, authz.New(db), false)

	owner, err := svc.Setup(ctx, "Fireball", "Pat", "owner@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	var orgID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatalf("read org: %v", err)
	}
	// The audit log refuses an ordinary delete, so this test's rows leave by the
	// one deliberate way out before the fixture's own cleanup empties the rest.
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `SELECT audit_purge_org($1)`, orgID) })

	super, err := svc.AddPerson(ctx, orgID, owner, "Luis", "super@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("add super admin: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		VALUES ($1, $2, 'super_admin', $3)`, orgID, super, owner); err != nil {
		t.Fatalf("grant super admin: %v", err)
	}
	plain, err := svc.AddPerson(ctx, orgID, owner, "Ana", "plain@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("add plain user: %v", err)
	}

	for who, want := range map[string]bool{owner: true, super: true, plain: false} {
		got, err := svc.IsAdmin(ctx, orgID, who)
		if err != nil {
			t.Fatalf("is admin: %v", err)
		}
		if got != want {
			t.Errorf("IsAdmin(%s) = %v, want %v", who, got, want)
		}
	}

	// A super admin adds people and suspends and reinstates them.
	extra, err := svc.AddPerson(ctx, orgID, super, "Ben", "extra@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("a super admin could not add a colleague: %v", err)
	}
	if err := svc.SetPersonStatus(ctx, orgID, super, plain, "suspended"); err != nil {
		t.Fatalf("a super admin could not suspend somebody: %v", err)
	}
	if err := svc.SetPersonStatus(ctx, orgID, super, plain, "active"); err != nil {
		t.Fatalf("a super admin could not reinstate somebody: %v", err)
	}

	// But never the owner.
	if err := svc.SetPersonStatus(ctx, orgID, super, owner, "suspended"); !errors.Is(err, auth.ErrIsOwner) {
		t.Fatalf("a super admin suspending the owner: got %v, want ErrIsOwner", err)
	}
	var status string
	_ = db.QueryRow(ctx, `SELECT status FROM principals WHERE id = $1`, owner).Scan(&status)
	if status != "active" {
		t.Fatalf("the owner is %q after a refused suspension", status)
	}

	// Nor themselves.
	if err := svc.SetPersonStatus(ctx, orgID, super, super, "suspended"); err == nil {
		t.Fatal("a super admin suspended their own account")
	}

	// And somebody who is neither gets nothing.
	if err := svc.SetPersonStatus(ctx, orgID, plain, extra, "suspended"); !errors.Is(err, auth.ErrNotOwner) {
		t.Fatalf("a plain user suspending somebody: got %v, want ErrNotOwner", err)
	}
	if _, err := svc.AddPerson(ctx, orgID, plain, "Mallory", "m@example.com", "a-long-enough-password"); !errors.Is(err, auth.ErrNotOwner) {
		t.Fatalf("a plain user adding somebody: got %v, want ErrNotOwner", err)
	}
}
