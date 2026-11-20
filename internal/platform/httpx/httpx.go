// Package httpx holds the HTTP plumbing every handler gets for free: request
// ids, structured access logging, and panic recovery.
//
// It knows nothing about any domain. Nothing in internal/platform may import a
// feature package, and a test in this repo enforces that.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientIPKey
)

// RequestID returns the id assigned to this request, or "" outside a request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ClientIP returns the address the request came from, or "" outside a request.
//
// The socket address, unless the deployment lists its proxies
// (HEARTH_TRUSTED_PROXIES). Then, and only when the connection itself comes from
// one of them, it is the client the nearest proxy saw, read the same way the
// rate limits read it (Proxies.ByIP). A header is written by whatever sits in
// front of this and can say anything at all, and a record of where somebody was
// that the caller chose is worse than no record: it looks like evidence. So a
// header from anywhere else is never believed, and with no proxies listed this
// is the socket address exactly as it always was.
func ClientIP(ctx context.Context) string {
	v, _ := ctx.Value(clientIPKey).(string)
	return v
}

// statusRecorder captures the status code so the access log can report it.
// http.ResponseWriter does not expose what was written.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
		r.ResponseWriter.WriteHeader(code)
	}
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which is
// what makes streaming responses work through this middleware. Without it,
// server-sent events silently buffer forever.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Middleware assigns a request id, logs one line per request, and turns a panic
// into a 500 instead of killing the process.
func Middleware(log *slog.Logger) func(http.Handler) http.Handler {
	return MiddlewareFrom(log, Proxies{})
}

// MiddlewareFrom is Middleware for a deployment behind proxies: the client
// address the audit trail and the sign-in records keep is the one the proxies
// saw, not the proxy.
func MiddlewareFrom(log *slog.Logger, proxies Proxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := newRequestID()
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			ctx = context.WithValue(ctx, clientIPKey, proxies.ByIP(r))
			r = r.WithContext(ctx)
			w.Header().Set("X-Request-Id", id)

			rec := &statusRecorder{ResponseWriter: w}
			start := time.Now()

			defer func() {
				if v := recover(); v != nil {
					log.ErrorContext(ctx, "panic serving request",
						"request_id", id,
						"trace_id", TraceID(ctx),
						"method", r.Method,
						"path", r.URL.Path,
						"panic", v,
						"stack", string(debug.Stack()),
					)
					if rec.status == 0 {
						http.Error(rec, "internal server error", http.StatusInternalServerError)
					}
				}
				if rec.status == 0 {
					rec.status = http.StatusOK
				}
				log.InfoContext(ctx, "request",
					"request_id", id,
					// The trace id, on every line, is what makes following one
					// request across the ingress, this process, and the worker
					// that finished its job a grep rather than a reconstruction.
					"trace_id", TraceID(ctx),
					"method", r.Method,
					"path", r.URL.Path,
					"status", rec.status,
					"bytes", rec.bytes,
					"duration_ms", time.Since(start).Milliseconds(),
				)
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// hostOf drops the port. An audit row saying somebody was at 10.0.1.56 is
// useful; one saying 10.0.1.56:51244 is the same fact with an ephemeral number
// stapled to it.
func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a request id is not a
		// security control. Degrade rather than refuse to serve.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
