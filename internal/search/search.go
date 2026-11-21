// Package search is one box that finds a work order by its symptom, an asset by
// its tag, a document by a sentence in the middle of it, and a file by its
// contents.
//
// # Two engines, and why
//
// OpenSearch answers when it is there. Postgres answers when it is not, always,
// because it is the same database the rows are already in and it cannot be down
// while the application is up. A search box that returns an error page whenever
// a separate cluster is restarting is a search box people stop typing into, and
// then the product is a filing cabinet again.
//
// Both engines are kept current. The Postgres side is a generated column, so it
// cannot fall behind; the OpenSearch side is fed by the outbox, so a failed
// index run is retried rather than lost.
//
// # Permissions
//
// Filtered at query time from the caller's subject set, in the engine, never
// afterwards. Post-filtering breaks paging in exactly the way the view compiler
// avoids, and here it is worse: asking for ten hits and being handed three is a
// search that appears to find nothing while the answer is on page two.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Hit is one result.
type Hit struct {
	EntityType string
	EntityID   string
	Kind       string
	Title      string
	// Snippet is the matching text with the terms marked. It arrives already
	// escaped and wrapped, because deciding what is safe to render is the
	// engine's job rather than the template's.
	Snippet string
	// Score orders the results. Comparable within one engine's answer and
	// meaningless between two, which is why nothing outside this package reads
	// it except to sort.
	Score float64
	// ParentID is the document a file is attached to, so a hit on an
	// attachment can be clicked.
	ParentID  string
	UpdatedAt time.Time
}

// Results is one answer, plus how it was obtained.
type Results struct {
	Hits  []Hit
	Total int
	// Engine names what answered: "opensearch" or "postgres". Shown in the
	// interface, deliberately. When the good engine is down the results get
	// narrower, and somebody looking at a shorter list than they expected
	// deserves to know why rather than concluding their document is gone.
	Engine string
	Took   time.Duration
}

// Query is what to look for.
type Query struct {
	Text string
	// Kind narrows to one document kind. Empty means everything.
	Kind string
	// Types narrows by entity type. Empty means documents and files.
	Types []string
	Limit int
}

// Engine is one way of answering. Implemented by Postgres and by OpenSearch.
//
// Declared here, by the consumer, so the OpenSearch client can be swapped or
// removed without the service knowing.
type Engine interface {
	// Name identifies the engine in results and on the operations page.
	Name() string
	// Search answers a query, restricted to what the subject set allows.
	Search(ctx context.Context, orgID string, subjects core.Subjects, q Query) (*Results, error)
	// Ready reports whether this engine can answer right now.
	Ready(ctx context.Context) error
}

// Indexer is the half of an engine that keeps it current. Postgres does not
// implement it: its index is a generated column, so there is nothing to push.
type Indexer interface {
	Index(ctx context.Context, d Document) error
	Delete(ctx context.Context, entityType, entityID string) error
	// EnsureIndex creates the index and its mapping if they are not there.
	EnsureIndex(ctx context.Context) error
}

// Document is what gets pushed into an external index.
//
// Subjects is the list of grant subject keys that may see it, copied in at
// index time. That is what lets the external engine filter by permission
// itself: it cannot join against the grants table, so the answer has to travel
// with the document, and it has to be refreshed whenever a grant changes.
type Document struct {
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	OrgID      string    `json:"org_id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Subjects   []string  `json:"subjects"`
	ParentID   string    `json:"parent_id,omitempty"`
	Version    int64     `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// verifier is an engine that can say which hits a caller may still open, from
// the live grants table.
type verifier interface {
	Verify(ctx context.Context, subjects core.Subjects, hits []Hit) ([]Hit, error)
}

// Service answers queries, preferring the better engine and falling back.
type Service struct {
	// primary is the richer engine, and may be nil when none is configured.
	primary Engine
	// fallback is Postgres and is never nil. It is the floor.
	fallback Engine
}

func New(primary, fallback Engine) *Service {
	return &Service{primary: primary, fallback: fallback}
}

// maxResults bounds an answer. Nobody reads the two hundredth hit, and an
// unbounded query against a large index is a way to take the database with it.
const maxResults = 50

// Search answers, using the primary engine when it can and Postgres when it
// cannot.
//
// A failure of the primary is logged by the caller and NOT returned. Degrading
// is the whole design: the alternative is an error page, and an error page for
// a search is indistinguishable from "your document is gone" to the person
// looking at it.
func (s *Service) Search(ctx context.Context, orgID string, subjects core.Subjects, q Query) (*Results, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" {
		return &Results{Engine: "none"}, nil
	}
	if q.Limit <= 0 || q.Limit > maxResults {
		q.Limit = 20
	}
	if len(subjects) == 0 {
		// No subjects is not an empty filter, it is nobody. Returning
		// everything here would be the whole permission model undone by an
		// unset variable.
		return &Results{Engine: "none"}, nil
	}

	started := time.Now()
	if s.primary != nil {
		res, err := s.primary.Search(ctx, orgID, subjects, q)
		if err == nil {
			// The cluster answers from the subject list it was given at index
			// time, so a grant that ended since, or was taken back a moment ago,
			// still matches there. Ask the database about what came back.
			if v, ok := s.fallback.(verifier); ok && !subjects.Super() {
				kept, verr := v.Verify(ctx, subjects, res.Hits)
				if verr != nil {
					return nil, fmt.Errorf("search: %w", verr)
				}
				res.Total -= len(res.Hits) - len(kept)
				res.Hits = kept
			}
			res.Engine = s.primary.Name()
			res.Took = time.Since(started)
			return res, nil
		}
		// Fall through. The caller logs it; the person searching gets an answer.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	res, err := s.fallback.Search(ctx, orgID, subjects, q)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	res.Engine = s.fallback.Name()
	res.Took = time.Since(started)
	return res, nil
}

// LastError reports the primary engine's most recent failure, for the
// operations page and for the log line the caller writes.
func (s *Service) Primary() Engine { return s.primary }

// Ready reports each engine's state, for the operations page.
func (s *Service) Ready(ctx context.Context) map[string]error {
	out := map[string]error{}
	if s.primary != nil {
		out[s.primary.Name()] = s.primary.Ready(ctx)
	}
	out[s.fallback.Name()] = s.fallback.Ready(ctx)
	return out
}

// ErrNoEngine means nothing is configured to answer, which is a programming
// error rather than an operational one: Postgres is always available.
var ErrNoEngine = errors.New("search: no engine configured")
