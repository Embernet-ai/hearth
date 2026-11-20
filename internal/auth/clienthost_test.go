package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/platform/httpx"
)

// A sign-in record keeps the client the proxies saw when the connection is inside
// HEARTH_TRUSTED_PROXIES, and the peer address when it is not.
func TestSignInRecordKeepsTheForwardedClientOnlyFromATrustedPeer(t *testing.T) {
	proxies, err := httpx.ParseProxies([]string{"10.42.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	keep := func(p httpx.Proxies, remote, forwarded string) string {
		var got *string
		h := httpx.MiddlewareFrom(slog.New(slog.NewTextHandler(io.Discard, nil)), p)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = clientHost(r.Context(), r)
		}))
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got == nil {
			return ""
		}
		return *got
	}
	for _, c := range []struct {
		name            string
		p               httpx.Proxies
		remote, forward string
		want            string
	}{
		{"peer inside the range: the forwarded client", proxies, "10.42.6.120:41000", "198.51.100.77", "198.51.100.77"},
		{"peer inside the range, a spoofed first hop: the hop the proxy added", proxies, "10.42.6.120:41000", "203.0.113.9, 198.51.100.77", "198.51.100.77"},
		{"peer outside the range: the peer, the header ignored", proxies, "198.51.100.4:41000", "203.0.113.9", "198.51.100.4"},
		{"no proxies listed: the peer", httpx.Proxies{}, "10.42.6.120:41000", "203.0.113.9", "10.42.6.120"},
	} {
		if got := keep(c.p, c.remote, c.forward); got != c.want {
			t.Errorf("%s: kept %q, want %q", c.name, got, c.want)
		}
	}
	// outside a request there is no middleware: the socket address is what is known
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "198.51.100.4:1"
	if got := clientHost(r.Context(), r); got == nil || *got != "198.51.100.4" {
		t.Errorf("without the middleware: %v", got)
	}
}
