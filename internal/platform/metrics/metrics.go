// Package metrics is the Prometheus exposition format, written out.
//
// # Why not the client library
//
// Because what this application needs is counters, gauges, and one histogram
// shape, and the official library brings a registry, a collector interface, a
// process collector, a Go runtime collector, and a push gateway client. That is
// a lot of surface to carry for text that looks like this:
//
//	hearth_http_requests_total{status="2xx"} 41283
//
// The format is stable, documented, and about eighty lines to produce. The rest
// of this codebase has made the same trade for Markdown, for QR symbols, and
// for SCIM, and the reason is the same each time: a dependency is a thing that
// has to be upgraded, audited, and understood by whoever is on call.
//
// # What is deliberately not here
//
// No labels with unbounded values. A counter labelled by path turns into a
// hundred thousand series the first time somebody scans for URLs, and the
// scrape then costs more than the traffic. Paths are bucketed by the route
// pattern the mux matched, which is a fixed set.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds every series this process reports.
//
// One per process, made in main and handed to whatever needs it, rather than a
// package level default. A package level registry is a global, and a global is
// what makes two tests in the same binary see each other's numbers.
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]*family
	order   []string
	// gauges are read at scrape time rather than written to, for the numbers
	// that are properties of the world rather than counts of events: the queue
	// depth is a question for Postgres, not a total this process keeps.
	gauges map[string]gaugeFunc
	gorder []string
}

type family struct {
	name   string
	help   string
	kind   string
	series map[string]*atomic.Int64
	mu     sync.RWMutex
}

type gaugeFunc struct {
	help string
	fn   func() float64
}

func New() *Registry {
	return &Registry{
		metrics: map[string]*family{},
		gauges:  map[string]gaugeFunc{},
	}
}

// Counter adds to a monotonically increasing total.
//
// Labels are pairs, flattened: Counter("hearth_jobs_total", 1, "kind", "email").
// An odd number of them is a programming error and is dropped rather than
// panicking, because a metric is never worth taking a request down for.
func (r *Registry) Counter(name string, delta int64, labels ...string) {
	r.family(name, "counter").add(labelKey(labels), delta)
}

// Gauge registers a number read at scrape time.
//
// A function rather than a value, because the interesting gauges here are
// answers to questions: how deep is the queue, how many sessions are open, how
// far behind is the outbox. Keeping a copy of those in memory means keeping it
// correct, and it would be wrong within a minute.
func (r *Registry) Gauge(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.gauges[name]; !ok {
		r.gorder = append(r.gorder, name)
	}
	r.gauges[name] = gaugeFunc{help: help, fn: fn}
}

// Help attaches a description to a family, for whoever is reading the scrape
// at two in the morning.
func (r *Registry) Help(name, help string) {
	f := r.family(name, "counter")
	f.mu.Lock()
	f.help = help
	f.mu.Unlock()
}

func (r *Registry) family(name, kind string) *family {
	r.mu.RLock()
	f, ok := r.metrics[name]
	r.mu.RUnlock()
	if ok {
		return f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.metrics[name]; ok {
		return f
	}
	f = &family{name: name, kind: kind, series: map[string]*atomic.Int64{}}
	r.metrics[name] = f
	r.order = append(r.order, name)
	return f
}

func (f *family) add(key string, delta int64) {
	f.mu.RLock()
	v, ok := f.series[key]
	f.mu.RUnlock()
	if !ok {
		f.mu.Lock()
		if v, ok = f.series[key]; !ok {
			v = &atomic.Int64{}
			f.series[key] = v
		}
		f.mu.Unlock()
	}
	v.Add(delta)
}

// labelKey turns pairs into the label text a scrape prints, sorted so the same
// labels always produce the same series whichever order they were passed in.
func labelKey(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	if len(labels)%2 != 0 {
		return `{malformed="true"}`
	}
	pairs := make([]string, 0, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		pairs = append(pairs, labels[i]+`="`+escape(labels[i+1])+`"`)
	}
	sort.Strings(pairs)
	return "{" + strings.Join(pairs, ",") + "}"
}

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

// Handler serves the scrape.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		r.write(&b)
		_, _ = w.Write([]byte(b.String()))
	})
}

