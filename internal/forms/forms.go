// Package forms is the Forms area: forms, surveys and their results, read and write, through the management API of the
// forms app the installation runs. Hearth is the interface; the app keeps the forms and the answers.
package forms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is a form that is not there.
var ErrNotFound = errors.New("forms: no such form")

// Client talks to the management API with an API key that may read and write.
type Client struct {
	base string
	key  string
	hc   *http.Client
}

// New builds a client. base is the app's address, with no trailing slash.
func New(base, apiKey string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), key: apiKey, hc: &http.Client{Timeout: 30 * time.Second}}
}

// Enabled reports whether an address and a key are set.
func (c *Client) Enabled() bool { return c != nil && c.base != "" && c.key != "" }

// Status is a form's state: draft, scheduled, inProgress (live), paused or completed.
type Status string

const (
	Draft      Status = "draft"
	Scheduled  Status = "scheduled"
	InProgress Status = "inProgress"
	Paused     Status = "paused"
	Completed  Status = "completed"
)

// Label is what a person reads for a status.
func (s Status) Label() string {
	switch s {
	case InProgress:
		return "Live"
	case Scheduled:
		return "Scheduled"
	case Paused:
		return "Paused"
	case Completed:
		return "Closed"
	}
	return "Draft"
}

// Question is one question as shown and edited.
type Question struct {
	ID       string
	Type     string // openText, multipleChoiceSingle, multipleChoiceMulti, rating, nps
	Headline string
	Required bool
	Choices  []string
}

// Form is one form with its state.
type Form struct {
	ID        string
	Name      string
	Type      string
	Status    Status
	Created   string
	Updated   string
	PublishOn string // when a scheduled form goes live
	CloseOn   string // when it closes by itself
	Questions []Question
}

// Input is what a person fills in to make or change a form.
type Input struct {
	Name      string
	Questions []Question
	Status    Status
	PublishOn string
	CloseOn   string
}

