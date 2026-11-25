package crmops_test

import (
	"context"
	"testing"
	"time"
)

func TestACustomersOrdersAndInvoicesAndTheCompanyNumbers(t *testing.T) {
	s, f := newOps(t)
	f.Put("Quotation", map[string]any{"name": "SAL-QTN-20", "status": "Open", "docstatus": float64(1)})
	f.Put("Sales Order", map[string]any{"name": "SAL-ORD-1", "customer": "CUST-1", "status": "To Deliver and Bill", "docstatus": float64(1), "grand_total": 5000, "currency": "USD", "transaction_date": "2026-11-10", "per_delivered": 40, "per_billed": 0})
	f.Put("Sales Order", map[string]any{"name": "SAL-ORD-2", "customer": "CUST-2", "status": "Completed", "docstatus": float64(1), "grand_total": 10, "currency": "USD", "transaction_date": "2026-11-11"})
	f.Put("Sales Order", map[string]any{"name": "SAL-ORD-3", "customer": "CUST-1", "status": "Draft", "docstatus": float64(0), "grand_total": 1})
	f.Put("Sales Invoice", map[string]any{"name": "SINV-1", "customer": "CUST-1", "status": "Overdue", "docstatus": float64(1), "grand_total": 3000, "outstanding_amount": 1200.5, "currency": "USD", "posting_date": "2026-10-01", "due_date": "2026-11-01"})
	f.Put("Sales Invoice", map[string]any{"name": "SINV-2", "customer": "CUST-1", "status": "Paid", "docstatus": float64(1), "grand_total": 800, "outstanding_amount": 0, "currency": "USD", "posting_date": "2026-09-01", "due_date": "2026-09-30"})
	f.Put("Sales Invoice", map[string]any{"name": "SINV-3", "customer": "CUST-2", "status": "Unpaid", "docstatus": float64(1), "grand_total": 400, "outstanding_amount": 400, "currency": "USD", "posting_date": "2026-11-20", "due_date": "2026-12-20"})
	f.Put("Bin", map[string]any{"name": "B1", "item_code": "BEARING", "warehouse": "Stores", "actual_qty": 3})
	f.Put("Bin", map[string]any{"name": "B2", "item_code": "BOLT", "warehouse": "Stores", "actual_qty": 500})
	f.Put("Item Reorder", map[string]any{"name": "R1", "parent": "BEARING", "warehouse": "Stores", "warehouse_reorder_level": 10})
	f.Put("Item Reorder", map[string]any{"name": "R2", "parent": "BOLT", "warehouse": "Stores", "warehouse_reorder_level": 100})
	day := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	orders, invoices, err := s.Selling(ctx, "CUST-1", day)
	if err != nil || len(orders) != 1 || orders[0].Name != "SAL-ORD-1" || orders[0].Delivered != 40 {
		t.Fatalf("orders %+v err %v (submitted ones for this customer only)", orders, err)
	}
	if len(invoices) != 2 || invoices[0].Name != "SINV-1" || !invoices[0].Overdue || invoices[0].Outstanding != 1200.5 || invoices[1].Overdue {
		t.Fatalf("invoices %+v", invoices)
	}

	n, err := s.Numbers(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if n.QuotationsOpen < 1 || n.OrdersOpen != 1 || n.InvoicesUnpaid != 2 || n.InvoicesOver != 1 || len(n.Unpaid) != 1 || n.Unpaid[0].Cents != 160050 || n.StockLines != 2 || n.StockLow != 1 {
		t.Fatalf("numbers = %+v", n)
	}
}

func TestAnEmberOpsWithNothingSoldYetReadsAsZeros(t *testing.T) {
	s, _ := newOps(t)
	n, err := s.Numbers(context.Background(), time.Now())
	if err != nil || n.OrdersOpen != 0 || n.InvoicesUnpaid != 0 || n.StockLines != 0 || n.StockLow != 0 || len(n.Unpaid) != 0 {
		t.Fatalf("numbers = %+v err %v", n, err)
	}
}
