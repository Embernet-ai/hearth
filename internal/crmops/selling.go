package crmops

// What EmberOps holds about selling and stock, read for the record's page and the dashboard: a customer's orders and
// invoices, and the numbers across the company (quotations out, orders open, invoices unpaid and overdue, stock running
// low). All reads, with the service account, behind the same Sales team and super admin gate as the rest of the sales pages.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/emberops"
)

// Order is one Sales Order as it shows on a customer.
type Order struct {
	Name      string
	Status    string
	Total     float64
	Currency  string
	Date      string
	Delivery  string
	Delivered float64 // percent
	Billed    float64 // percent
	URL       string
}

// Invoice is one Sales Invoice as it shows on a customer.
type Invoice struct {
	Name        string
	Status      string
	Total       float64
	Outstanding float64
	Currency    string
	Date        string
	Due         string
	Overdue     bool
	URL         string
}

var (
	orderFields   = []string{"name", "status", "grand_total", "currency", "transaction_date", "delivery_date", "per_delivered", "per_billed"}
	invoiceFields = []string{"name", "status", "grand_total", "outstanding_amount", "currency", "posting_date", "due_date"}
)

// Selling reads a customer's orders and invoices, newest first, submitted documents only.
func (s *Service) Selling(ctx context.Context, customer string, today time.Time) ([]Order, []Invoice, error) {
	ctx = emberops.WithUser(ctx, nil)
	rows, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Sales Order", Fields: orderFields,
		Filters: []emberops.Filter{emberops.F("customer", customer), emberops.F("docstatus", 1)}, OrderBy: "transaction_date desc", Limit: 50})
	if err != nil {
		return nil, nil, fmt.Errorf("crmops: read orders: %w", err)
	}
	var orders []Order
	for _, r := range rows {
		orders = append(orders, Order{Name: str(r, "name"), Status: str(r, "status"), Total: num(r, "grand_total"), Currency: str(r, "currency"),
			Date: str(r, "transaction_date"), Delivery: str(r, "delivery_date"), Delivered: num(r, "per_delivered"), Billed: num(r, "per_billed"),
			URL: s.docURL("Sales Order", str(r, "name"))})
	}
	rows, err = s.ops.List(ctx, emberops.ListQuery{Doctype: "Sales Invoice", Fields: invoiceFields,
		Filters: []emberops.Filter{emberops.F("customer", customer), emberops.F("docstatus", 1)}, OrderBy: "posting_date desc", Limit: 50})
	if err != nil {
		return nil, nil, fmt.Errorf("crmops: read invoices: %w", err)
	}
	var invoices []Invoice
	for _, r := range rows {
		inv := Invoice{Name: str(r, "name"), Status: str(r, "status"), Total: num(r, "grand_total"), Outstanding: num(r, "outstanding_amount"), Currency: str(r, "currency"),
			Date: str(r, "posting_date"), Due: str(r, "due_date"), URL: s.docURL("Sales Invoice", str(r, "name"))}
		inv.Overdue = inv.Outstanding > 0 && inv.Due != "" && inv.Due < today.Format("2006-01-02")
		invoices = append(invoices, inv)
	}
	return orders, invoices, nil
}

// Numbers is what the dashboard shows from EmberOps.
type Numbers struct {
	QuotationsOpen int
	OrdersOpen     int
	InvoicesUnpaid int
	InvoicesOver   int
	Unpaid         []crm.Amt // outstanding, by currency
	StockLines     int       // item and warehouse pairs EmberOps holds stock for
	StockLow       int       // of those, at or under the item's reorder level
}

// counter is the EmberOps client's count call, used when it is there and replaced by a list when it is not.
type counter interface {
	Count(ctx context.Context, doctype string, filters []emberops.Filter) (int, error)
}

func (s *Service) count(ctx context.Context, doctype string, f []emberops.Filter) (int, error) {
	if c, ok := s.ops.(counter); ok {
		return c.Count(ctx, doctype, f)
	}
	rows, err := s.ops.List(ctx, emberops.ListQuery{Doctype: doctype, Fields: []string{"name"}, Filters: f, Limit: 500})
	return len(rows), err
}

// Numbers reads the numbers across the company.
func (s *Service) Numbers(ctx context.Context, today time.Time) (*Numbers, error) {
	ctx = emberops.WithUser(ctx, nil)
	n := &Numbers{}
	var err error
	if n.QuotationsOpen, err = s.count(ctx, "Quotation", []emberops.Filter{emberops.F("docstatus", 1), emberops.F("status", "Open")}); err != nil {
		return nil, fmt.Errorf("crmops: count quotations: %w", err)
	}
	if n.OrdersOpen, err = s.count(ctx, "Sales Order", []emberops.Filter{emberops.F("docstatus", 1),
		{Field: "status", Op: "in", Value: []string{"To Deliver and Bill", "To Bill", "To Deliver"}}}); err != nil {
		return nil, fmt.Errorf("crmops: count orders: %w", err)
	}
	inv, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Sales Invoice", Fields: []string{"name", "currency", "outstanding_amount", "due_date"},
		Filters: []emberops.Filter{emberops.F("docstatus", 1), {Field: "outstanding_amount", Op: ">", Value: 0}}, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("crmops: read unpaid invoices: %w", err)
	}
	by := map[string]int64{}
	day := today.Format("2006-01-02")
	for _, r := range inv {
		n.InvoicesUnpaid++
		by[firstNonEmpty(str(r, "currency"), "USD")] += int64(num(r, "outstanding_amount")*100 + 0.5)
		if d := str(r, "due_date"); d != "" && d < day {
			n.InvoicesOver++
		}
	}
	for c, v := range by {
		n.Unpaid = append(n.Unpaid, crm.Amt{Currency: c, Cents: v, Text: crm.Money(v, c)})
	}
	sort.Slice(n.Unpaid, func(i, j int) bool { return n.Unpaid[i].Currency < n.Unpaid[j].Currency })

	bins, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Bin", Fields: []string{"item_code", "warehouse", "actual_qty"}, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("crmops: read stock: %w", err)
	}
	level := map[string]float64{}
	if len(bins) > 0 {
		re, err := s.ops.List(ctx, emberops.ListQuery{Doctype: "Item Reorder", Parent: "Item", Fields: []string{"parent", "warehouse", "warehouse_reorder_level"}, Limit: 500})
		if err != nil {
			return nil, fmt.Errorf("crmops: read reorder levels: %w", err)
		}
		for _, r := range re {
			level[strings.ToLower(str(r, "parent")+"\x00"+str(r, "warehouse"))] = num(r, "warehouse_reorder_level")
		}
	}
	for _, b := range bins {
		n.StockLines++
		if lv, ok := level[strings.ToLower(str(b, "item_code")+"\x00"+str(b, "warehouse"))]; ok && num(b, "actual_qty") <= lv {
			n.StockLow++
		}
	}
	return n, nil
}
