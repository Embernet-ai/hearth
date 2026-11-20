package stores

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/rules"
)

// Rules keeps automations (Hearth Rule) and the record of what they decided
// (Hearth Rule Run) in EmberOPS. The engine still runs in Hearth, which reads
// events from its own outbox; it is pointed at this store with
// Engine.UseStore, so a rule is read from, and a run written to, EmberOPS.
//
// A real run is unique per (rule, event): the run's Hearth id is derived from
// the pair, so a redelivered job finds the run already there and does nothing,
// which is the same "fires once" guarantee the unique index gives in Postgres.
// Rehearsals (dry runs) get random ids and are cleared before each rehearsal.
//
// The events a rehearsal replays come from Hearth's outbox (Events, below, is
// the Postgres service), because events are Hearth's own record.
type Rules struct {
	Base
	Dir    Directory
	Events RuleEvents
}

// RuleEvents is the recent-event history, which stays in Hearth's outbox.
type RuleEvents interface {
	RecentEvents(ctx context.Context, orgID, event string, limit int) ([]jobs.Event, error)
}

var (
	_ rules.Store    = (*Rules)(nil)
	_ rules.RunStore = (*Rules)(nil)
)

const (
	ruleDoctype = "Hearth Rule"
	runDoctype  = "Hearth Rule Run"
)

var ruleFields = []string{"name", "hearth_id", "rule_name", "enabled", "hearth_space", "trigger_event", "trigger_kind",
	"conditions", "actions", "owner", "creation", "modified"}

var runNS = uuid.MustParse("6f1c7a7e-2b0a-4e3e-9a55-6a1d2c1f0001")

func (s *Rules) rule(ctx context.Context, orgID string, r emberops.Row, spaces, owners map[string]string) *rules.Rule {
	out := &rules.Rule{ID: idOf(r), OrgID: orgID, Name: r.S("rule_name"), Enabled: r.B("enabled"),
		Trigger:   rules.Trigger{Event: r.S("trigger_event"), Kind: r.S("trigger_kind")},
		CreatedBy: owners[r.S("owner")], CreatedAt: r.T("creation", s.loc()), UpdatedAt: r.T("modified", s.loc())}
	_ = json.Unmarshal([]byte(r.S("conditions")), &out.Conditions)
	_ = json.Unmarshal([]byte(r.S("actions")), &out.Actions)
	if sp := r.S("hearth_space"); sp != "" {
		id := spaces[sp]
		out.SpaceID = &id
	}
	return out
}

func (s *Rules) rules(ctx context.Context, orgID string, rows []emberops.Row) ([]*rules.Rule, error) {
	var names []string
	for _, r := range rows {
		if r.Has("hearth_space") {
			names = append(names, r.S("hearth_space"))
		}
	}
	spaces, err := s.Dir.HearthIDs(ctx, "Hearth Space", names)
	if err != nil {
		return nil, err
	}
	owners, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*rules.Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.rule(ctx, orgID, r, spaces, owners))
	}
	return out, nil
}

func (s *Rules) List(ctx context.Context, orgID string) ([]*rules.Rule, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: ruleDoctype, Fields: ruleFields, OrderBy: "creation asc"}, 0)
	if err != nil {
		return nil, err
	}
	return s.rules(ctx, orgID, rows)
}

func (s *Rules) Matching(ctx context.Context, orgID, event string) ([]*rules.Rule, error) {
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: ruleDoctype, Fields: ruleFields, OrderBy: "creation asc",
		Filters: []emberops.Filter{emberops.F("trigger_event", event), emberops.F("enabled", 1)}}, 0)
	if err != nil {
		return nil, err
	}
	return s.rules(ctx, orgID, rows)
}

func (s *Rules) Get(ctx context.Context, orgID, id string) (*rules.Rule, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: ruleDoctype, Fields: ruleFields, Limit: 1,
		Filters: []emberops.Filter{emberops.F("hearth_id", id)}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, rules.ErrNotFound
	}
	rs, err := s.rules(ctx, orgID, rows)
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}

// Save creates or replaces a rule. Rules arrive disabled unless the caller
// says otherwise, so they can be rehearsed first.
func (s *Rules) Save(ctx context.Context, orgID, actorID string, r rules.Rule) (*rules.Rule, error) {
	r.Name = strings.TrimSpace(r.Name)
	if err := rules.Validate(r); err != nil {
		return nil, err
	}
	create := r.ID == ""
	if create {
		r.ID = uuid.NewString()
	} else if _, err := s.C.Resolve(ctx, ruleDoctype, r.ID); err != nil {
		return nil, err
	}
	rec := mapping.Rec{"id": r.ID, "name": r.Name, "enabled": r.Enabled,
		"trigger":    map[string]any{"event": r.Trigger.Event, "kind": r.Trigger.Kind},
		"conditions": toAny(r.Conditions), "actions": toAny(r.Actions)}
	if r.SpaceID != nil {
		rec["space_id"] = *r.SpaceID
	}
	doc := mapping.RuleDoc(rec)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if r.SpaceID == nil {
		doc["hearth_space"] = nil
	}
	if r.Trigger.Kind == "" {
		doc["trigger_kind"] = ""
	}
	if _, err := s.C.Upsert(ctx, ruleDoctype, r.ID, doc); err != nil {
		if errors.Is(err, emberops.ErrNotFound) {
			return nil, rules.ErrNotFound
		}
		return nil, err
	}
	r.OrgID = orgID
	if create {
		r.CreatedBy = actorID
	}
	return &r, nil
}

