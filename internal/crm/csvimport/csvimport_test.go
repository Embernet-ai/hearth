package csvimport

import (
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/crm"
)

func TestHeadersAreMatchedByNameInAnyOrder(t *testing.T) {
	s, err := Parse(Companies, strings.NewReader("\xef\xbb\xbfWebsite,Company Name,Industry,Notes\nbaldor.com,Baldor Electric,Motors,good\n,,,\nacme.io,Acme,,\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 2 {
		t.Fatalf("a blank line is not a record, got %d rows", len(s.Rows))
	}
	if s.Rows[0].Fields["name"] != "Baldor Electric" || s.Rows[0].Fields["domain"] != "baldor.com" || s.Rows[0].Fields["notes"] != "good" {
		t.Fatalf("row 0 = %v", s.Rows[0].Fields)
	}
	if len(s.Ignored) != 1 || s.Ignored[0] != "Industry" {
		t.Fatalf("a header that means nothing is listed back, got %v", s.Ignored)
	}
	if s.Rows[1].Line != 4 {
		t.Fatalf("line numbers are the file's, got %d", s.Rows[1].Line)
	}
}

func TestSemicolonAndTabFilesRead(t *testing.T) {
	for _, sep := range []string{";", "\t"} {
		s, err := Parse(Contacts, strings.NewReader("First Name"+sep+"Last Name"+sep+"E-mail\nAda"+sep+"Lovelace"+sep+"ada@x.io\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Rows[0].Fields; got["name"] != "Ada Lovelace" || got["email"] != "ada@x.io" {
			t.Fatalf("sep %q: %v", sep, got)
		}
	}
}

func TestARowThatCannotBeUsedSaysWhy(t *testing.T) {
	s, _ := Parse(Contacts, strings.NewReader("Name,Email,Phone\n,,555\nBo,,\n"))
	if s.Rows[0].Err == "" || s.Rows[1].Err != "" {
		t.Fatalf("a contact needs a name or an email: %+v", s.Rows)
	}
	s, _ = Parse(Deals, strings.NewReader("Deal,Amount\n,5\n"))
	if s.Rows[0].Err != "needs a name" {
		t.Fatalf("got %q", s.Rows[0].Err)
	}
}

func TestNothingRecognisedAndEmptyFilesAreErrors(t *testing.T) {
	if _, err := Parse(Companies, strings.NewReader("foo,bar\n1,2\n")); err != ErrNoColumns {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse(Companies, strings.NewReader("")); err != ErrNoHeader {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse("widgets", strings.NewReader("a\n")); err == nil {
		t.Fatal("an unknown kind must fail")
	}
}

func TestOneColumnIsNotTakenTwice(t *testing.T) {
	// "Company" means a contact's company, and must not also be taken as the contact's name
	s, _ := Parse(Contacts, strings.NewReader("Company,Email\nAcme,a@acme.io\n"))
	if s.Rows[0].Fields["company"] != "Acme" || s.Rows[0].Fields["name"] != "" {
		t.Fatalf("%v", s.Rows[0].Fields)
	}
}

func TestTheFileIsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("Name\n")
	for i := 0; i < MaxRows+10; i++ {
		b.WriteString("Co\n")
	}
	s, _ := Parse(Companies, strings.NewReader(b.String()))
	if len(s.Rows) != MaxRows || !s.Truncated {
		t.Fatalf("rows %d truncated %v", len(s.Rows), s.Truncated)
	}
}

func TestStage(t *testing.T) {
	for in, want := range map[string]string{"": crm.StageNew, "Proposal": crm.StageProposal, "no response": crm.StageNoResponse,
		"No-Response": crm.StageNoResponse, "won": crm.StageWon} {
		got, err := Stage(in)
		if err != nil || got != want {
			t.Errorf("Stage(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Stage("negotiation"); err == nil || !strings.Contains(err.Error(), "Proposal") {
		t.Errorf("an unknown stage names the real ones: %v", err)
	}
}

func TestCents(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "1500": 150000, "1,500.50": 150050, "$1,500": 150000, "1.500,50": 150050, "0.99": 99} {
		got, err := Cents(in)
		if err != nil || got != want {
			t.Errorf("Cents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"lots", "-5", "1.2.3"} {
		if _, err := Cents(bad); err == nil {
			t.Errorf("Cents(%q) should fail", bad)
		}
	}
}

func TestDate(t *testing.T) {
	for _, in := range []string{"2026-11-17", "11/17/2026", "17 Nov 2026", "Nov 17, 2026", "2026-11-17T09:00:00Z"} {
		d, err := Date(in)
		if err != nil || d == nil || d.Format("2006-01-02") != "2026-11-17" {
			t.Errorf("Date(%q) = %v, %v", in, d, err)
		}
	}
	if d, err := Date(""); d != nil || err != nil {
		t.Error("empty is no date")
	}
	if _, err := Date("soon"); err == nil {
		t.Error("soon is not a date")
	}
}

func TestCompanyKindFallsBackToOther(t *testing.T) {
	if CompanyKind("Vendor") != crm.KindVendor || CompanyKind("Reseller") != crm.KindOther {
		t.Fatal("kinds")
	}
}
