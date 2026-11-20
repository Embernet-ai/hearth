// Package emberops is Hearth's client for EmberOPS (Frappe/ERPNext) and the
// emberops_hearth addon that models Hearth's data there.
//
// It knows HTTP, bearer tokens, Frappe's envelopes and filters, and nothing
// about Hearth's features: the per-domain stores that use it live beside it.
// Every call acts as the person on the context (their Vord token), or as the
// service client when there is no person.
package emberops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxPage is the most rows asked for in one list call. Frappe caps nothing by
// itself, and one runaway page is a slow request for everyone.
const maxPage = 500

// Config builds a Client.
type Config struct {
	BaseURL *url.URL
	// Service is used when the context carries no person. May be nil in
	// tests that always set one.
	Service TokenSource
	HTTP    *http.Client
	// CacheTTL bounds the shared cache for reference reads (see
	// ListQuery.Reference). Zero disables it.
	CacheTTL time.Duration
}

// Client talks to one EmberOPS site.
type Client struct {
	base    *url.URL
	service TokenSource
	hc      *http.Client
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at   time.Time
	body []byte
}

// New returns a client.
func New(cfg Config) *Client {
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: cfg.BaseURL, service: cfg.Service, hc: hc, ttl: cfg.CacheTTL, cache: map[string]cacheEntry{}}
}

// BaseURL is the site this client talks to.
func (c *Client) BaseURL() *url.URL { return c.base }

// Row is one Frappe document or list row.
type Row map[string]any

// Filter is one Frappe filter triple.
type Filter struct {
	Field string
	Op    string // =, !=, in, not in, like, >, <, >=, <=, is, between
	Value any
}

// F is shorthand for an equality filter.
func F(field string, value any) Filter { return Filter{field, "=", value} }

// ListQuery is a list call. Fields are required: Frappe's default is name
// only, and asking for the columns a page needs in one call is what keeps a
// screen from becoming N+1 gets.
type ListQuery struct {
	Doctype string
	Fields  []string
	Filters []Filter
	// OrFilters are ORed with each other and ANDed with Filters.
	OrFilters []Filter
	OrderBy   string
	Limit     int
	Start     int
	// Parent selects rows of a child table (Timesheet Detail) through
	// frappe.client.get_list, which /api/resource cannot do.
	Parent string
	// Reference marks slow-changing data (property defs, teams, spaces) that
	// may be served from the shared short cache.
	Reference bool
}

// ---- transport ----

// memo is the per-request cache: every GET a request makes is remembered for
// that request only, and any write empties it, so a page that asks for the same
// thing twice asks EmberOPS once.
type memo struct {
	mu sync.Mutex
	m  map[string][]byte
}

type memoKey struct{}

// WithRequestMemo attaches a fresh per-request cache to ctx. The web layer does
// this once per request.
func WithRequestMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, memoKey{}, &memo{m: map[string][]byte{}})
}

func memoFrom(ctx context.Context) *memo { m, _ := ctx.Value(memoKey{}).(*memo); return m }

func (m *memo) reset() {
	m.mu.Lock()
	clear(m.m)
	m.mu.Unlock()
}

func (c *Client) token(ctx context.Context) (TokenSource, string, error) {
	ts := UserFrom(ctx)
	if ts == nil {
		ts = c.service
	}
	if ts == nil {
		return nil, "", errors.New("emberops: no token source for this call")
	}
	tok, err := ts.Token(ctx)
	if err != nil {
		noteTokenError(ctx, err)
		return nil, "", err
	}
	return ts, tok, nil
}

func fingerprint(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:6])
}

