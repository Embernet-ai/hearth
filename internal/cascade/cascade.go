// Package cascade is Hearth's client for Cascade, HotLoop's workflow engine: its runs, workflows, approvals and numbers,
// read and written through the REST API at /api/v1 with one service-account key (hlc_...). Cascade's own editor stays the
// place a workflow is built; Hearth lists, starts, stops, retries, switches on and off, and counts.
package cascade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to Cascade with a key.
type Client struct {
	base string
	key  string
	hc   *http.Client
}

// New builds a client. base is Cascade's address, with no trailing slash.
func New(base, key string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), key: key, hc: &http.Client{Timeout: 20 * time.Second}}
}

// Enabled reports whether an address and a key are set.
func (c *Client) Enabled() bool { return c != nil && c.base != "" && c.key != "" }

// BaseURL is where Cascade's own pages are, for links into the editor.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.base
}

// Workflow is one workflow in a project.
type Workflow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ProjectID string    `json:"projectId"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Execution is one run.
type Execution struct {
	ID           string    `json:"id"`
	WorkflowID   string    `json:"workflowId"`
	WorkflowName string    `json:"workflowName"`
	Status       string    `json:"status"`
	Mode         string    `json:"mode"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	StartedAt    time.Time `json:"startedAt"`
	FinishedAt   time.Time `json:"finishedAt"`
}

// Finished is true once a run has ended one way or another.
func (e Execution) Finished() bool {
	switch e.Status {
	case "queued", "running", "waiting", "":
		return false
	}
	return true
}

// Approval is a question a run stopped to ask a person.
type Approval struct {
	ID           string    `json:"id"`
	ExecutionID  string    `json:"executionId"`
	WorkflowName string    `json:"workflowName"`
	Title        string    `json:"title"`
	Message      string    `json:"message"`
	Status       string    `json:"status"`
	RequestedAt  time.Time `json:"requestedAt"`
	Comment      string    `json:"comment"`
}

// Stat is what a set of runs adds up to.
type Stat struct {
	Total       int     `json:"total"`
	Succeeded   int     `json:"succeeded"`
	Failed      int     `json:"failed"`
	Canceled    int     `json:"canceled"`
	Unfinished  int     `json:"unfinished"`
	FailureRate float64 `json:"failureRate"`
	AvgMillis   int64   `json:"avgMillis"`
}

// Insights is the runs of a window.
type Insights struct {
	Totals    Stat `json:"totals"`
	Workflows []struct {
		WorkflowID   string `json:"workflowId"`
		WorkflowName string `json:"workflowName"`
		Stat
	} `json:"workflows"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.base + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cascade: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("cascade: Cascade refused the key (%d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error
		if msg == "" {
			msg = e.Message
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("cascade: %d %s", resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("cascade: unreadable answer: %w", err)
		}
	}
	return nil
}

type list[T any] struct {
	Items []T `json:"items"`
}

// Workflows lists the workflows the key can read.
func (c *Client) Workflows(ctx context.Context) ([]Workflow, error) {
	var out list[Workflow]
	err := c.do(ctx, http.MethodGet, "/workflows", nil, nil, &out)
	return out.Items, err
}

// Executions lists runs, newest first. status and search narrow it; search matches a run's input, which is how a record's
// runs are found by its id.
func (c *Client) Executions(ctx context.Context, status, search string, limit int) ([]Execution, error) {
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if status != "" {
		q.Set("status", status)
	}
	if search != "" {
		q.Set("search", search)
	}
	var out list[Execution]
	err := c.do(ctx, http.MethodGet, "/executions", q, nil, &out)
	return out.Items, err
}

// Approvals lists the questions runs have asked, newest first. status is pending, approved, rejected or expired.
func (c *Client) Approvals(ctx context.Context, status string, limit int) ([]Approval, error) {
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if status != "" {
		q.Set("status", status)
	}
	var out list[Approval]
	err := c.do(ctx, http.MethodGet, "/approvals", q, nil, &out)
	return out.Items, err
}

// Insights counts runs from the start of the window to now.
func (c *Client) Insights(ctx context.Context, since time.Duration) (*Insights, error) {
	q := url.Values{"from": {time.Now().Add(-since).UTC().Format(time.RFC3339)}}
	var out Insights
	if err := c.do(ctx, http.MethodGet, "/insights", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

var errID = errors.New("cascade: no id given")

func escape(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", errID
	}
	return url.PathEscape(id), nil
}

// Activate switches a workflow's triggers on.
func (c *Client) Activate(ctx context.Context, id string) error {
	return c.post(ctx, "/workflows/%s/activate", id, nil, nil)
}

// Deactivate switches a workflow's triggers off.
func (c *Client) Deactivate(ctx context.Context, id string) error {
	return c.post(ctx, "/workflows/%s/deactivate", id, nil, nil)
}

// Run starts a workflow by hand with these trigger items (one empty item when none) and returns the run's id.
func (c *Client) Run(ctx context.Context, id string, items []map[string]any) (string, error) {
	var out struct {
		ExecutionID string `json:"executionId"`
	}
	body := map[string]any{}
	if len(items) > 0 {
		body["items"] = items
	}
	err := c.post(ctx, "/workflows/%s/run", id, body, &out)
	return out.ExecutionID, err
}

// Stop ends a run that has not finished.
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.post(ctx, "/executions/%s/stop", id, nil, nil)
}

// Retry runs a finished one again from its trigger's items and returns the new run's id.
func (c *Client) Retry(ctx context.Context, id string) (string, error) {
	var out struct {
		ExecutionID string `json:"executionId"`
	}
	err := c.post(ctx, "/executions/%s/retry", id, nil, &out)
	return out.ExecutionID, err
}

func (c *Client) post(ctx context.Context, pattern, id string, body, out any) error {
	e, err := escape(id)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf(pattern, e), nil, body, out)
}
