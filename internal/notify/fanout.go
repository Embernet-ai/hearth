package notify

// Turning events into inboxes.
//
// One job per event rather than one per recipient, because the fan-out is
// cheap and the alternative is a queue that grows with the size of the company
// every time somebody changes a status.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Job kinds.
const (
	KindFan    = "notify.fan"
	KindEmail  = "notify.email"
	KindDigest = "notify.digest"
	KindDues   = "notify.due_soon"
)

// Mailer is the slice of sending this package needs, declared here so the SMTP
// client can be absent entirely.
type Mailer interface {
	Send(ctx context.Context, orgID, to, subject, body string) error
	Configured(ctx context.Context, orgID string) bool
}

// Fanout works out who should be told what.
type Fanout struct {
	db      *pgxpool.Pool
	svc     *Service
	queue   *jobs.Queue
	mail    Mailer
	baseURL string
	log     *slog.Logger
}

func NewFanout(db *pgxpool.Pool, svc *Service, queue *jobs.Queue, mail Mailer, baseURL string, log *slog.Logger) *Fanout {
	return &Fanout{db: db, svc: svc, queue: queue, mail: mail, baseURL: baseURL, log: log}
}

// Register wires the handlers onto a worker.
func (f *Fanout) Register(h interface {
	Handle(kind string, fn jobs.Handler)
}) {
	h.Handle(KindFan, f.handleFan)
	h.Handle(KindEmail, f.handleEmail)
	h.Handle(KindDigest, f.handleDigest)
	h.Handle(KindDues, f.handleDueSoon)
}

// Routes are the outbox subscriptions.
//
// No unique key. Two different status changes to one work order are two things
// worth being told about, so collapsing them the way indexing does would drop
// the first one on the floor.
func Routes() []jobs.Route {
	return []jobs.Route{
		{Event: jobs.EventCreated, JobKind: KindFan},
		{Event: jobs.EventUpdated, JobKind: KindFan},
		{Event: jobs.EventStatus, JobKind: KindFan},
		{Event: jobs.EventAssigned, JobKind: KindFan},
		{Event: "message", JobKind: KindFan},
	}
}

// handleFan turns one event into notifications.
func (f *Fanout) handleFan(ctx context.Context, j *jobs.Job) error {
	var p jobs.EventPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	actor := ""
	if p.ActorID != nil {
		actor = *p.ActorID
	}
	ref := core.Ref{Type: core.EntityType(p.EntityType), ID: p.EntityID}

	switch p.Event {
	case jobs.EventAssigned:
		return f.assigned(ctx, j.OrgID, ref, actor, p)
	case "message":
		return f.message(ctx, j.OrgID, ref, actor, p)
	case jobs.EventCreated:
		// Creating something is how you come to be watching it. No
		// notification: nobody needs telling about a thing they just made.
		if actor != "" && p.EntityType == string(core.EntityDocument) {
			return f.svc.Watch(ctx, actor, ref, WatchCreated)
		}
		return nil
	default:
		return f.changed(ctx, j.OrgID, ref, actor, p)
	}
}

// assigned tells the person who now owns something, and starts them watching.
func (f *Fanout) assigned(ctx context.Context, orgID string, ref core.Ref, actor string, p jobs.EventPayload) error {
	var body struct {
		AssigneeID *string `json:"assignee_id"`
	}
	if len(p.Payload) > 0 {
		if err := json.Unmarshal(p.Payload, &body); err != nil {
			return fmt.Errorf("notify: decode assignment: %w", err)
		}
	}
	if body.AssigneeID == nil || *body.AssigneeID == "" {
		// Unassigned. Nobody to tell, and the previous holder losing it
		// silently is deliberate: "you are no longer responsible" is not news
		// anybody has ever wanted at eleven at night.
		return nil
	}

	title, err := f.titleOf(ctx, ref)
	if err != nil {
		return err
	}
	// Watching first, so that even if they read it and do nothing, the next
	// change reaches them.
	if err := f.svc.Watch(ctx, *body.AssigneeID, ref, WatchAssigned); err != nil {
		return err
	}
	created, err := f.svc.Deliver(ctx, Delivery{
		OrgID: orgID, PrincipalID: *body.AssigneeID, Reason: ReasonAssigned,
		Ref: ref, Title: title, ActorID: actor,
		// The event id, so the same assignment redelivered writes one row.
		DedupeKey: fmt.Sprintf("event:%d", p.EventID),
	})
	if err != nil {
		return err
	}
	if created {
		return f.maybeEmail(ctx, orgID, *body.AssigneeID)
	}
	return nil
}

