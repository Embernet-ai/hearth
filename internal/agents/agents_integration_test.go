package agents_test

// Agents, against a real Postgres.
//
// The two claims are that an agent cannot read a document its principal has no
// grant for, and that every agent write is attributed to that agent in
// history. Both are about the boundary between an integration and the
// permission model, which is the boundary that is usually missing.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/search"
)

type fx struct {
	svc   *agents.Service
	mcp   *agents.Connector
	docs  *docs.Service
	az    *authz.Service
	chans *channels.Service
	pools *pg.Pools
	org   string
	alice string
}

func setup(t *testing.T, suggestOnly bool) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	d := docs.New(pools.Web, az)
	svc := agents.New(pools.Web, az)
	ch := channels.New(pools.Web, az)
	f := &fx{
		svc: svc, docs: d, az: az, chans: ch, pools: pools,
		mcp: agents.NewConnector(svc, d, search.New(nil, search.NewPostgres(pools.Web)), nil, suggestOnly),
		org: uuid.NewString(), alice: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Agent Test')`, f.org, "at-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM agent_suggestions WHERE org_id=$1`,
			`DELETE FROM channel_members WHERE channel_id IN (SELECT id FROM channels WHERE org_id=$1)`,
			`DELETE FROM channels WHERE org_id=$1`,
			`DELETE FROM agent_tokens WHERE org_id=$1`,
			`DELETE FROM messages WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

// agent creates one and returns a caller holding a token with the given scopes.
func (f *fx) agent(t *testing.T, name string, scopes ...string) (*agents.Agent, *agents.Caller) {
	t.Helper()
	ctx := context.Background()
	a, err := f.svc.Create(ctx, f.org, f.alice, name, "")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	tok, err := f.svc.Mint(ctx, f.org, f.alice, a.ID, name+" token", scopes, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := f.svc.Authenticate(ctx, tok.Secret)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return a, c
}

// inChannel puts a principal in a channel Alice is also in, which is how work
// gets from an agent to a person.
func (f *fx) inChannel(t *testing.T, principalID string) string {
	t.Helper()
	ctx := context.Background()
	ch, err := f.chans.Create(ctx, f.org, f.alice, "Agent output", "", "private")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := f.chans.Invite(ctx, f.org, f.alice, ch.ID, principalID); err != nil {
		t.Fatalf("invite: %v", err)
	}
	return ch.ID
}

// reauth rebuilds a caller so its subject set picks up a new membership.
func (f *fx) reauth(t *testing.T, c *agents.Caller) *agents.Caller {
	t.Helper()
	subjects, err := f.az.SubjectsFor(context.Background(), c.OrgID, c.AgentID)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	c.Subjects = subjects
	return c
}

// call runs one tool and returns the text it produced.
func (f *fx) call(t *testing.T, c *agents.Caller, tool string, args map[string]any) (string, bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := f.mcp.Handle(context.Background(), c, body)
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	result, ok := resp.Result.(*agents.ToolResult)
	if !ok {
		t.Fatalf("result is %T", resp.Result)
	}
	var b strings.Builder
	for _, part := range result.Content {
		b.WriteString(part.Text)
	}
	return b.String(), result.IsError
}

// -----------------------------------------------------------------------------

// The criterion: an agent cannot read a document its principal has no grant
// for. Through the protocol, not through the service, because the protocol is
// where a bypass would be written.
func TestAnAgentCannotReadWhatItsPrincipalWasNotGranted(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	secret, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Quarterly wage review meeting notes",
		Body: "Nobody outside should read this.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, c := f.agent(t, "Reader", agents.ScopeRead)

	// Read by id, which is the direct attempt.
	out, isErr := f.call(t, c, "read", map[string]any{"id": secret.ID})
	if !isErr {
		t.Errorf("the agent read a document it was never granted: %q", out)
	}
	if strings.Contains(out, "wage review") || strings.Contains(out, "Nobody outside") {
		t.Errorf("the refusal leaked the contents: %q", out)
	}

	// Search, which is the indirect one.
	out, _ = f.call(t, c, "search", map[string]any{"query": "wage review"})
	if strings.Contains(out, secret.ID) || strings.Contains(out, "wage review meeting") {
		t.Errorf("search returned something the agent has no grant for: %q", out)
	}

	// List, which is the one people forget.
	out, _ = f.call(t, c, "list", map[string]any{"kind": "work_order"})
	if strings.Contains(out, secret.ID) {
		t.Errorf("list returned something the agent has no grant for: %q", out)
	}

	// And once granted, all three work. A permission model that only ever says
	// no is not being tested, it is being broken.
	if err := f.az.Grant(ctx, f.org, secret.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: c.AgentID}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	out, isErr = f.call(t, c, "read", map[string]any{"id": secret.ID})
	if isErr || !strings.Contains(out, "wage review") {
		t.Errorf("the agent was granted view and still cannot read it: %q", out)
	}
}

// The criterion: every agent write is attributed to that agent in history.
func TestEveryAgentWriteIsAttributedToThatAgent(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()

	a, c := f.agent(t, "Writer", agents.ScopeWrite)
	// In a channel, which is how an agent's output reaches anybody. An agent
	// in none of them raises work only it can see, and the agents page says so
	// rather than leaving somebody to find out.
	f.inChannel(t, a.ID)
	c = f.reauth(t, c)

	out, isErr := f.call(t, c, "create", map[string]any{
		"kind": "work_order", "title": "Raised by the agent",
	})
	if isErr {
		t.Fatalf("create failed: %q", out)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(out), "Created "), ".")

	// The row itself says who made it.
	var createdBy, updatedBy string
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT created_by::text, updated_by::text FROM documents WHERE id=$1`, id).
		Scan(&createdBy, &updatedBy); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if createdBy != a.ID {
		t.Errorf("created_by is %s, want the agent %s.\n"+
			"An integration that writes as a shared service account produces "+
			"history saying the integration did it, which is not an answer "+
			"anybody accepts when a work order changed overnight.", createdBy, a.ID)
	}

	// And so does the version history, which is what somebody actually reads.
	var author string
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT author_id::text FROM document_versions WHERE document_id=$1 ORDER BY version LIMIT 1`,
		id).Scan(&author); err != nil {
		t.Fatalf("read the version: %v", err)
	}
	if author != a.ID {
		t.Errorf("the first version is attributed to %s, want the agent", author)
	}

	// An update, which is the other half.
	if out, isErr := f.call(t, c, "update", map[string]any{
		"id": id, "status": "in_progress",
	}); isErr {
		t.Fatalf("update failed: %q", out)
	}
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT updated_by::text FROM documents WHERE id=$1`, id).Scan(&updatedBy); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if updatedBy != a.ID {
		t.Errorf("updated_by is %s, want the agent", updatedBy)
	}

	// And a person can see what it made. An agent has no hands to share with
	// afterwards, so a work order it raised that only it can read is a feature
	// that looks like it works and does nothing.
	if _, err := f.docs.Get(ctx, f.org, f.subj(t, f.alice), id); err != nil {
		t.Errorf("the owner cannot see what the agent created: %v.\n"+
			"An agent cannot share afterwards, so the sharing has to happen at "+
			"the moment of creation or it never happens at all.", err)
	}
	history, err := f.docs.History(ctx, f.org, f.subj(t, f.alice), id, 10)
	if err != nil {
		t.Fatalf("the owner cannot read the history of what the agent created: %v", err)
	}
	if len(history) == 0 || history[len(history)-1].AuthorID != a.ID {
		t.Errorf("the history does not name the agent as the author: %+v", history)
	}

	// The outbox event names the agent too, so a rule reacting to it and a
	// notification about it both say who.
	var actor string
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT actor_id::text FROM outbox WHERE entity_id=$1 AND event='created'`, id).
		Scan(&actor); err != nil {
		t.Fatalf("read the event: %v", err)
	}
	if actor != a.ID {
		t.Errorf("the created event was published by %s, want the agent", actor)
	}
}

// A token narrows what its agent may do. It never widens it.
func TestATokenNarrowsAndNeverWidens(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// An agent that CAN edit, holding a token that says read only.
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Editable by the agent",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, c := f.agent(t, "Narrow", agents.ScopeRead)
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: c.AgentID}, core.AccessAdmin, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// It can read, because the grant allows and the scope allows.
	if out, isErr := f.call(t, c, "read", map[string]any{"id": d.ID}); isErr {
		t.Errorf("a read token could not read: %q", out)
	}
	// It cannot write, because the scope says no even though the grant says yes.
	out, isErr := f.call(t, c, "update", map[string]any{"id": d.ID, "status": "done"})
	if !isErr {
		t.Errorf("a read token changed something: %q", out)
	}
	// And the write tools are not even offered, so it does not spend a turn
	// discovering the door is locked.
	for _, tool := range f.mcp.Tools(c) {
		if tool.Name == "update" || tool.Name == "create" {
			t.Errorf("a read token was offered the %s tool", tool.Name)
		}
	}

	// The other direction: a write token on an agent with no grant still
	// cannot touch anything, because the grants are the ceiling.
	_, w := f.agent(t, "Wide", agents.ScopeWrite)
	if out, isErr := f.call(t, w, "update", map[string]any{"id": d.ID, "status": "done"}); !isErr {
		t.Errorf("a write token reached a document its agent was never granted: %q", out)
	}
}

// Suspending an agent has to work on the token already in somebody's script,
// not on its next restart.
func TestSuspendingAnAgentStopsTheTokenItAlreadyIssued(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()

	a, err := f.svc.Create(ctx, f.org, f.alice, "Stoppable", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tok, err := f.svc.Mint(ctx, f.org, f.alice, a.ID, "t", []string{agents.ScopeRead}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, tok.Secret); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if err := f.svc.SetStatus(ctx, f.org, a.ID, "suspended"); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, tok.Secret); !errors.Is(err, agents.ErrNoToken) {
		t.Errorf("a suspended agent's token still works: %v.\n"+
			"The stop button has to work now, not on the next restart.", err)
	}

	// Restoring brings it back, or suspend is delete with extra steps.
	if err := f.svc.SetStatus(ctx, f.org, a.ID, "active"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, tok.Secret); err != nil {
		t.Errorf("restoring the agent did not bring its token back: %v", err)
	}

	// And revoking one token leaves the agent alone.
	if err := f.svc.Revoke(ctx, f.org, tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, tok.Secret); !errors.Is(err, agents.ErrNoToken) {
		t.Errorf("a revoked token still works: %v", err)
	}
}

func TestAnExpiredTokenStopsWorking(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()

	a, err := f.svc.Create(ctx, f.org, f.alice, "Temporary", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tok, err := f.svc.Mint(ctx, f.org, f.alice, a.ID, "t", []string{agents.ScopeRead}, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, tok.Secret); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	// Wind it back rather than waiting an hour.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE agent_tokens SET expires_at = now() - interval '1 minute' WHERE id=$1`,
		tok.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, tok.Secret); !errors.Is(err, agents.ErrNoToken) {
		t.Errorf("an expired token still works: %v", err)
	}
}

