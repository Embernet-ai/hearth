package mcpserver_test

// The MCP door end to end: a real MCP client, real signed tokens from a stand-in
// Vord, a real Postgres. Every refusal is tested beside the permission it
// guards, because a check that always says yes passes every happy-path test.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/idptest"
	"github.com/Embernet-ai/hearth/internal/mcpserver"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/search"
)

const audience = "hearth-mcp"

type fx struct {
	server  *httptest.Server
	idp     *idptest.Provider
	docs    *docs.Service
	az      *authz.Service
	plant   *plant.Service
	agents  *agents.Service
	org     string
	ana     string // a person, linked to Vord as subject "sub-ana"
	bob     string // another person, not linked
	admin   string // a super admin, for seeding the space tree in fixtures
	visible string // a task Ana can read
	hidden  string // a task only Bob can read
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_mcp_test")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	db := pools.Web
	az := authz.New(db)
	f := &fx{az: az, plant: plant.New(db, az), docs: docs.New(db, az), agents: agents.New(db, az),
		org: uuid.NewString(), ana: uuid.NewString(), bob: uuid.NewString(), admin: uuid.NewString(), idp: idptest.New(t)}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1, $2, 'MCP Co')`, f.org, "mcp-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', 'Ana')`, f.ana, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', 'Bob')`, f.bob, f.org)
	// A super admin, so fixtures can seed the space tree: creating a top-level
	// department is a super admin's act, not a plain member's.
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1, $2, 'user', 'Admin')`, f.admin, f.org)
	exec(`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1, $2, 'super_admin', $2)`, f.org, f.admin)
	exec(`INSERT INTO user_identities (principal_id, issuer, subject, email) VALUES ($1, $2, 'sub-ana', 'ana@example.com')`,
		f.ana, f.idp.Issuer())

	task := func(who, title string) string {
		t.Helper()
		sub, err := az.SubjectsFor(ctx, f.org, who)
		if err != nil {
			t.Fatal(err)
		}
		d, err := f.docs.Create(ctx, f.org, sub, docs.CreateParams{Kind: docs.KindTask, Title: title,
			Task: &docs.TaskFields{Status: docs.StatusTodo}})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	f.visible = task(f.ana, "Ana's task")
	f.hidden = task(f.bob, "Bob's private task")

	authSvc := auth.New(db, az, false)
	tools := agents.NewMCP(f.agents, f.docs, search.New(search.NewPostgres(db), nil), f.plant, false)
	public, _ := url.Parse("https://hearth.example.com")
	srv := mcpserver.New(mcpserver.Options{
		Log: log, Verifier: f.idp.Verifier(t, audience), PublicURL: public, Issuer: f.idp.Issuer(),
		RequiredRole: "fireball-staff", Agents: f.agents, Tools: tools, People: authSvc, Subjects: az,
	})
	mux := http.NewServeMux()
	for pattern, h := range srv.Handlers() {
		mux.Handle(pattern, h)
	}
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// staff is a good token for Ana.
func (f *fx) staff(t *testing.T) string {
	return f.idp.Mint(t, idptest.Token{Subject: "sub-ana", Audience: []string{audience},
		ClientID: "claude", RealmRoles: []string{"fireball-staff"}})
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func (f *fx) connect(t *testing.T, token string) (*sdk.ClientSession, error) {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "hearth-test", Version: "1"}, nil)
	return client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:             f.server.URL + mcpserver.Path,
		HTTPClient:           &http.Client{Transport: bearer{token, http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
}

func text(r *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestAPersonSignedInWithVordWorksAsThemselves(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s, err := f.connect(t, f.staff(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	list, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 5 {
		t.Fatalf("a person got %d tools, want all 5 (search, read, list, create, update)", len(list.Tools))
	}

	read, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "read", Arguments: map[string]any{"id": f.visible}})
	if err != nil || read.IsError || !strings.Contains(text(read), "Ana's task") {
		t.Fatalf("Ana could not read her own task: %v %q", err, text(read))
	}
	hidden, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "read", Arguments: map[string]any{"id": f.hidden}})
	if err != nil || !hidden.IsError || strings.Contains(text(hidden), "Bob") {
		t.Fatalf("Ana read Bob's private task through MCP: %v %q", err, text(hidden))
	}

	// A person's write lands directly, as them -- no suggestion queue.
	created, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "create",
		Arguments: map[string]any{"kind": "task", "title": "Made by Claude for Ana"}})
	if err != nil || created.IsError {
		t.Fatalf("Ana could not create a task: %v %q", err, text(created))
	}
	var owner string
	sub, _ := f.az.SubjectsFor(ctx, f.org, f.ana)
	mine, err := f.docs.List(ctx, f.org, sub, docs.ListParams{Kind: docs.KindTask, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range mine {
		if d.Title == "Made by Claude for Ana" {
			owner = d.OwnerID
		}
	}
	if owner != f.ana {
		t.Fatalf("the task Claude made is owned by %q, want Ana", owner)
	}
}

func TestOnlyTheRightTokenGetsIn(t *testing.T) {
	f := setup(t)
	for name, tc := range map[string]struct {
		token string
		code  int
	}{
		"no token":              {"", http.StatusUnauthorized},
		"wrong audience":        {f.idp.Mint(t, idptest.Token{Subject: "sub-ana", Audience: []string{"hearth"}, RealmRoles: []string{"fireball-staff"}}), http.StatusUnauthorized},
		"expired":               {f.idp.Mint(t, idptest.Token{Subject: "sub-ana", Audience: []string{audience}, RealmRoles: []string{"fireball-staff"}, Lifetime: -time.Minute}), http.StatusUnauthorized},
		"not staff":             {f.idp.Mint(t, idptest.Token{Subject: "sub-ana", Audience: []string{audience}}), http.StatusForbidden},
		"no Hearth account":     {f.idp.Mint(t, idptest.Token{Subject: "sub-nobody", Audience: []string{audience}, RealmRoles: []string{"fireball-staff"}}), http.StatusForbidden},
		"forged agent token":    {"hearth_agent_not-a-real-token", http.StatusUnauthorized},
		"staff with an account": {f.staff(t), http.StatusOK},
	} {
		req, _ := http.NewRequest(http.MethodPost, f.server.URL+mcpserver.Path,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Errorf("%s: HTTP %d, want %d", name, resp.StatusCode, tc.code)
		}
		if tc.code == http.StatusUnauthorized && !strings.Contains(resp.Header.Get("WWW-Authenticate"),
			`resource_metadata="https://hearth.example.com/.well-known/oauth-protected-resource/mcp"`) {
			t.Errorf("%s: the 401 does not point at the resource metadata: %q", name, resp.Header.Get("WWW-Authenticate"))
		}
		// RFC 6750 3.1: no credentials means no error code, and a rejected
		// token carries the registered one. The scope names the audience scope
		// a client has to ask Vord for.
		if tc.code == http.StatusUnauthorized {
			h := resp.Header.Get("WWW-Authenticate")
			if !strings.Contains(h, `scope="openid profile email hearth-mcp"`) {
				t.Errorf("%s: the 401 does not name the scope to request: %q", name, h)
			}
			if hasErr := strings.Contains(h, "error="); name == "no token" && hasErr {
				t.Errorf("%s: a request with no credentials got an error code: %q", name, h)
			} else if name != "no token" && !strings.Contains(h, `error="invalid_token"`) {
				t.Errorf("%s: a rejected token did not get invalid_token: %q", name, h)
			}
		}
	}
}

func TestDiscoveryTellsAClientWhereToSignIn(t *testing.T) {
	f := setup(t)
	for _, path := range []string{"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp", "/mcp/.well-known/oauth-protected-resource"} {
		resp, err := http.Get(f.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			Scopes               []string `json:"scopes_supported"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&doc)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || doc.Resource != "https://hearth.example.com/mcp" ||
			len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != f.idp.Issuer() ||
			!strings.Contains(strings.Join(doc.Scopes, " "), audience) {
			t.Errorf("%s: %d %+v", path, resp.StatusCode, doc)
		}
	}
	// A preflight answers without a token, and never allows credentials.
	req, _ := http.NewRequest(http.MethodOptions, f.server.URL+mcpserver.Path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("preflight: %d, credentials %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Credentials"))
	}
}