// message tells the people a comment concerns: whoever was mentioned, and
// whoever is watching the thing being discussed.
func (f *Fanout) message(ctx context.Context, orgID string, ref core.Ref, actor string, p jobs.EventPayload) error {
	var body struct {
		MessageID string   `json:"message_id"`
		Mentions  []string `json:"mentions"`
		Excerpt   string   `json:"excerpt"`
	}
	if len(p.Payload) > 0 {
		if err := json.Unmarshal(p.Payload, &body); err != nil {
			return fmt.Errorf("notify: decode message: %w", err)
		}
	}

	title, err := f.titleOf(ctx, ref)
	if err != nil {
		return err
	}

	told := map[string]bool{}
	// Mentions first, and they win: being named is a stronger reason than
	// watching, and somebody who is both should be told the stronger one.
	for _, m := range body.Mentions {
		kind, id, ok := strings.Cut(m, ":")
		if !ok || core.EntityType(kind) != core.EntityPrincipal {
			continue
		}
		created, err := f.svc.Deliver(ctx, Delivery{
			OrgID: orgID, PrincipalID: id, Reason: ReasonMentioned,
			Ref: ref, Title: title, Excerpt: body.Excerpt, ActorID: actor,
			DedupeKey: fmt.Sprintf("event:%d", p.EventID),
		})
		if err != nil {
			return err
		}
		told[id] = true
		if created {
			if err := f.maybeEmail(ctx, orgID, id); err != nil {
				return err
			}
		}
	}

	// Commenting on something is how you come to be following it.
	if actor != "" && ref.Type != core.EntityChannel {
		if err := f.svc.Watch(ctx, actor, ref, WatchCommented); err != nil {
			return err
		}
	}

	// A channel message reaches its members through their own unread count
	// rather than through the inbox. An inbox row per person per message turns
	// a busy channel into a thousand rows nobody reads.
	if ref.Type == core.EntityChannel {
		return nil
	}

	watchers, err := f.svc.Watchers(ctx, ref, actor)
	if err != nil {
		return err
	}
	for _, w := range watchers {
		if told[w] {
			continue
		}
		created, err := f.svc.Deliver(ctx, Delivery{
			OrgID: orgID, PrincipalID: w, Reason: ReasonCommented,
			Ref: ref, Title: title, Excerpt: body.Excerpt, ActorID: actor,
			DedupeKey: fmt.Sprintf("event:%d", p.EventID),
		})
		if err != nil {
			return err
		}
		if created {
			if err := f.maybeEmail(ctx, orgID, w); err != nil {
				return err
			}
		}
	}
	return nil
}

