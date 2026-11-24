package crmops_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crmops"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
)

func newOps(t *testing.T) (*crmops.Service, *fake.Frappe) {
	t.Helper()
	f := fake.New(map[string]string{"svc": "hearth-service"})
	t.Cleanup(f.Close)
	f.Put("Quotation", map[string]any{"name": "SAL-QTN-1", "quotation_to": "Customer", "party_name": "CUST-1", "customer_name": "Acme", "status": "Open", "currency": "USD", "grand_total": 1250.5, "transaction_date": "2026-11-10", "valid_till": "2026-12-10", "opportunity": "CRM-OPP-1"})
	f.Put("Quotation", map[string]any{"name": "SAL-QTN-2", "quotation_to": "Customer", "party_name": "CUST-2", "customer_name": "Other", "status": "Draft", "currency": "USD", "grand_total": 99, "transaction_date": "2026-11-11"})
	f.Put("Quotation", map[string]any{"name": "SAL-QTN-3", "quotation_to": "Lead", "party_name": "LEAD-1", "customer_name": "Acme", "status": "Open", "currency": "EUR", "grand_total": 10, "transaction_date": "2026-11-12", "opportunity": "CRM-OPP-1"})
	f.Put("Call Log", map[string]any{"name": "CL-1", "type": "Incoming", "status": "Completed", "from": "+1 (216) 555-0142", "to": "100", "start_time": "2026-11-20 10:00:00", "duration": 125, "customer": "CUST-1"})
	f.Put("Call Log", map[string]any{"name": "CL-2", "type": "Outgoing", "status": "Missed", "from": "100", "to": "3105550000", "start_time": "2026-11-21 10:00:00", "duration": 0})
	u, _ := url.Parse(f.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc")})
	return crmops.New(c), f
}

func TestAnOpportunityShowsItsQuotesAndALinkToMakeOne(t *testing.T) {
	s, _ := newOps(t)
	p, err := s.For(context.Background(), []crm.EmberOpsLink{{Label: "Opportunity", Name: "CRM-OPP-1"}}, nil)
	if err != nil || len(p.Quotes) != 2 {
		t.Fatalf("quotes = %+v (%v), want the two on the opportunity", p, err)
	}
	if p.Quotes[0].Name != "SAL-QTN-3" || p.Quotes[1].Total != 1250.5 || !strings.HasSuffix(p.Quotes[1].URL, "/app/quotation/SAL-QTN-1") {
		t.Errorf("quotes = %+v", p.Quotes)
	}
	if !strings.Contains(p.NewQuoteURL, "/app/quotation/new?") || !strings.Contains(p.NewQuoteURL, "opportunity=CRM-OPP-1") {
		t.Errorf("new quote link = %q", p.NewQuoteURL)
	}
}

func TestACustomerShowsItsQuotesAndCallsByCustomerOrNumber(t *testing.T) {
	s, _ := newOps(t)
	p, err := s.For(context.Background(), []crm.EmberOpsLink{{Label: "Customer", Name: "CUST-1"}}, []string{"216-555-0142", "(310) 555-0000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Quotes) != 1 || p.Quotes[0].Party != "Acme" {
		t.Errorf("quotes = %+v, want only CUST-1's", p.Quotes)
	}
	if len(p.Calls) != 2 {
		t.Fatalf("calls = %+v, want both, one by number and one by the customer", p.Calls)
	}
	if p.Calls[0].Seconds != 0 && p.Calls[1].Seconds != 125 {
		t.Errorf("calls = %+v", p.Calls)
	}
	if !strings.Contains(p.NewQuoteURL, "quotation_to=Customer") || !strings.Contains(p.NewQuoteURL, "party_name=CUST-1") {
		t.Errorf("link = %q", p.NewQuoteURL)
	}
}

func TestARecordEmberOpsNeverHeardOfHasAnEmptyPanel(t *testing.T) {
	s, _ := newOps(t)
	p, err := s.For(context.Background(), nil, []string{"12"})
	if err != nil || len(p.Quotes) != 0 || len(p.Calls) != 0 || p.NewQuoteURL != "" {
		t.Errorf("panel = %+v (%v), want empty", p, err)
	}
}

func TestQuotedBetweenTotalsByCurrencyAndStatus(t *testing.T) {
	s, _ := newOps(t)
	tot, err := s.QuotedBetween(context.Background(), "2026-11-01", "2026-11-30")
	if err != nil || tot.Count != 3 {
		t.Fatalf("totals = %+v (%v), want 3", tot, err)
	}
	if len(tot.ByCurrency) != 2 || tot.ByCurrency[0].Currency != "EUR" || tot.ByCurrency[1].Cents != 134950 {
		t.Errorf("by currency = %+v, want EUR 10 and USD 1349.50", tot.ByCurrency)
	}
	if len(tot.ByStatus) != 2 {
		t.Errorf("by status = %+v", tot.ByStatus)
	}
}
