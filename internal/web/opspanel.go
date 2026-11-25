package web

// EmberOps's selling and stock numbers on the dashboard, for the Sales team and super admins: quotations out, orders open,
// invoices unpaid and overdue, and stock running low. Read from EmberOps itself and shared for two minutes. A company
// that has not sold through EmberOps yet sees zeros and a line saying where to start.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
)

type opsCache struct {
	mu  sync.Mutex
	at  time.Time
	sec *teamSection
}

// opsSection is the EmberOps part of the dashboard, nil for somebody who may not see sales or when EmberOps is not connected.
func (s *Server) opsSection(ctx context.Context, id *auth.Identity) *teamSection {
	if s.crmOps == nil || s.sales == nil || id == nil || !s.worksSales(ctx, id) {
		return nil
	}
	c := &s.opsNums
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sec != nil && time.Since(c.at) < 2*time.Minute {
		return c.sec
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sec := &teamSection{Key: "emberops", Title: "EmberOps selling and stock", Href: "/deals"}
	n, err := s.crmOps.Numbers(rctx, time.Now())
	if err != nil {
		sec.Notice = "EmberOps did not answer: " + areaErr(err)
		return sec
	}
	owed := "none owing"
	if len(n.Unpaid) > 0 {
		var parts []string
		for _, a := range n.Unpaid {
			parts = append(parts, a.Text)
		}
		owed = strings.Join(parts, ", ") + " owing"
	}
	sec.Tiles = []statTile{
		{Label: "Quotations out", Value: num(int64(n.QuotationsOpen)), Period: "open", Href: "/deals"},
		{Label: "Orders open", Value: num(int64(n.OrdersOpen)), Period: "to deliver or bill", Href: "/deals"},
		{Label: "Invoices unpaid", Value: num(int64(n.InvoicesUnpaid)), Detail: owed, Period: fmt.Sprintf("%d overdue", n.InvoicesOver), Href: "/deals", Tone: toneIf(n.InvoicesOver > 0)},
		{Label: "Stock low", Value: num(int64(n.StockLow)), Period: fmt.Sprintf("of %d stocked lines", n.StockLines), Href: "/parts", Tone: toneIf(n.StockLow > 0)},
	}
	if n.QuotationsOpen == 0 && n.OrdersOpen == 0 && n.InvoicesUnpaid == 0 {
		sec.Rows = []teamRow{{Title: "No quotations yet", Detail: "Create one from a deal.", Href: "/deals"}}
	}
	c.sec, c.at = sec, time.Now()
	return sec
}
