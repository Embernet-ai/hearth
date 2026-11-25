package crm

// What a sales team does every day beside the board: owe a record a follow-up, see what the pipeline is worth
// weighted by how likely each deal is, read how the quarter is going, and change many records at once.
//
// Every list here is filtered the way the deals list is: a record counts for the caller when they may see it, a
// super admin sees all of it, and a person outside sales sees none of it. Nothing defaults to "mine".

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// stageProbability is how likely a deal in each stage is to close, in percent. A deal in Customer has agreed and is
// being set up, so it is nearly there; Won is done, Lost and No response are not coming.
var stageProbability = map[string]int{
	StageNew: 10, StageScreening: 20, StageMeeting: 40, StageProposal: 60, StageCustomer: 90,
	StageWon: 100, StageLost: 0, StageNoResponse: 0,
}

// Probability is the chance, in percent, that a deal in this stage closes.
func Probability(stage string) int { return stageProbability[stage] }

// Amt is money in one currency, with the text a person reads.
type Amt struct {
	Currency string
	Cents    int64
	Text     string
}

func addAmt(m map[string]int64, cur string, cents int64) { m[cur] += cents }

func amts(m map[string]int64) []Amt {
	out := make([]Amt, 0, len(m))
	for cur, c := range m {
		out = append(out, Amt{Currency: cur, Cents: c, Text: Money(c, cur)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// ActivityEdit is the parts of a timeline entry that can change. Nil leaves a part alone; its kind and what it is about
// never change (a note about a deal stays a note about that deal).
type ActivityEdit struct {
	Title       *string
	Body        *string
	OccurredAt  *time.Time
	Direction   *string
	Disposition *string
	DurationMs  *int64
}

// UpdateActivity changes a note, call, meeting or email already on a timeline. The author may, and so may anybody with
// edit on every record it is about, the same as removing one. It must still say something afterwards.
func (s *Service) UpdateActivity(ctx context.Context, orgID string, actor core.Subjects, id string, e ActivityEdit) (*Activity, error) {
	var kind, title, body, direction, disposition, author string
	var occurred time.Time
	var dur *int64
	err := s.db.QueryRow(ctx, `SELECT kind, title, body, coalesce(direction,''), disposition, occurred_at, duration_ms, created_by::text
		FROM crm_activities WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID).Scan(&kind, &title, &body, &direction, &disposition, &occurred, &dur, &author)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("crm: find activity: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT target_type, target_id::text FROM crm_activity_targets WHERE activity_id=$1`, id)
	if err != nil {
		return nil, fmt.Errorf("crm: activity targets: %w", err)
	}
	var refs []core.Ref
	for rows.Next() {
		var t, tid string
		if err := rows.Scan(&t, &tid); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, core.Ref{Type: core.EntityType(t), ID: tid})
	}
	rows.Close()
	if author != actor.Principal() {
		for _, r := range refs {
			if err := s.authz.Require(ctx, actor, r, core.AccessEdit); err != nil {
				return nil, ErrNotFound
			}
		}
	} else {
		// The author must still be able to see something it is about, so an entry never turns up on a record they cannot read.
		seen := false
		for _, r := range refs {
			if s.authz.Require(ctx, actor, r, core.AccessView) == nil {
				seen = true
			}
		}
		if !seen {
			return nil, ErrNotFound
		}
	}
	if e.Title != nil {
		title = strings.TrimSpace(*e.Title)
	}
	if e.Body != nil {
		body = strings.TrimSpace(*e.Body)
	}
	if title == "" && body == "" {
		return nil, fmt.Errorf("%w: write something first", ErrInvalid)
	}
	if e.OccurredAt != nil && !e.OccurredAt.IsZero() {
		occurred = *e.OccurredAt
	}
	if e.Direction != nil {
		d := strings.TrimSpace(*e.Direction)
		if d != "" && d != Inbound && d != Outbound {
			return nil, fmt.Errorf("%w: a %s goes in or out", ErrInvalid, kind)
		}
		if kind == ActNote || kind == ActMeeting {
			d = ""
		}
		direction = d
	}
	if e.Disposition != nil && kind == ActCall {
		disposition = strings.TrimSpace(*e.Disposition)
	}
	if e.DurationMs != nil && (kind == ActCall || kind == ActMeeting) {
		if *e.DurationMs < 0 {
			return nil, fmt.Errorf("%w: a duration cannot be negative", ErrInvalid)
		}
		dur = e.DurationMs
	}
	if _, err := s.db.Exec(ctx, `UPDATE crm_activities SET title=$3, body=$4, occurred_at=$5, direction=NULLIF($6,''), disposition=$7,
		duration_ms=$8, updated_at=now() WHERE id=$1 AND org_id=$2`, id, orgID, title, body, occurred, direction, disposition, dur); err != nil {
		return nil, fmt.Errorf("crm: update activity: %w", err)
	}
	return &Activity{ID: id, Kind: kind, Title: title, Body: body, OccurredAt: occurred, Direction: direction, Disposition: disposition, DurationMs: dur, CreatedBy: author, Targets: refs}, nil
}

// SetLostReason records why a deal was lost, in the seller's words. It is its own call because a lost reason is
// read far more often than it is written, and because every writer of a deal (the forms, the importers, the sync)
// would otherwise have to carry it to avoid wiping it.
func (s *Service) SetLostReason(ctx context.Context, orgID string, actor core.Subjects, id, reason string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDeal, ID: id}, core.AccessEdit); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		return fmt.Errorf("%w: a lost reason is at most 500 characters", ErrInvalid)
	}
	ct, err := s.db.Exec(ctx, `UPDATE deals SET lost_reason=$3, updated_at=now()
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID, reason)
	if err != nil {
		return fmt.Errorf("crm: set lost reason: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// -----------------------------------------------------------------------------
// Follow-ups
// -----------------------------------------------------------------------------

// Followup is something owed to a company, a contact, or a deal, and when.
type Followup struct {
	ID           string
	Title        string
	Body         string
	Status       string
	DueAt        *time.Time
	AssigneeID   string
	AssigneeName string
	Target       core.Ref
	TargetName   string
	TargetHref   string
	Overdue      bool
	CompletedAt  *time.Time
	CreatedAt    time.Time
}

// FollowupParams is a new follow-up.
type FollowupParams struct {
	Title    string
	Body     string
	DueAt    *time.Time
	Assignee string // a person; empty means the person making it
	Target   core.Ref
}

// FollowupQuery filters the queue. Empty means everybody's, open and done, which is the sales default.
type FollowupQuery struct {
	Assignee string // a person id, or "none"
	Status   string // "open", "done", or "" for both
	Due      string // "overdue", "today", "week", or ""
	Target   *core.Ref
	Limit    int
	Offset   int
}

func hrefOfTarget(t core.EntityType, id string) string {
	switch t {
	case core.EntityCompany:
		return "/companies/" + id
	case core.EntityContact:
		return "/contacts/" + id
	case core.EntityDeal:
		return "/deals/" + id
	}
	return ""
}

func validTarget(t core.EntityType) bool {
	return t == core.EntityCompany || t == core.EntityContact || t == core.EntityDeal
}

// AddFollowup writes a follow-up on a record the caller may edit.
func (s *Service) AddFollowup(ctx context.Context, orgID string, actor core.Subjects, p FollowupParams) (*Followup, error) {
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" {
		return nil, fmt.Errorf("%w: say what to follow up on", ErrInvalid)
	}
	if len(p.Title) > 200 {
		return nil, fmt.Errorf("%w: a title is at most 200 characters", ErrInvalid)
	}
	if !validTarget(p.Target.Type) {
		return nil, fmt.Errorf("%w: a follow-up is about a company, a contact, or a deal", ErrInvalid)
	}
	if err := s.authz.Require(ctx, actor, p.Target, core.AccessEdit); err != nil {
		return nil, err
	}
	assignee := strings.TrimSpace(p.Assignee)
	if assignee == "" {
		assignee = principal
	}
	if err := s.checkOwner(ctx, orgID, assignee); err != nil {
		return nil, err
	}
	f := &Followup{ID: uuid.NewString(), Title: p.Title, Body: strings.TrimSpace(p.Body), Status: "open",
		DueAt: p.DueAt, AssigneeID: assignee, Target: p.Target}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		INSERT INTO crm_followups (id, org_id, title, body, due_at, assignee_id, target_type, target_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING created_at`,
		f.ID, orgID, f.Title, f.Body, f.DueAt, assignee, string(p.Target.Type), p.Target.ID, principal).Scan(&f.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("crm: add follow-up: %w", err)
	}
	// The record changed: what is owed on it. Webhook receivers (Cascade flows) hear it like any other change.
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{OrgID: orgID, EntityType: string(p.Target.Type), EntityID: p.Target.ID,
		Event: jobs.EventUpdated, ActorID: principal, Payload: map[string]any{"followup": "created", "title": f.Title}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("crm: commit: %w", err)
	}
	f.TargetHref = hrefOfTarget(p.Target.Type, p.Target.ID)
	f.Overdue = f.DueAt != nil && f.DueAt.Before(time.Now())
	_ = s.db.QueryRow(ctx, `SELECT display_name FROM principals WHERE id=$1`, assignee).Scan(&f.AssigneeName)
	table := map[core.EntityType]string{core.EntityCompany: "companies", core.EntityContact: "contacts", core.EntityDeal: "deals"}[p.Target.Type]
	_ = s.db.QueryRow(ctx, `SELECT name FROM `+table+` WHERE id=$1`, p.Target.ID).Scan(&f.TargetName)
	return f, nil
}

