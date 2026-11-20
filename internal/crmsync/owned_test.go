package crmsync

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// opsSpy records what the sync tried to write.
type opsSpy struct {
	Ops
	sent []map[string]any
}

func (o *opsSpy) Upsert(_ context.Context, _, _ string, v map[string]any) (emberops.UpsertResult, error) {
	o.sent = append(o.sent, v)
	return emberops.UpsertResult{Name: "x"}, nil
}

func TestNothingAccountingOwnsCanBeSent(t *testing.T) {
	for dt, fields := range hearthOwned {
		for _, f := range EmberOpsOwned {
			if fields[f] {
				t.Errorf("%s lists %s as Hearth's, but it is accounting's", dt, f)
			}
		}
	}
	spy := &opsSpy{}
	s := &Syncer{ops: spy}
	for _, dt := range []string{"Customer", "Supplier", "Contact", "Lead", "Opportunity"} {
		for _, f := range EmberOpsOwned {
			_, err := s.send(context.Background(), dt, "id", map[string]any{f: "x", "hearth_id": "id"})
			if !errors.Is(err, ErrNotHearths) {
				t.Errorf("sending %s on %s was not refused: %v", f, dt, err)
			}
		}
	}
	if len(spy.sent) != 0 {
		t.Errorf("%d refused documents still reached EmberOps", len(spy.sent))
	}
	// An unknown doctype is refused, not waved through.
	if _, err := s.send(context.Background(), "Sales Invoice", "id", map[string]any{"hearth_id": "id"}); !errors.Is(err, ErrNotHearths) {
		t.Errorf("a doctype the sync has no list for was sent: %v", err)
	}
	// What Hearth owns goes through.
	if _, err := s.send(context.Background(), "Customer", "id", map[string]any{"customer_name": "A", "_tags": []string{"Type: Client"}}); err != nil {
		t.Errorf("a Hearth-owned document was refused: %v", err)
	}
}

func TestEveryDocumentTheMappingBuildsIsHearthsAlone(t *testing.T) {
	z := mapping.TZ{}
	docs := map[string]map[string]any{
		"Customer": z.CompanyDoc(mapping.Rec{"id": "1", "name": "A", "kind": "customer", "domain": "A.EXAMPLE", "notes": "n"}),
		"Supplier": z.CompanyDoc(mapping.Rec{"id": "2", "name": "B", "kind": "vendor", "domain": "b.example", "notes": "n"}),
		"Contact": z.ContactDoc(mapping.Rec{"id": "3", "name": "Dana Whitfield", "email": "d@x.example", "phone": "1",
			"title": "Eng", "notes": "n", "company_id": "1", "company_kind": "customer"}),
	}
	for dt, doc := range docs {
		if dt != "Contact" {
			doc["_tags"] = []string{"Type: Client"}
		}
		if dt == "Supplier" {
			doc["us_ein"] = "12-3456789"
		}
		if err := guard(dt, doc); err != nil {
			t.Errorf("%s: %v", dt, err)
		}
	}
}

func TestAnOverwriteIsReportedWithBothValues(t *testing.T) {
	s := &Syncer{}
	rep := &Report{}
	have := emberops.Row{"customer_name": "Old Name", "hearth_domain": "same.example", "hearth_notes": ""}
	doc := map[string]any{"customer_name": "New Name", "hearth_domain": "same.example", "hearth_notes": "fresh", "_tags": []string{"a"}}
	s.overwrites(rep, "customer", "id", "New Name", doc, have, true)
	if len(rep.Actions) != 1 {
		t.Fatalf("want exactly the one changed field reported, got %+v", rep.Actions)
	}
	a := rep.Actions[0]
	if a.Verb != "overwrite" || !strings.Contains(a.Detail, "customer_name") ||
		!strings.Contains(a.Detail, "Old Name") || !strings.Contains(a.Detail, "New Name") {
		t.Errorf("the report line is %+v", a)
	}
	// An empty field in EmberOps is a fill, not an overwrite, and a dry run says "would".
	rep = &Report{}
	s.overwrites(rep, "customer", "id", "x", doc, have, false)
	if rep.Actions[0].Verb != "would overwrite" {
		t.Errorf("a dry run says %q", rep.Actions[0].Verb)
	}
	rep = &Report{}
	s.overwrites(rep, "customer", "id", "x", doc, nil, true)
	if len(rep.Actions) != 0 {
		t.Errorf("a new record reported overwrites: %+v", rep.Actions)
	}
}

