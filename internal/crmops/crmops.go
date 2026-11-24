// Package crmops puts what EmberOps holds about a CRM record next to the record: its quotations and its call logs, and
// how much has been quoted in a period.
//
// EmberOps owns quotes, price lists, the CPQ configurator and the telephony call log. Hearth does not rebuild them. It
// reads them, with the service account and not the viewer's own EmberOps permissions, because who may see a CRM record is
// already decided by the page that asks (the Sales team and super admins see everything), and it links to EmberOps for the
// part only EmberOps does: building a quote.
package crmops

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/emberops"
)

// Lister is the part of the EmberOps client this package reads with.
type Lister interface {
	List(ctx context.Context, lq emberops.ListQuery) ([]emberops.Row, error)
	BaseURL() *url.URL
}

// Quote is one Quotation as it shows on a record.
type Quote struct {
	Name      string
	Party     string
	Status    string
	Currency  string
	Total     float64
	Date      string
	ValidTill string
	URL       string
}

// CallLog is one telephone call EmberOps recorded.
type CallLog struct {
	Name     string
	Type     string
	Status   string
	From, To string
	Start    string
	Seconds  int
	URL      string
}

// Panel is everything EmberOps adds to one record's page.
type Panel struct {
	Quotes []Quote
	Calls  []CallLog
	// NewQuoteURL opens a new Quotation in EmberOps with the record's party and opportunity filled in. Empty when the
	// record has no EmberOps party yet.
	NewQuoteURL string
}

// Service reads the panel.
type Service struct {
	ops Lister
}

// New returns a Service over an EmberOps client.
func New(ops Lister) *Service { return &Service{ops: ops} }

func (s *Service) docURL(doctype, name string) string {
	return strings.TrimRight(s.ops.BaseURL().String(), "/") + "/app/" + strings.ReplaceAll(strings.ToLower(doctype), " ", "-") + "/" + url.PathEscape(name)
}

var nonDigits = regexp.MustCompile(`\D+`)

// tail10 is the last ten digits of a number: enough to match however a phone system wrote it.
func tail10(n string) string {
	d := nonDigits.ReplaceAllString(n, "")
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	if len(d) < 7 {
		return ""
	}
	return d
}