func (r *Registry) write(b *strings.Builder) {
	r.mu.RLock()
	order := append([]string(nil), r.order...)
	gorder := append([]string(nil), r.gorder...)
	families := make(map[string]*family, len(order))
	for _, name := range order {
		families[name] = r.metrics[name]
	}
	gauges := make(map[string]gaugeFunc, len(gorder))
	for _, name := range gorder {
		gauges[name] = r.gauges[name]
	}
	r.mu.RUnlock()

	for _, name := range order {
		f := families[name]
		f.mu.RLock()
		help, kind := f.help, f.kind
		keys := make([]string, 0, len(f.series))
		values := make(map[string]int64, len(f.series))
		for key, v := range f.series {
			keys = append(keys, key)
			values[key] = v.Load()
		}
		f.mu.RUnlock()
		sort.Strings(keys)

		if help != "" {
			fmt.Fprintf(b, "# HELP %s %s\n", name, help)
		}
		fmt.Fprintf(b, "# TYPE %s %s\n", name, kind)
		for _, key := range keys {
			fmt.Fprintf(b, "%s%s %d\n", name, key, values[key])
		}
	}
	for _, name := range gorder {
		g := gauges[name]
		if g.help != "" {
			fmt.Fprintf(b, "# HELP %s %s\n", name, g.help)
		}
		fmt.Fprintf(b, "# TYPE %s gauge\n", name)
		fmt.Fprintf(b, "%s %s\n", name, strconv.FormatFloat(g.fn(), 'f', -1, 64))
	}
}

// ---------------------------------------------------------------------------
// The HTTP middleware
// ---------------------------------------------------------------------------

// Middleware counts requests and how long they took.
//
// Status is bucketed to a class rather than reported exactly, because the
// question anybody asks of this is "how many are failing" and a series per
// status code is thirty series where three would do. The route comes from the
// pattern the mux matched, which is a fixed set: bucketing by the actual path
// would produce a series per work order.
func (r *Registry) Middleware(next http.Handler) http.Handler {
	r.Help("hearth_http_requests_total", "Requests served, by method, route, and status class.")
	r.Help("hearth_http_request_duration_seconds_total",
		"Total time spent serving requests, in seconds, by route. Divide by the "+
			"request count for a mean, which is the only average a counter can give.")
	r.Help("hearth_http_requests_in_flight_total",
		"Requests started and requests finished. The difference is what is in flight.")

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		started := time.Now()
		rec := &statusWriter{ResponseWriter: w}
		r.Counter("hearth_http_requests_in_flight_total", 1, "phase", "started")

		defer func() {
			route := req.Pattern
			if route == "" {
				route = "unmatched"
			}
			class := fmt.Sprintf("%dxx", rec.status()/100)
			r.Counter("hearth_http_requests_total", 1,
				"method", req.Method, "route", route, "status", class)
			// Milliseconds as an integer counter, converted to seconds at
			// scrape time by the dashboard. A float counter in this format is
			// legal and a rounded integer of milliseconds is easier to reason
			// about when the numbers are being read by a person rather than by
			// a query.
			r.Counter("hearth_http_request_duration_milliseconds_total",
				time.Since(started).Milliseconds(), "route", route)
			r.Counter("hearth_http_requests_in_flight_total", 1, "phase", "finished")
		}()

		next.ServeHTTP(rec, req)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// Unwrap keeps server-sent events working through this middleware. Without it
// an http.ResponseController cannot reach the real writer and every stream
// buffers forever, which is the exact bug the other recorder in this codebase
// carries a comment about.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