// changed tells watchers that something they follow moved.
func (f *Fanout) changed(ctx context.Context, orgID string, ref core.Ref, actor string, p jobs.EventPayload) error {
	if ref.Type != core.EntityDocument {
		return nil
	}
	watchers, err := f.svc.Watchers(ctx, ref, actor)
	if err != nil || len(watchers) == 0 {
		return err
	}
	title, err := f.titleOf(ctx, ref)
	if err != nil {
		return err
	}

	excerpt := ""
	if p.Event == jobs.EventStatus {
		var body struct {
			From       string `json:"from"`
			To         string `json:"to"`
			ClosedWith string `json:"closed_with"`
			WithParent string `json:"with_parent"`
		}
		if err := json.Unmarshal(p.Payload, &body); err == nil && body.To != "" {
			excerpt = "now " + body.To
		}
		if body.ClosedWith == "" {
			body.ClosedWith = body.WithParent
		}
		if body.ClosedWith != "" {
			// A subtask closed along with its parent. Whoever follows the
			// parent already hears that it closed, and one line per subtask
			// on top of that is the flood: they get the parent's row and no
			// more, and people who follow only the subtask hear about it.
			parentWatchers, err := f.svc.Watchers(ctx, core.Ref{Type: core.EntityDocument, ID: body.ClosedWith}, actor)
			if err != nil {
				return err
			}
			skip := make(map[string]bool, len(parentWatchers))
			for _, w := range parentWatchers {
				skip[w] = true
			}
			kept := watchers[:0:0]
			for _, w := range watchers {
				if !skip[w] {
					kept = append(kept, w)
				}
			}
			if watchers = kept; len(watchers) == 0 {
				return nil
			}
			if body.To == "done" {
				excerpt = "now done, closed with the task it sits under"
			} else {
				excerpt = "now " + body.To + ", moved with the task it sits under"
			}
		}
	} else {
		// A plain edit. Telling somebody "changed" for every keystroke-sized
		// save is the noise that makes people stop reading, so only a status
		// move is worth an inbox row.
		return nil
	}

	for _, w := range watchers {
		created, err := f.svc.Deliver(ctx, Delivery{
			OrgID: orgID, PrincipalID: w, Reason: ReasonChanged,
			Ref: ref, Title: title, Excerpt: excerpt, ActorID: actor,
			DedupeKey: fmt.Sprintf("event:%d", p.EventID),
		})
		if err != nil {
			return err
		}
		if created {
			if err := f.maybeEmail(ctx, orgID, w); err != nil {
				return err
			}
		}
	}
	return nil
}

// titleOf reads what to call a thing, so the notification still reads properly
// after it has been renamed or deleted.
func (f *Fanout) titleOf(ctx context.Context, ref core.Ref) (string, error) {
	var title string
	var err error
	switch ref.Type {
	case core.EntityChannel:
		err = f.db.QueryRow(ctx, `SELECT name FROM channels WHERE id=$1`, ref.ID).Scan(&title)
	default:
		err = f.db.QueryRow(ctx,
			`SELECT COALESCE(NULLIF(title,''), 'Untitled') FROM documents WHERE id=$1`,
			ref.ID).Scan(&title)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "something that has since been removed", nil
	case err != nil:
		return "", fmt.Errorf("notify: read title: %w", err)
	}
	return title, nil
}

// maybeEmail queues an immediate email when that is what somebody asked for.
//
// A digest recipient gets nothing here: their row is already in the inbox and
// the digest job will find it. That is the whole reason emailed_at is a
// separate column from read_at.
func (f *Fanout) maybeEmail(ctx context.Context, orgID, principalID string) error {
	if f.mail == nil || !f.mail.Configured(ctx, orgID) {
		return nil
	}
	r, err := f.svc.RecipientFor(ctx, principalID)
	if err != nil || r == nil {
		return err
	}
	if r.Prefs.Email != EmailImmediate {
		return nil
	}
	if r.Prefs.Quiet(time.Now()) {
		// Held rather than dropped. The digest picks it up, which is what
		// quiet hours should mean: later, not never.
		return nil
	}
	_, err = f.queue.Enqueue(ctx, jobs.Spec{
		OrgID: orgID, Kind: KindEmail,
		Payload: map[string]any{"principal_id": principalID},
	})
	return err
}

// handleEmail sends whatever is pending for one person, as one message.
//
// Batched even in immediate mode, because two events a second apart should not
// be two emails, and the job queue naturally coalesces them: by the time this
// runs, both rows are pending.
func (f *Fanout) handleEmail(ctx context.Context, j *jobs.Job) error {
	var p struct {
		PrincipalID string `json:"principal_id"`
	}
	if err := j.Decode(&p); err != nil {
		return err
	}
	return f.send(ctx, j.OrgID, p.PrincipalID, false)
}

