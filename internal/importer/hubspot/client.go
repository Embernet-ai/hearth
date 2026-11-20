// Package hubspot brings what changed in HubSpot since the last import into Hearth's CRM.
//
// It reads HubSpot's own API with a private app token and writes only through Hearth's CRM service, so every record it
// touches gets the grants, events and audit row a person's edit would. See run.go for what is matched and what is never
// overwritten.
package hubspot

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

// DefaultBase is HubSpot's API.
const DefaultBase = "https://api.hubapi.com"

// SearchCap is how many results one HubSpot search will ever page through. After this many the search has to be asked
// again from the last modified time it reached. A variable so a test can make the window small.
var SearchCap = 10000

// Object is one HubSpot record.
type Object struct {
	ID       string
	Props    map[string]string
	Modified time.Time
	// Assoc is filled by Associations: the HubSpot ids of the records this one points at, by kind.
	Assoc map[string][]string
}

// APIError is HubSpot refusing a call.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("hubspot answered %d: %s", e.Status, e.Message) }

// ErrScope says the token is not allowed to make a call, which is HubSpot's 403 for a private app without the scope.
var ErrScope = errors.New("the HubSpot token is not granted the scope for this")

// Client talks to HubSpot.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
	// Pace is the wait between calls, well inside HubSpot's burst limit.
	Pace time.Duration
	// Backoff is the first wait after a 429 or a 5xx, doubled each retry. A 429's Retry-After wins when it is longer.
	Backoff time.Duration
	// Retries is how many more times a throttled or failed call is tried.
	Retries int
	// PageSize is how many a search returns per page; HubSpot's maximum is 100.
	PageSize int

	last time.Time
}

// NewClient makes a client for the real API.
func NewClient(token string) *Client {
	return &Client{Base: DefaultBase, Token: token, HTTP: &http.Client{Timeout: 60 * time.Second},
		Pace: 120 * time.Millisecond, Backoff: 2 * time.Second, Retries: 5, PageSize: 100}
}

func (c *Client) pageSize() int {
	if c.PageSize <= 0 || c.PageSize > 100 {
		return 100
	}
	return c.PageSize
}

// modProp is the property HubSpot keeps an object's last change in. Contacts call it something else.
func modProp(object string) string {
	if object == "contacts" {
		return "lastmodifieddate"
	}
	return "hs_lastmodifieddate"
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	wait := c.Backoff
	for attempt := 0; ; attempt++ {
		if d := c.Pace - time.Since(c.last); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		c.last = time.Now()
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.Base, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.HTTP.Do(req)
		var raw []byte
		status := 0
		retryAfter := time.Duration(0)
		if err == nil {
			status = res.StatusCode
			raw, _ = io.ReadAll(io.LimitReader(res.Body, 64<<20))
			res.Body.Close()
			if s := res.Header.Get("Retry-After"); s != "" {
				if n, e := strconv.Atoi(s); e == nil {
					retryAfter = time.Duration(n) * time.Second
				}
			}
		}
		switch {
		case err == nil && status >= 200 && status < 300:
			if out == nil || len(raw) == 0 {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("hubspot: read the answer to %s: %w", path, err)
			}
			return nil
		case err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden):
			return fmt.Errorf("%w (%s %s): %s", ErrScope, method, path, message(raw))
		case err == nil && status != http.StatusTooManyRequests && status < 500:
			return &APIError{Status: status, Message: message(raw)}
		}
		if attempt >= c.Retries {
			if err != nil {
				return fmt.Errorf("hubspot: %s %s: %w", method, path, err)
			}
			return &APIError{Status: status, Message: message(raw)}
		}
		if retryAfter > wait {
			wait = retryAfter
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		wait *= 2
	}
}

