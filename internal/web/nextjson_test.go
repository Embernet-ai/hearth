package web

import (
	"encoding/json"
	"fmt"
	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/platform/markdown"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func renderWith(t *testing.T, mut func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	s := newServer(t)
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	s.render(w, r, "account", page{Title: "Your account", Message: "Password changed."})
	return w
}

func TestAPageAsksForItsDataWithAcceptOrTheQueryAndGetsTheShellOtherwise(t *testing.T) {
	for name, mut := range map[string]func(*http.Request){
		"accept": func(r *http.Request) { r.Header.Set("Accept", "application/json") },
		"query":  func(r *http.Request) { r.URL.RawQuery = "_data=1" },
	} {
		w := renderWith(t, mut)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s: content type %q", name, ct)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: page data must never be cached", name)
		}
		// The service worker keeps only responses carrying this mark, so no other JSON is ever stored on a phone.
		if w.Header().Get("X-Hearth-Page-Data") != "1" {
			t.Errorf("%s: page data is not marked for the service worker", name)
		}
		var env struct {
			Template string
			Page     struct{ Title, Message string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if env.Template != "account" || env.Page.Title != "Your account" || env.Page.Message != "Password changed." {
			t.Errorf("%s: envelope %+v", name, env)
		}
	}
	// A browser navigation gets the shell.
	withBuild(t, fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}})
	for name, mut := range map[string]func(*http.Request){
		"browser": func(r *http.Request) { r.Header.Set("Accept", "text/html,application/xhtml+xml") },
		"none":    nil,
	} {
		w := renderWith(t, mut)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s got %q, want markup", name, ct)
		}
	}
}

// Every field of the page value is on the wire in the JSON mode, so adding one is
// a decision about whether somebody may see it, not an accident. The list lives
// in next/page-fields.txt; add the new name there when the answer is yes, and
// tag the field json:"-" when it is no.
func TestEveryFieldOfThePageIsADecision(t *testing.T) {
	raw, err := os.ReadFile("next/page-fields.txt")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			known[l] = true
		}
	}
	typ := reflect.TypeOf(page{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Tag.Get("json") == "-" || !f.IsExported() {
			continue
		}
		if !known[f.Name] {
			t.Errorf("page.%s is new and is sent to the browser as JSON; add it to internal/web/next/page-fields.txt if people may see it, or tag it json:\"-\"", f.Name)
		}
		delete(known, f.Name)
	}
	for name := range known {
		t.Errorf("next/page-fields.txt lists %s, which page no longer has", name)
	}
}

// A view can read a method where JSON would carry a field, so the walk adds the listed methods.
func TestMethodsTheViewsReadAreOnTheWire(t *testing.T) {
	deal := &crm.Deal{ID: "d1", Name: "Line upgrade", AmountCents: 1250000, Currency: "USD"}
	got := tree(reflect.ValueOf(page{Deal: deal}), true).(map[string]any)["Deal"].(map[string]any)
	if got["Amount"] != "$12,500" || got["Name"] != "Line upgrade" {
		t.Errorf("deal on the wire: %v", got)
	}
	// And the plain walk does not add them.
	if _, ok := tree(reflect.ValueOf(page{Deal: deal}), false).(map[string]any)["Deal"].(map[string]any)["Amount"]; ok {
		t.Error("the plain walk added a method")
	}
}

// A refusal or a missing page is answered through fail(), which writes the status line before render runs.
// A request for data has to still come back as JSON, or the rebuilt interface cannot show the error.
func TestARefusalAskedForAsDataIsStillJSON(t *testing.T) {
	s := newServer(t)
	r := httptest.NewRequest(http.MethodGet, "/people", nil)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	s.fail(w, r, http.StatusForbidden, "only the owner or a super admin can add accounts")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("a refusal asked for as data came back as %q", ct)
	}
	var env struct {
		Template string
		Page     struct {
			Status  int
			Message string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Template != "error" || env.Page.Status != 403 || !strings.Contains(env.Page.Message, "owner") {
		t.Errorf("error page data: %+v", env)
	}
	// and a browser still gets the page
	withBuild(t, fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}})
	w = httptest.NewRecorder()
	s.fail(w, httptest.NewRequest(http.MethodGet, "/people", nil), http.StatusForbidden, "x")
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("a browser got %q", w.Header().Get("Content-Type"))
	}
}

// A view reads Active on an agent, which is a method, and Query.Kind, a field JSON would have left out when empty: both were
// wrong in the browser once (an active agent offered "Restore", and the Daily reports section vanished).
func TestViewsSeeMethodsAndGoZeroValues(t *testing.T) {
	m := tree(reflect.ValueOf(page{Agents: []*agents.Agent{{Status: "active"}, {Status: "suspended"}}}), true).(map[string]any)
	ags := m["Agents"].([]any)
	if ags[0].(map[string]any)["Active"] != true || ags[1].(map[string]any)["Active"] != false {
		t.Errorf("Agent.Active on the wire: %v %v", ags[0], ags[1])
	}
	q := m["Query"].(map[string]any)
	if k, ok := q["Kind"]; !ok || k != "" {
		t.Errorf("an unset Query.Kind must be present as the empty string under its Go name, got %v (present %v)", k, ok)
	}
	if _, ok := q["SpaceID"]; !ok {
		t.Error("a nil SpaceID must be present as null under its Go name so the template can test it")
	}
	if _, ok := q["space_id"]; ok {
		t.Error("the json tag name leaked into the page data")
	}
	// A name no template references is not added.
	if _, ok := tree(reflect.ValueOf(crm.Deal{}), true).(map[string]any)["Href"]; ok {
		t.Error("a method no template calls on this type was added")
	}
}

// A Capability is a string with methods. {{.Label}} and {{.Describes}} on it were lost when it went out as a bare
// string, which emptied the capability columns on the People pages.
func TestANamedStringThatCarriesMethodsGoesOutWithThem(t *testing.T) {
	newServer(t)
	got := tree(reflect.ValueOf(authz.Grantable), true).([]any)
	first := got[0].(map[string]any)
	if fmt.Sprint(first["$"]) != "finance" || first["Label"] != "Financial" || first["Describes"] == "" {
		t.Errorf("a capability on the wire: %v", first)
	}
	// Plain mode is unchanged: a bare string, as encoding/json writes it.
	if _, wrapped := tree(reflect.ValueOf(authz.Grantable), false).([]any)[0].(map[string]any); wrapped {
		t.Error("the plain walk wrapped a capability")
	}
}

// {{markdown .Body}} is rendered here, by the same renderer, and sent beside the field as Body_md.
func TestMarkdownFieldsComeWithTheirRenderedHTML(t *testing.T) {
	newServer(t)
	type note struct{ Body, Title string }
	m := tree(reflect.ValueOf(note{Body: "**bold** and <script>x</script>", Title: "**not rendered**"}), true).(map[string]any)
	md, ok := m["Body_md"].(string)
	if !ok || !strings.Contains(md, "<strong>bold</strong>") || strings.Contains(md, "<script>") {
		t.Errorf("Body_md = %q", md)
	}
	if _, ok := m["Title_md"]; ok {
		t.Error("a field no template passes to markdown was rendered")
	}
	if md != (markdown.Render("**bold** and <script>x</script>", markdown.Options{})) {
		t.Errorf("not the template function's own output: %q", md)
	}
}
