package httpx

// Rate limits.
//
// # What this protects against, and what it does not
//
// It protects against one client hammering one endpoint: a runaway integration
// in a retry loop, a script pointed at the sign-in form, a provisioning job
// that lost its mind. Those are the ones that actually happen to a self-hosted
// workspace, and they are all one source making a lot of requests.
//
// It is per replica, in memory, and it says so out loud. A limiter shared
// across replicas needs Redis or a database round trip on every request, and
// both of those are a new thing that can be down in front of the thing they are
// protecting. Two replicas mean twice the budget, which is the honest trade and
// is fine: the budgets here are set to stop abuse rather than to meter usage.
//
// # A token bucket, not a fixed window
//
// A fixed window lets somebody spend the whole minute's budget in the last
// second and the next minute's in the first, which is twice the rate at exactly
// the moment it matters. A bucket refills continuously and has no edge.

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limit is a rate and how much of it can be saved up.
type Limit struct {
	// PerMinute is the sustained rate.
	PerMinute float64
	// Burst is how many requests can arrive at once before the rate applies.
	// A burst smaller than a page's own fan-out makes an ordinary page load
	// trip the limit, which is why this is not simply one.
	Burst float64
}

// Limiter hands out budgets by key.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   Limit
	// now is injectable so the test can move time rather than sleep through it.
	// A rate limit tested with real sleeps is a slow test that still only ever
	// checks one instant.
	now func() time.Time
	// swept is when dead buckets were last cleared out.
	swept time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

func NewLimiter(limit Limit) *Limiter {
	if limit.PerMinute <= 0 {
		limit.PerMinute = 60
	}
	if limit.Burst <= 0 {
		limit.Burst = limit.PerMinute
	}
	return &Limiter{
		buckets: map[string]*bucket{}, limit: limit, now: time.Now,
	}
}

// Allow spends one token, and reports how long until the next one if there is
// none left.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	// Housekeeping, on the way past rather than on a timer. A goroutine per
	// limiter is a goroutine to shut down, and this map only grows while
	// traffic is arriving.
	if now.Sub(l.swept) > 10*time.Minute {
		for key, b := range l.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.buckets, key)
			}
		}
		l.swept = now
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.limit.Burst, seen: now}
		l.buckets[key] = b
	}
	// Refill for the time that has passed, capped at the burst.
	elapsed := now.Sub(b.seen).Minutes()
	if elapsed > 0 {
		b.tokens += elapsed * l.limit.PerMinute
		if b.tokens > l.limit.Burst {
			b.tokens = l.limit.Burst
		}
	}
	b.seen = now

	if b.tokens < 1 {
		missing := 1 - b.tokens
		wait := time.Duration(missing / l.limit.PerMinute * float64(time.Minute))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// KeyFunc decides what a limit is per. Returning "" exempts the request.
type KeyFunc func(*http.Request) string

// Limited wraps a handler with a budget.
//
// The 429 carries Retry-After, because a client that is told to wait waits, and
// one that is told nothing retries immediately and makes it worse.
func Limited(l *Limiter, key KeyFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := key(r)
		if k == "" {
			next.ServeHTTP(w, r)
			return
		}
		ok, wait := l.Allow(k)
		if ok {
			next.ServeHTTP(w, r)
			return
		}
		seconds := int(wait.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, "too many requests from here. Try again in %ds.\n", seconds)
	})
}

// ByIP keys on the socket address.
//
// The socket, not a forwarded header, for the same reason the audit log uses
// the socket: a header is written by whatever is in front of this and a limiter
// keyed on something the caller chooses is not a limiter. A deployment behind a
// proxy is limiting the proxy, which is honest and is what the ingress's own
// limits are for. This is what runs when HEARTH_TRUSTED_PROXIES is empty.
func ByIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ByIPAndPath is for the endpoints where one address making many attempts is
// the whole attack: signing in, and the endpoints a machine authenticates to.
func ByIPAndPath(r *http.Request) string {
	return ByIP(r) + " " + r.URL.Path
}

// Proxies is the set of addresses whose X-Forwarded-For Hearth believes: the
// ingress or load balancer in front of it. A header is only as honest as the
// hop that wrote it, so it is read only when the TCP peer is one of these.
type Proxies struct {
	nets []netip.Prefix
}

// ParseProxies reads a list of addresses and CIDR ranges, such as
// "10.42.0.0/16, 192.168.1.5". An empty list is valid and means nothing is
// trusted, which is ByIP's behavior exactly.
func ParseProxies(list []string) (Proxies, error) {
	var p Proxies
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			pfx, err := netip.ParsePrefix(item)
			if err != nil {
				return Proxies{}, fmt.Errorf("%q is not an address range: %w", item, err)
			}
			p.nets = append(p.nets, pfx.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return Proxies{}, fmt.Errorf("%q is not an address or an address range: %w", item, err)
		}
		p.nets = append(p.nets, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
	}
	return p, nil
}

func (p Proxies) trusts(a netip.Addr) bool {
	a = a.Unmap()
	for _, n := range p.nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// ByIPFrom is ByIP for a deployment behind proxies. When the socket address is
// one of the proxies, the client is the first address in X-Forwarded-For,
// reading from the right, that is not itself a proxy: the proxies append their
// peer, so everything to the left of the last untrusted hop is whatever the
// client chose to write. With no proxies listed, or a socket that is not one, or
// a header that is missing or unreadable, the key is the socket address, so a
// misconfiguration limits the proxy, which is today's behavior, and never
// exempts anybody.
func (p Proxies) ByIP(r *http.Request) string {
	sock := ByIP(r)
	if len(p.nets) == 0 {
		return sock
	}
	peer, err := netip.ParseAddr(sock)
	if err != nil || !p.trusts(peer) {
		return sock
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return sock
		}
		if !p.trusts(addr) {
			return addr.Unmap().String()
		}
	}
	return sock
}

// ByIPAndPath is ByIPAndPath with the forwarded client address.
func (p Proxies) ByIPAndPath(r *http.Request) string {
	return p.ByIP(r) + " " + r.URL.Path
}