// do performs one call and returns the response body. GETs retry on transient
// failures; writes never do, because a retried upsert is safe but a retried
// insert is not. A 401 invalidates the token once and tries again.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, cacheable, reference bool) ([]byte, error) {
	// path arrives already escaped (names may hold spaces or slashes), so both
	// forms are set and the URL is not escaped twice.
	u := *c.base
	u.RawPath = strings.TrimSuffix(c.base.EscapedPath(), "/") + path
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery = q.Encode()

	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}

	write := method != http.MethodGet && !isReadCall(method, path)
	mm := memoFrom(ctx)
	if write {
		if mm != nil {
			mm.reset()
		}
		c.flush()
	}

	refreshed := false
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		ts, tok, err := c.token(ctx)
		if err != nil {
			return nil, err
		}
		key := fingerprint(tok) + " " + method + " " + u.String()
		if !write && payload != nil {
			key += " " + string(payload)
		}
		if cacheable {
			if mm != nil {
				mm.mu.Lock()
				b, ok := mm.m[key]
				mm.mu.Unlock()
				if ok {
					return b, nil
				}
			}
			if reference {
				if b, ok := c.cached(key); ok {
					return b, nil
				}
			}
		}

		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("emberops: %s %s: %w", method, path, err)
			if write || ctx.Err() != nil {
				return nil, lastErr
			}
			if sleep(ctx, attempt) != nil {
				return nil, lastErr
			}
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("emberops: read %s: %w", path, rerr)
		}

		switch {
		case resp.StatusCode == http.StatusUnauthorized && !refreshed:
			refreshed = true
			if inv, ok := ts.(Invalidator); ok {
				inv.Invalidate()
				attempt-- // the retry is not a transient failure
				continue
			}
			return nil, parseError(resp.StatusCode, path, b)
		case !write && (resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504):
			lastErr = parseError(resp.StatusCode, path, b)
			if sleep(ctx, attempt) != nil {
				return nil, lastErr
			}
			continue
		case resp.StatusCode < 200 || resp.StatusCode > 299:
			return nil, parseError(resp.StatusCode, path, b)
		}

		if cacheable {
			if mm != nil {
				mm.mu.Lock()
				mm.m[key] = b
				mm.mu.Unlock()
			}
			if reference {
				c.store(key, b)
			}
		}
		return b, nil
	}
	return nil, lastErr
}

func sleep(ctx context.Context, attempt int) error {
	t := time.NewTimer(time.Duration(100<<attempt) * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) cached(key string) ([]byte, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok || time.Since(e.at) > c.ttl {
		delete(c.cache, key)
		return nil, false
	}
	return e.body, true
}

func (c *Client) store(key string, b []byte) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) > 512 {
		clear(c.cache)
	}
	c.cache[key] = cacheEntry{at: time.Now(), body: b}
}

// flush empties the shared cache; any write through this client may have
// changed what a reference read would return.
func (c *Client) flush() {
	c.mu.Lock()
	clear(c.cache)
	c.mu.Unlock()
}

// ---- Frappe envelopes ----

