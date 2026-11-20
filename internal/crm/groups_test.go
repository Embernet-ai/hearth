package crm

import "testing"

// A company with no kind (the ones brought in from another system have none) falls under a heading that says so, never a blank one.
func TestAnEmptyKindHasAHeadingThatSaysSo(t *testing.T) {
	if got := CompanyGroup(&CompanyRow{}, "kind"); got != "No kind" {
		t.Fatalf("heading %q", got)
	}
	if got := CompanyGroup(&CompanyRow{Company: Company{Kind: "vendor"}}, "kind"); got != "vendor" {
		t.Fatalf("heading %q", got)
	}
	if got := ContactGroup(&ContactRow{}, "company"); got != "No company" {
		t.Fatalf("heading %q", got)
	}
	if got := DealGroup(&Deal{}, "owner"); got != "Nobody" {
		t.Fatalf("heading %q", got)
	}
	// and the totals query names the same heading the rows fall under
	for _, g := range groupsFor("companies") {
		if g.Key == "kind" && g.name != "coalesce(nullif(c.kind,''),'No kind')" {
			t.Fatalf("totals name the heading %q", g.name)
		}
	}
}
