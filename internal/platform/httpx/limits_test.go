package httpx

// Rate limits and trace context.
//
// The limiter's clock is injectable, so this moves time rather than sleeping
// through it. A rate limit tested with real sleeps is a slow test that still
// only ever checks one instant, and the interesting behaviour is what happens
// across a minute.

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestABucketRefillsRatherThanResetting(t *testing.T) {
	now := time.Now()
	l := NewLimiter(Limit{PerMinute: 60, Burst: 5})
	l.now = func() time.Time { return now }

	// The burst goes straight through.
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("dana"); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	ok, wait := l.Allow("dana")
	if ok {
		t.Fatal("the sixth request went through, so the burst is not a limit")
	}
	if wait <= 0 || wait > time.Minute {
		t.Errorf("Retry-After would be %v, which tells the caller nothing useful", wait)
	}

	// Sixty a minute is one a second, and after a second there is one.
	now = now.Add(time.Second)
	if ok, _ := l.Allow("dana"); !ok {
		t.Error("nothing had refilled after a second at sixty a minute")
	}
	if ok, _ := l.Allow("dana"); ok {
		t.Error("a second refilled two tokens")
	}

	// And it does not save up more than the burst. A fixed window would let
	// somebody spend a whole minute's budget in the last second and the next
	// minute's in the first, which is twice the rate at the moment it matters.
	now = now.Add(time.Hour)
	allowed := 0
	for i := 0; i < 50; i++ {
		if ok, _ := l.Allow("dana"); ok {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("an hour of not asking bought %d requests, and the burst is 5", allowed)
	}
}

// One person's budget is not another's, or one busy integration would lock
// everybody else out, which is a worse outage than the one being prevented.
func TestBudgetsAreSeparate(t *testing.T) {
	now := time.Now()
	l := NewLimiter(Limit{PerMinute: 60, Burst: 2})
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("dana"); !ok {
			t.Fatal("Dana's own burst was refused")
		}
	}
	if ok, _ := l.Allow("dana"); ok {
		t.Fatal("Dana is over and was allowed")
	}
	if ok, _ := l.Allow("sam"); !ok {
		t.Error("Sam was refused because Dana was busy")
	}
}

func TestALimitedHandlerSaysWhenToComeBack(t *testing.T) {
	now := time.Now()
	l := NewLimiter(Limit{PerMinute: 6, Burst: 1})
	l.now = func() time.Time { return now }

	h := Limited(l, ByIP, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.1.56:51244"

	first := httptest.NewRecorder()
	h.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("the first request got HTTP %d", first.Code)
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("the second request got HTTP %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("a 429 with no Retry-After makes a client retry immediately, " +
			"which makes it worse")
	}

	// A different address is a different budget, and the port is not part of
	// the key: every request from one machine has a different one.
	other := httptest.NewRequest(http.MethodGet, "/", nil)
	other.RemoteAddr = "10.0.1.57:33221"
	third := httptest.NewRecorder()
	h.ServeHTTP(third, other)
	if third.Code != http.StatusOK {
		t.Errorf("a different address got HTTP %d", third.Code)
	}

	same := httptest.NewRequest(http.MethodGet, "/", nil)
	same.RemoteAddr = "10.0.1.56:60001"
	fourth := httptest.NewRecorder()
	h.ServeHTTP(fourth, same)
	if fourth.Code != http.StatusTooManyRequests {
		t.Error("a second connection from the same machine got a fresh budget, " +
			"so the limit is per socket rather than per client")
	}
}

// ---------------------------------------------------------------------------
// Trace context
// ---------------------------------------------------------------------------

func TestATraceIsKeptWhenOneArrivesAndMadeWhenOneDoesNot(t *testing.T) {
	var seen string
	h := Trace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TraceID(r.Context())
	}))

	// Nothing arriving: a new one, and it comes back in the response so
	// whatever called can write it down.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if len(seen) != 32 {
		t.Errorf("the trace id is %q, and the specification says thirty-two hex", seen)
	}
	if !strings.Contains(rec.Header().Get("traceparent"), seen) {
		t.Errorf("the response does not carry the trace: %q",
			rec.Header().Get("traceparent"))
	}
	if rec.Header().Get("X-Trace-Id") != seen {
		t.Error("the plain header a person reads off a response does not match")
	}

	// One arriving: kept, so the trail joins up with the ingress in front.
	incoming := "4bf92f3577b34da6a3ce929d0e0e4736"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("traceparent", "00-"+incoming+"-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != incoming {
		t.Errorf("the arriving trace was replaced with %q", seen)
	}
}