// handleDigest sends one person their digest and schedules the next one.
//
// Self-chaining rather than a cron entry, for the same reason the backfill is:
// there is no scheduler here, and adding one to run a job the queue can already
// run on a delay would be a second thing that can be down.
func (f *Fanout) handleDigest(ctx context.Context, j *jobs.Job) error {
	var p struct {
		PrincipalID string `json:"principal_id"`
	}
	if err := j.Decode(&p); err != nil {
		return err
	}
	if err := f.send(ctx, j.OrgID, p.PrincipalID, true); err != nil {
		return err
	}
	return f.scheduleDigest(ctx, j.OrgID, p.PrincipalID)
}

// ScheduleDigest queues somebody's next digest.
func (f *Fanout) scheduleDigest(ctx context.Context, orgID, principalID string) error {
	r, err := f.svc.RecipientFor(ctx, principalID)
	if err != nil || r == nil {
		return err
	}
	if r.Prefs.Email != EmailDigest {
		return nil
	}
	next := nextDigestAt(time.Now(), r.Prefs)
	_, err = f.queue.Enqueue(ctx, jobs.Spec{
		OrgID: orgID, Kind: KindDigest, RunAfter: next,
		// One pending digest per person. Without the key, every event that
		// arrives while one is queued would schedule another.
		UniqueKey: "digest:" + principalID,
		Payload:   map[string]any{"principal_id": principalID},
	})
	return err
}

// nextDigestAt is the next occurrence of somebody's digest hour, in their own
// clock.
func nextDigestAt(now time.Time, p Prefs) time.Time {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(),
		p.DigestAt/60, p.DigestAt%60, 0, 0, loc)
	if !at.After(local) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// send composes and sends. It is one function for both paths because a digest
