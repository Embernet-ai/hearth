package hubspot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

// activities brings notes and calls onto the timeline. Hearth's timeline is append only through its service, so an
// activity already there is left as it is, even if HubSpot has since changed its text.
func (im *Importer) activities(ctx context.Context, object string, objs []Object, cnt *Count) error {
	var fresh []Object
	for _, o := range objs {
		if _, _, ok := im.lookup(object, o.ID); ok {
			cnt.Same++
			continue
		}
		fresh = append(fresh, o)
	}
	if len(fresh) == 0 {
		return nil
	}
	for _, to := range []string{Contacts, Companies, Deals} {
		if err := im.hs.Associations(ctx, object, to, fresh); err != nil {
			return fmt.Errorf("read %s associations: %w", object, err)
		}
	}
	for _, o := range fresh {
		if err := ctx.Err(); err != nil {
			return err
		}
		im.addActivity(ctx, object, o, cnt)
	}
	return nil
}

func (im *Importer) addActivity(ctx context.Context, object string, o Object, cnt *Count) {
	var targets []core.Ref
	seen := map[string]bool{}
	add := func(t core.EntityType, id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			targets = append(targets, core.Ref{Type: t, ID: id})
		}
	}
	for _, h := range o.Assoc[Contacts] {
		add(core.EntityContact, im.contactID(h))
	}
	for _, h := range o.Assoc[Companies] {
		add(core.EntityCompany, im.companyID(h))
	}
	for _, h := range o.Assoc[Deals] {
		if r, _, ok := im.lookup(Deals, h); ok {
			add(core.EntityDeal, r.id)
		}
	}
	if len(targets) == 0 {
		cnt.skip("about no company, contact or deal that Hearth has")
		return
	}
	a := crm.Activity{OccurredAt: atTime(o.Props["hs_timestamp"]), Targets: targets}
	switch object {
	case Notes:
		body := plain(o.Props["hs_note_body"])
		a.Kind, a.Body, a.Title = crm.ActNote, body, firstLine(body, 80)
		if a.Title == "" {
			a.Title = "Note"
		}
		if body == "" {
			cnt.skip("an empty note")
			return
		}
	case Calls:
		a.Kind = crm.ActCall
		a.Body = plain(o.Props["hs_call_body"])
		a.Title = strings.TrimSpace(o.Props["hs_call_title"])
		if a.Title == "" {
			a.Title = firstLine(a.Body, 60)
		}
		if a.Title == "" {
			a.Title = "Call"
		}
		switch strings.ToUpper(strings.TrimSpace(o.Props["hs_call_direction"])) {
		case "INBOUND":
			a.Direction = crm.Inbound
		case "OUTBOUND":
			a.Direction = crm.Outbound
		}
		if d := strings.TrimSpace(o.Props["hs_call_disposition"]); d != "" {
			a.Disposition = d
			if l, ok := im.calls[d]; ok {
				a.Disposition = l
			}
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(o.Props["hs_call_duration"]), 10, 64); err == nil && n > 0 {
			a.DurationMs = &n
		}
	}
	id := im.fakeID(object, o.ID)
	if im.opt.Apply {
		made, err := im.svc.AddActivity(ctx, im.org, im.actor, a)
		if err != nil {
			cnt.Failed++
			im.rep.problem("%s %s: %v", object, o.ID, err)
			return
		}
		id = made.ID
		if err := im.audit(ctx, "crm.hubspot_import.create", "activity", id, object, o.ID, nil,
			map[string]any{"kind": a.Kind, "title": a.Title, "targets": len(targets)}); err != nil {
			cnt.Failed++
			im.rep.problem("%s %s: %v", object, o.ID, err)
			return
		}
	}
	cnt.Create++
	if err := im.record(ctx, object, o.ID, "activity", id, nil, o.Modified); err != nil {
		cnt.Failed++
		im.rep.problem("%s %s: %v", object, o.ID, err)
	}
}
