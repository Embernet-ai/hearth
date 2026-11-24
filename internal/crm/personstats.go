package crm

// What one person's deals add up to, for the dashboard.
//
// Every figure is a query over the deals table, filtered the way every list is
// (a deal is counted only when the caller may see it), and every figure has a
// matching ListQuery so the page can link to the exact rows behind it. A number
// whose list disagrees with it is a number nobody trusts, so the filters here and
// the filters in Deals are the same three: open, overdue, closed in a window.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Closed stages: a deal in one of these is no longer in the pipeline.
func isClosed(stage string) bool {
	return stage == StageWon || stage == StageLost || stage == StageNoResponse
}

// Sum is a count of deals and what they are worth in one currency.
type Sum struct {
	Currency string
	Count    int
	Cents    int64
}

// Amount is the value as a person reads it.
func (s Sum) Amount() string { return Money(s.Cents, s.Currency) }

// StageRow is one stage of the pipeline for one person.
type StageRow struct {
	Stage string
	Label string
	Count int
	Sums  []Sum
}

// PersonStats is one person's deals.
type PersonStats struct {
	// Open is everything not won, lost, or no response, by currency.
	Open      []Sum
	OpenCount int
	// Overdue is open deals whose expected close date has passed.
	Overdue int
	// Won and Lost are the deals that closed inside the window asked for.
	Won, Lost           []Sum
	WonCount, LostCount int
	// Stages is the open pipeline, stage by stage, in pipeline order.
	Stages []StageRow
}

// PersonStats counts the deals owned by owner (everyone's when owner is empty) that the actor may see, with
// wins and losses counted inside [from, to).
func (s *Service) PersonStats(ctx context.Context, orgID string, actor core.Subjects,
	owner string, today, from, to time.Time) (*PersonStats, error) {

	keys := actor.Keys()
	out := &PersonStats{}

	rows, err := s.db.Query(ctx, `
		SELECT d.stage, d.currency, count(*)::int, coalesce(sum(d.amount_cents), 0)::bigint
		  FROM deals d
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL AND ($3 = '' OR d.owner_id::text = $3)
		   AND `+visible("deal", "d", 2)+`
		   AND d.stage NOT IN ('won', 'lost', 'no_response')
		 GROUP BY d.stage, d.currency`, orgID, keys, owner)
	if err != nil {
		return nil, fmt.Errorf("crm: open deals: %w", err)
	}
	byStage := map[string][]Sum{}
	open := map[string]*Sum{}
	for rows.Next() {
		var st, cur string
		var n int
		var cents int64
		if err := rows.Scan(&st, &cur, &n, &cents); err != nil {
			rows.Close()
			return nil, fmt.Errorf("crm: scan open deals: %w", err)
		}
		byStage[st] = append(byStage[st], Sum{Currency: cur, Count: n, Cents: cents})
		o := open[cur]
		if o == nil {
			o = &Sum{Currency: cur}
			open[cur] = o
		}
		o.Count += n
		o.Cents += cents
		out.OpenCount += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Open = sortedSums(open)
	for _, st := range Stages {
		if isClosed(st) {
			continue
		}
		r := StageRow{Stage: st, Label: StageLabel(st), Sums: byStage[st]}
		for _, sm := range r.Sums {
			r.Count += sm.Count
		}
		out.Stages = append(out.Stages, r)
	}

	if err := s.db.QueryRow(ctx, `
		SELECT count(*)::int FROM deals d
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL AND ($3 = '' OR d.owner_id::text = $3)
		   AND `+visible("deal", "d", 2)+`
		   AND d.stage NOT IN ('won', 'lost', 'no_response')
		   AND d.close_date IS NOT NULL AND d.close_date < $4::date`,
		orgID, keys, owner, today).Scan(&out.Overdue); err != nil {
		return nil, fmt.Errorf("crm: overdue deals: %w", err)
	}

	closed, err := s.db.Query(ctx, `
		SELECT d.stage, d.currency, count(*)::int, coalesce(sum(d.amount_cents), 0)::bigint
		  FROM deals d
		 WHERE d.org_id = $1 AND d.deleted_at IS NULL AND ($3 = '' OR d.owner_id::text = $3)
		   AND `+visible("deal", "d", 2)+`
		   AND d.stage IN ('won', 'lost')
		   AND d.closed_at >= $4 AND d.closed_at < $5
		 GROUP BY d.stage, d.currency`, orgID, keys, owner, from, to)
	if err != nil {
		return nil, fmt.Errorf("crm: closed deals: %w", err)
	}
	defer closed.Close()
	won, lost := map[string]*Sum{}, map[string]*Sum{}
	for closed.Next() {
		var st, cur string
		var n int
		var cents int64
		if err := closed.Scan(&st, &cur, &n, &cents); err != nil {
			return nil, fmt.Errorf("crm: scan closed deals: %w", err)
		}
		into := won
		if st == StageLost {
			into = lost
			out.LostCount += n
		} else {
			out.WonCount += n
		}
		into[cur] = &Sum{Currency: cur, Count: n, Cents: cents}
	}
	if err := closed.Err(); err != nil {
		return nil, err
	}
	out.Won, out.Lost = sortedSums(won), sortedSums(lost)
	return out, nil
}

func sortedSums(m map[string]*Sum) []Sum {
	out := make([]Sum, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}