func message(raw []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

type wireObject struct {
	ID         string            `json:"id"`
	Properties map[string]string `json:"properties"`
}

func toObject(object string, w wireObject) Object {
	o := Object{ID: w.ID, Props: w.Properties, Assoc: map[string][]string{}}
	if o.Props == nil {
		o.Props = map[string]string{}
	}
	o.Modified = parseTime(o.Props[modProp(object)])
	return o
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(n).UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

type searchPage struct {
	Total   int          `json:"total"`
	Results []wireObject `json:"results"`
	Paging  struct {
		Next struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

func (c *Client) search(ctx context.Context, object string, props []string, since time.Time, after string, limit int) (*searchPage, error) {
	body := map[string]any{
		"filterGroups": []any{map[string]any{"filters": []any{map[string]any{
			"propertyName": modProp(object), "operator": "GTE", "value": strconv.FormatInt(since.UnixMilli(), 10)}}}},
		"sorts":      []any{map[string]any{"propertyName": modProp(object), "direction": "ASCENDING"}},
		"properties": append(append([]string{}, props...), modProp(object)),
		"limit":      limit,
	}
	if after != "" {
		body["after"] = after
	}
	var p searchPage
	if err := c.do(ctx, http.MethodPost, "/crm/v3/objects/"+object+"/search", body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Modified calls each, oldest change first, for every record of an object changed at or after since. HubSpot only
// pages through 10,000 results of one search, so a bigger set is read in windows that each start at the last change the
// one before reached; a record on the edge of two windows is given once.
func (c *Client) Modified(ctx context.Context, object string, props []string, since time.Time, each func(Object) error) error {
	seen := map[string]bool{}
	cursor := since
	for {
		after, n := "", 0
		var reached time.Time
		fresh := 0
		for {
			p, err := c.search(ctx, object, props, cursor, after, c.pageSize())
			if err != nil {
				return err
			}
			for _, w := range p.Results {
				n++
				o := toObject(object, w)
				reached = o.Modified
				if seen[o.ID] {
					continue
				}
				seen[o.ID] = true
				fresh++
				if err := each(o); err != nil {
					return err
				}
			}
			after = p.Paging.Next.After
			if after == "" || len(p.Results) == 0 {
				return nil
			}
			if n+c.pageSize() > SearchCap {
				break
			}
		}
		if fresh == 0 {
			return fmt.Errorf("hubspot: %d %s share one modified time and cannot be paged past it", SearchCap, object)
		}
		cursor = reached
	}
}

// CountModified is how many records of an object changed at or after since, without reading them.
func (c *Client) CountModified(ctx context.Context, object string, since time.Time) (int, error) {
	p, err := c.search(ctx, object, nil, since, "", 1)
	if err != nil {
		return 0, err
	}
	return p.Total, nil
}

// Get reads records by id, in batches.
func (c *Client) Get(ctx context.Context, object string, props, ids []string) ([]Object, error) {
	var out []Object
	for i := 0; i < len(ids); i += 100 {
		end := min(i+100, len(ids))
		in := make([]any, 0, end-i)
		for _, id := range ids[i:end] {
			in = append(in, map[string]string{"id": id})
		}
		var res struct {
			Results []wireObject `json:"results"`
		}
		if err := c.do(ctx, http.MethodPost, "/crm/v3/objects/"+object+"/batch/read",
			map[string]any{"properties": append(append([]string{}, props...), modProp(object)), "inputs": in}, &res); err != nil {
			return nil, err
		}
		for _, w := range res.Results {
			out = append(out, toObject(object, w))
		}
	}
	return out, nil
}

// primaryAssociation is HubSpot's own association type for "the" company of a contact or deal and "the" contact of a deal.
var primaryAssociation = map[string]int{"contacts>companies": 1, "deals>companies": 5, "deals>contacts": 3}

// Associations fills Assoc on each object with the HubSpot ids of the records of kind `to` it points at.
func (c *Client) Associations(ctx context.Context, from, to string, objs []Object) error {
	idx := map[string]int{}
	for i, o := range objs {
		idx[o.ID] = i
	}
	for i := 0; i < len(objs); i += 100 {
		end := min(i+100, len(objs))
		in := make([]any, 0, end-i)
		for _, o := range objs[i:end] {
			in = append(in, map[string]string{"id": o.ID})
		}
		var res struct {
			Results []struct {
				From struct {
					ID string `json:"id"`
				} `json:"from"`
				To []struct {
					ToObjectID   json.Number `json:"toObjectId"`
					Associations []struct {
						TypeID int `json:"typeId"`
					} `json:"associationTypes"`
				} `json:"to"`
			} `json:"results"`
		}
		path := "/crm/v4/associations/" + url.PathEscape(from) + "/" + url.PathEscape(to) + "/batch/read"
		if err := c.do(ctx, http.MethodPost, path, map[string]any{"inputs": in}, &res); err != nil {
			return err
		}
		for _, r := range res.Results {
			j, ok := idx[r.From.ID]
			if !ok {
				continue
			}
			// The primary association goes first: where a record points at several companies, the first one is the one
			// HubSpot calls its company.
			var primary, rest []string
			for _, t := range r.To {
				isPrimary := false
				for _, a := range t.Associations {
					isPrimary = isPrimary || a.TypeID == primaryAssociation[from+">"+to]
				}
				if isPrimary {
					primary = append(primary, t.ToObjectID.String())
				} else {
					rest = append(rest, t.ToObjectID.String())
				}
			}
			objs[j].Assoc[to] = append(objs[j].Assoc[to], append(primary, rest...)...)
		}
	}
	return nil
}

// Owner is a HubSpot user who can own records.
type Owner struct {
	ID    string
	Email string
}

// Owners lists every owner. A token without the owners scope gets ErrScope, which the caller reports and carries on
// without, because a record with no mapped owner is still imported.
func (c *Client) Owners(ctx context.Context) ([]Owner, error) {
	var out []Owner
	after := ""
	for {
		path := "/crm/v3/owners?limit=100&archived=false"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		var res struct {
			Results []struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			} `json:"results"`
			Paging struct {
				Next struct {
					After string `json:"after"`
				} `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
			return nil, err
		}
		for _, r := range res.Results {
			out = append(out, Owner{ID: r.ID, Email: r.Email})
		}
		if after = res.Paging.Next.After; after == "" {
			return out, nil
		}
	}
}

// DealStages maps a deal stage id to its label, across every pipeline.
func (c *Client) DealStages(ctx context.Context) (map[string]string, error) {
	var res struct {
		Results []struct {
			Stages []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"stages"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/crm/v3/pipelines/deals", nil, &res); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range res.Results {
		for _, s := range p.Stages {
			out[s.ID] = s.Label
		}
	}
	return out, nil
}

// CallDispositions maps a call outcome's id to its label. It is best effort: without it an outcome is kept as the id.
func (c *Client) CallDispositions(ctx context.Context) map[string]string {
	var res []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	out := map[string]string{}
	if err := c.do(ctx, http.MethodGet, "/calling/v1/dispositions", nil, &res); err != nil {
		return out
	}
	for _, d := range res {
		out[d.ID] = d.Label
	}
	return out
}