func unwrap(b []byte, key string, out any) error {
	if out == nil {
		return nil
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("emberops: decode response: %w", err)
	}
	raw, ok := env[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// maxQuery is how long a read's query string may be before it is sent in a POST body instead. EmberOps's web server refuses a
// request line over 4094 bytes with a 400, and a page of Fireball's tasks asks for 500 Hearth ids at once (about 20,000
// bytes), so /tasks and /documents were a 500 in the EmberOps back end until reads this size went by POST.
const maxQuery = 3000

// isReadCall is a POST that only reads: frappe.client.get_list and get_count accept their arguments in a JSON body. It is
// neither flushed like a write nor refused a retry.
func isReadCall(method, path string) bool {
	return method == http.MethodPost && (path == "/api/method/frappe.client.get_list" || path == "/api/method/frappe.client.get_count")
}

func jsonArg(v any) string { b, _ := json.Marshal(v); return string(b) }

func filterArg(fs []Filter) string { return jsonArg(filterRows(fs)) }

func filterRows(fs []Filter) [][]any {
	out := make([][]any, 0, len(fs))
	for _, f := range fs {
		op := f.Op
		if op == "" {
			op = "="
		}
		out = append(out, []any{f.Field, op, f.Value})
	}
	return out
}

// List runs one list call and returns up to Limit rows (default 100, at most
// 500). Use ListAll to page through a larger set.
func (c *Client) List(ctx context.Context, lq ListQuery) ([]Row, error) {
	limit := lq.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, maxPage)
	q := url.Values{}
	q.Set("fields", jsonArg(lq.Fields))
	if len(lq.Filters) > 0 {
		q.Set("filters", filterArg(lq.Filters))
	}
	if len(lq.OrFilters) > 0 {
		q.Set("or_filters", filterArg(lq.OrFilters))
	}
	if lq.OrderBy != "" {
		q.Set("order_by", lq.OrderBy)
	}
	q.Set("limit_start", strconv.Itoa(lq.Start))
	q.Set("limit_page_length", strconv.Itoa(limit))

	path := "/api/resource/" + url.PathEscape(lq.Doctype)
	key := "data"
	if lq.Parent != "" {
		path, key = "/api/method/frappe.client.get_list", "message"
		q.Set("doctype", lq.Doctype)
		q.Set("parent", lq.Parent)
	}
	method, body := http.MethodGet, any(nil)
	if len(q.Encode()) > maxQuery {
		method, path, key = http.MethodPost, "/api/method/frappe.client.get_list", "message"
		args := map[string]any{"doctype": lq.Doctype, "fields": lq.Fields, "limit_start": lq.Start, "limit_page_length": limit}
		if len(lq.Filters) > 0 {
			args["filters"] = filterRows(lq.Filters)
		}
		if len(lq.OrFilters) > 0 {
			args["or_filters"] = filterRows(lq.OrFilters)
		}
		if lq.OrderBy != "" {
			args["order_by"] = lq.OrderBy
		}
		if lq.Parent != "" {
			args["parent"] = lq.Parent
		}
		q, body = nil, args
	}
	b, err := c.do(ctx, method, path, q, body, true, lq.Reference)
	if err != nil {
		return nil, err
	}
	var rows []Row
	if err := unwrap(b, key, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// ListAll pages until the set is exhausted or max rows are read (0 means 5000).
func (c *Client) ListAll(ctx context.Context, lq ListQuery, max int) ([]Row, error) {
	if max <= 0 {
		max = 5000
	}
	var all []Row
	lq.Limit = maxPage
	for len(all) < max {
		page, err := c.List(ctx, lq)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < maxPage {
			break
		}
		lq.Start += len(page)
	}
	return all, nil
}

// Count is frappe.client.get_count.
func (c *Client) Count(ctx context.Context, doctype string, filters []Filter) (int, error) {
	q := url.Values{"doctype": {doctype}}
	if len(filters) > 0 {
		q.Set("filters", filterArg(filters))
	}
	method, body := http.MethodGet, any(nil)
	if len(q.Encode()) > maxQuery {
		method, q, body = http.MethodPost, nil, map[string]any{"doctype": doctype, "filters": filterRows(filters)}
	}
	b, err := c.do(ctx, method, "/api/method/frappe.client.get_count", q, body, true, false)
	if err != nil {
		return 0, err
	}
	var n int
	return n, unwrap(b, "message", &n)
}

// Get reads one document by its Frappe name.
func (c *Client) Get(ctx context.Context, doctype, name string) (Row, error) {
	b, err := c.do(ctx, http.MethodGet, "/api/resource/"+url.PathEscape(doctype)+"/"+url.PathEscape(name), nil, nil, true, false)
	if err != nil {
		return nil, err
	}
	var r Row
	return r, unwrap(b, "data", &r)
}

// Insert creates a document.
func (c *Client) Insert(ctx context.Context, doctype string, values map[string]any) (Row, error) {
	b, err := c.do(ctx, http.MethodPost, "/api/resource/"+url.PathEscape(doctype), nil, values, false, false)
	if err != nil {
		return nil, err
	}
	var r Row
	return r, unwrap(b, "data", &r)
}

// Update changes fields of a document. Include "modified" to make it a write
// guard: Frappe answers TimestampMismatchError if someone else saved since.
func (c *Client) Update(ctx context.Context, doctype, name string, values map[string]any) (Row, error) {
	b, err := c.do(ctx, http.MethodPut, "/api/resource/"+url.PathEscape(doctype)+"/"+url.PathEscape(name), nil, values, false, false)
	if err != nil {
		return nil, err
	}
	var r Row
	return r, unwrap(b, "data", &r)
}

// Delete removes a document.
func (c *Client) Delete(ctx context.Context, doctype, name string) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/resource/"+url.PathEscape(doctype)+"/"+url.PathEscape(name), nil, nil, false, false)
	return err
}

// Call POSTs a whitelisted method and decodes its "message".
func (c *Client) Call(ctx context.Context, method string, args any, out any) error {
	b, err := c.do(ctx, http.MethodPost, "/api/method/"+method, nil, args, false, false)
	if err != nil {
		return err
	}
	return unwrap(b, "message", out)
}

// CallGet GETs a whitelisted read-only method and decodes its "message".
func (c *Client) CallGet(ctx context.Context, method string, args url.Values, out any) error {
	b, err := c.do(ctx, http.MethodGet, "/api/method/"+method, args, nil, true, false)
	if err != nil {
		return err
	}
	return unwrap(b, "message", out)
}

// ---- the emberops_hearth addon ----

// UpsertResult is what emberops_hearth.api.upsert answers.
type UpsertResult struct {
	Name     string `json:"name"`
	Created  bool   `json:"created"`
	Modified string `json:"modified"`
}

// Upsert creates or updates the record with this Hearth id. Values may name
// other records as "hearth:<id>"; "_assign" and "_tags" set assignees and tags.
func (c *Client) Upsert(ctx context.Context, doctype, hearthID string, values map[string]any) (UpsertResult, error) {
	var r UpsertResult
	err := c.Call(ctx, "emberops_hearth.api.upsert",
		map[string]any{"doctype": doctype, "hearth_id": hearthID, "values": values}, &r)
	return r, err
}

// BulkItem is one record of a bulk upsert.
type BulkItem struct {
	Doctype  string         `json:"doctype"`
	HearthID string         `json:"hearth_id"`
	Values   map[string]any `json:"values"`
}

// BulkResult is the addon's answer: per-item failures are reported, not fatal.
type BulkResult struct {
	OK     int `json:"ok"`
	Failed []struct {
		Doctype  string `json:"doctype"`
		HearthID string `json:"hearth_id"`
		Error    string `json:"error"`
	} `json:"failed"`
	Done []struct {
		Doctype  string `json:"doctype"`
		HearthID string `json:"hearth_id"`
		Name     string `json:"name"`
		Created  bool   `json:"created"`
	} `json:"done"`
}

// MaxBulk is the addon's per-call limit.
const MaxBulk = 500

// BulkUpsert sends items in order, in batches of MaxBulk, and merges results.
func (c *Client) BulkUpsert(ctx context.Context, items []BulkItem) (BulkResult, error) {
	var total BulkResult
	for len(items) > 0 {
		n := min(len(items), MaxBulk)
		var r BulkResult
		if err := c.Call(ctx, "emberops_hearth.api.bulk_upsert", map[string]any{"items": items[:n]}, &r); err != nil {
			return total, err
		}
		total.OK += r.OK
		total.Failed = append(total.Failed, r.Failed...)
		total.Done = append(total.Done, r.Done...)
		items = items[n:]
	}
	return total, nil
}

// Resolve returns the Frappe name for a Hearth id, or "" when there is none.
func (c *Client) Resolve(ctx context.Context, doctype, hearthID string) (string, error) {
	var name *string
	err := c.CallGet(ctx, "emberops_hearth.api.resolve", url.Values{"doctype": {doctype}, "hearth_id": {hearthID}}, &name)
	if err != nil || name == nil {
		return "", err
	}
	return *name, nil
}

// GetByHearthID returns the whole document for a Hearth id, or ErrNotFound.
func (c *Client) GetByHearthID(ctx context.Context, doctype, hearthID string) (Row, error) {
	var r Row
	err := c.CallGet(ctx, "emberops_hearth.api.get_by_hearth_id", url.Values{"doctype": {doctype}, "hearth_id": {hearthID}}, &r)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("%w: %s %s", ErrNotFound, doctype, hearthID)
	}
	return r, nil
}

// DeleteByHearthID deletes the record with this Hearth id.
func (c *Client) DeleteByHearthID(ctx context.Context, doctype, hearthID string) error {
	name, err := c.Resolve(ctx, doctype, hearthID)
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("%w: %s %s", ErrNotFound, doctype, hearthID)
	}
	return c.Delete(ctx, doctype, name)
}
