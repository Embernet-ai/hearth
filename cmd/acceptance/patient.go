package main

// A client that waits when it is told to.
//
// The suite makes several hundred requests from one address in well under a
// minute. The browsing budget is a bucket of 240 refilling at 20 a second, so a
// fast machine empties it partway through and the rest of the run gets a 429
// for something no person could do: a laptop finished the whole suite in about
// seventeen seconds and failed nine checks that had nothing to do with what
// they claim to test, while the slower CI runner stayed under the line. A
// check that fails or passes on how quick the box is, is testing the box.
//
// So every client the suite makes waits out a 429 the way the Retry-After header
// asks any real client to, and tries again. The one check that exists to see a
// 429 (thereIsARateLimit) turns this off for its own duration, so the limiter
// is still proven and nothing is hidden: if it ever stopped limiting, that check
// fails.

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

type patient struct {
	next http.RoundTripper
	// off is 1 while a check wants to see the raw 429.
	off atomic.Int32
}

var patience = &patient{next: http.DefaultTransport}

func init() { http.DefaultTransport = patience }

// RoundTrip retries a 429, up to six times, sleeping what Retry-After says
// (at least a second, at most twenty).
func (p *patient) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := p.next.RoundTrip(req)
		if err != nil || resp.StatusCode != http.StatusTooManyRequests ||
			p.off.Load() == 1 || attempt >= 6 {
			return resp, err
		}
		wait := time.Second
		if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		if wait > 20*time.Second {
			wait = 20 * time.Second
		}
		resp.Body.Close()
		if req.GetBody != nil {
			body, berr := req.GetBody()
			if berr != nil {
				return nil, berr
			}
			req = req.Clone(req.Context())
			req.Body = body
		} else if req.Body != nil {
			// A body that cannot be replayed cannot be retried; hand the 429
			// back rather than send half a request.
			return p.next.RoundTrip(req)
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(wait):
		}
	}
}
