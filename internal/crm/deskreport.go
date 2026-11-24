package crm

// The sales report, and the bulk change. Both read and write through the same checks as the single record.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// StageLine is one open stage of the pipeline.
type StageLine struct {
	Stage       string
	Label       string
	Probability int
	Count       int
	Value       []Amt
	Weighted    []Amt
}

// OwnerLine is one person's pipeline and results.
type OwnerLine struct {
	OwnerID   string
	Name      string
	OpenCount int
	Open      []Amt
	Weighted  []Amt
	WonCount  int
	Won       []Amt
	LostCount int
}

// ActivityLine is what one person logged in the period.
type ActivityLine struct {
	PersonID string
	Name     string
	Notes    int
	Calls    int
	Meetings int
	Emails   int
	Total    int
}

// MonthLine is the deals made in one month.
type MonthLine struct {
	Month string
	Count int
	Value []Amt
}

// ReasonLine is the deals lost for one reason in the period.
type ReasonLine struct {
	Reason string
	Count  int
	Value  []Amt
}

// AgeLine is how long the open deals in one stage have been there.
type AgeLine struct {
	Stage      string
	Label      string
	Count      int
	AvgDays    int
	OldestDays int
}

// SalesReport is how the pipeline and the team are doing.
type SalesReport struct {
	From, To  time.Time
	Pipeline  []StageLine
	OpenCount int
	Open      []Amt
	Weighted  []Amt
	WonCount  int
	Won       []Amt
	LostCount int
	Lost      []Amt
	// WinRate is won as a percent of everything that closed in the period, or -1 when nothing closed.
	WinRate int
	// AvgWon is the average won deal, per currency, and AvgCycleDays the average days from made to won.
	AvgWon       []Amt
	AvgCycleDays int
	Owners       []OwnerLine
	Activity     []ActivityLine
	Created      []MonthLine
	// LostReasons is why the deals lost in the period were lost, most common first, and StageAge how long the deals open now
	// have sat in their stage.
	LostReasons []ReasonLine
	StageAge    []AgeLine
}