func TestTheStageMap(t *testing.T) {
	for stage, want := range map[string][2]string{
		"new": {"Open", "Prospecting"}, "screening": {"Open", "Qualification"}, "meeting": {"Open", "Needs Analysis"},
		"proposal": {"Open", "Proposal/Price Quote"}, "customer": {"", ""}, "won": {"", ""},
		"lost": {"Lost", ""}, "no_response": {"Closed", ""},
	} {
		st, ss := stageOf(stage)
		if st != want[0] || ss != want[1] {
			t.Errorf("%s -> %s, %s; want %v", stage, st, ss, want)
		}
	}
}

// EmberOps sets Converted from an Opportunity's Quotations and answers 417 to anyone else, so Hearth never sends it.
func TestAWonDealNeverSendsAStatusEmberOpsOwns(t *testing.T) {
	for _, stage := range []string{"new", "screening", "meeting", "proposal", "customer", "won", "lost", "no_response", "x"} {
		st, _ := stageOf(stage)
		if opsStatuses[st] {
			t.Errorf("stage %s sends %s, which EmberOps derives", stage, st)
		}
	}
	for _, c := range []struct {
		want string
		have emberops.Row
		out  string
	}{
		{"Open", emberops.Row{"status": "Open"}, "Open"},
		{"Open", emberops.Row{"status": "Converted"}, ""},
		{"Open", emberops.Row{"status": "Quotation"}, ""},
		{"Lost", emberops.Row{"status": "Converted"}, ""},
		{"Lost", emberops.Row{"status": "Open"}, "Lost"},
		{"", emberops.Row{"status": "Open"}, ""},
		{"Converted", emberops.Row{"status": "Open"}, ""},
	} {
		if got := settableStatus(c.want, c.have); got != c.out {
			t.Errorf("settableStatus(%q, %v) = %q, want %q", c.want, c.have["status"], got, c.out)
		}
	}
}

func TestSplitNameIsEmberOpsOwn(t *testing.T) {
	for in, want := range map[string][3]string{
		"ANDRITZ": {"ANDRITZ", "", ""}, "Paul Fish": {"Paul", "", "Fish"},
		"Austin Powder Company": {"Austin", "Powder", "Company"}, "  Jim   Mooney ": {"Jim", "", "Mooney"},
		"a b c d": {"a", "b c", "d"}, "": {"", "", ""},
	} {
		f, m, l := splitName(in)
		if [3]string{f, m, l} != want {
			t.Errorf("splitName(%q) = %q %q %q, want %v", in, f, m, l, want)
		}
	}
}

// ERPNext rebuilds a Lead's lead_name from its name parts on every save, so a stored Lead looks like this: the name
// Hearth last sent comes back as the parts, and lead_name is whatever they join to. Before, the sync sent lead_name
// alone, the parts kept the old name, and 176 Leads were "different" and rewritten every hour.
func TestAnUnchangedLeadReadsSame(t *testing.T) {
	co := company{Name: "ATS Corporation"}
	stored := func(doc map[string]any) emberops.Row {
		row := emberops.Row{}
		for k, v := range doc {
			row[k] = v
		}
		row["lead_name"] = strings.TrimSpace(strings.Join([]string{doc["first_name"].(string), doc["middle_name"].(string), doc["last_name"].(string)}, " "))
		row["lead_name"] = strings.Join(strings.Fields(row["lead_name"].(string)), " ")
		return row
	}
	for _, who := range []string{"Jim Mooney", "ANDRITZ", "Austin Powder Company", "support@apollo.io"} {
		made := leadDoc(who, co, false)
		if made["lead_name"] != who {
			t.Errorf("a new Lead for %q is sent lead_name %v", who, made["lead_name"])
		}
		row := stored(made)
		again := leadDoc(who, co, true)
		if _, ok := again["lead_name"]; ok {
			t.Errorf("an existing Lead is sent lead_name: %v", again)
		}
		if !sameDoc(again, row, nil) {
			t.Errorf("an unchanged Lead for %q is not same: sent %v, stored %v", who, again, row)
		}
	}
	// The Lead EmberOps has under the old company name, and a first contact that has since changed, is one update.
	old := emberops.Row{"first_name": "Steve", "middle_name": nil, "last_name": "Wardell", "lead_name": "Steve Wardell", "company_name": "ATS Corporation"}
	if sameDoc(leadDoc("Jim Mooney", co, true), old, nil) {
		t.Error("a Lead whose first contact changed read as same")
	}
}