// A malformed header means the id would not join up with anything anyway, and
// propagating a broken one makes two unrelated requests look like one trace.
func TestAMalformedTraceparentIsReplacedRatherThanPropagated(t *testing.T) {
	var seen string
	h := Trace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TraceID(r.Context())
	}))

	for name, header := range map[string]string{
		"the wrong version":  "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"too short":          "00-4bf92f35-00f067aa0ba902b7-01",
		"not hex":            "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
		"the all zero id":    "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"missing a field":    "00-4bf92f3577b34da6a3ce929d0e0e4736-01",
		"nothing at all":     "",
		"somebody's opinion": "yes please",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set("traceparent", header)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if len(seen) != 32 || strings.Trim(seen, "0") == "" {
			t.Errorf("%s produced the trace id %q", name, seen)
		}
		if seen == "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Errorf("%s was propagated as though it were valid", name)
		}
	}
}

// The address the audit log records comes off the socket, never off a header.
// A record of where somebody was that the caller chose is worse than no record:
// it looks like evidence.
func TestTheClientAddressComesOffTheSocket(t *testing.T) {
	var seen string
	h := Middleware(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ClientIP(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.1.56:51244"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "10.0.1.56" {
		t.Errorf("the address recorded is %q, want the socket's without its port", seen)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAClientIsTheLastAddressThatIsNotAProxy(t *testing.T) {
	p, err := ParseProxies([]string{"10.42.0.0/16", "192.168.1.5"})
	if err != nil {
		t.Fatal(err)
	}
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	for _, c := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"a proxy forwarding a client", req("10.42.0.7:5000", "203.0.113.9"), "203.0.113.9"},
		{"two proxies in a chain", req("10.42.0.7:5000", "203.0.113.9, 10.42.1.1"), "203.0.113.9"},
		{"a client who writes a header of its own is read from the right", req("10.42.0.7:5000", "1.2.3.4, 203.0.113.9"), "203.0.113.9"},
		{"a bare proxy address in the list", req("192.168.1.5:5000", "203.0.113.9"), "203.0.113.9"},
		{"the header from a socket that is not a proxy is ignored", req("198.51.100.4:5000", "203.0.113.9"), "198.51.100.4"},
		{"no header means the proxy itself", req("10.42.0.7:5000"), "10.42.0.7"},
		{"an unreadable header means the proxy itself", req("10.42.0.7:5000", "not-an-ip"), "10.42.0.7"},
		{"only proxies in the header means the proxy itself", req("10.42.0.7:5000", "10.42.1.1"), "10.42.0.7"},
		{"two header lines are one list", req("10.42.0.7:5000", "1.2.3.4", "203.0.113.9"), "203.0.113.9"},
		{"ipv6 client", req("10.42.0.7:5000", "2001:db8::1"), "2001:db8::1"},
	} {
		if got := p.ByIP(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := p.ByIPAndPath(req("10.42.0.7:5000", "203.0.113.9")); got != "203.0.113.9 /x" {
		t.Errorf("by path: %q", got)
	}
}

// Empty is today's behavior, byte for byte, whatever header is sent.
func TestNoTrustedProxiesIsTheSocketAddress(t *testing.T) {
	p, err := ParseProxies(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = "10.42.0.7:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if p.ByIP(r) != ByIP(r) || p.ByIP(r) != "10.42.0.7" {
		t.Errorf("got %q, want the socket address %q", p.ByIP(r), ByIP(r))
	}
	if p.ByIPAndPath(r) != ByIPAndPath(r) {
		t.Error("by path differs from the socket form")
	}
}

func TestBadProxyListsAreRefused(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/33", "not-an-address", "10.0.0.1/x"} {
		if _, err := ParseProxies([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The address the audit trail and sign-in records keep follows the same rule as
// the limits: the client the proxies saw, only when the connection is a proxy's.
func TestRecordedClientAddressFollowsTheTrustedProxies(t *testing.T) {
	p, err := ParseProxies([]string{"10.42.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	seen := func(proxies Proxies, remote, forwarded string) string {
		var got string
		h := MiddlewareFrom(slog.New(slog.NewTextHandler(io.Discard, nil)), proxies)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = ClientIP(r.Context())
		}))
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	for _, c := range []struct {
		name            string
		proxies         Proxies
		remote, forward string
		want            string
	}{
		{"a trusted proxy passes the client through", p, "10.42.6.120:5000", "198.51.100.77", "198.51.100.77"},
		{"a client's own claim in front of the proxy's is not believed", p, "10.42.6.120:5000", "203.0.113.9, 198.51.100.77", "198.51.100.77"},
		{"a header from outside the proxy range is ignored", p, "198.51.100.4:5000", "203.0.113.9", "198.51.100.4"},
		{"no proxies listed is the socket, whatever is sent", Proxies{}, "10.42.6.120:5000", "203.0.113.9", "10.42.6.120"},
		{"a trusted proxy with no header is the proxy", p, "10.42.6.120:5000", "", "10.42.6.120"},
	} {
		if got := seen(c.proxies, c.remote, c.forward); got != c.want {
			t.Errorf("%s: recorded %q, want %q", c.name, got, c.want)
		}
	}
}