// followupTarget finds what a follow-up is about, or ErrNotFound.
func (s *Service) followupTarget(ctx context.Context, orgID, id string) (core.Ref, error) {
	var t, tid string
	err := s.db.QueryRow(ctx, `SELECT target_type, target_id::text FROM crm_followups
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID).Scan(&t, &tid)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Ref{}, ErrNotFound
	}
	if err != nil {
		return core.Ref{}, fmt.Errorf("crm: find follow-up: %w", err)
	}
	return core.Ref{Type: core.EntityType(t), ID: tid}, nil
}

// SetFollowupDone completes a follow-up, or opens it again.
func (s *Service) SetFollowupDone(ctx context.Context, orgID string, actor core.Subjects, id string, done bool) error {
	ref, err := s.followupTarget(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return ErrNotFound
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE crm_followups SET status=$3, updated_at=now(),
		completed_at = CASE WHEN $4 THEN now() END, completed_by = CASE WHEN $4 THEN $5::uuid END
		WHERE id=$1 AND org_id=$2`, id, orgID, map[bool]string{true: "done", false: "open"}[done], done, actor.Principal()); err != nil {
		return fmt.Errorf("crm: complete follow-up: %w", err)
	}
	what := map[bool]string{true: "done", false: "reopened"}[done]
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{OrgID: orgID, EntityType: string(ref.Type), EntityID: ref.ID,
		Event: jobs.EventUpdated, ActorID: actor.Principal(), Payload: map[string]any{"followup": what}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RescheduleFollowup changes when a follow-up is due (nil clears it), and who has it when assignee is not empty.
func (s *Service) RescheduleFollowup(ctx context.Context, orgID string, actor core.Subjects, id string, due *time.Time, assignee string) error {
	ref, err := s.followupTarget(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return ErrNotFound
	}
	if assignee != "" {
		if err := s.checkOwner(ctx, orgID, assignee); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(ctx, `UPDATE crm_followups SET due_at=$3, updated_at=now(),
		assignee_id = coalesce(NULLIF($4,'')::uuid, assignee_id) WHERE id=$1 AND org_id=$2`, id, orgID, due, assignee)
	if err != nil {
		return fmt.Errorf("crm: reschedule follow-up: %w", err)
	}
	return nil
}

// DeleteFollowup removes a follow-up.
func (s *Service) DeleteFollowup(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	ref, err := s.followupTarget(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return ErrNotFound
	}
	_, err = s.db.Exec(ctx, `UPDATE crm_followups SET deleted_at=now(), updated_at=now() WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("crm: delete follow-up: %w", err)
	}
	return nil
}

const followupFrom = `
	FROM crm_followups f
	LEFT JOIN principals p ON p.id = f.assignee_id
	LEFT JOIN deals de ON f.target_type = 'deal' AND de.id = f.target_id AND de.deleted_at IS NULL
	LEFT JOIN companies co ON f.target_type = 'company' AND co.id = f.target_id AND co.deleted_at IS NULL
	LEFT JOIN contacts ct ON f.target_type = 'contact' AND ct.id = f.target_id AND ct.deleted_at IS NULL
	WHERE f.org_id = $1 AND f.deleted_at IS NULL AND (
	      (de.id IS NOT NULL AND ` + `($2::text[] @> ARRAY['*super'] OR EXISTS (SELECT 1 FROM grants g WHERE g.entity_type='deal' AND g.entity_id=de.id AND g.subject_key = ANY($2) AND (g.expires_at IS NULL OR g.expires_at > now()))))
	   OR (co.id IS NOT NULL AND ` + `($2::text[] @> ARRAY['*super'] OR EXISTS (SELECT 1 FROM grants g WHERE g.entity_type='company' AND g.entity_id=co.id AND g.subject_key = ANY($2) AND (g.expires_at IS NULL OR g.expires_at > now()))))
	   OR (ct.id IS NOT NULL AND ` + `($2::text[] @> ARRAY['*super'] OR EXISTS (SELECT 1 FROM grants g WHERE g.entity_type='contact' AND g.entity_id=ct.id AND g.subject_key = ANY($2) AND (g.expires_at IS NULL OR g.expires_at > now()))))
	)`

// Followups is the queue: every follow-up on a record the caller may see, soonest due first, open ones before done.
func (s *Service) Followups(ctx context.Context, orgID string, actor core.Subjects, q FollowupQuery) ([]*Followup, int, error) {
	args := []any{orgID, keysOf(actor)}
	where := ""
	switch q.Status {
	case "open", "done":
		args = append(args, q.Status)
		where += fmt.Sprintf(" AND f.status = $%d", len(args))
	}
	where += ownerFilter("f.assignee_id", q.Assignee, &args)
	now := time.Now()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch q.Due {
	case "overdue":
		args = append(args, now)
		where += fmt.Sprintf(" AND f.status = 'open' AND f.due_at < $%d", len(args))
	case "today":
		args = append(args, startOfToday, startOfToday.AddDate(0, 0, 1))
		where += fmt.Sprintf(" AND f.due_at >= $%d AND f.due_at < $%d", len(args)-1, len(args))
	case "week":
		args = append(args, startOfToday, startOfToday.AddDate(0, 0, 7))
		where += fmt.Sprintf(" AND f.due_at >= $%d AND f.due_at < $%d", len(args)-1, len(args))
	}
	if q.Target != nil {
		args = append(args, string(q.Target.Type), q.Target.ID)
		where += fmt.Sprintf(" AND f.target_type = $%d AND f.target_id = $%d", len(args)-1, len(args))
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) `+followupFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("crm: count follow-ups: %w", err)
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit, q.Offset)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT f.id::text, f.title, f.body, f.status, f.due_at, coalesce(f.assignee_id::text,''), coalesce(p.display_name,''),
		       f.target_type, f.target_id::text, coalesce(de.name, co.name, ct.name, ''), f.completed_at, f.created_at `+
		followupFrom+where+`
		ORDER BY (f.status = 'done'), f.due_at NULLS LAST, f.created_at, f.id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("crm: list follow-ups: %w", err)
	}
	defer rows.Close()
	var out []*Followup
	for rows.Next() {
		f := &Followup{}
		var tt string
		if err := rows.Scan(&f.ID, &f.Title, &f.Body, &f.Status, &f.DueAt, &f.AssigneeID, &f.AssigneeName,
			&tt, &f.Target.ID, &f.TargetName, &f.CompletedAt, &f.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("crm: scan follow-up: %w", err)
		}
		f.Target.Type = core.EntityType(tt)
		if f.DueAt != nil {
			// A due date is a day, stored as midnight UTC, and is read back as that day wherever the server is.
			u := f.DueAt.UTC()
			f.DueAt = &u
		}
		f.TargetHref = hrefOfTarget(f.Target.Type, f.Target.ID)
		f.Overdue = f.Status == "open" && f.DueAt != nil && f.DueAt.Before(now)
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// -----------------------------------------------------------------------------
// Forecast
// -----------------------------------------------------------------------------

// ForecastBucket is the open deals expected to close in one month, plus two buckets that need a person: the ones
// whose expected close date has passed and the ones with no date.
type ForecastBucket struct {
	Key      string // "overdue", "none", or 2026-12
	Label    string
	Count    int
	Value    []Amt
	Weighted []Amt
	Deals    []*Deal
}

// ForecastOwner is one person's open pipeline.
type ForecastOwner struct {
	OwnerID  string
	Name     string
	Count    int
	Value    []Amt
	Weighted []Amt
}

// Forecast is the open pipeline weighted by stage probability, and what has already closed this month.
type Forecast struct {
	Buckets  []ForecastBucket
	Owners   []ForecastOwner
	Count    int
	Value    []Amt
	Weighted []Amt
	// WonCount and Won are the deals won since the first of this month.
	WonCount int
	Won      []Amt
}

func weighted(cents int64, stage string) int64 { return cents * int64(Probability(stage)) / 100 }

// Forecast reads every open deal the caller may see, whoever owns it. owner narrows it to one person, "none" to the
// unowned, and "" is everybody, which is the default.
func (s *Service) Forecast(ctx context.Context, orgID string, actor core.Subjects, owner string, now time.Time) (*Forecast, error) {
	q := ListQuery{Open: true, Owner: owner, Limit: 5000, Sort: "close"}
	deals, _, err := s.Deals(ctx, orgID, actor, q)
	if err != nil {
		return nil, err
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	byKey := map[string]*ForecastBucket{}
	val, wt := map[string]map[string]int64{}, map[string]map[string]int64{}
	ensure := func(key, label string) *ForecastBucket {
		b, ok := byKey[key]
		if !ok {
			b = &ForecastBucket{Key: key, Label: label}
			byKey[key] = b
			val[key], wt[key] = map[string]int64{}, map[string]int64{}
		}
		return b
	}
	f := &Forecast{}
	totalV, totalW := map[string]int64{}, map[string]int64{}
	type own struct {
		o    ForecastOwner
		v, w map[string]int64
	}
	owners := map[string]*own{}
	for _, d := range deals {
		var key, label string
		switch {
		case d.CloseDate == nil:
			key, label = "none", "No close date"
		case d.CloseDate.UTC().Before(today):
			key, label = "overdue", "Past its close date"
		default:
			key, label = d.CloseDate.UTC().Format("2006-01"), d.CloseDate.UTC().Format("January 2006")
		}
		b := ensure(key, label)
		b.Count++
		b.Deals = append(b.Deals, d)
		w := weighted(d.AmountCents, d.Stage)
		addAmt(val[key], d.Currency, d.AmountCents)
		addAmt(wt[key], d.Currency, w)
		addAmt(totalV, d.Currency, d.AmountCents)
		addAmt(totalW, d.Currency, w)
		f.Count++
		oid := ""
		if d.OwnerID != nil {
			oid = *d.OwnerID
		}
		o, ok := owners[oid]
		if !ok {
			name := d.OwnerName
			if oid == "" {
				name = "Nobody"
			}
			o = &own{o: ForecastOwner{OwnerID: oid, Name: name}, v: map[string]int64{}, w: map[string]int64{}}
			owners[oid] = o
		}
		o.o.Count++
		addAmt(o.v, d.Currency, d.AmountCents)
		addAmt(o.w, d.Currency, w)
	}
	for _, b := range byKey {
		b.Value, b.Weighted = amts(val[b.Key]), amts(wt[b.Key])
		f.Buckets = append(f.Buckets, *b)
	}
	order := func(k string) string {
		switch k {
		case "overdue":
			return "0"
		case "none":
			return "9"
		}
		return "1" + k
	}
	sort.Slice(f.Buckets, func(i, j int) bool { return order(f.Buckets[i].Key) < order(f.Buckets[j].Key) })
	for _, o := range owners {
		o.o.Value, o.o.Weighted = amts(o.v), amts(o.w)
		f.Owners = append(f.Owners, o.o)
	}
	sort.Slice(f.Owners, func(i, j int) bool { return f.Owners[i].Name < f.Owners[j].Name })
	f.Value, f.Weighted = amts(totalV), amts(totalW)

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	wonV := map[string]int64{}
	wargs := []any{orgID, keysOf(actor), monthStart}
	wown := ownerFilter("d.owner_id", owner, &wargs)
	rows, err := s.db.Query(ctx, `SELECT d.currency, count(*)::int, coalesce(sum(d.amount_cents),0)::bigint FROM deals d
		WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+`
		  AND d.stage = 'won' AND d.closed_at >= $3`+wown+`
		GROUP BY d.currency`, wargs...)
	if err != nil {
		return nil, fmt.Errorf("crm: forecast won: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cur string
		var n int
		var c int64
		if err := rows.Scan(&cur, &n, &c); err != nil {
			return nil, err
		}
		f.WonCount += n
		addAmt(wonV, cur, c)
	}
	f.Won = amts(wonV)
	return f, rows.Err()
}

// Desk is the daily-work side of the CRM beside Sales. *Service is the only one.
type Desk interface {
	SetLostReason(ctx context.Context, orgID string, actor core.Subjects, id, reason string) error
	AddFollowup(ctx context.Context, orgID string, actor core.Subjects, p FollowupParams) (*Followup, error)
	SetFollowupDone(ctx context.Context, orgID string, actor core.Subjects, id string, done bool) error
	RescheduleFollowup(ctx context.Context, orgID string, actor core.Subjects, id string, due *time.Time, assignee string) error
	DeleteFollowup(ctx context.Context, orgID string, actor core.Subjects, id string) error
	UpdateActivity(ctx context.Context, orgID string, actor core.Subjects, id string, e ActivityEdit) (*Activity, error)
	Followups(ctx context.Context, orgID string, actor core.Subjects, q FollowupQuery) ([]*Followup, int, error)
	Forecast(ctx context.Context, orgID string, actor core.Subjects, owner string, now time.Time) (*Forecast, error)
	SalesReport(ctx context.Context, orgID string, actor core.Subjects, from, to time.Time, owner string) (*SalesReport, error)
	BulkApply(ctx context.Context, orgID string, actor core.Subjects, b BulkSpec) (*BulkResult, error)
	Duplicates(ctx context.Context, orgID string, actor core.Subjects, t core.EntityType) ([]DupGroup, error)
	Merge(ctx context.Context, orgID string, actor core.Subjects, t core.EntityType, survivor string, losers []string) error
}

var _ Desk = (*Service)(nil)
