package search

// The OpenSearch engine, over plain HTTP.
//
// No SDK. The official client pulls in a transport stack, a retry framework,
// and a code-generated API surface for an interface this uses six endpoints of.
// Six endpoints is less code than the wrapper, it compiles into a binary that
// stays small, and it is one fewer library to be told to upgrade.
//
// Permission filtering happens INSIDE the query. OpenSearch cannot join against
// the grants table, so every document carries the list of subject keys that may
// see it, and the query filters on the caller's set. That means a grant change
// has to reindex what it affected, which is why granting is an event.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// OpenSearch talks to a cluster.
type OpenSearch struct {
	base   string
	index  string
	client *http.Client
	user   string
	pass   string
}

// OpenSearchConfig configures the client.
type OpenSearchConfig struct {
	// URL is the cluster address, scheme included.
	URL string
	// Index is the index name. One index for every entity type, because a
	// query spans them and a cross-index search is a join the cluster does
	// badly.
	Index    string
	Username string
	Password string
	Timeout  time.Duration
}

func NewOpenSearch(cfg OpenSearchConfig) *OpenSearch {
	if cfg.Index == "" {
		cfg.Index = "hearth"
	}
	if cfg.Timeout <= 0 {
		// Short on purpose. A search that takes longer than this has already
		// failed as far as the person waiting is concerned, and falling back to
		// Postgres will answer sooner than the cluster is going to.
		cfg.Timeout = 5 * time.Second
	}
	return &OpenSearch{
		base:   strings.TrimSuffix(cfg.URL, "/"),
		index:  cfg.Index,
		user:   cfg.Username,
		pass:   cfg.Password,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

func (o *OpenSearch) Name() string { return "opensearch" }

func (o *OpenSearch) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("search: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("search: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.user != "" {
		req.SetBasicAuth(o.user, o.pass)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("search: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	// Bounded, because an error page from a proxy in front of the cluster can
	// be arbitrarily large and there is nothing useful past the first few
	// kilobytes of one.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("search: read response: %w", err)
	}
	return raw, resp.StatusCode, nil
}

// Ready reports whether the cluster will answer.
//
// It asks for the index rather than the cluster root, because a cluster that is
// up with no index is not ready to search and would answer every query with a
// 404 that reads like an empty result.
func (o *OpenSearch) Ready(ctx context.Context) error {
	_, code, err := o.do(ctx, http.MethodGet, "/"+o.index, nil)
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		return fmt.Errorf("search: index %q does not exist", o.index)
	}
	if code >= 300 {
		return fmt.Errorf("search: cluster returned HTTP %d", code)
	}
	return nil
}

// mapping is the index definition.
//
// subjects is a keyword field, not text: it holds "principal:<uuid>" values
// that must match exactly. Analysed as text it would be split on the colon and
// a search for one principal would match every principal, which is a permission
// filter that permits everything.
var mapping = map[string]any{
	"settings": map[string]any{
		"index": map[string]any{
			// One shard. This is a self-hosted single node; more shards is
			// more overhead per query and buys nothing until there is a second
			// machine to put them on.
			"number_of_shards":   1,
			"number_of_replicas": 0,
		},
	},
	"mappings": map[string]any{
		"properties": map[string]any{
			"entity_type": map[string]any{"type": "keyword"},
			"entity_id":   map[string]any{"type": "keyword"},
			"org_id":      map[string]any{"type": "keyword"},
			"kind":        map[string]any{"type": "keyword"},
			"subjects":    map[string]any{"type": "keyword"},
			"parent_id":   map[string]any{"type": "keyword"},
			"version":     map[string]any{"type": "long"},
			"updated_at":  map[string]any{"type": "date"},
			"title": map[string]any{
				"type": "text", "analyzer": "english",
				// A boost at query time rather than in the mapping, so it can
				// be changed without reindexing everything.
			},
			"body": map[string]any{"type": "text", "analyzer": "english"},
		},
	},
}

// EnsureIndex creates the index if it is not there. Safe to call at boot and on
// every backfill; an existing index is left exactly as it is.
func (o *OpenSearch) EnsureIndex(ctx context.Context) error {
	_, code, err := o.do(ctx, http.MethodGet, "/"+o.index, nil)
	if err != nil {
		return err
	}
	if code == http.StatusOK {
		return nil
	}
	raw, code, err := o.do(ctx, http.MethodPut, "/"+o.index, mapping)
	if err != nil {
		return err
	}
	// A 400 saying it already exists is a race with another replica doing the
	// same thing at boot, and it is a success.
	if code >= 300 && !bytes.Contains(raw, []byte("resource_already_exists_exception")) {
		return fmt.Errorf("search: create index: HTTP %d: %s", code, truncate(raw))
	}
	return nil
}

// docID keys a document in the index. Namespaced by type, because a file and a
// document can share an id space and overwriting one with the other would make
// an attachment disappear from search when a document was indexed.
func docID(entityType, entityID string) string { return entityType + ":" + entityID }

// Index writes or replaces one document.
//
// refresh=wait_for rather than fire and forget. Without it an edit is
// searchable "eventually", which in practice means the acceptance check for
// "findable within five seconds" is a sleep with a guess in it. Waiting costs
// up to one refresh interval on a background job, where nobody is waiting.
func (o *OpenSearch) Index(ctx context.Context, d Document) error {
	path := "/" + o.index + "/_doc/" + docID(d.EntityType, d.EntityID) + "?refresh=wait_for"
	raw, code, err := o.do(ctx, http.MethodPut, path, d)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("search: index %s: HTTP %d: %s", d.EntityID, code, truncate(raw))
	}
	return nil
}

// Delete removes one document. A 404 is success: the job is idempotent, and
// asking twice to remove something is not a failure the second time.
func (o *OpenSearch) Delete(ctx context.Context, entityType, entityID string) error {
	path := "/" + o.index + "/_doc/" + docID(entityType, entityID) + "?refresh=wait_for"
	raw, code, err := o.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if code >= 300 && code != http.StatusNotFound {
		return fmt.Errorf("search: delete %s: HTTP %d: %s", entityID, code, truncate(raw))
	}
	return nil
}

// Search queries the cluster.
func (o *OpenSearch) Search(ctx context.Context, orgID string, subjects core.Subjects, q Query) (*Results, error) {
	filters := []any{
		map[string]any{"term": map[string]any{"org_id": orgID}},
		// The permission filter. Not a post-filter, not a rescore: a hard
		// filter clause, so a document the caller cannot see never reaches the
		// scorer and never occupies a slot in the page.
		map[string]any{"terms": map[string]any{"subjects": subjects.Keys()}},
	}
	// A super admin may reach everything, so the permission clause is dropped for
	// them, as it is in the Postgres engine and in every list.
	if subjects.Super() {
		filters = filters[:1]
	}
	if q.Kind != "" {
		filters = append(filters, map[string]any{"term": map[string]any{"kind": q.Kind}})
	}
	if len(q.Types) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"entity_type": q.Types}})
	}

	body := map[string]any{
		"size": q.Limit,
		"query": map[string]any{
			"bool": map[string]any{
				"must": []any{
					map[string]any{
						"multi_match": map[string]any{
							"query": q.Text,
							// The title is worth three of the body. A word in
							// the name of a work order is what it is about; the
							// same word in the middle of a paragraph is often
							// in passing.
							"fields":    []string{"title^3", "body"},
							"fuzziness": "AUTO",
						},
					},
				},
				"filter": filters,
			},
		},
		"highlight": map[string]any{
			"pre_tags":  []string{"<mark>"},
			"post_tags": []string{"</mark>"},
			"fields": map[string]any{
				"body":  map[string]any{"fragment_size": 160, "number_of_fragments": 1},
				"title": map[string]any{"number_of_fragments": 0},
			},
		},
	}

	raw, code, err := o.do(ctx, http.MethodPost, "/"+o.index+"/_search", body)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, fmt.Errorf("search: query: HTTP %d: %s", code, truncate(raw))
	}

	var parsed struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
			Hits []struct {
				Score     float64  `json:"_score"`
				Source    Document `json:"_source"`
				Highlight struct {
					Body []string `json:"body"`
				} `json:"highlight"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("search: decode response: %w", err)
	}

	out := &Results{Total: parsed.Hits.Total.Value}
	for _, h := range parsed.Hits.Hits {
		hit := Hit{
			EntityType: h.Source.EntityType,
			EntityID:   h.Source.EntityID,
			Kind:       h.Source.Kind,
			Title:      h.Source.Title,
			ParentID:   h.Source.ParentID,
			Score:      h.Score,
			UpdatedAt:  h.Source.UpdatedAt,
		}
		if len(h.Highlight.Body) > 0 {
			// The cluster escapes the fragment and inserts the marks, so this
			// is the same trust boundary as ts_headline on the Postgres side.
			hit.Snippet = h.Highlight.Body[0]
		}
		out.Hits = append(out.Hits, hit)
	}
	return out, nil
}

// truncate bounds an error message. A cluster error can be a page of JSON and
// the useful part is at the front.
func truncate(raw []byte) string {
	const limit = 400
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "..."
}