// The secret is never stored, so a stolen backup is not a pile of working
// credentials.
func TestTheTokenItselfIsNeverStored(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()

	a, err := f.svc.Create(ctx, f.org, f.alice, "Hashed", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tok, err := f.svc.Mint(ctx, f.org, f.alice, a.ID, "t", []string{agents.ScopeRead}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	var found int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM agent_tokens WHERE encode(hash,'escape') LIKE '%' || $1 || '%'`,
		tok.Secret).Scan(&found); err != nil {
		t.Fatalf("search for the secret: %v", err)
	}
	if found != 0 {
		t.Error("the token is recoverable from the database")
	}
	// And it is not readable back through the interface either.
	list, err := f.svc.Tokens(ctx, f.org)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, listed := range list {
		if listed.Secret != "" {
			t.Errorf("a listed token carries its secret: %+v", listed)
		}
	}
}

// With suggest-only on, a write token still only proposes. This is the
// deployment-wide safety catch, and a catch that a token can override is not
// one.
func TestSuggestOnlyOverridesAWriteToken(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()

	a, c := f.agent(t, "Eager", agents.ScopeWrite)
	out, isErr := f.call(t, c, "create", map[string]any{
		"kind": "work_order", "title": "Should only be proposed",
		"reason": "the pump has been rumbling for a week",
	})
	if isErr {
		t.Fatalf("create failed: %q", out)
	}
	if !strings.Contains(out, "Proposed") {
		t.Errorf("a write token wrote directly with suggest-only on: %q", out)
	}

	var made int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE org_id=$1 AND title='Should only be proposed'`,
		f.org).Scan(&made); err != nil {
		t.Fatalf("count: %v", err)
	}
	if made != 0 {
		t.Error("something was created while it was only supposed to be proposed")
	}

	pending, err := f.svc.Pending(ctx, f.org)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d suggestions waiting, want 1", len(pending))
	}
	if pending[0].AgentID != a.ID {
		t.Errorf("the suggestion is attributed to %s, want the agent", pending[0].AgentID)
	}
	if pending[0].Reason == "" {
		t.Error("the suggestion carries no reason, so nobody can evaluate it")
	}

	// Deciding it once works and twice does not, because a second accept would
	// apply the same change again.
	if err := f.svc.Decide(ctx, f.org, pending[0].ID, f.alice, "accepted", "done"); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if err := f.svc.Decide(ctx, f.org, pending[0].ID, f.alice, "accepted", "done"); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("a suggestion was decided twice: %v", err)
	}
}

