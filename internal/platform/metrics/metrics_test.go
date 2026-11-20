package metrics

// The exposition format is a contract with something else's parser, so the
// tests here are about the bytes rather than about the numbers.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type is %q, and Prometheus reads text", ct)
	}
	return rec.Body.String()
}

func TestTheScrapeIsInTheFormatSomethingElseParses(t *testing.T) {
	r := New()
	r.Help("hearth_things_total", "Things that happened.")
	r.Counter("hearth_things_total", 3, "kind", "email")
	r.Counter("hearth_things_total", 1, "kind", "email")
	r.Counter("hearth_things_total", 7, "kind", "index")
	r.Gauge("hearth_queue_depth", "Waiting.", func() float64 { return 42 })

	body := scrape(t, r)
	for _, want := range []string{
		"# HELP hearth_things_total Things that happened.",
		"# TYPE hearth_things_total counter",
		`hearth_things_total{kind="email"} 4`,
		`hearth_things_total{kind="index"} 7`,
		"# TYPE hearth_queue_depth gauge",
		"hearth_queue_depth 42",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape does not contain %q.\nIt is:\n%s", want, body)
		}
	}
}

// Labels are sorted, so the same labels always produce the same series
// whichever order they were passed in. Without this, two call sites that agree
// about the labels and disagree about their order produce two series that look
// identical on a dashboard and add up to half the traffic each.
func TestLabelOrderDoesNotMakeASecondSeries(t *testing.T) {
	r := New()
	r.Counter("hearth_requests_total", 1, "method", "GET", "status", "2xx")
	r.Counter("hearth_requests_total", 1, "status", "2xx", "method", "GET")

	body := scrape(t, r)
	if n := strings.Count(body, "hearth_requests_total{"); n != 1 {
		t.Errorf("%d series for one thing counted twice.\n%s", n, body)
	}
	if !strings.Contains(body, `hearth_requests_total{method="GET",status="2xx"} 2`) {
		t.Errorf("the two increments did not land on the same series.\n%s", body)
	}
}

// A label value with a quote or a newline in it would end the label early and
// produce a line the parser rejects, taking the whole scrape with it.
func TestALabelValueCannotBreakTheLine(t *testing.T) {
	r := New()
	r.Counter("hearth_odd_total", 1, "route", `/a"b`+"\n"+`c\d`)

	body := scrape(t, r)
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "hearth_odd_total{") {
			t.Errorf("a label value broke the output onto a line of its own: %q", line)
		}
	}
	if !strings.Contains(body, `\"`) || !strings.Contains(body, `\n`) {
		t.Errorf("the quote and the newline were not escaped.\n%s", body)
	}
}

// An odd number of label arguments is a programming error. It must not panic:
// a metric is never worth taking a request down for.
func TestMalformedLabelsDoNotTakeTheRequestDown(t *testing.T) {
	r := New()
	r.Counter("hearth_odd_total", 1, "route")
	body := scrape(t, r)
	if !strings.Contains(body, "malformed") {
		t.Errorf("a malformed label set was silently dropped rather than being "+
			"visible in the scrape.\n%s", body)
	}
}

// The middleware counts what happened, bucketed so the label set stays fixed.
func TestTheMiddlewareCountsByRouteAndStatusClass(t *testing.T) {
	r := New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /documents/{id}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := r.Middleware(mux)

	// Three different documents, which is one route and must be one series.
	for _, id := range []string{"a", "b", "c"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/documents/"+id, nil))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/broken", nil))

	body := scrape(t, r)
	if !strings.Contains(body,
		`hearth_http_requests_total{method="GET",route="GET /documents/{id}",status="2xx"} 3`) {
		t.Errorf("three requests to one route did not land on one series.\n%s", body)
	}
	if !strings.Contains(body, `status="5xx"`) {
		t.Errorf("a server error was not counted as one.\n%s", body)
	}
	// The whole point of bucketing by pattern: no document id anywhere.
	for _, id := range []string{"/documents/a", "/documents/b", "/documents/c"} {
		if strings.Contains(body, id) {
			t.Errorf("%q is in the scrape, so a scan for URLs would make a series "+
				"per work order", id)
		}
	}
	if !strings.Contains(body, "hearth_http_request_duration_milliseconds_total") {
		t.Error("no timing was recorded")
	}
}

// A handler that never returns has to be visible. Started minus finished is the
// only thing a pair of counters can say about it, and it has to say it.
func TestInFlightIsStartedMinusFinished(t *testing.T) {
	r := New()
	done := make(chan struct{})
	blocked := make(chan struct{})
	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		close(blocked)
		<-done
	}))

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
	<-blocked

	body := scrape(t, r)
	if !strings.Contains(body, `hearth_http_requests_in_flight_total{phase="started"} 1`) {
		t.Errorf("a request in progress was not counted as started.\n%s", body)
	}
	if strings.Contains(body, `hearth_http_requests_in_flight_total{phase="finished"} 1`) {
		t.Error("a request that has not returned was counted as finished")
	}
	close(done)
}