func toAny(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Rules) SetEnabled(ctx context.Context, orgID, id string, on bool) error {
	if _, err := s.Get(ctx, orgID, id); err != nil {
		return err
	}
	v := 0
	if on {
		v = 1
	}
	_, err := s.C.Upsert(ctx, ruleDoctype, id, map[string]any{"enabled": v})
	return err
}

func (s *Rules) Delete(ctx context.Context, orgID, id string) error {
	return notFound(s.C.DeleteByHearthID(ctx, ruleDoctype, id), rules.ErrNotFound)
}

// ---- runs ----

func (s *Rules) ruleName(ctx context.Context, ruleID string) (string, error) {
	n, err := s.C.Resolve(ctx, ruleDoctype, ruleID)
	if err != nil {
		return "", err
	}
	if n == "" {
		return "", rules.ErrNotFound
	}
	return n, nil
}

func realRunID(ruleID string, eventID int64) string {
	return uuid.NewSHA1(runNS, []byte(strings.ToLower(ruleID)+":"+strconv.FormatInt(eventID, 10))).String()
}

// RecordRun writes a run. A real run that is already there is not written
// again and the call answers false.
func (s *Rules) RecordRun(ctx context.Context, r *rules.Run) (bool, error) {
	id := uuid.NewString()
	if !r.DryRun {
		id = realRunID(r.RuleID, r.EventID)
		if n, err := s.C.Resolve(ctx, runDoctype, id); err != nil {
			return false, err
		} else if n != "" {
			return false, nil
		}
	}
	vals := map[string]any{"hearth_rule": emberops.Ref(r.RuleID), "event_id": r.EventID, "entity_name": r.EntityID,
		"matched": b2i(r.Matched), "dry_run": b2i(r.DryRun), "ran_at": s.tsv(time.Now()),
		"reason": r.Reason, "actions_taken": mapping.Dumps(orEmpty(r.ActionsTaken)), "error": r.Error}
	if _, err := s.C.Upsert(ctx, runDoctype, id, vals); err != nil {
		if errors.Is(err, emberops.ErrDuplicate) || errors.Is(err, emberops.ErrConflict) {
			return false, nil // a concurrent delivery got there first
		}
		return false, err
	}
	return true, nil
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (s *Rules) FinishRun(ctx context.Context, ruleID string, eventID int64, taken []string, errText string) error {
	_, err := s.C.Upsert(ctx, runDoctype, realRunID(ruleID, eventID),
		map[string]any{"actions_taken": mapping.Dumps(orEmpty(taken)), "error": errText})
	return err
}

func (s *Rules) ClearDryRuns(ctx context.Context, ruleID string) error {
	name, err := s.ruleName(ctx, ruleID)
	if err != nil {
		return err
	}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: runDoctype, Fields: []string{"name"},
		Filters: []emberops.Filter{emberops.F("hearth_rule", name), emberops.F("dry_run", 1)}}, 0)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.C.Delete(ctx, runDoctype, r.S("name")); err != nil && !errors.Is(err, emberops.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (s *Rules) RecentEvents(ctx context.Context, orgID, event string, limit int) ([]jobs.Event, error) {
	return s.Events.RecentEvents(ctx, orgID, event, limit)
}

func (s *Rules) Runs(ctx context.Context, orgID, ruleID string, limit int) ([]*rules.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var fs []emberops.Filter
	if ruleID != "" {
		name, err := s.ruleName(ctx, ruleID)
		if errors.Is(err, rules.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		fs = append(fs, emberops.F("hearth_rule", name))
	}
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: runDoctype, Limit: limit, OrderBy: "event_id desc, ran_at desc", Filters: fs,
		Fields: []string{"name", "hearth_rule", "event_id", "entity_name", "matched", "dry_run", "ran_at", "reason", "actions_taken", "error"}})
	if err != nil {
		return nil, err
	}
	var rnames []string
	for _, r := range rows {
		rnames = append(rnames, r.S("hearth_rule"))
	}
	ids, err := s.Dir.HearthIDs(ctx, ruleDoctype, rnames)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	if len(rnames) > 0 {
		var in []any
		for _, n := range rnames {
			in = append(in, n)
		}
		rs, err := s.C.List(ctx, emberops.ListQuery{Doctype: ruleDoctype, Fields: []string{"name", "rule_name"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in}}})
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			titles[r.S("name")] = r.S("rule_name")
		}
	}
	out := make([]*rules.Run, 0, len(rows))
	for _, r := range rows {
		run := &rules.Run{RuleID: ids[r.S("hearth_rule")], OrgID: orgID, RuleName: titles[r.S("hearth_rule")],
			EventID: r.I("event_id"), EntityType: "document", EntityID: r.S("entity_name"), Matched: r.B("matched"),
			Reason: r.S("reason"), Error: r.S("error"), DryRun: r.B("dry_run"), RanAt: r.T("ran_at", s.loc())}
		if n, err := strconv.ParseInt(strings.TrimLeft(strings.TrimPrefix(r.S("name"), "HRUN-"), "0"), 10, 64); err == nil {
			run.ID = n
		}
		_ = json.Unmarshal([]byte(r.S("actions_taken")), &run.ActionsTaken)
		out = append(out, run)
	}
	return out, nil
}
