package hubspot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHub is a HubSpot that answers the calls the importer makes, from memory, and remembers every request.
type fakeHub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	objs     map[string][]fakeObj // object to its records
	assoc    map[string][]string  // "from>to:id" to the ids it points at
	owners   []Owner              // served when ownersOK
	ownersOK bool
	stages   map[string]string
	// throttle429 makes the next N calls answer 429.
	throttle429 int
	requests    []fakeReq
}

type fakeObj struct {
	id       string
	props    map[string]string
	modified time.Time
}

type fakeReq struct {
	method, path string
	body         map[string]any
}

func newFakeHub(t *testing.T) *fakeHub {
	f := &fakeHub{t: t, objs: map[string][]fakeObj{}, assoc: map[string][]string{}, stages: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHub) client() *Client {
	c := NewClient("test-token")
	c.Base = f.srv.URL
	c.Pace, c.Backoff = 0, time.Millisecond
	return c
}

func (f *fakeHub) add(object, id string, mod time.Time, props map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := map[string]string{}
	for k, v := range props {
		p[k] = v
	}
	for i, o := range f.objs[object] {
		if o.id == id { // HubSpot has one record per id: a second add is a later edit of it
			f.objs[object][i] = fakeObj{id, p, mod}
			return
		}
	}
	f.objs[object] = append(f.objs[object], fakeObj{id, p, mod})
}

func (f *fakeHub) link(from, to, id string, targets ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assoc[from+">"+to+":"+id] = targets
}

// searches returns the "since" of every search of an object, in order.
func (f *fakeHub) searches(object string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []time.Time
	for _, r := range f.requests {
		if r.method == http.MethodPost && r.path == "/crm/v3/objects/"+object+"/search" {
			if after, _ := r.body["after"].(string); after != "" {
				continue
			}
			fg := r.body["filterGroups"].([]any)[0].(map[string]any)["filters"].([]any)[0].(map[string]any)
			ms, _ := strconv.ParseInt(fg["value"].(string), 10, 64)
			out = append(out, time.UnixMilli(ms).UTC())
		}
	}
	return out
}

func (f *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.requests = append(f.requests, fakeReq{r.Method, r.URL.Path, body})
	if r.Header.Get("Authorization") != "Bearer test-token" {
		http.Error(w, `{"message":"bad token"}`, http.StatusUnauthorized)
		return
	}
	if f.throttle429 > 0 {
		f.throttle429--
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"message":"slow down"}`, http.StatusTooManyRequests)
		return
	}
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/crm/v3/owners":
		if !f.ownersOK {
			http.Error(w, `{"message":"This app hasn't been granted all required scopes"}`, http.StatusForbidden)
			return
		}
		var res []map[string]string
		for _, o := range f.owners {
			res = append(res, map[string]string{"id": o.ID, "email": o.Email})
		}
		write(map[string]any{"results": res})
	case r.URL.Path == "/crm/v3/pipelines/deals":
		var stages []map[string]string
		for id, label := range f.stages {
			stages = append(stages, map[string]string{"id": id, "label": label})
		}
		write(map[string]any{"results": []any{map[string]any{"stages": stages}}})
	case r.URL.Path == "/calling/v1/dispositions":
		write([]any{})
	case len(parts) == 5 && parts[3] != "" && parts[4] == "search": // crm v3 objects X search
		f.search(w, parts[3], body)
	case len(parts) == 6 && parts[2] == "objects" && parts[5] == "read":
		object := parts[3]
		var res []map[string]any
		for _, in := range body["inputs"].([]any) {
			id := in.(map[string]any)["id"].(string)
			for _, o := range f.objs[object] {
				if o.id == id {
					res = append(res, map[string]any{"id": o.id, "properties": f.props(object, o)})
				}
			}
		}
		write(map[string]any{"results": res})
	case len(parts) == 7 && parts[1] == "v4" && parts[2] == "associations":
		from, to := parts[3], parts[4]
		var res []map[string]any
		for _, in := range body["inputs"].([]any) {
			id := in.(map[string]any)["id"].(string)
			var tos []map[string]any
			for _, t := range f.assoc[from+">"+to+":"+id] {
				n, _ := strconv.ParseInt(t, 10, 64)
				tos = append(tos, map[string]any{"toObjectId": n})
			}
			if len(tos) > 0 {
				res = append(res, map[string]any{"from": map[string]any{"id": id}, "to": tos})
			}
		}
		write(map[string]any{"results": res})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHub) props(object string, o fakeObj) map[string]string {
	p := map[string]string{}
	for k, v := range o.props {
		p[k] = v
	}
	p[modProp(object)] = o.modified.Format("2006-01-02T15:04:05.000Z")
	return p
}

func (f *fakeHub) search(w http.ResponseWriter, object string, body map[string]any) {
	fg := body["filterGroups"].([]any)[0].(map[string]any)["filters"].([]any)[0].(map[string]any)
	if fg["propertyName"] != modProp(object) {
		http.Error(w, `{"message":"wrong modified property for `+object+`"}`, http.StatusBadRequest)
		return
	}
	ms, _ := strconv.ParseInt(fg["value"].(string), 10, 64)
	since := time.UnixMilli(ms).UTC()
	var hit []fakeObj
	for _, o := range f.objs[object] {
		if !o.modified.Before(since) {
			hit = append(hit, o)
		}
	}
	sort.SliceStable(hit, func(i, j int) bool { return hit[i].modified.Before(hit[j].modified) })
	limit := int(body["limit"].(float64))
	off := 0
	if a, ok := body["after"].(string); ok && a != "" {
		off, _ = strconv.Atoi(a)
	}
	if off >= SearchCap {
		http.Error(w, `{"message":"paging beyond the search limit"}`, http.StatusBadRequest)
		return
	}
	end := min(off+limit, len(hit))
	var res []map[string]any
	for _, o := range hit[off:end] {
		res = append(res, map[string]any{"id": o.id, "properties": f.props(object, o)})
	}
	out := map[string]any{"total": len(hit), "results": res}
	if end < len(hit) {
		out["paging"] = map[string]any{"next": map[string]any{"after": strconv.Itoa(end)}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