// and an immediate email differ only in how many rows they cover.
func (f *Fanout) send(ctx context.Context, orgID, principalID string, digest bool) error {
	if f.mail == nil || !f.mail.Configured(ctx, orgID) {
		return nil
	}
	r, err := f.svc.RecipientFor(ctx, principalID)
	if err != nil || r == nil {
		return err
	}
	if r.Prefs.Email == EmailOff {
		return nil
	}
	pending, err := f.svc.Pending(ctx, principalID, 200)
	if err != nil || len(pending) == 0 {
		return err
	}

	subject := pending[0].Subject()
	if digest || len(pending) > 1 {
		subject = fmt.Sprintf("Hearth: %d things need you", len(pending))
	}

	var b strings.Builder
	if r.Name != "" {
		fmt.Fprintf(&b, "%s,\n\n", r.Name)
	}
	for _, n := range pending {
		fmt.Fprintf(&b, "  %s\n  %s%s\n", n.Sentence(), f.baseURL, n.Href())
		if n.Excerpt != "" {
			fmt.Fprintf(&b, "  %s\n", n.Excerpt)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Your inbox: %s/inbox\nChange how often you hear from this: %s/account\n",
		f.baseURL, f.baseURL)

	if err := f.mail.Send(ctx, orgID, r.Email, subject, b.String()); err != nil {
		return err
	}

	ids := make([]string, len(pending))
	for i, n := range pending {
		ids[i] = n.ID
	}
	if err := f.svc.MarkEmailed(ctx, ids); err != nil {
		return err
	}
	f.log.InfoContext(ctx, "sent notification email",
		"to", principalID, "items", len(pending), "digest", digest)
	return nil
}

// -----------------------------------------------------------------------------
// Due soon
// -----------------------------------------------------------------------------

// dueWindow is how far ahead "due soon" looks.
const dueWindow = 24 * time.Hour

// handleDueSoon is the one sweep in the system, and it is a sweep because
// nothing happens when a date passes: there is no event to react to.
//
// It is safe to run repeatedly because the dedupe key names the document and
// the day rather than the run, so a job that fires twice tells nobody twice.
// That is the same discipline preventive maintenance needs and does not get,
// which is why generation there works forwards from completion instead.
func (f *Fanout) handleDueSoon(ctx context.Context, j *jobs.Job) error {
	rows, err := f.db.Query(ctx, `
		SELECT d.id::text, COALESCE(NULLIF(d.title,''), 'Untitled'),
		       t.assignee_id::text, t.due_at
		FROM documents d
		JOIN task_fields t ON t.document_id = d.id
		WHERE d.org_id = $1 AND d.deleted_at IS NULL
		  AND t.assignee_id IS NOT NULL
		  AND t.status NOT IN ('done', 'cancelled')
		  AND t.due_at IS NOT NULL
		  AND t.due_at <= now() + $2::interval
		LIMIT 2000`, j.OrgID, dueWindow.String())
	if err != nil {
		return fmt.Errorf("notify: due soon scan: %w", err)
	}
	defer rows.Close()

	type due struct {
		id, title, assignee string
		at                  time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.title, &d.assignee, &d.at); err != nil {
			return fmt.Errorf("notify: scan due: %w", err)
		}
		list = append(list, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, d := range list {
		created, err := f.svc.Deliver(ctx, Delivery{
			OrgID: j.OrgID, PrincipalID: d.assignee, Reason: ReasonDueSoon,
			Ref: core.Ref{Type: core.EntityDocument, ID: d.id}, Title: d.title,
			Excerpt: dueExcerpt(d.at),
			// The day, not the run. Sweeping hourly tells somebody once.
			DedupeKey: "due:" + d.id + ":" + d.at.UTC().Format("2006-01-02"),
		})
		if err != nil {
			return err
		}
		if created {
			if err := f.maybeEmail(ctx, j.OrgID, d.assignee); err != nil {
				return err
			}
		}
	}

	// Chain the next sweep. Hourly, so a due date set for this afternoon is
	// noticed this afternoon.
	_, err = f.queue.Enqueue(ctx, jobs.Spec{
		OrgID: j.OrgID, Kind: KindDues, UniqueKey: "due_sweep",
		RunAfter: time.Now().Add(time.Hour),
	})
	return err
}

// StartAll queues the recurring work for every org this installation has.
//
// Called at boot, and safe to call on every boot: both the sweep and each
// digest carry a unique key, so a restart does not multiply them. A failure
// here is logged and does not stop the process, because a deployment that
// refuses to start because it could not schedule tomorrow's digest is worse
// than one that has no digest tomorrow.
func (f *Fanout) StartAll(ctx context.Context) error {
	rows, err := f.db.Query(ctx, `SELECT id::text FROM orgs WHERE playground_of IS NULL`)
	if err != nil {
		return fmt.Errorf("notify: list orgs: %w", err)
	}
	defer rows.Close()
	var orgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("notify: scan org: %w", err)
		}
		orgs = append(orgs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, org := range orgs {
		if err := f.Start(ctx, org); err != nil {
			return err
		}
	}
	return nil
}

// Start queues the recurring work for one org: the due sweep, and a digest for
// everybody who wants one.
//
// Called at boot. Both use unique keys, so a restart does not multiply them.
func (f *Fanout) Start(ctx context.Context, orgID string) error {
	if _, err := f.queue.Enqueue(ctx, jobs.Spec{
		OrgID: orgID, Kind: KindDues, UniqueKey: "due_sweep",
	}); err != nil {
		return err
	}
	rows, err := f.db.Query(ctx,
		`SELECT id::text FROM principals WHERE org_id=$1 AND kind='user' AND status='active'`, orgID)
	if err != nil {
		return fmt.Errorf("notify: list principals: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("notify: scan principal: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := f.scheduleDigest(ctx, orgID, id); err != nil {
			return err
		}
	}
	return nil
}

// dueExcerpt says when something is due. A due date is a date, stored as midnight
// UTC, so it is named by that day: formatted in the server's own zone it was the
// evening before for anybody west of UTC, and carried a time of day (00:00 or
// 19:00) that nobody chose.
func dueExcerpt(at time.Time) string {
	return "due " + at.UTC().Format("2 Jan")
}
