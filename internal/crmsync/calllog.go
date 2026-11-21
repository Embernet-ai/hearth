package crmsync

// The phone's calls as Call Logs, one way.
//
// The phone system (asterisk-pod's push service) writes every external call into Hearth's CRM as a Call activity on the
// contact and company, with external_refs source "phone" and the call's own id. EmberOps gets each of those as a Call Log,
// ERPNext's own telephony document, keyed on the activity id, linked to the Contact and the Customer, Supplier or Lead the
// call was with. Hearth owns the text. A call logged by a person on the timeline (no "phone" source) stays a Comment only.
//
// The phone writes the number into the activity body as a first line "Number: +12165550142", and that is where the
// Call Log's from and to come from.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// PhoneSource is the external_refs source the phone system writes on every call it logs.
const PhoneSource = "phone"

type phoneCall struct {
	ID, Title, Body, Direction, Disposition string
	At                                      time.Time
	DurationMS                              int64
	Targets                                 []target
}

type target struct{ Type, ID string }

func (s *Syncer) phoneCalls(ctx context.Context) ([]phoneCall, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.title, a.body, a.occurred_at, coalesce(a.direction, ''), a.disposition, coalesce(a.duration_ms, 0),
		       t.target_type, t.target_id::text
		FROM crm_activities a
		JOIN external_refs r ON r.org_id = a.org_id AND r.source = $2 AND r.entity_type = 'activity' AND r.entity_id = a.id
		LEFT JOIN crm_activity_targets t ON t.activity_id = a.id
		WHERE a.org_id = $1 AND a.deleted_at IS NULL AND a.kind = 'call'
		ORDER BY a.occurred_at, a.id, t.target_type, t.target_id`, s.org, PhoneSource)
	if err != nil {
		return nil, fmt.Errorf("crmsync: read the phone's calls: %w", err)
	}
	defer rows.Close()
	var out []phoneCall
	for rows.Next() {
		var c phoneCall
		var tt, tid *string
		if err := rows.Scan(&c.ID, &c.Title, &c.Body, &c.At, &c.Direction, &c.Disposition, &c.DurationMS, &tt, &tid); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == c.ID {
			if tt != nil && tid != nil {
				out[n-1].Targets = append(out[n-1].Targets, target{*tt, *tid})
			}
			continue
		}
		if tt != nil && tid != nil {
			c.Targets = []target{{*tt, *tid}}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// callNumber is the number on the first line of a phone call's body, and the rest of the body without that line.
func callNumber(body string) (number, rest string) {
	first, after, _ := strings.Cut(body, "\n")
	if n, ok := strings.CutPrefix(strings.TrimSpace(first), "Number:"); ok {
		return strings.TrimSpace(n), strings.TrimSpace(after)
	}
	return "", strings.TrimSpace(body)
}

// callStatus is ERPNext's Call Log status for what the phone recorded.
func callStatus(disposition string) string {
	switch strings.ToLower(strings.TrimSpace(disposition)) {
	case "answered":
		return "Completed"
	case "busy":
		return "Busy"
	case "failed":
		return "Failed"
	}
	return "No Answer"
}

// callLogDoc is the Call Log a phone call becomes. zone is the site's time zone: a Datetime in Frappe has none, so it is
// written in the one the site is set to.
func callLogDoc(c phoneCall, zone *time.Location, links []map[string]any, customer string) map[string]any {
	number, rest := callNumber(c.Body)
	typ, from, to := "Incoming", number, ""
	if c.Direction == "outbound" {
		typ, from, to = "Outgoing", "", number
	}
	secs := int((c.DurationMS + 500) / 1000)
	start := c.At.In(zone)
	summary := strings.TrimSpace(c.Title)
	if rest != "" {
		summary += "\n" + rest
	}
	doc := map[string]any{
		"id": "hearth-" + c.ID, "type": typ, "status": callStatus(c.Disposition), "from": from, "to": to,
		"start_time": start.Format("2006-01-02 15:04:05"), "end_time": start.Add(time.Duration(secs) * time.Second).Format("2006-01-02 15:04:05"),
		"duration": secs, "medium": "Fireball Phone", "summary": summary, "links": links,
	}
	if customer != "" {
		doc["customer"] = customer
	}
	return doc
}

func (s *Syncer) callLogs(ctx context.Context, rep *Report, write bool) {
	calls, err := s.phoneCalls(ctx)
	if err != nil {
		s.add(rep, Action{"calllog", "", "phone calls", "failed", err.Error()})
		return
	}
	if len(calls) == 0 {
		return
	}
	existing, err := s.ops.ListAll(ctx, emberops.ListQuery{Doctype: "Call Log",
		Fields:  []string{"name", "hearth_id", "type", "status", "from", "to", "duration", "summary", "medium", "start_time"},
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}}, readAll)
	if err != nil {
		s.add(rep, Action{"calllog", "", "timeline", "failed", "read the Call Logs EmberOps has: " + err.Error()})
		return
	}
	have := make(map[string]emberops.Row, len(existing))
	for _, r := range existing {
		if h, _ := r["hearth_id"].(string); h != "" {
			have[h] = r
		}
	}
	zone := s.zone
	if zone == nil {
		zone = time.UTC
	}
	for _, c := range calls {
		var links []map[string]any
		customer := ""
		for _, t := range c.Targets {
			dt, name := s.recordFor(ctx, t.Type, t.ID)
			if dt == "" {
				continue
			}
			links = append(links, map[string]any{"link_doctype": dt, "link_name": name})
			if dt == "Customer" && customer == "" {
				customer = name
			}
		}
		doc := callLogDoc(c, zone, links, customer)
		label := c.Title
		row, ok := have[c.ID]
		verb := "create"
		if ok {
			verb = "update"
			cmp := map[string]any{}
			for k, v := range doc {
				if k != "links" && k != "start_time" && k != "end_time" && k != "id" && k != "customer" {
					cmp[k] = v
				}
			}
			if sameDoc(cmp, row, func(string, string) string { return "" }) {
				s.add(rep, Action{"calllog", c.ID, label, "same", ""})
				continue
			}
		}
		if !write {
			s.add(rep, Action{"calllog", c.ID, label, "would " + verb, ""})
			continue
		}
		if _, err := s.send(ctx, "Call Log", c.ID, doc); err != nil {
			s.add(rep, Action{"calllog", c.ID, label, "failed", "EmberOps refused the Call Log, so the pass stopped: " + err.Error()})
			return
		}
		s.add(rep, Action{"calllog", c.ID, label, verb, ""})
	}
}
