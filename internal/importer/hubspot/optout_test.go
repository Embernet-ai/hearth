package hubspot

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
)

// HubSpot's do-not-email standing is read from three contact properties, and the importer has to ask for them by name.
func TestTheImportAsksHubSpotForTheDoNotEmailProperties(t *testing.T) {
	have := map[string]bool{}
	for _, p := range props[Contacts] {
		have[p] = true
	}
	for _, want := range []string{"hs_email_optout", "hs_email_hard_bounce_reason_enum", "hs_email_bad_address"} {
		if !have[want] {
			t.Errorf("contacts are read without %s, so a HubSpot unsubscribe or bounce could never reach Hearth", want)
		}
	}
}

func TestOptOutReasonFromHubSpotProperties(t *testing.T) {
	for _, c := range []struct {
		name  string
		props map[string]string
		want  string
	}{
		{"nothing", map[string]string{"email": "a@example.com"}, ""},
		{"unsubscribed", map[string]string{"hs_email_optout": "true"}, "unsubscribed"},
		{"false is not an unsubscribe", map[string]string{"hs_email_optout": "false"}, ""},
		{"hard bounce", map[string]string{"hs_email_hard_bounce_reason_enum": "INVALID_EMAIL_ADDRESS"}, "bounced"},
		{"bad address", map[string]string{"hs_email_bad_address": "true"}, "bounced"},
		{"the person's own choice wins over a bounce", map[string]string{"hs_email_optout": "true", "hs_email_bad_address": "true"}, "unsubscribed"},
	} {
		if got := optOutOf(c.props); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHubSpotOptOutsAndBouncesBecomeHearthOptOuts(t *testing.T) {
	f := setupFx(t)
	ctx := context.Background()
	later := t0.Add(24 * time.Hour)
	h := f.hub
	h.add(Contacts, "6001", later, map[string]string{"firstname": "Una", "email": "una@example.com", "hs_email_optout": "true"})
	h.add(Contacts, "6002", later, map[string]string{"firstname": "Bo", "email": "bo@example.com", "hs_email_hard_bounce_reason_enum": "UNKNOWN_USER"})
	h.add(Contacts, "6003", later, map[string]string{"firstname": "Cleo", "email": "cleo@example.com", "hs_email_optout": "false"})
	h.add(Contacts, "6004", later, map[string]string{"firstname": "Already", "email": "already@example.com", "hs_email_optout": "true"})
	already, err := f.svc.CreateContact(ctx, f.org, f.actor, crm.Contact{Name: "Already", Email: "already@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE contacts SET marketing_opt_out_at = '2026-10-01T00:00:00Z', marketing_opt_out_reason='manual' WHERE id=$1`, already.ID)

	opt := Options{Baseline: t0}
	dry := f.run(false, opt)
	var out bytes.Buffer
	dry.Print(&out)
	if !strings.Contains(out.String(), "would opt out 1 unsubscribed and 1 bounced in Hearth, 1 already opted out") {
		t.Errorf("the dry run does not count the opt-outs:\n%s", out.String())
	}
	if n := f.count(`SELECT count(*) FROM contacts WHERE org_id=$1 AND marketing_opt_out_at IS NOT NULL`, f.org); n != 1 {
		t.Fatalf("a dry run opted out contacts: %d are opted out, want only the one that already was", n)
	}

	applied := f.run(true, opt)
	if applied.Failed() {
		t.Fatalf("failures: %v", applied.Problems)
	}
	reason := func(email string) string {
		return f.str(`SELECT CASE WHEN marketing_opt_out_at IS NULL THEN 'none' ELSE marketing_opt_out_reason END FROM contacts WHERE org_id=$1 AND email=$2`, f.org, email)
	}
	for email, want := range map[string]string{"una@example.com": "unsubscribed", "bo@example.com": "bounced", "cleo@example.com": "none", "already@example.com": "manual"} {
		if got := reason(email); got != want {
			t.Errorf("%s is %q in Hearth, want %q", email, got, want)
		}
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE org_id=$1 AND action='crm.hubspot_import.opt_out'`, f.org); n != 2 {
		t.Errorf("%d opt-out audit rows, want 2 (one per contact newly opted out)", n)
	}
	var again bytes.Buffer
	applied.Print(&again)
	if !strings.Contains(again.String(), "opted out 1 unsubscribed and 1 bounced in Hearth, 1 already opted out") {
		t.Errorf("the applied report does not count the opt-outs:\n%s", again.String())
	}
}
