package web

// Automation: Cascade, HotLoop's workflow engine, from inside Hearth. The Sales team and super admins see the workflows
// with their state and last run, start one by hand with an input, switch one on or off, read the runs (and retry or stop
// one), and read the questions runs have stopped to ask a person. The numbers also sit on the dashboard. Building a
// workflow stays in Cascade's own editor, which the page links to.
//
// Cascade's REST API has no route to answer an approval, so the card lists them and sends the person to Cascade to
// answer; it does not pretend otherwise.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/cascade"
)

// AutomationView is the Automation page.
type AutomationView struct {
	Configured bool
	Notice     string
	// Record narrows the runs to the ones whose input mentions this id (a company, person or deal).
	Record     string
	Status     string
	EditorURL  string
	Workflows  []AutomationWorkflow
	Executions []AutomationRun
	Approvals  []cascade.Approval
	Day, Week  cascade.Stat
	HasNumbers bool
}

// AutomationWorkflow is a workflow with its last run.
type AutomationWorkflow struct {
	cascade.Workflow
	LastStatus string
	LastAt     string
}

// AutomationRun is one run with its times written for a person.
type AutomationRun struct {
	cascade.Execution
	When     string
	Duration string
	CanStop  bool
}

// automationAllowed is whether this person may use Automation: Cascade is connected and they are on the Sales team or a
// super admin.
func (s *Server) automationAllowed(ctx context.Context, id *auth.Identity) bool {
	if !s.cascade.Enabled() || s.sales == nil || id == nil {
		return false
	}
	return s.worksSales(ctx, id)
}

func (s *Server) automationGate(w http.ResponseWriter, r *http.Request) bool {
	if !s.cascade.Enabled() {
		s.fail(w, r, http.StatusNotFound, "Automation is not set up on this installation")
		return false
	}
	if !s.automationAllowed(r.Context(), auth.From(r.Context())) {
		s.fail(w, r, http.StatusNotFound, "no such page")
		return false
	}
	return true
}

func (s *Server) getAutomation(w http.ResponseWriter, r *http.Request) {
	if !s.automationGate(w, r) {
		return
	}
	q := r.URL.Query()
	v := &AutomationView{Configured: true, Notice: q.Get("notice"), Record: strings.TrimSpace(q.Get("record")), Status: q.Get("status"), EditorURL: s.cascade.BaseURL()}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	runs, err := s.cascade.Executions(ctx, v.Status, v.Record, 50)
	if err != nil {
		v.Notice = areaErr(err)
	}
	last := map[string]cascade.Execution{}
	for _, e := range runs {
		if _, ok := last[e.WorkflowID]; !ok {
			last[e.WorkflowID] = e
		}
		v.Executions = append(v.Executions, runLine(e))
	}
	wfs, err := s.cascade.Workflows(ctx)
	if err != nil && v.Notice == "" {
		v.Notice = areaErr(err)
	}
	for _, wf := range wfs {
		l := AutomationWorkflow{Workflow: wf}
		if e, ok := last[wf.ID]; ok {
			l.LastStatus, l.LastAt = e.Status, whenLine(e)
		}
		v.Workflows = append(v.Workflows, l)
	}
	if pend, err := s.cascade.Approvals(ctx, "pending", 25); err == nil {
		v.Approvals = pend
	}
	if d, err := s.cascade.Insights(ctx, 24*time.Hour); err == nil {
		if w7, err := s.cascade.Insights(ctx, 7*24*time.Hour); err == nil {
			v.Day, v.Week, v.HasNumbers = d.Totals, w7.Totals, true
		}
	}
	s.render(w, r, "automation", page{Title: "Automation", AutomationArea: v})
}

func whenLine(e cascade.Execution) string {
	t := e.CreatedAt
	if !e.StartedAt.IsZero() {
		t = e.StartedAt
	}
	return t.Local().Format("2006-01-02 15:04")
}

func runLine(e cascade.Execution) AutomationRun {
	r := AutomationRun{Execution: e, When: whenLine(e), CanStop: !e.Finished()}
	if !e.StartedAt.IsZero() && !e.FinishedAt.IsZero() {
		r.Duration = e.FinishedAt.Sub(e.StartedAt).Round(time.Millisecond).String()
	}
	return r
}

