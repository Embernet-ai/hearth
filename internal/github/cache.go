package github

import (
	"fmt"
	"sync"
	"time"
)

type metricCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]cachedReport
}

type cachedReport struct {
	at  time.Time
	rep *Report
}

func newMetricCache(ttl time.Duration) *metricCache {
	return &metricCache{ttl: ttl, m: map[string]cachedReport{}}
}

func key(p string, days int) string { return fmt.Sprintf("%s|%d", p, days) }

func (c *metricCache) get(p string, days int) (*Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.m[key(p, days)]
	if !ok || time.Since(h.at) > c.ttl {
		return nil, false
	}
	return h.rep, true
}

func (c *metricCache) put(p string, days int, r *Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key(p, days)] = cachedReport{time.Now(), r}
}

func (c *metricCache) forget(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if len(k) > len(p) && k[:len(p)+1] == p+"|" {
			delete(c.m, k)
		}
	}
}