func str(r emberops.Row, k string) string {
	if v, ok := r[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func num(r emberops.Row, k string) float64 {
	switch v := r[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

var quoteFields = []string{"name", "quotation_to", "party_name", "customer_name", "status", "currency", "grand_total", "transaction_date", "valid_till", "opportunity"}

func (s *Service) quote(r emberops.Row) Quote {
	return Quote{Name: str(r, "name"), Party: firstNonEmpty(str(r, "customer_name"), str(r, "party_name")), Status: str(r, "status"),
		Currency: str(r, "currency"), Total: num(r, "grand_total"), Date: str(r, "transaction_date"), ValidTill: str(r, "valid_till"), URL: s.docURL("Quotation", str(r, "name"))}
}

func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// For reads the quotations and calls for a record. links are its EmberOps pages (crm.EmberOpsLinks) and phones the
// numbers a contact holds. A record EmberOps has never heard of has an empty panel, not an error.
func (s *Service) For(ctx context.Context, links []crm.EmberOpsLink, phones []string) (*Panel, error) {
	ctx = emberops.WithUser(ctx, nil) // the service account, whoever is looking
	p := &Panel{}
	seen := map[string]bool{}
	var customers []string
	for _, l := range links {
		var filters []emberops.Filter
		switch l.Label {
		case "Opportunity":
			filters = []emberops.Filter{emberops.F("opportunity", l.Name)}
			if p.NewQuoteURL == "" {
				p.NewQuoteURL = s.newQuote(l.Name, "", "")
			}
		case "Customer", "Lead":
			filters = []emberops.Filter{emberops.F("quotation_to", l.Label), emberops.F("party_name", l.Name)}
			if l.Label == "Customer" {
				customers = append(customers, l.Name)
			}
			if p.NewQuoteURL == "" {
				p.NewQuoteURL = s.newQuote("", l.Label, l.Name)
			}
		default:
			continue
		}
		rows, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Quotation", Fields: quoteFields, Filters: filters, OrderBy: "transaction_date desc", Limit: 50})
		if err != nil {
			return nil, fmt.Errorf("crmops: read quotations: %w", err)
		}
		for _, r := range rows {
			if q := s.quote(r); !seen[q.Name] {
				seen[q.Name] = true
				p.Quotes = append(p.Quotes, q)
			}
		}
	}
	sort.SliceStable(p.Quotes, func(i, j int) bool { return p.Quotes[i].Date > p.Quotes[j].Date })

	var or []emberops.Filter
	for _, ph := range phones {
		if t := tail10(ph); t != "" {
			or = append(or, emberops.Filter{Field: "from", Op: "like", Value: "%" + t + "%"}, emberops.Filter{Field: "to", Op: "like", Value: "%" + t + "%"})
		}
	}
	for _, c := range customers {
		or = append(or, emberops.F("customer", c))
	}
	if len(or) > 0 {
		rows, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Call Log", Fields: []string{"name", "type", "status", "from", "to", "start_time", "duration"}, OrFilters: or, OrderBy: "start_time desc", Limit: 50})
		if err != nil {
			return nil, fmt.Errorf("crmops: read call logs: %w", err)
		}
		for _, r := range rows {
			p.Calls = append(p.Calls, CallLog{Name: str(r, "name"), Type: str(r, "type"), Status: str(r, "status"), From: str(r, "from"), To: str(r, "to"),
				Start: str(r, "start_time"), Seconds: int(num(r, "duration")), URL: s.docURL("Call Log", str(r, "name"))})
		}
	}
	return p, nil
}

func (s *Service) newQuote(opportunity, to, party string) string {
	q := url.Values{}
	if opportunity != "" {
		q.Set("opportunity", opportunity)
	}
	if party != "" {
		q.Set("quotation_to", to)
		q.Set("party_name", party)
	}
	return strings.TrimRight(s.ops.BaseURL().String(), "/") + "/app/quotation/new?" + q.Encode()
}

// Totals is how much has been quoted over a period, by currency and status.
type Totals struct {
	Count int
	// ByCurrency is the quoted total per currency.
	ByCurrency []crm.Amt
	ByStatus   []StatusCount
}

// StatusCount is the quotations in one status.
type StatusCount struct {
	Status string
	Count  int
}

// QuotedBetween reads every Quotation dated from the first day to the second (days included).
func (s *Service) QuotedBetween(ctx context.Context, from, to string) (*Totals, error) {
	ctx = emberops.WithUser(ctx, nil)
	rows, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Quotation", Fields: []string{"name", "status", "currency", "grand_total", "docstatus"},
		Filters: []emberops.Filter{{Field: "transaction_date", Op: "between", Value: []string{from, to}}, {Field: "docstatus", Op: "!=", Value: 2}}, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("crmops: read quotations: %w", err)
	}
	t := &Totals{Count: len(rows)}
	cur, st := map[string]int64{}, map[string]int{}
	for _, r := range rows {
		cur[firstNonEmpty(str(r, "currency"), "USD")] += int64(num(r, "grand_total")*100 + 0.5)
		st[firstNonEmpty(str(r, "status"), "Draft")]++
	}
	for c, v := range cur {
		t.ByCurrency = append(t.ByCurrency, crm.Amt{Currency: c, Cents: v, Text: crm.Money(v, c)})
	}
	sort.Slice(t.ByCurrency, func(i, j int) bool { return t.ByCurrency[i].Currency < t.ByCurrency[j].Currency })
	for k, v := range st {
		t.ByStatus = append(t.ByStatus, StatusCount{k, v})
	}
	sort.Slice(t.ByStatus, func(i, j int) bool { return t.ByStatus[i].Status < t.ByStatus[j].Status })
	return t, nil
}