// SalesReport reads the deals and the timeline the caller may see. Everybody's by default; owner narrows deals to
// one person (or "none"). The period is [from, to], days included, and counts what closed and what was logged in it.
func (s *Service) SalesReport(ctx context.Context, orgID string, actor core.Subjects, from, to time.Time, owner string) (*SalesReport, error) {
	r := &SalesReport{From: from, To: to, WinRate: -1}
	end := to.AddDate(0, 0, 1)
	keys := keysOf(actor)

	// The open pipeline, by stage.
	args := []any{orgID, keys}
	own := ownerFilter("d.owner_id", owner, &args)
	rows, err := s.db.Query(ctx, `SELECT d.stage, d.currency, count(*)::int, coalesce(sum(d.amount_cents),0)::bigint
		FROM deals d WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+own+`
		  AND d.stage NOT IN ('won','lost','no_response') GROUP BY d.stage, d.currency`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: report pipeline: %w", err)
	}
	type acc struct {
		n    int
		v, w map[string]int64
	}
	stages := map[string]*acc{}
	openV, openW := map[string]int64{}, map[string]int64{}
	for rows.Next() {
		var st, cur string
		var n int
		var c int64
		if err := rows.Scan(&st, &cur, &n, &c); err != nil {
			rows.Close()
			return nil, err
		}
		a := stages[st]
		if a == nil {
			a = &acc{v: map[string]int64{}, w: map[string]int64{}}
			stages[st] = a
		}
		a.n += n
		addAmt(a.v, cur, c)
		addAmt(a.w, cur, weighted(c, st))
		r.OpenCount += n
		addAmt(openV, cur, c)
		addAmt(openW, cur, weighted(c, st))
	}
	rows.Close()
	for _, st := range Stages {
		if isClosed(st) {
			continue
		}
		a := stages[st]
		line := StageLine{Stage: st, Label: StageLabel(st), Probability: Probability(st)}
		if a != nil {
			line.Count, line.Value, line.Weighted = a.n, amts(a.v), amts(a.w)
		}
		r.Pipeline = append(r.Pipeline, line)
	}
	r.Open, r.Weighted = amts(openV), amts(openW)

	// What closed in the period.
	args = []any{orgID, keys, from, end}
	own = ownerFilter("d.owner_id", owner, &args)
	rows, err = s.db.Query(ctx, `SELECT d.stage, d.currency, count(*)::int, coalesce(sum(d.amount_cents),0)::bigint,
		       coalesce(avg(extract(epoch FROM (d.closed_at - d.created_at)) / 86400), 0)::float8
		FROM deals d WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+own+`
		  AND d.stage IN ('won','lost','no_response') AND d.closed_at >= $3 AND d.closed_at < $4
		GROUP BY d.stage, d.currency`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: report closed: %w", err)
	}
	wonV, lostV := map[string]int64{}, map[string]int64{}
	wonCount := map[string]int{}
	var cycleSum float64
	var cycleN int
	for rows.Next() {
		var st, cur string
		var n int
		var c int64
		var days float64
		if err := rows.Scan(&st, &cur, &n, &c, &days); err != nil {
			rows.Close()
			return nil, err
		}
		if st == StageWon {
			r.WonCount += n
			addAmt(wonV, cur, c)
			wonCount[cur] += n
			cycleSum += days * float64(n)
			cycleN += n
		} else {
			r.LostCount += n
			addAmt(lostV, cur, c)
		}
	}
	rows.Close()
	r.Won, r.Lost = amts(wonV), amts(lostV)
	if r.WonCount+r.LostCount > 0 {
		r.WinRate = r.WonCount * 100 / (r.WonCount + r.LostCount)
	}
	if cycleN > 0 {
		r.AvgCycleDays = int(cycleSum/float64(cycleN) + 0.5)
	}
	avg := map[string]int64{}
	for cur, n := range wonCount {
		if n > 0 {
			avg[cur] = wonV[cur] / int64(n)
		}
	}
	r.AvgWon = amts(avg)

	// By person: the open pipeline now, and what they closed in the period.
	args = []any{orgID, keys, from, end}
	own = ownerFilter("d.owner_id", owner, &args)
	rows, err = s.db.Query(ctx, `SELECT coalesce(d.owner_id::text,''), coalesce(op.display_name,'Nobody'), d.stage, d.currency,
		       count(*)::int, coalesce(sum(d.amount_cents),0)::bigint
		FROM deals d LEFT JOIN principals op ON op.id = d.owner_id
		WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+own+`
		  AND (d.stage NOT IN ('won','lost','no_response') OR (d.closed_at >= $3 AND d.closed_at < $4))
		GROUP BY 1, 2, d.stage, d.currency`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: report owners: %w", err)
	}
	type oacc struct {
		line       OwnerLine
		open, w, v map[string]int64
	}
	owners := map[string]*oacc{}
	for rows.Next() {
		var oid, name, st, cur string
		var n int
		var c int64
		if err := rows.Scan(&oid, &name, &st, &cur, &n, &c); err != nil {
			rows.Close()
			return nil, err
		}
		o := owners[oid]
		if o == nil {
			o = &oacc{line: OwnerLine{OwnerID: oid, Name: name}, open: map[string]int64{}, w: map[string]int64{}, v: map[string]int64{}}
			owners[oid] = o
		}
		switch st {
		case StageWon:
			o.line.WonCount += n
			addAmt(o.v, cur, c)
		case StageLost, StageNoResponse:
			o.line.LostCount += n
		default:
			o.line.OpenCount += n
			addAmt(o.open, cur, c)
			addAmt(o.w, cur, weighted(c, st))
		}
	}
	rows.Close()
	for _, o := range owners {
		o.line.Open, o.line.Weighted, o.line.Won = amts(o.open), amts(o.w), amts(o.v)
		r.Owners = append(r.Owners, o.line)
	}
	sort.Slice(r.Owners, func(i, j int) bool { return r.Owners[i].Name < r.Owners[j].Name })

	// What people logged in the period, on records the caller may see.
	rows, err = s.db.Query(ctx, `SELECT a.created_by::text, coalesce(p.display_name,''), a.kind, count(*)::int
		FROM crm_activities a LEFT JOIN principals p ON p.id = a.created_by
		WHERE a.org_id=$1 AND a.deleted_at IS NULL AND a.occurred_at >= $3 AND a.occurred_at < $4
		  AND EXISTS (SELECT 1 FROM crm_activity_targets tg WHERE tg.activity_id = a.id AND
		        ($2::text[] @> ARRAY['*super'] OR EXISTS (SELECT 1 FROM grants g WHERE g.entity_type = tg.target_type
		          AND g.entity_id = tg.target_id AND g.subject_key = ANY($2) AND (g.expires_at IS NULL OR g.expires_at > now()))))
		GROUP BY 1, 2, 3`, orgID, keys, from, end)
	if err != nil {
		return nil, fmt.Errorf("crm: report activity: %w", err)
	}
	people := map[string]*ActivityLine{}
	for rows.Next() {
		var pid, name, kind string
		var n int
		if err := rows.Scan(&pid, &name, &kind, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if owner != "" && owner != "none" && pid != owner {
			continue
		}
		l := people[pid]
		if l == nil {
			l = &ActivityLine{PersonID: pid, Name: name}
			people[pid] = l
		}
		switch kind {
		case ActNote:
			l.Notes += n
		case ActCall:
			l.Calls += n
		case ActMeeting:
			l.Meetings += n
		case ActEmail:
			l.Emails += n
		}
		l.Total += n
	}
	rows.Close()
	for _, l := range people {
		r.Activity = append(r.Activity, *l)
	}
	sort.Slice(r.Activity, func(i, j int) bool {
		if r.Activity[i].Total != r.Activity[j].Total {
			return r.Activity[i].Total > r.Activity[j].Total
		}
		return r.Activity[i].Name < r.Activity[j].Name
	})

	// Deals made, month by month, for the six months to the end of the period.
	args = []any{orgID, keys, time.Date(to.Year(), to.Month()-5, 1, 0, 0, 0, 0, to.Location()), end}
	own = ownerFilter("d.owner_id", owner, &args)
	rows, err = s.db.Query(ctx, `SELECT to_char(date_trunc('month', d.created_at), 'YYYY-MM'), d.currency, count(*)::int,
		       coalesce(sum(d.amount_cents),0)::bigint
		FROM deals d WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+own+`
		  AND d.created_at >= $3 AND d.created_at < $4 GROUP BY 1, 2 ORDER BY 1`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: report created: %w", err)
	}
	months := map[string]*MonthLine{}
	mv := map[string]map[string]int64{}
	for rows.Next() {
		var m, cur string
		var n int
		var c int64
		if err := rows.Scan(&m, &cur, &n, &c); err != nil {
			rows.Close()
			return nil, err
		}
		ml := months[m]
		if ml == nil {
			ml = &MonthLine{Month: m}
			months[m] = ml
			mv[m] = map[string]int64{}
		}
		ml.Count += n
		addAmt(mv[m], cur, c)
	}
	rows.Close()
	for m, ml := range months {
		ml.Value = amts(mv[m])
		r.Created = append(r.Created, *ml)
	}
	sort.Slice(r.Created, func(i, j int) bool { return r.Created[i].Month < r.Created[j].Month })

	// Why deals were lost in the period.
	args = []any{orgID, keys, from, end}
	own = ownerFilter("d.owner_id", owner, &args)
	rows, err = s.db.Query(ctx, `SELECT coalesce((array_agg(btrim(d.lost_reason) ORDER BY d.closed_at DESC))[1], ''), d.currency, count(*)::int,
		       coalesce(sum(d.amount_cents),0)::bigint
		FROM deals d WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+own+`
		  AND d.stage = 'lost' AND d.closed_at >= $3 AND d.closed_at < $4
		GROUP BY lower(btrim(d.lost_reason)), d.currency`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: report lost reasons: %w", err)
	}
	type racc struct {
		line ReasonLine
		v    map[string]int64
	}
	reasons := map[string]*racc{}
	for rows.Next() {
		var why, cur string
		var n int
		var c int64
		if err := rows.Scan(&why, &cur, &n, &c); err != nil {
			rows.Close()
			return nil, err
		}
		key := strings.ToLower(why)
		a := reasons[key]
		if a == nil {
			label := why
			if label == "" {
				label = "No reason given"
			}
			a = &racc{line: ReasonLine{Reason: label}, v: map[string]int64{}}
			reasons[key] = a
		}
		a.line.Count += n
		addAmt(a.v, cur, c)
	}
	rows.Close()
	for _, a := range reasons {
		a.line.Value = amts(a.v)
		r.LostReasons = append(r.LostReasons, a.line)
	}
	sort.Slice(r.LostReasons, func(i, j int) bool {
		if r.LostReasons[i].Count != r.LostReasons[j].Count {
			return r.LostReasons[i].Count > r.LostReasons[j].Count
		}
		return r.LostReasons[i].Reason < r.LostReasons[j].Reason
	})

	// How long the open deals have sat in their stage.
	args = []any{orgID, keys}
	own = ownerFilter("d.owner_id", owner, &args)
	rows, err = s.db.Query(ctx, `SELECT d.stage, count(*)::int,
		       coalesce(avg(extract(epoch FROM (now() - d.stage_changed_at)) / 86400), 0)::float8,
		       coalesce(max(extract(epoch FROM (now() - d.stage_changed_at)) / 86400), 0)::float8
		FROM deals d WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+own+`
		  AND d.stage NOT IN ('won','lost','no_response') GROUP BY d.stage`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: report stage age: %w", err)
	}
	age := map[string]AgeLine{}
	for rows.Next() {
		var st string
		var n int
		var avg, oldest float64
		if err := rows.Scan(&st, &n, &avg, &oldest); err != nil {
			rows.Close()
			return nil, err
		}
		age[st] = AgeLine{Stage: st, Label: StageLabel(st), Count: n, AvgDays: int(avg + 0.5), OldestDays: int(oldest)}
	}
	rows.Close()
	for _, st := range Stages {
		if a, ok := age[st]; ok {
			r.StageAge = append(r.StageAge, a)
		}
	}
	return r, nil
}

