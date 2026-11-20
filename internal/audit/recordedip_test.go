package audit

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/platform/httpx"
)

// The address an audit row keeps is the client the proxies saw when the
// connection is inside HEARTH_TRUSTED_PROXIES, and the peer address when it is not.
func TestAuditRowKeepsTheForwardedClientOnlyFromATrustedPeer(t *testing.T) {
	proxies, err := httpx.ParseProxies([]string{"10.42.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(p httpx.Proxies, e Entry, remote, forwarded string) string {
		var got string
		h := httpx.MiddlewareFrom(slog.New(slog.NewTextHandler(io.Discard, nil)), p)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = recordedIP(r.Context(), e)
		}))
		r := httptest.NewRequest("POST", "/x", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	for _, c := range []struct {
		name            string
		p               httpx.Proxies
		e               Entry
		remote, forward string
		want            string
	}{
		{"peer inside the range: the forwarded client", proxies, Entry{}, "10.42.6.120:41000", "198.51.100.77", "198.51.100.77"},
		{"peer inside the range, a spoofed first hop: the hop the proxy added", proxies, Entry{}, "10.42.6.120:41000", "203.0.113.9, 198.51.100.77", "198.51.100.77"},
		{"peer outside the range: the peer, the header ignored", proxies, Entry{}, "198.51.100.4:41000", "203.0.113.9", "198.51.100.4"},
		{"no proxies listed: the peer", httpx.Proxies{}, Entry{}, "10.42.6.120:41000", "203.0.113.9", "10.42.6.120"},
		{"an address the caller names wins", proxies, Entry{IP: "192.0.2.50"}, "10.42.6.120:41000", "198.51.100.77", "192.0.2.50"},
	} {
		if got := record(c.p, c.e, c.remote, c.forward); got != c.want {
			t.Errorf("%s: kept %q, want %q", c.name, got, c.want)
		}
	}
}
