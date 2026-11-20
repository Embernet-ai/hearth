package rules

// The engine: deciding whether a rule applies, and doing what it says.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/notify"
)

// KindEvaluate is the job that considers one event against every rule.
const KindEvaluate = "rules.evaluate"

// maxDepth is the loop guard, and it is one.
//
// A person's change is depth zero. Anything a rule causes is one. Nothing at
// depth one or above is considered again, so two rules that trigger each other
// run exactly one cycle and stop. A larger number would let a chain of three
// rules oscillate more slowly, which is worse than not oscillating: it looks
// like it works until the afternoon somebody adds the third rule.
const maxDepth = 1

// Engine evaluates rules and performs their actions.
type Engine struct {
	db     *pgxpool.Pool
	svc    *Service
	store  Store
	runs   RunStore
	docs   docs.Store
	chans  *channels.Service
	notify *notify.Service
	queue  *jobs.Queue
	client *http.Client
	log    *slog.Logger
}

// UseStore points the engine at another home for rules and their runs, which
// is how the EmberOPS back end keeps them in EmberOPS. The Postgres service is
// what the engine starts with.
func (e *Engine) UseStore(st Store, runs RunStore) { e.store, e.runs = st, runs }

func NewEngine(db *pgxpool.Pool, svc *Service, d docs.Store, c *channels.Service,
	n *notify.Service, q *jobs.Queue, log *slog.Logger) *Engine {

	return &Engine{
		db: db, svc: svc, store: svc, runs: svc, docs: d, chans: c, notify: n, queue: q, log: log,
		// Short, because a rule waiting on somebody else's endpoint is a rule
		// holding a worker, and a webhook that takes ten seconds has failed as
		// far as the work order is concerned.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Register wires the handler onto a worker.
func (e *Engine) Register(h interface {
	Handle(kind string, fn jobs.Handler)
}) {
	h.Handle(KindEvaluate, e.handle)
}

// Routes subscribe the engine to everything a rule can trigger on.
func Routes() []jobs.Route {
	return []jobs.Route{
		{Event: jobs.EventCreated, JobKind: KindEvaluate},
		{Event: jobs.EventUpdated, JobKind: KindEvaluate},
		{Event: jobs.EventStatus, JobKind: KindEvaluate},
		{Event: jobs.EventAssigned, JobKind: KindEvaluate},
		{Event: "message", JobKind: KindEvaluate},
	}
}

// handle considers one event.
func (e *Engine) handle(ctx context.Context, j *jobs.Job) error {
	var p jobs.EventPayload
	if err := j.Decode(&p); err != nil {
		return err
	}

	// The loop guard, read from the event rather than tracked in memory. A
	// counter held in the process would reset on every deploy and would not
	// survive the job being retried on another replica.
	var depth int
	var causeRule *string
	err := e.db.QueryRow(ctx,
		`SELECT depth, cause_rule_id::text FROM outbox WHERE id=$1`, p.EventID).
		Scan(&depth, &causeRule)
	if err != nil {
		// An event the relay has since tidied away. Nothing to do, and not a
		// failure worth retrying.
		return nil
	}
	if depth >= maxDepth {
		return nil
	}

	rules, err := e.store.Matching(ctx, j.OrgID, p.Event)
	if err != nil {
		return err
	}
	// Two passes, and the split matters more than it looks.
	//
	// Every rule triggered by one event is judged against the SAME world: the
	// state before any of them acted. Evaluating and acting rule by rule means
	// the second rule sees the first one's write, so two rules that each react
	// to what the other does complete a full cycle inside one event and the
	// depth guard never gets a chance. It also makes the order they happen to
	// be listed in decide the outcome, which is exactly the kind of mystery
	// this phase exists to avoid.
	type decision struct {
		rule    *Rule
		matched bool
		reason  string
	}
	var decisions []decision
	for _, r := range rules {
		if causeRule != nil && *causeRule == r.ID {
			// A rule never reacts to its own work, whatever the depth says.
			continue
		}
		matched, reason := true, ""
		if r.Trigger.Kind != "" || len(r.Conditions) > 0 {
			var err error
			matched, reason, err = e.matches(ctx, r, p)
			if err != nil {
				return err
			}
		}
		decisions = append(decisions, decision{rule: r, matched: matched, reason: reason})
	}

	for _, d := range decisions {
		if err := e.act(ctx, d.rule, p, d.matched, d.reason); err != nil {
			return err
		}
	}
	return nil
}

// act records one decision and performs the actions if it matched.
func (e *Engine) act(ctx context.Context, r *Rule, p jobs.EventPayload, matched bool, reason string) error {
	run := &Run{
		RuleID: r.ID, OrgID: r.OrgID, EventID: p.EventID,
		EntityType: p.EntityType, EntityID: p.EntityID,
		Matched: matched, Reason: reason,
	}
	if !matched {
		// Recorded even though it did nothing. "Why did it not fire" is the
		// question every rules engine gets asked and most cannot answer.
		_, err := e.runs.RecordRun(ctx, run)
		return err
	}

	// Claim the event BEFORE doing anything. The unique index is what makes a
	// redelivered job a no-op rather than a second set of actions, and
	// claiming afterwards would leave open the window it exists to close.
	first, err := e.runs.RecordRun(ctx, run)
	if err != nil {
		return err
	}
	if !first {
		return nil
	}

	taken, actionErr := e.perform(ctx, r, p, false)
	var errText string
	if actionErr != nil {
		errText = actionErr.Error()
	}
	if err := e.runs.FinishRun(ctx, r.ID, p.EventID, taken, errText); err != nil {
		return err
	}
	// An action that failed is worth retrying, but the run is already claimed
	// so a retry would skip it. Logged and swallowed instead: the row carries
	// the error and the operations page shows it.
	if actionErr != nil {
		e.log.WarnContext(ctx, "a rule action failed",
			"rule", r.Name, "entity", p.EntityID, "error", actionErr)
	}
	return nil
}

// rehearse works out what a rule would do to one event, and records it as a
// dry run without touching anything.
func (e *Engine) rehearse(ctx context.Context, r *Rule, p jobs.EventPayload) error {
	run := &Run{
		RuleID: r.ID, OrgID: r.OrgID, EventID: p.EventID,
		EntityType: p.EntityType, EntityID: p.EntityID, DryRun: true,
	}
	if r.Trigger.Kind != "" || len(r.Conditions) > 0 {
		ok, reason, err := e.matches(ctx, r, p)
		if err != nil {
			return err
		}
		if !ok {
			run.Reason = reason
			_, err := e.runs.RecordRun(ctx, run)
			return err
		}
	}
	run.Matched = true
	taken, actionErr := e.perform(ctx, r, p, true)
	run.ActionsTaken = taken
	if actionErr != nil {
		run.Error = actionErr.Error()
	}
	_, err := e.runs.RecordRun(ctx, run)
	return err
}

// matches evaluates the trigger's kind filter and every condition.
//
// It reads the entity from the database rather than from the event payload,
// because a rule testing "priority is urgent" on a status change needs a field
// the status event never carried. Every rule triggered by one event is
// evaluated BEFORE any of them acts, so they all see the same world: see the
// two passes in handle.
func (e *Engine) matches(ctx context.Context, r *Rule, p jobs.EventPayload) (bool, string, error) {
	if p.EntityType != string(core.EntityDocument) {
		if r.Trigger.Kind != "" {
			return false, "not a document", nil
		}
		return true, "", nil
	}

	// The rule engine reads with full authority, deliberately. A rule is
	// configured by somebody who can already see the space it covers, and
	// evaluating it against a subject set would make it fire or not depending
	// on who happened to trigger it.
	q := docs.Query{
		Filters: append([]docs.Filter{}, toFilters(r.Conditions)...),
		Limit:   1,
	}
	if r.Trigger.Kind != "" {
		q.Kind = r.Trigger.Kind
	}

	ok, err := e.docs.MatchesUnfiltered(ctx, r.OrgID, p.EntityID, q)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, describe(r), nil
	}
	return true, "", nil
}

func toFilters(cs []Condition) []docs.Filter {
	out := make([]docs.Filter, 0, len(cs))
	for _, c := range cs {
		out = append(out, docs.Filter{Field: c.Field, Op: c.Op, Values: c.Values})
	}
	return out
}

// describe says which conditions were being asked for, so a run that did not
// match reads as an explanation rather than as a shrug.
func describe(r *Rule) string {
	var parts []string
	if r.Trigger.Kind != "" {
		parts = append(parts, "kind is "+r.Trigger.Kind)
	}
	for _, c := range r.Conditions {
		parts = append(parts, c.Field.Key()+" "+c.Op+" "+strings.Join(c.Values, " or "))
	}
	if len(parts) == 0 {
		return "did not match"
	}
	return "did not match: " + strings.Join(parts, ", and ")
}

// perform runs the actions and reports what it did.
func (e *Engine) perform(ctx context.Context, r *Rule, p jobs.EventPayload, dry bool) ([]string, error) {
	ref := core.Ref{Type: core.EntityType(p.EntityType), ID: p.EntityID}
	// Everything a rule does is attributed to the rule, at depth one, so
	// nothing reacts to it and the audit trail says which automation did it.
	actor := docs.RuleActor{RuleID: r.ID, PrincipalID: r.CreatedBy, Depth: maxDepth}

	var taken []string
	for _, a := range r.Actions {
		desc, err := e.one(ctx, r, a, ref, actor, dry)
		if desc != "" {
			taken = append(taken, desc)
		}
		if err != nil {
			return taken, err
		}
	}
	return taken, nil
}

func (e *Engine) one(ctx context.Context, r *Rule, a Action, ref core.Ref,
	actor docs.RuleActor, dry bool) (string, error) {

	switch a.Kind {
	case ActionSetField:
		desc := fmt.Sprintf("set %s to %q", a.Field.Key(), a.Value)
		if dry {
			return "would " + desc, nil
		}
		return desc, e.docs.SetFieldAsRule(ctx, r.OrgID, actor, ref.ID, a.Field, a.Value)

	case ActionAssign:
		desc := "assign to " + shortID(a.PrincipalID)
		if dry {
			return "would " + desc, nil
		}
		return desc, e.docs.SetFieldAsRule(ctx, r.OrgID, actor, ref.ID,
			docs.Col("assignee"), a.PrincipalID)

	case ActionComment:
		desc := "comment"
		if dry {
			return "would " + desc, nil
		}
		_, err := e.chans.PostAsRule(ctx, r.OrgID, actor.PrincipalID, r.ID, ref, a.Text)
		return desc, err

	case ActionNotify:
		who := a.PrincipalID
		desc := "notify " + shortID(who)
		if dry {
			return "would " + desc, nil
		}
		title, _ := e.titleOf(ctx, ref)
		_, err := e.notify.Deliver(ctx, notify.Delivery{
			OrgID: r.OrgID, PrincipalID: who, Reason: notify.ReasonChanged,
			Ref: ref, Title: title, Excerpt: a.Text,
			// Keyed on the rule and the entity rather than the event, so a
			// rule that fires repeatedly on one job does not fill an inbox.
			DedupeKey: "rule:" + r.ID + ":" + ref.ID,
		})
		return desc, err

	case ActionCreateLinked:
		desc := fmt.Sprintf("create a %s called %q", orDefault(a.DocumentKind, docs.KindWorkOrder), a.Title)
		if dry {
			return "would " + desc, nil
		}
		return desc, e.createLinked(ctx, r, a, ref, actor)

	case ActionWebhook:
		desc := "call " + a.URL
		if dry {
			return "would " + desc, nil
		}
		return desc, e.webhook(ctx, r, a, ref)
	}
	return "", fmt.Errorf("rules: %q is not something a rule can do", a.Kind)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (e *Engine) titleOf(ctx context.Context, ref core.Ref) (string, error) {
	var title string
	err := e.db.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(title,''), 'Untitled') FROM documents WHERE id=$1`,
		ref.ID).Scan(&title)
	if err != nil {
		return "an item", nil
	}
	return title, nil
}

// createLinked makes a new document and points it at the one that triggered
// the rule, which is how "when a work order is completed, raise the follow-up"
// produces something anybody can trace back.
func (e *Engine) createLinked(ctx context.Context, r *Rule, a Action, ref core.Ref, actor docs.RuleActor) error {
	kind := orDefault(a.DocumentKind, docs.KindWorkOrder)
	return e.docs.CreateAsRule(ctx, r.OrgID, actor, docs.CreateParams{
		Kind: kind, Title: a.Title, Body: a.Text,
	}, ref)
}

// webhook posts the event to somebody else's endpoint, signed.
//
// The signature is an HMAC over the exact bytes sent, in a header, because a
// receiver that cannot tell a real call from a forged one is a receiver that
// has to trust the network. Signing is one line here and impossible to add
// later without breaking every existing consumer.
func (e *Engine) webhook(ctx context.Context, r *Rule, a Action, ref core.Ref) error {
	body, err := json.Marshal(map[string]any{
		"rule":        r.Name,
		"rule_id":     r.ID,
		"entity_type": string(ref.Type),
		"entity_id":   ref.ID,
		"at":          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("rules: encode webhook: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("rules: build webhook: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hearth")
	if a.Secret != "" {
		mac := hmac.New(sha256.New, []byte(a.Secret))
		mac.Write(body)
		req.Header.Set("X-Hearth-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("rules: call %s: %w", a.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("rules: %s returned HTTP %d", a.URL, resp.StatusCode)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Dry run
// -----------------------------------------------------------------------------

// DryRun replays a rule against the last events it would have seen and reports
// what it would have done, changing nothing.
//
// Against real history rather than against invented input, because the useful
// question is not "does this rule work" but "how often would this have fired
// last week, and on what". A rule that looks sensible and would have touched
// four hundred work orders is one nobody should enable.
func (e *Engine) DryRun(ctx context.Context, orgID, ruleID string, limit int) ([]*Run, error) {
	r, err := e.store.Get(ctx, orgID, ruleID)
	if err != nil {
		return nil, err
	}
	events, err := e.runs.RecentEvents(ctx, orgID, r.Trigger.Event, limit)
	if err != nil {
		return nil, err
	}

	// Clear the previous dry run, so the page shows this one rather than an
	// accumulation nobody can date.
	if err := e.runs.ClearDryRuns(ctx, ruleID); err != nil {
		return nil, err
	}

	for _, ev := range events {
		p := jobs.EventPayload{
			EventID: ev.ID, Event: ev.Event, EntityType: ev.EntityType,
			EntityID: ev.EntityID, ActorID: ev.ActorID, Payload: ev.Payload,
		}
		if err := e.rehearse(ctx, r, p); err != nil {
			return nil, err
		}
	}
	return e.store.Runs(ctx, orgID, ruleID, limit)
}
