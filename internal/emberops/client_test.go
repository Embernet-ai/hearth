package emberops_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
)

func newClient(t *testing.T) (*emberops.Client, *fake.Frappe) {
	t.Helper()
	f := fake.New(map[string]string{"user-tok": "ann@example.com", "svc-tok": "svc@example.com"})
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	return emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc-tok"), CacheTTL: time.Minute}), f
}

func TestPersonTokenIsForwardedAndServiceIsTheFallback(t *testing.T) {
	c, f := newClient(t)
	ctx := emberops.WithUser(context.Background(), emberops.StaticToken("user-tok"))
	if _, err := c.List(ctx, emberops.ListQuery{Doctype: "Task", Fields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(context.Background(), emberops.ListQuery{Doctype: "Task", Fields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	if f.Users[0] != "ann@example.com" || f.Users[1] != "svc@example.com" {
		t.Fatalf("acted as %v", f.Users)
	}
}

func TestUnknownTokenIsUnauthorized(t *testing.T) {
	c, _ := newClient(t)
	ctx := emberops.WithUser(context.Background(), emberops.StaticToken("nope"))
	_, err := c.Get(ctx, "Task", "x")
	if !errors.Is(err, emberops.ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestListFiltersFieldsAndPaging(t *testing.T) {
	c, f := newClient(t)
	for i, st := range []string{"Open", "Open", "Completed"} {
		f.Put("Task", map[string]any{"subject": string(rune('a' + i)), "status": st})
	}
	rows, err := c.List(context.Background(), emberops.ListQuery{
		Doctype: "Task", Fields: []string{"subject", "status"},
		Filters: []emberops.Filter{emberops.F("status", "Open")}, OrderBy: "subject desc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].S("subject") != "b" {
		t.Fatalf("rows = %v", rows)
	}
	n, err := c.Count(context.Background(), "Task", []emberops.Filter{emberops.F("status", "Open")})
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}
}

func TestRequestMemoDedupesAndWritesInvalidate(t *testing.T) {
	c, f := newClient(t)
	name := f.Put("Task", map[string]any{"subject": "one"})
	ctx := emberops.WithRequestMemo(context.Background())
	for range 3 {
		if _, err := c.Get(ctx, "Task", name); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.CallCount("GET /api/resource/Task/" + name); n != 1 {
		t.Fatalf("3 reads in one request made %d calls", n)
	}
	if _, err := c.Update(ctx, "Task", name, map[string]any{"subject": "two"}); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Get(ctx, "Task", name)
	if got.S("subject") != "two" {
		t.Fatalf("stale read after write: %v", got)
	}
}

func TestReferenceReadsShareTheShortCache(t *testing.T) {
	c, f := newClient(t)
	f.Put("Hearth Property Def", map[string]any{"prop_key": "k"})
	q := emberops.ListQuery{Doctype: "Hearth Property Def", Fields: []string{"prop_key"}, Reference: true}
	for range 3 {
		if _, err := c.List(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.CallCount("/api/resource/Hearth Property Def"); n != 1 {
		t.Fatalf("made %d calls", n)
	}
}

func TestErrorsMapToSentinels(t *testing.T) {
	c, f := newClient(t)
	_, err := c.Get(context.Background(), "Task", "missing")
	if !errors.Is(err, emberops.ErrNotFound) {
		t.Fatalf("not found: %v", err)
	}
	name := f.Put("Task", map[string]any{"subject": "x", "modified": "m1"})
	_, err = c.Update(context.Background(), "Task", name, map[string]any{"modified": "stale"})
	if !errors.Is(err, emberops.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestUpsertResolvesHearthReferencesAndBulk(t *testing.T) {
	c, f := newClient(t)
	ctx := context.Background()
	if _, err := c.Upsert(ctx, "Hearth Space", "S1", map[string]any{"space_name": "Ops"}); err != nil {
		t.Fatal(err)
	}
	res, err := c.BulkUpsert(ctx, []emberops.BulkItem{
		{Doctype: "Task", HearthID: "T1", Values: map[string]any{"subject": "a", "hearth_space": emberops.Ref("S1")}},
		{Doctype: "Task", HearthID: "T1", Values: map[string]any{"subject": "a2"}},
	})
	if err != nil || res.OK != 2 {
		t.Fatalf("bulk = %+v, %v", res, err)
	}
	if res.Done[0].Created != true || res.Done[1].Created != false {
		t.Fatalf("second upsert should update: %+v", res.Done)
	}
	tasks := f.All("Task")
	if len(tasks) != 1 || tasks[0]["hearth_space"] == "hearth:S1" {
		t.Fatalf("task = %v", tasks)
	}
	name, err := c.Resolve(ctx, "Task", "t1") // case-normalised
	if err != nil || name == "" {
		t.Fatalf("resolve = %q, %v", name, err)
	}
	if _, err := c.GetByHearthID(ctx, "Task", "nope"); !errors.Is(err, emberops.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestBulkUpsertBatchesAt500(t *testing.T) {
	c, f := newClient(t)
	items := make([]emberops.BulkItem, 1100)
	for i := range items {
		items[i] = emberops.BulkItem{Doctype: "Task", HearthID: "id" + strconv.Itoa(i), Values: map[string]any{}}
	}
	res, err := c.BulkUpsert(context.Background(), items)
	if err != nil || res.OK != 1100 {
		t.Fatalf("ok=%d err=%v", res.OK, err)
	}
	if n := f.CallCount("bulk_upsert"); n != 3 {
		t.Fatalf("bulk calls = %d", n)
	}
}

type refreshing struct {
	tok         atomic.Value
	invalidated atomic.Int32
}

func (r *refreshing) Token(context.Context) (string, error) { return r.tok.Load().(string), nil }
func (r *refreshing) Invalidate()                           { r.invalidated.Add(1); r.tok.Store("user-tok") }

func TestRefusedTokenIsInvalidatedAndRetriedOnce(t *testing.T) {
	c, _ := newClient(t)
	src := &refreshing{}
	src.tok.Store("expired")
	ctx := emberops.WithUser(context.Background(), src)
	if _, err := c.List(ctx, emberops.ListQuery{Doctype: "Task", Fields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	if src.invalidated.Load() != 1 {
		t.Fatalf("invalidated %d times", src.invalidated.Load())
	}
}

func TestReadsRetryTransientFailuresWritesDoNot(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if gets.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		posts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("t")})
	if _, err := c.List(context.Background(), emberops.ListQuery{Doctype: "Task", Fields: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(context.Background(), "Task", map[string]any{"subject": "x"}); err == nil {
		t.Fatal("expected failure")
	}
	if gets.Load() != 2 || posts.Load() != 1 {
		t.Fatalf("gets=%d posts=%d", gets.Load(), posts.Load())
	}
}

func TestClientCredentialsCachesUntilExpiry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		user, pass, _ := r.BasicAuth()
		if user != "hearth-service" || pass != "s3cret" || r.FormValue("grant_type") != "client_credentials" || r.FormValue("scope") != "emberops" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"svc-tok","expires_in":300}`))
	}))
	defer srv.Close()
	now := time.Unix(1000, 0)
	cc := &emberops.ClientCredentials{TokenURL: srv.URL, ClientID: "hearth-service", ClientSecret: "s3cret", Scope: "emberops"}
	emberops.SetClock(cc, func() time.Time { return now })
	for range 3 {
		tok, err := cc.Token(context.Background())
		if err != nil || tok != "svc-tok" {
			t.Fatalf("%q %v", tok, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times", hits.Load())
	}
	now = now.Add(290 * time.Second) // inside the 30s skew
	_, _ = cc.Token(context.Background())
	if hits.Load() != 2 {
		t.Fatalf("did not refetch near expiry: %d", hits.Load())
	}
}

// A page of Fireball's tasks asks for 500 Hearth ids at once. In the query string that is about 20,000 bytes, and EmberOps's
// web server answers a request line over 4094 bytes with a 400, so /tasks and /documents were a 500 on a real site. Reads that
// long go in a POST body instead, and come back the same.
func TestAReadTooLongForAURLGoesByPost(t *testing.T) {
	c, f := newClient(t)
	var ids []any
	for i := 0; i < 600; i++ {
		id := fmt.Sprintf("0b5469ec-cd29-47c0-a23f-ef52be19%04d", i)
		ids = append(ids, id)
		if i < 3 {
			f.Put("Task", map[string]any{"subject": id, "status": "Open", "hearth_id": id})
		}
	}
	f.Calls = nil
	q := emberops.ListQuery{Doctype: "Task", Fields: []string{"subject", "hearth_id"}, Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids}}}
	rows, err := c.List(context.Background(), q)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	n, err := c.Count(context.Background(), "Task", q.Filters)
	if err != nil || n != 3 {
		t.Fatalf("count = %d, %v", n, err)
	}
	for _, call := range f.Calls {
		if len(call) > 3500 {
			t.Fatalf("a request line of %d bytes would be refused: %.80s", len(call), call)
		}
	}
	if !strings.Contains(strings.Join(f.Calls, "\n"), "POST /api/method/frappe.client.get_list") {
		t.Fatalf("the long read did not go by POST: %v", f.Calls)
	}
	// and it is not a write: it is not retried as one nor does it empty the cache
	if _, err := c.List(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

// Vord is one pod. While it restarts the token endpoint answers 503 and the hourly crm-sync job used to give up with 204 failed records
// (2026-11-26, bead a9e566). A token fetch now waits it out, but a refusal that is not a restart (401) is final at once.
func TestServiceTokenWaitsOutAVordRestart(t *testing.T) {
	defer emberops.SetTokenRetryDelays(emberops.SetTokenRetryDelays([]time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			http.Error(w, "no available server", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"svc-tok","expires_in":300}`))
	}))
	defer srv.Close()
	cc := &emberops.ClientCredentials{TokenURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	tok, err := cc.Token(context.Background())
	if err != nil || tok != "svc-tok" || hits.Load() != 3 {
		t.Fatalf("%q %v after %d hits", tok, err, hits.Load())
	}
}

func TestServiceTokenGivesUpAfterTheLastWaitAndNeverRetriesARefusal(t *testing.T) {
	defer emberops.SetTokenRetryDelays(emberops.SetTokenRetryDelays([]time.Duration{time.Millisecond, time.Millisecond}))
	var hits atomic.Int32
	code := http.StatusServiceUnavailable
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "down", code)
	}))
	defer srv.Close()
	cc := &emberops.ClientCredentials{TokenURL: srv.URL, ClientID: "c", ClientSecret: "s"}
	if _, err := cc.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "503") || hits.Load() != 3 {
		t.Fatalf("err %v after %d hits", err, hits.Load())
	}
	hits.Store(0)
	code = http.StatusUnauthorized
	if _, err := cc.Token(context.Background()); err == nil || hits.Load() != 1 {
		t.Fatalf("a 401 must not be retried: err %v, %d hits", err, hits.Load())
	}
}
