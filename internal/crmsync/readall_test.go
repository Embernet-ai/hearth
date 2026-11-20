package crmsync

import (
	"context"
	"fmt"
	"testing"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// capped answers a list the way the real client does: a limit of 0 means 5000.
type capped struct {
	Ops
	rows []emberops.Row
}

func (c capped) ListAll(_ context.Context, _ emberops.ListQuery, max int) ([]emberops.Row, error) {
	if max <= 0 {
		max = 5000
	}
	if len(c.rows) > max {
		return c.rows[:max], nil
	}
	return c.rows, nil
}

// More Leads than the client's default limit are all read, or the ones past it look
// missing and are created again on every run.
func TestTheSyncReadsEveryLeadNotTheFirstFiveThousand(t *testing.T) {
	var rows []emberops.Row
	for i := 0; i < 6700; i++ {
		rows = append(rows, emberops.Row{"name": fmt.Sprint("CRM-LEAD-", i), "hearth_id": fmt.Sprint("id-", i)})
	}
	s := New(nil, capped{rows: rows}, "org")
	got, err := s.all(context.Background(), "Lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6700 {
		t.Fatalf("read %d of 6700 Leads", len(got))
	}
	if _, ok, _ := s.known(context.Background(), "Lead", "id-6699"); !ok {
		t.Fatal("the last Lead is unknown to the sync")
	}
}
