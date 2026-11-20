// Package mautic is a small client for the Mautic REST API: the calls Hearth's mautic-sync needs and nothing else.
//
// Auth is OAuth2 client credentials against /oauth/v2/token, cached until shortly before it expires. Mautic's routes are https
// only once SAML is on, so every call sends X-Forwarded-Proto: https, which Mautic honours from the cluster range in its
// trusted_proxies (the Hearth pod is inside it). Called from outside the cluster that header is ignored and the calls 404.
package mautic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to one Mautic.
type Client struct {
	base, id, secret string
	hc               *http.Client

	mu    sync.Mutex
	token string
	exp   time.Time
}

// New builds a client. base is the Mautic root, for example http://mautic.marketing.svc.cluster.local.
func New(base, clientID, clientSecret string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), id: clientID, secret: clientSecret,
		hc: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is a refusal from Mautic.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300]
	}
	return fmt.Sprintf("mautic: %s %s answered %d: %s", e.Method, e.Path, e.Status, b)
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.exp.Add(-30*time.Second)) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.id}, "client_secret": {c.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/oauth/v2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("mautic: token: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out.AccessToken == "" {
		return "", &APIError{Status: resp.StatusCode, Method: "POST", Path: "/oauth/v2/token", Body: redact(string(b))}
	}
	c.token = out.AccessToken
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	c.exp = time.Now().Add(ttl)
	return c.token, nil
}

// redact keeps a token endpoint's refusal readable without carrying anything that looks like a credential.
func redact(s string) string {
	if i := strings.Index(s, "access_token"); i >= 0 {
		return s[:i] + "access_token ..."
	}
	return s
}

// do sends one API call. A nil out discards the body.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	u := c.base + "/api" + path
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
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Forwarded-Proto", "https")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("mautic: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(b)}
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("mautic: %s %s: unreadable answer: %w", method, path, err)
	}
	return nil
}

// IsNotFound reports a 404 from Mautic.
func IsNotFound(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.Status == http.StatusNotFound
}

// -----------------------------------------------------------------------------
// Contacts and companies
// -----------------------------------------------------------------------------

// Fields are Mautic field values by alias (firstname, email, hearth_id), plus the keys owner and tags.
type Fields map[string]any

// idOf reads the id out of {"contact": {"id": 5}} and its kin. Mautic writes ids as numbers in some answers and strings in
// others, so both are accepted.
func idOf(raw json.RawMessage) (int64, error) {
	var o struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		var s struct {
			ID string `json:"id"`
		}
		if err2 := json.Unmarshal(raw, &s); err2 != nil {
			return 0, err
		}
		return strconv.ParseInt(s.ID, 10, 64)
	}
	return o.ID.Int64()
}

// UpsertContact makes or updates a contact. With id 0 it posts to /contacts/new, which Mautic turns into an update of the
// contact that already holds the same email address, so a contact somebody had already put in Mautic is adopted, not doubled.
func (c *Client) UpsertContact(ctx context.Context, id int64, f Fields) (int64, error) {
	var out struct {
		Contact json.RawMessage `json:"contact"`
	}
	var err error
	if id == 0 {
		err = c.do(ctx, http.MethodPost, "/contacts/new", nil, f, &out)
	} else {
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/contacts/%d/edit", id), nil, f, &out)
	}
	if err != nil {
		return 0, err
	}
	return idOf(out.Contact)
}

// UpsertCompany does the same for a company.
func (c *Client) UpsertCompany(ctx context.Context, id int64, f Fields) (int64, error) {
	var out struct {
		Company json.RawMessage `json:"company"`
	}
	var err error
	if id == 0 {
		err = c.do(ctx, http.MethodPost, "/companies/new", nil, f, &out)
	} else {
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/companies/%d/edit", id), nil, f, &out)
	}
	if err != nil {
		return 0, err
	}
	return idOf(out.Company)
}

// LinkCompany puts a contact on a company, and UnlinkCompany takes it off.
func (c *Client) LinkCompany(ctx context.Context, companyID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/companies/%d/contact/%d/add", companyID, contactID), nil, nil, nil)
}

// UnlinkCompany takes a contact off a company.
func (c *Client) UnlinkCompany(ctx context.Context, companyID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/companies/%d/contact/%d/remove", companyID, contactID), nil, nil, nil)
}

// DNC reasons, as Mautic numbers them.
const (
	DNCUnsubscribed = 1
	DNCBounced      = 2
	DNCManual       = 3
)

// AddDoNotContact marks a contact do-not-contact for email.
func (c *Client) AddDoNotContact(ctx context.Context, contactID int64, reason int, comment string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/contacts/%d/dnc/email/add", contactID), nil,
		map[string]any{"reason": reason, "comments": comment}, nil)
}

// -----------------------------------------------------------------------------
// Segments, fields, users
// -----------------------------------------------------------------------------

// Segments lists every segment by alias.
func (c *Client) Segments(ctx context.Context) (map[string]int64, error) {
	var out struct {
		Lists json.RawMessage `json:"lists"`
	}
	if err := c.do(ctx, http.MethodGet, "/segments", url.Values{"limit": {"1000"}}, nil, &out); err != nil {
		return nil, err
	}
	// An empty list is [] and a full one is an object keyed by id.
	var lists map[string]struct {
		ID    json.Number `json:"id"`
		Alias string      `json:"alias"`
	}
	m := map[string]int64{}
	if json.Unmarshal(out.Lists, &lists) != nil {
		return m, nil
	}
	for _, l := range lists {
		id, _ := l.ID.Int64()
		m[l.Alias] = id
	}
	return m, nil
}