// -----------------------------------------------------------------------------
// Bulk change
// -----------------------------------------------------------------------------

// BulkSpec is one change applied to many records of one kind. Owner is set when it is not nil ("" clears the owner).
type BulkSpec struct {
	Type    core.EntityType
	IDs     []string
	Owner   *string
	Stage   string // deals only
	AddTag  string // companies only
	AddType string // companies only: a type key
}

// BulkFailure is one record the change did not reach, and why.
type BulkFailure struct {
	ID  string
	Why string
}

// BulkResult says how many records changed and names the ones that did not.
type BulkResult struct {
	Done   int
	Failed []BulkFailure
}

// BulkApply makes the change record by record through the same writes a single edit uses, so each one is checked
// against the caller's edit access and every one publishes its own event for the sync. A record that fails does not
// stop the rest.
func (s *Service) BulkApply(ctx context.Context, orgID string, actor core.Subjects, b BulkSpec) (*BulkResult, error) {
	if !validTarget(b.Type) {
		return nil, fmt.Errorf("%w: bulk changes are for companies, contacts, and deals", ErrInvalid)
	}
	if len(b.IDs) == 0 {
		return nil, fmt.Errorf("%w: pick at least one record", ErrInvalid)
	}
	if len(b.IDs) > 500 {
		return nil, fmt.Errorf("%w: at most 500 records at a time", ErrInvalid)
	}
	b.AddTag, b.AddType = strings.TrimSpace(b.AddTag), strings.TrimSpace(b.AddType)
	if b.Owner == nil && b.Stage == "" && b.AddTag == "" && b.AddType == "" {
		return nil, fmt.Errorf("%w: say what to change", ErrInvalid)
	}
	if b.Stage != "" {
		if b.Type != core.EntityDeal {
			return nil, fmt.Errorf("%w: only deals have a stage", ErrInvalid)
		}
		if !ValidStage(b.Stage) {
			return nil, fmt.Errorf("%w: %q is not a stage", ErrInvalid, b.Stage)
		}
	}
	if (b.AddTag != "" || b.AddType != "") && b.Type != core.EntityCompany {
		return nil, fmt.Errorf("%w: tags and types are for companies", ErrInvalid)
	}
	if b.Owner != nil && *b.Owner != "" {
		if err := s.checkOwner(ctx, orgID, *b.Owner); err != nil {
			return nil, err
		}
	}
	res := &BulkResult{}
	seen := map[string]bool{}
	for _, id := range b.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if err := s.bulkOne(ctx, orgID, actor, b, id); err != nil {
			res.Failed = append(res.Failed, BulkFailure{ID: id, Why: err.Error()})
			continue
		}
		res.Done++
	}
	return res, nil
}

func (s *Service) bulkOne(ctx context.Context, orgID string, actor core.Subjects, b BulkSpec, id string) error {
	ref := core.Ref{Type: b.Type, ID: id}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return ErrNotFound
	}
	if b.Owner != nil {
		if err := s.SetOwner(ctx, orgID, actor, ref, *b.Owner); err != nil {
			return err
		}
	}
	if b.Stage != "" {
		if err := s.MoveDeal(ctx, orgID, actor, id, b.Stage, nil); err != nil {
			return err
		}
	}
	if b.AddTag != "" || b.AddType != "" {
		l, err := s.CompanyLabels(ctx, orgID, actor, id)
		if err != nil {
			return err
		}
		var typeKeys, tags *[]string
		if b.AddType != "" {
			ks := l.Keys()
			if !contains(ks, b.AddType) {
				ks = append(ks, b.AddType)
			}
			typeKeys = &ks
		}
		if b.AddTag != "" {
			ts := append([]string(nil), l.Tags...)
			if !containsFold(ts, b.AddTag) {
				ts = append(ts, b.AddTag)
			}
			tags = &ts
		}
		return s.SetCompanyLabels(ctx, orgID, actor, id, typeKeys, tags)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