// The protocol handshake has to be right or no client gets as far as a tool.
func TestTheProtocolHandshakeAnswersCorrectly(t *testing.T) {
	f := setup(t, false)
	_, c := f.agent(t, "Speaker", agents.ScopeRead)

	init, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2024-11-05"},
	})
	resp := f.mcp.Handle(context.Background(), c, init)
	if resp.Error != nil {
		t.Fatalf("initialize errored: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is %T", resp.Result)
	}
	if result["protocolVersion"] == "" {
		t.Error("no protocol version in the handshake")
	}
	if _, ok := result["capabilities"]; !ok {
		t.Error("no capabilities in the handshake")
	}
	// The instructions tell the model what it is, which is what makes a
	// proposing agent write reasons worth reading.
	if s, _ := result["instructions"].(string); !strings.Contains(s, "Speaker") {
		t.Errorf("the instructions do not name the agent: %q", s)
	}

	// tools/list, and a read-only token gets three.
	listReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	resp = f.mcp.Handle(context.Background(), c, listReq)
	if resp.Error != nil {
		t.Fatalf("tools/list errored: %+v", resp.Error)
	}
	tools, _ := resp.Result.(map[string]any)["tools"].([]agents.Tool)
	if len(tools) != 3 {
		t.Errorf("%d tools for a read token, want 3", len(tools))
	}
	for _, tool := range tools {
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %q has no description or no schema, and a model "+
				"chooses from those", tool.Name)
		}
	}

	// A method nobody implements is an error rather than a silence.
	bad, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "resources/list",
	})
	if resp := f.mcp.Handle(context.Background(), c, bad); resp.Error == nil {
		t.Error("an unimplemented method answered as if it worked")
	}
	// And rubbish is a parse error rather than a panic.
	if resp := f.mcp.Handle(context.Background(), c, []byte("{not json")); resp.Error == nil {
		t.Error("malformed JSON did not produce an error")
	}
}

// Suspending an agent takes it off its teams too, the same as a person: a
// stopped agent that is still in a team keeps what the team was granted.
func TestSuspendingAnAgentTakesItOffItsTeams(t *testing.T) {
	f, ctx := setup(t, false), context.Background()
	a, _ := f.agent(t, "Reporter", "read")
	team := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO teams (id, org_id, name) VALUES ($1,$2,'Reports')`, team, f.org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM teams WHERE org_id=$1`, f.org) })
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO team_members (team_id, principal_id) VALUES ($1,$2)`, team, a.ID); err != nil {
		t.Fatal(err)
	}
	count := func() (n int) {
		_ = f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE principal_id=$1`, a.ID).Scan(&n)
		return
	}
	if err := f.svc.SetStatus(ctx, f.org, a.ID, "suspended"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Errorf("a suspended agent is still in %d teams", n)
	}
	if err := f.svc.SetStatus(ctx, f.org, a.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Errorf("restoring an agent put it back in %d teams on its own", n)
	}
}
