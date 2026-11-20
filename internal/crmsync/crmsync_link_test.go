package crmsync_test

// What the sync does with records EmberOps already had, and with a company that has no name.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/crmsync"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func TestTheSyncLinksWhatEmberOpsAlreadyHasAndSkipsTheUnnamed(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatal(err)
	}
	org, who := uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Link Test')`, org, "cl-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Sync')`, who, org)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	exec(`INSERT INTO company_types (id, org_id, key, label, erp_role, position) VALUES (gen_random_uuid(), $1, 'client', 'Client', 'customer', 1)`, org)
	mk := func(name, domain string) string {
		id := uuid.NewString()
		exec(`INSERT INTO companies (id, org_id, name, domain, kind, created_by) VALUES ($1,$2,$3,$4,'',$5)`, id, org, name, domain, who)
		exec(`INSERT INTO company_type_links SELECT $1, id FROM company_types WHERE org_id=$2 AND key='client'`, id, org)
		return id
	}
	mine := mk("Erin Already Has Me", "erin.example")
	exec(`UPDATE companies SET notes = 'hearth note' WHERE id = $1`, mine)
	exec(`INSERT INTO company_tags VALUES ($1, 'dayton')`, mine)
	fresh := mk("Fresh Co", "fresh.example")
	taken := mk("Owned Elsewhere", "other.example")
	nameless := mk("(unnamed 41643296275)", "")
	exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES
		(gen_random_uuid(), $1, $2, 'Known Person', 'known@erin.example', $3),
		(gen_random_uuid(), $1, $4, 'Nobody', 'nobody@x.example', $3)`, org, mine, who, nameless)

	f := fake.New(map[string]string{"svc": "svc@example.com"})
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	ops := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})
	f.Put("Customer", map[string]any{"name": "Erin Already Has Me", "customer_name": "Erin Already Has Me Inc", "territory": "Dayton",
		"hearth_notes": "Erin's own note", "_tags": []any{"erin-tag"}})
	f.Put("Customer", map[string]any{"name": "Owned Elsewhere", "customer_name": "Owned Elsewhere", "hearth_id": uuid.NewString()})
	f.Put("Contact", map[string]any{"name": "Known Person-Erin", "first_name": "Known", "email_id": "KNOWN@erin.example"})
	s := crmsync.New(pools.Web, ops, org)

	rep, err := s.Run(ctx, crmsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count("customer", "would link") != 1 || rep.Count("customer", "would create") != 1 || rep.Count("contact", "would link") != 1 ||
		rep.Count("customer", "conflict") != 1 || rep.Count("company", "skipped") != 1 {
		t.Fatalf("dry run: %+v", rep.Actions)
	}
	if n := len(f.All("Customer")); n != 2 {
		t.Errorf("the dry run changed the number of Customers to %d", n)
	}

	rep, err = s.Run(ctx, crmsync.Options{Write: true})
	if err != nil || rep.Failed() {
		t.Fatalf("write: %v %+v", err, rep.Actions)
	}
	if n := len(f.All("Customer")); n != 3 {
		t.Errorf("linking made %d Customers, want 3 (two that were there and Fresh Co)", n)
	}
	for _, c := range f.All("Customer") {
		if c["name"] == "Erin Already Has Me" {
			if c["hearth_id"] != mine {
				t.Errorf("the existing Customer was not linked: %v", c["hearth_id"])
			}
			if c["territory"] != "Dayton" {
				t.Errorf("a field EmberOps owns changed: %v", c["territory"])
			}
		}
		if c["name"] == "Owned Elsewhere" && c["hearth_id"] == taken {
			t.Errorf("a record that belongs to another Hearth id was claimed")
		}
	}
	for _, c := range f.All("Contact") {
		if c["email_id"] == "KNOWN@erin.example" && c["hearth_id"] == "" {
			t.Errorf("the existing Contact was not linked")
		}
	}
	if n := len(f.All("Contact")); n != 1 {
		t.Errorf("linking made %d Contacts, want 1", n)
	}
	var out bytes.Buffer
	rep.Print(&out)
	if !strings.Contains(out.String(), "unnamed") {
		t.Errorf("the report does not flag the unnamed company:\n%s", out.String())
	}

	// A second run changes nothing EmberOps owns, then a Hearth edit still does not.
	custOf := func(name string) map[string]any {
		for _, c := range f.All("Customer") {
			if c["name"] == name {
				return c
			}
		}
		return nil
	}
	rep, _ = s.Run(ctx, crmsync.Options{Write: true})
	if rep.Failed() {
		t.Fatalf("second run: %+v", rep.Actions)
	}
	exec(`UPDATE companies SET name = 'Erin Already Has Me, LLC', notes = 'hearth note, edited' WHERE id = $1`, mine)
	exec(`INSERT INTO company_tags VALUES ($1, 'rewind')`, mine)
	exec(`UPDATE companies SET name = 'Fresh Co Renamed' WHERE id = $1`, fresh)
	out.Reset()
	rep, _ = s.Run(ctx, crmsync.Options{})
	rep.Print(&out)
	for _, want := range []string{"would fill", "difference", "customer_name: EmberOps keeps \"Erin Already Has Me Inc\"", "hearth_notes: EmberOps keeps \"Erin's own note\""} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run does not show %q:\n%s", want, out.String())
		}
	}
	rep, _ = s.Run(ctx, crmsync.Options{Write: true})
	if rep.Failed() {
		t.Fatalf("third run: %+v", rep.Actions)
	}
	erin := custOf("Erin Already Has Me")
	if erin["customer_name"] != "Erin Already Has Me Inc" || erin["hearth_notes"] != "Erin's own note" || erin["territory"] != "Dayton" {
		t.Errorf("a value EmberOps had was overwritten: %v", erin)
	}
	tags, _ := erin["_tags"].([]any)
	got := map[string]bool{}
	for _, tg := range tags {
		got[fmt.Sprint(tg)] = true
	}
	if !got["erin-tag"] || !got["Type: Client"] || !got["dayton"] || !got["rewind"] {
		t.Errorf("tags are not a union: %v", tags)
	}
	if custOf("Erin Already Has Me, LLC") != nil {
		t.Errorf("the EmberOps record was renamed")
	}
	if d := erin["hearth_domain"]; d != "erin.example" {
		t.Errorf("an empty field was not filled: %v", d)
	}
	// The record the sync created follows Hearth.
	var f2 map[string]any
	for _, c := range f.All("Customer") {
		if c["hearth_id"] == fresh {
			f2 = c
		}
	}
	if f2 == nil || f2["customer_name"] != "Fresh Co Renamed" {
		t.Errorf("a record the sync made did not follow Hearth: %v", f2)
	}
}