// CreateSegment makes a segment with no filters: membership is the sync's to keep.
func (c *Client) CreateSegment(ctx context.Context, name, alias, description string) (int64, error) {
	var out struct {
		List json.RawMessage `json:"list"`
	}
	if err := c.do(ctx, http.MethodPost, "/segments/new", nil, map[string]any{
		"name": name, "alias": alias, "description": description, "isPublished": true, "isGlobal": true, "filters": []any{},
	}, &out); err != nil {
		return 0, err
	}
	return idOf(out.List)
}

// SegmentAdd and SegmentRemove move one contact in or out of a segment.
func (c *Client) SegmentAdd(ctx context.Context, segmentID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/segments/%d/contact/%d/add", segmentID, contactID), nil, nil, nil)
}

// SegmentRemove takes a contact out of a segment.
func (c *Client) SegmentRemove(ctx context.Context, segmentID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/segments/%d/contact/%d/remove", segmentID, contactID), nil, nil, nil)
}

// EnsureField makes a custom text field if the object (contact or company) does not have it yet.
func (c *Client) EnsureField(ctx context.Context, object, alias, label string) error {
	var out struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := c.do(ctx, http.MethodGet, "/fields/"+object, url.Values{"search": {alias}, "limit": {"100"}}, nil, &out); err != nil {
		return err
	}
	var have map[string]struct {
		Alias string `json:"alias"`
	}
	_ = json.Unmarshal(out.Fields, &have) // [] when there are none
	for _, f := range have {
		if f.Alias == alias {
			return nil
		}
	}
	return c.do(ctx, http.MethodPost, "/fields/"+object+"/new", nil, map[string]any{
		"label": label, "alias": alias, "type": "text", "isPublished": true, "isPubliclyUpdatable": false,
		"isUniqueIdentifer": false, "group": "core",
	}, nil)
}

// Users maps each Mautic user's email address, lower case, to the user id.
func (c *Client) Users(ctx context.Context) (map[string]int64, error) {
	var out struct {
		Users []struct {
			ID    json.Number `json:"id"`
			Email string      `json:"email"`
		} `json:"users"`
	}
	if err := c.do(ctx, http.MethodGet, "/users", url.Values{"limit": {"500"}}, nil, &out); err != nil {
		return nil, err
	}
	m := map[string]int64{}
	for _, u := range out.Users {
		id, _ := u.ID.Int64()
		m[strings.ToLower(strings.TrimSpace(u.Email))] = id
	}
	return m, nil
}

// -----------------------------------------------------------------------------
// What happened: the stats tables
// -----------------------------------------------------------------------------

// Row is one stats row. Mautic writes every value as a string or null.
type Row map[string]string

// ID is the row's id.
func (r Row) ID() int64 { n, _ := strconv.ParseInt(r["id"], 10, 64); return n }

// Int reads a numeric column.
func (r Row) Int(k string) int64 { n, _ := strconv.ParseInt(r[k], 10, 64); return n }

// Time reads a UTC timestamp column ("2026-11-20 01:02:03"); the zero time when it is empty.
func (r Row) Time(k string) time.Time {
	v := r[k]
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05-07:00"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Where is one filter on a stats table.
type Where struct{ Col, Expr, Val string }

// Stats reads up to limit rows of a stats table that match every filter, oldest first by the column named in order (id when empty).
func (c *Client) Stats(ctx context.Context, table, order string, limit int, filters ...Where) ([]Row, error) {
	if order == "" {
		order = "id"
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}, "order[0][col]": {order}, "order[0][dir]": {"asc"}}
	for i, w := range filters {
		p := fmt.Sprintf("where[%d]", i)
		q.Set(p+"[col]", w.Col)
		q.Set(p+"[expr]", w.Expr)
		q.Set(p+"[val]", w.Val)
	}
	var out struct {
		Stats []map[string]any `json:"stats"`
	}
	if err := c.do(ctx, http.MethodGet, "/stats/"+table, q, nil, &out); err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(out.Stats))
	for _, m := range out.Stats {
		r := Row{}
		for k, v := range m {
			switch t := v.(type) {
			case nil:
				r[k] = ""
			case string:
				r[k] = t
			case float64:
				r[k] = strconv.FormatFloat(t, 'f', -1, 64)
			default:
				b, _ := json.Marshal(t)
				r[k] = string(b)
			}
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// Names answers what an email or a form is called, for the line on the timeline.
type Names struct {
	Name, Subject string
}

// Email reads an email's name and subject.
func (c *Client) Email(ctx context.Context, id int64) (Names, error) {
	var out struct {
		Email struct {
			Name    string `json:"name"`
			Subject string `json:"subject"`
		} `json:"email"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/emails/%d", id), nil, nil, &out); err != nil {
		return Names{}, err
	}
	return Names{Name: out.Email.Name, Subject: out.Email.Subject}, nil
}

// Form reads a form's name.
func (c *Client) Form(ctx context.Context, id int64) (string, error) {
	var out struct {
		Form struct {
			Name string `json:"name"`
		} `json:"form"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/forms/%d", id), nil, nil, &out); err != nil {
		return "", err
	}
	return out.Form.Name, nil
}