// Response is one person's answers.
type Response struct {
	ID       string
	Created  string
	Finished bool
	Answers  map[string]any
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.base + "/api/v1/management" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
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
	req.Header.Set("x-api-key", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("forms: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("forms: the forms app refused the key (%d): it needs read and write on the workspace", resp.StatusCode)
	case resp.StatusCode >= 300:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("forms: %d %s", resp.StatusCode, strings.TrimSpace(e.Message))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Workspace reads the id forms are made in.
func (c *Client) Workspace(ctx context.Context) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, "/me", nil, nil, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

type rawSurvey struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Status    Status          `json:"status"`
	Created   string          `json:"createdAt"`
	Updated   string          `json:"updatedAt"`
	PublishOn *string         `json:"publishOn"`
	CloseOn   *string         `json:"closeOn"`
	Questions json.RawMessage `json:"questions"`
}

func (r rawSurvey) form() Form {
	f := Form{ID: r.ID, Name: r.Name, Type: r.Type, Status: r.Status, Created: r.Created, Updated: r.Updated}
	if r.PublishOn != nil {
		f.PublishOn = *r.PublishOn
	}
	if r.CloseOn != nil {
		f.CloseOn = *r.CloseOn
	}
	var qs []struct {
		ID       string            `json:"id"`
		Type     string            `json:"type"`
		Headline map[string]string `json:"headline"`
		Required bool              `json:"required"`
		Choices  []struct {
			Label map[string]string `json:"label"`
		} `json:"choices"`
	}
	if json.Unmarshal(r.Questions, &qs) == nil {
		for _, q := range qs {
			qq := Question{ID: q.ID, Type: q.Type, Headline: q.Headline["default"], Required: q.Required}
			for _, ch := range q.Choices {
				qq.Choices = append(qq.Choices, ch.Label["default"])
			}
			f.Questions = append(f.Questions, qq)
		}
	}
	return f
}

// Forms lists every form.
func (c *Client) Forms(ctx context.Context) ([]Form, error) {
	var out struct {
		Data []rawSurvey `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/surveys", nil, nil, &out); err != nil {
		return nil, err
	}
	forms := make([]Form, 0, len(out.Data))
	for _, s := range out.Data {
		forms = append(forms, s.form())
	}
	return forms, nil
}

// Get reads one form.
func (c *Client) Get(ctx context.Context, id string) (*Form, error) {
	var out struct {
		Data rawSurvey `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/surveys/"+url.PathEscape(id), nil, nil, &out); err != nil {
		return nil, err
	}
	f := out.Data.form()
	return &f, nil
}

func questionBody(q Question, n int) map[string]any {
	id := q.ID
	if id == "" {
		id = "q" + strconv.Itoa(n+1)
	}
	b := map[string]any{"id": id, "type": q.Type, "headline": map[string]string{"default": q.Headline}, "required": q.Required}
	switch q.Type {
	case "multipleChoiceSingle", "multipleChoiceMulti":
		choices := make([]map[string]any, 0, len(q.Choices))
		for i, ch := range q.Choices {
			choices = append(choices, map[string]any{"id": fmt.Sprintf("%s-c%d", id, i+1), "label": map[string]string{"default": ch}})
		}
		b["choices"] = choices
		b["shuffleOption"] = "none"
	case "rating":
		b["range"], b["scale"] = 5, "star"
	case "nps":
		// Needs nothing beyond the headline.
	default:
		b["type"], b["inputType"], b["charLimit"] = "openText", "text", map[string]bool{"enabled": false}
	}
	return b
}

func (in Input) body(workspace string) map[string]any {
	qs := make([]map[string]any, 0, len(in.Questions))
	for i, q := range in.Questions {
		if strings.TrimSpace(q.Headline) != "" {
			qs = append(qs, questionBody(q, i))
		}
	}
	status := in.Status
	if status == "" {
		status = Draft
	}
	b := map[string]any{"name": in.Name, "type": "link", "status": status, "questions": qs}
	if workspace != "" {
		b["environmentId"] = workspace
	}
	if in.PublishOn != "" {
		b["publishOn"] = in.PublishOn
	}
	if in.CloseOn != "" {
		b["closeOn"] = in.CloseOn
	}
	return b
}

// Create makes a form and returns its id.
func (c *Client) Create(ctx context.Context, in Input) (string, error) {
	if strings.TrimSpace(in.Name) == "" {
		return "", errors.New("forms: a form needs a name")
	}
	ws, err := c.Workspace(ctx)
	if err != nil {
		return "", err
	}
	var out struct {
		Data rawSurvey `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/surveys", nil, in.body(ws), &out); err != nil {
		return "", err
	}
	return out.Data.ID, nil
}

// Update changes a form's name, questions, state and dates.
func (c *Client) Update(ctx context.Context, id string, in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("forms: a form needs a name")
	}
	return c.do(ctx, http.MethodPut, "/surveys/"+url.PathEscape(id), nil, in.body(""), nil)
}

// SetStatus publishes (InProgress), pauses, closes or returns a form to draft, without touching its questions.
func (c *Client) SetStatus(ctx context.Context, id string, s Status) error {
	return c.do(ctx, http.MethodPut, "/surveys/"+url.PathEscape(id), nil, map[string]any{"status": s}, nil)
}

// Schedule sets the day a form goes live and, optionally, the day it closes, as dates ("2026-12-25"). The forms app keeps
// these as whole days: it goes live at the start of that day in its own zone, whatever time of day was sent, and shows the
// form as scheduled while it waits, so the form stays a draft until then. An empty date clears that one.
func (c *Client) Schedule(ctx context.Context, id string, publishDate, closeDate string) error {
	body := map[string]any{}
	for k, v := range map[string]string{"publishOn": publishDate, "closeOn": closeDate} {
		if v == "" {
			body[k] = nil
			continue
		}
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			return fmt.Errorf("forms: %q is not a date like 2026-12-25", v)
		}
		// Midday UTC is the same calendar day in every zone from UTC-11 to UTC+11, so the day sent is the day kept.
		body[k] = d.Add(12 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	}
	return c.do(ctx, http.MethodPut, "/surveys/"+url.PathEscape(id), nil, body, nil)
}

// Date turns a time the forms app holds into the day it means: the app keeps the start of a day in its own zone, so half
// a day is added before reading the date.
func Date(held string) string {
	t, err := time.Parse(time.RFC3339, held)
	if err != nil {
		return ""
	}
	return t.Add(12 * time.Hour).UTC().Format("2006-01-02")
}

// IsScheduled reports a draft that is waiting for its time.
func (f Form) IsScheduled(now time.Time) bool {
	if f.Status != Draft || f.PublishOn == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, f.PublishOn)
	return err == nil && t.After(now)
}

// Delete removes a form and its answers.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/surveys/"+url.PathEscape(id), nil, nil, nil)
}

// Responses lists the answers to a form, newest first.
func (c *Client) Responses(ctx context.Context, formID string, limit, skip int) ([]Response, error) {
	var out struct {
		Data []struct {
			ID       string         `json:"id"`
			Created  string         `json:"createdAt"`
			Finished bool           `json:"finished"`
			Data     map[string]any `json:"data"`
		} `json:"data"`
	}
	q := url.Values{"surveyId": {formID}, "limit": {strconv.Itoa(limit)}, "skip": {strconv.Itoa(skip)}}
	if err := c.do(ctx, http.MethodGet, "/responses", q, nil, &out); err != nil {
		return nil, err
	}
	rs := make([]Response, 0, len(out.Data))
	for _, r := range out.Data {
		rs = append(rs, Response{ID: r.ID, Created: r.Created, Finished: r.Finished, Answers: r.Data})
	}
	return rs, nil
}

// DeleteResponse removes one person's answers from a form.
func (c *Client) DeleteResponse(ctx context.Context, responseID string) error {
	return c.do(ctx, http.MethodDelete, "/responses/"+url.PathEscape(responseID), nil, nil, nil)
}

// AllResponses reads every answer to a form, a page at a time, up to five thousand.
func (c *Client) AllResponses(ctx context.Context, formID string) ([]Response, error) {
	var all []Response
	for skip := 0; skip < 5000; skip += 200 {
		page, err := c.Responses(ctx, formID, 200, skip)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 200 {
			break
		}
	}
	return all, nil
}

// Results is what a form has collected so far.
type Results struct {
	Total     int
	Finished  int
	Responses []Response
}

// Results counts and lists a form's answers, up to the most recent 200.
func (c *Client) Results(ctx context.Context, formID string) (*Results, error) {
	rs, err := c.Responses(ctx, formID, 200, 0)
	if err != nil {
		return nil, err
	}
	out := &Results{Total: len(rs), Responses: rs}
	for _, r := range rs {
		if r.Finished {
			out.Finished++
		}
	}
	return out, nil
}

// ---- the question editor's text -----------------------------------------------------------------------------------

var kinds = map[string]string{"text": "openText", "choice": "multipleChoiceSingle", "multi": "multipleChoiceMulti", "rating": "rating", "nps": "nps"}

// ParseQuestions reads the editor's text, one question per line:
//
//	[text] What did you think?
//	[choice] Pick one :: Yes; No
//	[multi] Pick some :: A; B; C
//	[rating] How was it?
//	[nps] Would you recommend us?
//
// A line with no [kind] is a text question, and one ending in * is required. Blank lines are skipped.
func ParseQuestions(text string) []Question {
	var out []Question
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		q := Question{Type: "openText"}
		if strings.HasPrefix(line, "[") {
			if end := strings.Index(line, "]"); end > 0 {
				if t, ok := kinds[strings.ToLower(strings.TrimSpace(line[1:end]))]; ok {
					q.Type = t
				}
				line = strings.TrimSpace(line[end+1:])
			}
		}
		if head, rest, ok := strings.Cut(line, "::"); ok {
			line = strings.TrimSpace(head)
			for _, ch := range strings.Split(rest, ";") {
				if ch = strings.TrimSpace(ch); ch != "" {
					q.Choices = append(q.Choices, ch)
				}
			}
		}
		if strings.HasSuffix(line, "*") {
			q.Required, line = true, strings.TrimSpace(strings.TrimSuffix(line, "*"))
		}
		if line == "" {
			continue
		}
		q.Headline = line
		if (q.Type == "multipleChoiceSingle" || q.Type == "multipleChoiceMulti") && len(q.Choices) < 2 {
			q.Type = "openText"
			q.Choices = nil
		}
		out = append(out, q)
	}
	return out
}

// QuestionsText is the editor's text for a form's questions, the inverse of ParseQuestions.
func QuestionsText(qs []Question) string {
	rev := map[string]string{}
	for k, v := range kinds {
		rev[v] = k
	}
	var b strings.Builder
	for _, q := range qs {
		b.WriteString("[" + rev[q.Type] + "] " + q.Headline)
		if q.Required {
			b.WriteString(" *")
		}
		if len(q.Choices) > 0 {
			b.WriteString(" :: " + strings.Join(q.Choices, "; "))
		}
		b.WriteString("\n")
	}
	return b.String()
}
