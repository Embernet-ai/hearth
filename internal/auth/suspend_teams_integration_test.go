package auth_test

// Suspending an account takes it off its teams, in the same step.
//
// A team is a grant subject, so somebody suspended who stays in one still holds
// everything the team was granted, and anything that reads grants without going
// through sign-in (a token, a report, a list of who can see a record) goes on
// counting them. The maintainer, 2026-10-31: suspended accounts come off teams when
// suspended.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
)

func TestSuspendingAnAccountTakesItOffItsTeams(t *testing.T) {
	db := freshInstall(t)
	ctx := context.Background()
	svc := auth.New(db, authz.New(db), false)

	owner, err := svc.Setup(ctx, "Fireball", "Pat", "owner@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	var orgID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(context.Background(), `SELECT audit_purge_org($1)`, orgID) })

	ana, _ := svc.AddPerson(ctx, orgID, owner, "Ana", "ana@example.com", "a-long-enough-password")
	ben, _ := svc.AddPerson(ctx, orgID, owner, "Ben", "ben@example.com", "a-long-enough-password")
	cy, _ := svc.AddPerson(ctx, orgID, owner, "Cy", "cy@example.com", "a-long-enough-password")

	sales, ops := uuid.NewString(), uuid.NewString()
	for id, name := range map[string]string{sales: "Sales", ops: "Operations"} {
		if _, err := db.Exec(ctx, `INSERT INTO teams (id, org_id, name) VALUES ($1,$2,$3)`, id, orgID, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, who := range []string{ana, ben, cy} {
		for _, team := range []string{sales, ops} {
			if _, err := db.Exec(ctx, `INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2)`, team, who); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func(who string) int {
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE principal_id = $1`, who).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The by-hand path: an admin suspends Ana.
	if err := svc.SetPersonStatus(ctx, orgID, owner, ana, "suspended"); err != nil {
		t.Fatal(err)
	}
	if n := count(ana); n != 0 {
		t.Errorf("a suspended account is still in %d teams after SetPersonStatus", n)
	}
	// Other people keep theirs.
	if n := count(ben); n != 2 {
		t.Errorf("suspending Ana changed Ben's teams: %d", n)
	}

	// The directory path: the identity provider says Ben is inactive.
	if err := svc.SetProvisionedStatus(ctx, orgID, ben, "suspended"); err != nil {
		t.Fatal(err)
	}
	if n := count(ben); n != 0 {
		t.Errorf("a suspended account is still in %d teams after SetProvisionedStatus", n)
	}

	// What was removed is on the record, so somebody can put it back on purpose.
	var after []byte
	if err := db.QueryRow(ctx, `SELECT after FROM audit_log WHERE entity_id = $1 AND action = 'person.status'
		ORDER BY at DESC LIMIT 1`, ana).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Teams []string `json:"removed_from_teams"`
	}
	_ = json.Unmarshal(after, &rec)
	if len(rec.Teams) != 2 {
		t.Errorf("the audit entry names %v, want both team names", rec.Teams)
	}

	// Reinstating restores nothing. It was an admin's removal, so putting
	// somebody back is an admin's act too, not a side effect of a status flip.
	if err := svc.SetPersonStatus(ctx, orgID, owner, ana, "active"); err != nil {
		t.Fatal(err)
	}
	if n := count(ana); n != 0 {
		t.Errorf("reinstating Ana put her back in %d teams on its own", n)
	}

	// Suspending somebody in no team is fine, and an active account is untouched.
	if err := svc.SetPersonStatus(ctx, orgID, owner, ana, "suspended"); err != nil {
		t.Errorf("suspending somebody with no teams: %v", err)
	}
	if n := count(cy); n != 2 {
		t.Errorf("Cy, never suspended, has %d teams", n)
	}
}
