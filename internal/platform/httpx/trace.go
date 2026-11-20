package httpx

// Trace context: one id that follows a request across the estate.
//
// # What this is, and what it is honestly not
//
// It is W3C Trace Context. A request arriving with a traceparent header keeps
// its trace id, one that arrives without gets a new one, every log line carries
// it, and it goes back out in a header so whatever called can write it down
// too. Following one request through the ingress, this application, and the
// worker that finished its job is then a grep.
//
// It is not spans, and there is no exporter. A collector is another thing in
// the estate to run, to secure, and to keep up, and the question this actually
// answers day to day is "what else happened during that request", which an id
// in the log answers completely. When there is a collector worth sending to,
// the id is already here and correct, which is the part that is expensive to
// retrofit.
//
// The format is the specification's, exactly, so nothing downstream has to
// learn a local convention:
//
//	traceparent: 00-<32 hex trace id>-<16 hex span id>-<2 hex flags>

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	traceIDKey ctxKey = iota + 100
	spanIDKey
)

// TraceID returns the id following this request, or "" outside one.
func TraceID(ctx context.Context) string {
	v, _ := ctx.Value(traceIDKey).(string)
	return v
}

// SpanID is this application's own leg of the trace.
func SpanID(ctx context.Context) string {
	v, _ := ctx.Value(spanIDKey).(string)
	return v
}

// WithTrace carries a trace into work that outlives the request that started
// it, which is what a queued job is. Without it the trail stops at the handler
// that enqueued something and picks up again as an unrelated id.
func WithTrace(ctx context.Context, traceID string) context.Context {
	if !validTraceID(traceID) {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey, traceID)
}

// Trace attaches a trace id to every request.
//
// Placed outside the logging middleware, so the id is available to the line it
// writes. Both are in one chain in main and the order there is the contract.
func Trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace, parent, sampled := parseTraceparent(r.Header.Get("traceparent"))
		if trace == "" {
			trace = randomHex(16)
			// Sampled by default. There is no sampler here and nothing is
			// dropped, so claiming otherwise in the header would be a lie to
			// whatever is downstream.
			sampled = true
		}
		span := randomHex(8)

		ctx := context.WithValue(r.Context(), traceIDKey, trace)
		ctx = context.WithValue(ctx, spanIDKey, span)
		r = r.WithContext(ctx)

		flags := "00"
		if sampled {
			flags = "01"
		}
		// Back out, so a caller can record which trace their request became,
		// and so a person with a browser can read it off the response and go
		// looking in the log.
		w.Header().Set("traceparent", "00-"+trace+"-"+span+"-"+flags)
		w.Header().Set("X-Trace-Id", trace)
		_ = parent

		next.ServeHTTP(w, r)
	})
}

// parseTraceparent reads the header, refusing anything that is not exactly
// right.
//
// Strict on purpose. A malformed traceparent means the id would not join up
// with anything anyway, and inventing a new one is better than propagating a
// broken one that makes two unrelated requests look like the same trace.
func parseTraceparent(v string) (trace, parent string, sampled bool) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) != 4 || parts[0] != "00" {
		return "", "", false
	}
	if !validTraceID(parts[1]) || !isHex(parts[2], 16) || !isHex(parts[3], 2) {
		return "", "", false
	}
	return parts[1], parts[2], parts[3] != "00"
}

// validTraceID is thirty-two hex characters, not all zero. The all-zero id is
// reserved by the specification as "invalid", and a caller sending it is
// telling us it does not have one.
func validTraceID(v string) bool {
	return isHex(v, 32) && strings.Trim(v, "0") != ""
}

func isHex(v string, length int) bool {
	if len(v) != length {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice, and a trace id is not a
		// security control. Degrading beats refusing to serve.
		return strings.Repeat("0", n*2-1) + "1"
	}
	return hex.EncodeToString(b)
}
