package hubspot

import (
	"context"
	"errors"
	"strings"

	"github.com/Embernet-ai/hearth/internal/crm"
)

// The HubSpot contact properties that say a person must not be emailed. They are ordinary contact properties, so the
// crm.objects.contacts.read scope the import already needs reads them; no extra scope is involved.
const (
	// PropOptOut is "true" when the person unsubscribed from all email.
	PropOptOut = "hs_email_optout"
	// PropHardBounce holds the reason HubSpot gave when an address hard bounced, and is empty otherwise.
	PropHardBounce = "hs_email_hard_bounce_reason_enum"
	// PropBadAddress is "true" when HubSpot marked the address unusable.
	PropBadAddress = "hs_email_bad_address"
)

// OptOutCount is what the run did, or in a dry run would do, about HubSpot's do-not-email contacts.
type OptOutCount struct {
	Unsubscribed int // newly opted out in Hearth because HubSpot has them unsubscribed
	Bounced      int // newly opted out in Hearth because HubSpot has the address hard bounced or bad
	Already      int // HubSpot says do not email and Hearth already holds the opt-out
}

// Total is every contact HubSpot flags.
func (c OptOutCount) Total() int { return c.Unsubscribed + c.Bounced + c.Already }

// optOutOf is why HubSpot says this person is not to be emailed: "unsubscribed" (the person's own choice, which wins
// when both hold), "bounced", or "" when nothing says so.
func optOutOf(p map[string]string) string {
	if strings.EqualFold(strings.TrimSpace(p[PropOptOut]), "true") {
		return "unsubscribed"
	}
	if strings.TrimSpace(p[PropHardBounce]) != "" || strings.EqualFold(strings.TrimSpace(p[PropBadAddress]), "true") {
		return "bounced"
	}
	return ""
}

// carryOptOut makes HubSpot's do-not-email standing Hearth's own opted-out flag on the contact this HubSpot contact
// is, with an audit row. It never clears an opt-out (nothing here can), and it counts what it would do in a dry run.
func (im *Importer) carryOptOut(ctx context.Context, o Object) {
	reason := optOutOf(o.Props)
	if reason == "" {
		return
	}
	id := im.contactID(o.ID)
	if id == "" {
		return
	}
	oc := &im.rep.OptOuts
	fresh := func() {
		if reason == "unsubscribed" {
			oc.Unsubscribed++
		} else {
			oc.Bounced++
		}
	}
	if strings.HasPrefix(id, "dry-run:") {
		fresh()
		return
	}
	d, err := im.svc.ContactDetails(ctx, im.org, im.actor, id)
	if errors.Is(err, crm.ErrNotFound) {
		return // deleted in Hearth, left deleted
	}
	if err != nil {
		im.rep.problem("contact %s: read opt-out: %v", o.ID, err)
		return
	}
	if d.MarketingOptOutAt != nil {
		oc.Already++
		return
	}
	if !im.opt.Apply {
		fresh()
		return
	}
	if set, err := im.svc.SetContactOptOut(ctx, im.org, im.actor, id, reason); err != nil {
		im.rep.problem("contact %s: opt out: %v", o.ID, err)
		return
	} else if !set {
		oc.Already++
		return
	}
	if err := im.audit(ctx, "crm.hubspot_import.opt_out", "contact", id, "contacts", o.ID,
		map[string]string{"marketing_opt_out": ""}, map[string]string{"marketing_opt_out": reason}); err != nil {
		im.rep.problem("contact %s: %v", o.ID, err)
		return
	}
	fresh()
}