func (s *Server) postAutomationRun(w http.ResponseWriter, r *http.Request) {
	if !s.automationGate(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	var items []map[string]any
	if in := strings.TrimSpace(r.FormValue("input")); in != "" {
		var one map[string]any
		if err := json.Unmarshal([]byte(in), &one); err != nil {
			areaBack(w, r, "/automation", nil, "The input has to be a JSON object like {\"deal\": \"123\"}.", nil)
			return
		}
		items = []map[string]any{one}
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	exec, err := s.cascade.Run(ctx, r.PathValue("id"), items)
	if err == nil {
		s.log.InfoContext(ctx, "automation run started", "workflow", r.PathValue("id"), "execution", exec, "by", auth.From(r.Context()).PrincipalID)
	}
	areaBack(w, r, "/automation", nil, "Started.", err)
}

func (s *Server) postAutomationActive(w http.ResponseWriter, r *http.Request) {
	if !s.automationGate(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	on := r.FormValue("active") == "1"
	var err error
	msg := "Switched off."
	if on {
		msg = "Switched on."
		err = s.cascade.Activate(ctx, r.PathValue("id"))
	} else {
		err = s.cascade.Deactivate(ctx, r.PathValue("id"))
	}
	s.autoCache.clear()
	areaBack(w, r, "/automation", nil, msg, err)
}

func (s *Server) postAutomationRetry(w http.ResponseWriter, r *http.Request) {
	if !s.automationGate(w, r) {
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	_, err := s.cascade.Retry(ctx, r.PathValue("id"))
	areaBack(w, r, "/automation", nil, "Started again.", err)
}

func (s *Server) postAutomationStop(w http.ResponseWriter, r *http.Request) {
	if !s.automationGate(w, r) {
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	areaBack(w, r, "/automation", nil, "Stopped.", s.cascade.Stop(ctx, r.PathValue("id")))
}

// autoCache holds the dashboard section for two minutes: it is the same for every Sales viewer.
type autoCache struct {
	mu  sync.Mutex
	at  time.Time
	sec *teamSection
}

func (c *autoCache) clear() { c.mu.Lock(); c.sec = nil; c.mu.Unlock() }

// automationSection is Cascade's numbers on the dashboard, nil for somebody who may not use Automation.
func (s *Server) automationSection(ctx context.Context, id *auth.Identity) *teamSection {
	if !s.automationAllowed(ctx, id) {
		return nil
	}
	c := &s.autoCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sec != nil && time.Since(c.at) < 2*time.Minute {
		return c.sec
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sec := &teamSection{Key: "automation", Title: "Automation", Href: "/automation"}
	day, err := s.cascade.Insights(rctx, 24*time.Hour)
	if err != nil {
		sec.Notice = "Automation did not answer: " + areaErr(err)
		return sec
	}
	week, err := s.cascade.Insights(rctx, 7*24*time.Hour)
	if err != nil {
		sec.Notice = "Automation did not answer: " + areaErr(err)
		return sec
	}
	pending, _ := s.cascade.Approvals(rctx, "pending", 100)
	wfs, _ := s.cascade.Workflows(rctx)
	active := 0
	for _, w := range wfs {
		if w.Active {
			active++
		}
	}
	d, w := day.Totals, week.Totals
	sec.Tiles = []statTile{
		{Label: "Runs", Value: num(int64(d.Total)), Detail: pct(int64(d.Succeeded), int64(d.Succeeded+d.Failed)) + " succeeded", Period: "last 24 hours", Href: "/automation"},
		{Label: "Failed", Value: num(int64(w.Failed)), Detail: num(int64(d.Failed)) + " today", Period: "last 7 days", Href: "/automation?status=failed", Tone: toneIf(w.Failed > 0)},
		{Label: "Waiting on a person", Value: num(int64(len(pending))), Detail: num(int64(d.Unfinished)) + " running", Period: "approvals open", Href: "/automation", Tone: toneIf(len(pending) > 0)},
		{Label: "Workflows on", Value: num(int64(active)), Period: fmt.Sprintf("of %d", len(wfs)), Href: "/automation"},
	}
	for _, wf := range week.Workflows {
		if len(sec.Rows) >= 5 {
			break
		}
		if wf.Failed == 0 {
			continue
		}
		sec.Rows = append(sec.Rows, teamRow{Title: wf.WorkflowName, Detail: fmt.Sprintf("%d failed of %d runs", wf.Failed, wf.Total),
			Href: "/automation?status=failed&workflow=" + url.QueryEscape(wf.WorkflowID)})
	}
	c.sec, c.at = sec, time.Now()
	return sec
}
