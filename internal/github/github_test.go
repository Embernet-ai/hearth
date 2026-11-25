package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fake GitHub: every call is a GET, the token is sent as a bearer, and the numbers are the ones in the answers.
func fakeGitHub(t *testing.T) (*Client, *int) {
	t.Helper()
	calls := 0
	now := time.Now().UTC()
	recent := now.Add(-48 * time.Hour).Format(time.RFC3339)
	old := now.Add(-90 * 24 * time.Hour).Format(time.RFC3339)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("a %s to %s: this client must only ever read", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/org/missing/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case r.URL.Path == "/user":
			w.Write([]byte(`{"login":"octo"}`))
		case r.URL.Path == "/user/repos":
			w.Write([]byte(`[{"full_name":"org/app","private":true,"pushed_at":"` + recent + `"}]`))
		case strings.HasSuffix(r.URL.Path, "/commits"):
			w.Write([]byte(`[{},{},{}]`))
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			w.Write([]byte(`[{"created_at":"` + recent + `","merged_at":"` + recent + `"},{"created_at":"` + old + `","merged_at":"` + old + `"}]`))
		case strings.HasSuffix(r.URL.Path, "/issues"):
			w.Write([]byte(`[{"created_at":"` + recent + `","closed_at":"` + recent + `"},{"created_at":"` + recent + `","pull_request":{}}]`))
		case strings.HasSuffix(r.URL.Path, "/releases"):
			w.Write([]byte(`[{"tag_name":"v2.0.0","published_at":"` + recent + `"},{"tag_name":"v1.0.0","published_at":"` + old + `"}]`))
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			w.Write([]byte(`{"workflow_runs":[{"conclusion":"success"},{"conclusion":"success"},{"conclusion":"failure"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, HC: srv.Client()}, &calls
}

func TestMeasureCountsTheLastMonthAndOnlyReads(t *testing.T) {
	c, _ := fakeGitHub(t)
	ctx := context.Background()
	if who, err := c.Who(ctx, "good"); err != nil || who != "octo" {
		t.Fatalf("who = %q, %v", who, err)
	}
	if _, err := c.Who(ctx, "bad"); err != ErrBadToken {
		t.Errorf("a refused token: %v, want ErrBadToken", err)
	}
	m := c.Measure(ctx, "good", "org/app", time.Now().AddDate(0, 0, -30))
	if m.Err != "" {
		t.Fatalf("measure: %s", m.Err)
	}
	if m.Commits != 3 || m.PRsOpened != 1 || m.PRsMerged != 1 || m.IssuesOpened != 1 || m.IssuesClosed != 1 || m.Releases != 1 || m.LatestRelease != "v2.0.0" ||
		m.Runs != 3 || m.RunsPassed != 2 || m.RunsFailed != 1 {
		t.Errorf("metrics = %+v", m)
	}
	// A name that is not owner/name, and a repository the token cannot see, say why and do not hide the others.
	if m := c.Measure(ctx, "good", "nope", time.Now()); m.Err == "" {
		t.Errorf("a bad name had no error")
	}
	if m := c.Measure(ctx, "good", "org/missing", time.Now()); m.Err == "" {
		t.Errorf("an unreadable repository had no error")
	}
}

func TestReportTotalsAdd(t *testing.T) {
	r := &Report{Repos: []Metrics{{Commits: 2, PRsMerged: 1, Runs: 3, RunsPassed: 2}, {Commits: 5, Releases: 1, Runs: 1, RunsFailed: 1}}}
	if got := r.Total(); got.Commits != 7 || got.PRsMerged != 1 || got.Releases != 1 || got.Runs != 4 || got.RunsPassed != 2 || got.RunsFailed != 1 {
		t.Errorf("total = %+v", got)
	}
}