func TestAnAgentTokenStillWorksAndStaysNarrow(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a, err := f.agents.Create(ctx, f.org, f.ana, "Reader bot", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.agents.Mint(ctx, f.org, f.ana, a.ID, "read only", []string{agents.ScopeRead}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.connect(t, tok.Secret)
	if err != nil {
		t.Fatalf("an agent token no longer connects: %v", err)
	}
	defer s.Close()
	list, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 3 {
		t.Fatalf("a read-only agent got %d tools, want only the 3 that read", len(list.Tools))
	}
	// The agent has no grants, so even Ana's task is invisible to it.
	read, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "read", Arguments: map[string]any{"id": f.visible}})
	if err != nil || !read.IsError {
		t.Fatalf("an ungranted agent read a task: %v %q", err, text(read))
	}
}

// The list filters, which exist because a super admin asking Claude for their
// tasks got every task in the org. Each filter is checked for what it keeps
// and what it drops, since a filter that drops nothing passes a keep-only test.
func TestListNarrowsToWhatWasAskedFor(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ana, _ := f.az.SubjectsFor(ctx, f.org, f.ana)
	// The tree is seeded by a super admin: a top-level department is a super
	// admin's to create, not a plain member's. Ana still owns the tasks filed
	// in it, which is what the list filters below are about.
	admin, _ := f.az.SubjectsFor(ctx, f.org, f.admin)

	eng, err := f.plant.CreateSpace(ctx, f.org, admin, "Engineering", plant.KindFolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	line, err := f.plant.CreateSpace(ctx, f.org, admin, "Line 3", plant.KindFolder, &eng.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The admin shares Engineering (and so its subtree) with Ana, the way a
	// super admin hands a department to the person who runs it. This is what
	// lets Ana see "Engineering" to filter by it -- and she is given nothing on
	// "Bob's corner" below, which is the space she must not be able to name.
	if err := f.az.GrantSpace(ctx, f.org, f.admin, f.ana, eng.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plant.CreateSpace(ctx, f.org, admin, "Bob's corner", plant.KindFolder, nil); err != nil {
		t.Fatal(err)
	}
	task := func(title, status string, space *string) {
		t.Helper()
		if _, err := f.docs.Create(ctx, f.org, ana, docs.CreateParams{Kind: docs.KindTask, Title: title,
			SpaceID: space, Task: &docs.TaskFields{Status: status, AssigneeID: &f.ana}}); err != nil {
			t.Fatal(err)
		}
	}
	task("Wire the panel", docs.StatusTodo, &line.ID)
	task("Old finished job", docs.StatusDone, &eng.ID)
	// f.visible, "Ana's task", is Ana's but assigned to nobody and in no space.

	s, err := f.connect(t, f.staff(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()
	list := func(args map[string]any) (string, bool) {
		t.Helper()
		r, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "list", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return text(r), r.IsError
	}
	expect := func(what string, got string, keep []string, drop []string) {
		t.Helper()
		for _, k := range keep {
			if !strings.Contains(got, k) {
				t.Errorf("%s: missing %q in %q", what, k, got)
			}
		}
		for _, d := range drop {
			if strings.Contains(got, d) {
				t.Errorf("%s: should not include %q: %q", what, d, got)
			}
		}
	}

	got, _ := list(map[string]any{"assigned_to_me": true})
	expect("assigned to me", got, []string{"Wire the panel"}, []string{"Old finished job", "Ana's task"})

	got, _ = list(map[string]any{"assigned_to_me": "true", "status": "all"})
	expect("assigned to me, all", got, []string{"Wire the panel", "Old finished job"}, []string{"Ana's task"})

	got, _ = list(map[string]any{"status": "done"})
	expect("done only", got, []string{"Old finished job"}, []string{"Wire the panel", "Ana's task"})

	got, _ = list(map[string]any{"department": "engineering"})
	expect("department subtree", got, []string{"Wire the panel"}, []string{"Ana's task", "Old finished job"})

	got, isErr := list(map[string]any{"department": "Nowhere"})
	if !isErr || !strings.Contains(got, "Engineering") {
		t.Fatalf("an unknown department should be refused with the real ones named: %q", got)
	}
	got, isErr = list(map[string]any{"department": "Bob's corner"})
	_, named, _ := strings.Cut(got, "Departments:")
	if !isErr || strings.Contains(named, "Bob's corner") {
		t.Fatalf("Ana filtered by a space she cannot see, or was told its name: %q", got)
	}
	if got, isErr = list(map[string]any{"status": "whenever"}); !isErr {
		t.Fatalf("a made-up status was accepted: %q", got)
	}
}
