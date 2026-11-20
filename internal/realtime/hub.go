// Package realtime pushes live updates to browsers over server-sent events.
//
// # SSE, not WebSocket
//
// The traffic here is one-directional: the server tells the browser something
// changed, and the browser sends everything else as ordinary form posts. SSE is
// plain HTTP, so it crosses any proxy without an upgrade dance, it reconnects by
// itself, and HTMX consumes it natively. A WebSocket would buy bidirectional
// framing that nothing in this application needs.
//
// # Why events carry ids and not HTML
//
// The obvious design is to broadcast a rendered fragment to everyone watching a
// topic. It is also a data leak waiting to happen: what a document list looks
// like depends on who is asking, so one rendered fragment cannot be correct for
// two subscribers with different permissions. Instead an event names WHAT
// changed, and each browser refetches through the normal handler, which applies
// the normal permission filter. Slower by one request, and impossible to get
// wrong.
package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Embernet-ai/hearth/internal/platform/buildinfo"
)

// Topic names what a subscriber cares about.
type Topic string

// OrgTopic covers everything in one organisation. Coarse on purpose: this is a
// signal to refetch, not a delivery mechanism, so a slightly noisy topic costs
// one cheap request rather than correctness.
func OrgTopic(orgID string) Topic { return Topic("org:" + orgID) }

// DocTopic covers one document, for anyone with it open.
func DocTopic(docID string) Topic { return Topic("doc:" + docID) }

// ChannelTopic covers one conversation.
//
// Named rather than folded into the org topic, because a message is the first
// event frequent enough for the difference to matter: on the coarse topic,
// every open tab in the company refetches every list on every message somebody
// types.
func ChannelTopic(channelID string) Topic { return Topic("channel:" + channelID) }

// Event is what goes down the wire. Name becomes the SSE event name, which is
// what HTMX keys its swaps on.
type Event struct {
	Name string
	Data string
}

type subscriber struct {
	ch     chan Event
	topics map[Topic]bool
}

// Hub fans events out to connected browsers.
//
// Sharded by topic hash so that a busy topic does not serialise every other
// one behind a single mutex.
type Hub struct {
	log    *slog.Logger
	shards [16]shard
}

type shard struct {
	mu   sync.RWMutex
	subs map[*subscriber]bool
}

func NewHub(log *slog.Logger) *Hub {
	h := &Hub{log: log}
	for i := range h.shards {
		h.shards[i].subs = make(map[*subscriber]bool)
	}
	return h
}

func (h *Hub) shardFor(t Topic) *shard {
	var sum uint32
	for i := 0; i < len(t); i++ {
		sum = sum*31 + uint32(t[i])
	}
	return &h.shards[sum%uint32(len(h.shards))]
}

// Publish delivers an event to everyone subscribed to a topic.
//
// A subscriber that cannot keep up is DROPPED rather than waited on. Blocking
// here would let one browser on a bad connection stall the request that
// triggered the event, which turns a slow client into a slow server.
func (h *Hub) Publish(t Topic, e Event) {
	s := h.shardFor(t)
	s.mu.RLock()
	defer s.mu.RUnlock()

	for sub := range s.subs {
		if !sub.topics[t] {
			continue
		}
		select {
		case sub.ch <- e:
		default:
			// Buffer full. Losing an event is fine because the event only says
			// "refetch"; the next one it does receive brings it up to date, and
			// the reconnect on the client side covers the rest.
			h.log.Debug("dropped realtime event for a slow subscriber", "topic", string(t))
		}
	}
}

func (h *Hub) add(sub *subscriber) {
	for t := range sub.topics {
		s := h.shardFor(t)
		s.mu.Lock()
		s.subs[sub] = true
		s.mu.Unlock()
	}
}

func (h *Hub) remove(sub *subscriber) {
	for t := range sub.topics {
		s := h.shardFor(t)
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}
}

// Count reports how many subscriptions exist, for diagnostics.
func (h *Hub) Count() int {
	n := 0
	for i := range h.shards {
		h.shards[i].mu.RLock()
		n += len(h.shards[i].subs)
		h.shards[i].mu.RUnlock()
	}
	return n
}

const (
	// keepAlive stops proxies deciding an idle stream is dead. Traefik and most
	// load balancers will close a connection with no traffic well before this.
	keepAlive = 20 * time.Second
	// buffer per subscriber. Small: these are refetch signals, and a client
	// that is 32 behind gains nothing from being 64 behind.
	subscriberBuffer = 32
)

// Handler serves the SSE stream for one authenticated caller.
//
// topics are computed by the caller, which is where authorisation happens: a
// subscriber only ever receives topics it was granted at connect time.
func (h *Hub) Handler(topics []Topic) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Nginx and some ingress controllers buffer responses by default, which
		// holds every event until the connection closes. This disables it.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// ResponseController, NOT a w.(http.Flusher) assertion.
		//
		// The logging middleware wraps the ResponseWriter, so a direct type
		// assertion fails and streaming silently degrades to a 500. The
		// controller walks the Unwrap chain, which is precisely why that
		// middleware implements Unwrap.
		//
		// Flushed AFTER the headers are written: flushing first would commit the
		// response with none of them set, and the browser would never treat it
		// as an event stream.
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			return
		}
		// A live stream must outlive the ordinary write deadline.
		_ = rc.SetWriteDeadline(time.Time{})

		sub := &subscriber{
			ch:     make(chan Event, subscriberBuffer),
			topics: make(map[Topic]bool, len(topics)),
		}
		for _, t := range topics {
			sub.topics[t] = true
		}
		h.add(sub)
		defer h.remove(sub)

		// Tell the browser which build it is talking to, immediately. The client
		// compares this with the build it loaded, and reloads if they differ,
		// which is how an open tab picks up a deploy without anybody pressing
		// refresh.
		fmt.Fprintf(w, "event: build\ndata: %s\n\n", buildinfo.Get().Revision)
		_ = rc.Flush()

		ticker := time.NewTicker(keepAlive)
		defer ticker.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-sub.ch:
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, e.Data)
				_ = rc.Flush()
			case <-ticker.C:
				// A comment line is a valid SSE heartbeat and triggers nothing
				// on the client.
				fmt.Fprint(w, ": keep-alive\n\n")
				_ = rc.Flush()
			}
		}
	}
}

// Shutdown is a placeholder for symmetry: subscribers end when their request
// context is cancelled, which happens on server shutdown already.
func (h *Hub) Shutdown(context.Context) error { return nil }
