package agents

// The assistant connector server.
//
// JSON-RPC 2.0 over one HTTP endpoint, which is the whole transport. No SDK:
// the protocol is a request envelope, a response envelope, and three methods,
// and a dependency to write forty lines of JSON is a dependency to keep in step
// with somebody else's release schedule.
//
// # The rule this file exists to keep
//
// Every tool goes through the ordinary service call with the agent's own
// subject set. Not a bypass, not an internal API, not a "trusted" path. The
// grants are the same object a person's request hits, which is why "an agent
// cannot read a document its principal has no grant for" is true by
// construction rather than by a check somebody remembered to add to each tool.
//
// The moment one tool reaches past that, the claim stops being true for all of
// them, and the way that happens is somebody adding a tool in a hurry.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/search"
)

// The protocol version this implements.
const protocolVersion = "2024-11-05"

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// The JSON-RPC codes this uses.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Tool is one thing an agent can do.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Content is one piece of a tool's answer.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult is what a call returns.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Spine is the slice of the product the tools reach.
//
// Declared here, by the consumer, and deliberately narrow. Every method on it
// takes a subject set, which is what makes it impossible to write a tool in
// this file that skips the permission check: there is no method available that
// would let one.
type Spine interface {
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*docs.Document, error)
	// Create is the ordinary create, for a person: they share what they made
	// the way they always do.
	Create(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error)
	// CreateAsAgent rather than Create. An agent has no hands to share what it
	// made with afterwards, so the sharing has to happen as part of making it.
	CreateAsAgent(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error)
	Update(ctx context.Context, orgID string, actor core.Subjects, id string, p docs.UpdateParams) (*docs.Document, error)
	List(ctx context.Context, orgID string, actor core.Subjects, p docs.ListParams) ([]*docs.Document, error)
	// Search is the views engine: the filtered list a person gets on the
	// Views page, so "assigned to me" means the same thing in both places.
	Search(ctx context.Context, orgID string, actor core.Subjects, q docs.Query) ([]*docs.Row, error)
}

// Finder is search, on the same subject set.
type Finder interface {
	Search(ctx context.Context, orgID string, actor core.Subjects, q search.Query) (*search.Results, error)
}

// Places is the space tree as the caller sees it, which is how a department
// named in a tool call becomes an id. Through the subject set like the rest:
// naming a department the caller cannot see finds nothing.
type Places interface {
	SpaceTree(ctx context.Context, orgID string, actor core.Subjects) ([]*plant.Space, error)
}

// Connector answers protocol requests.
type Connector struct {
	agents *Service
	docs   Spine
	find   Finder
	places Places
	// suggestOnly makes every write a suggestion regardless of scope. The
	// deployment-wide safety catch, for a site that wants agents reading and
	// proposing and nothing else.
	suggestOnly bool
	// extra are tool sets that live beside the document tools, such as the CRM's.
	extra []ToolSet
}

// ToolSet is a group of tools kept somewhere else and offered through this
// endpoint. write says whether this caller's writes land directly, which is the
// same test the document tools use, so a read-only token is never offered a
// write tool and a suggest-only deployment offers none.
type ToolSet interface {
	Tools(c *Caller, write bool) []Tool
	Call(ctx context.Context, c *Caller, name string, args map[string]any, write bool) (*ToolResult, bool)
}

// Add offers another tool set through this endpoint.
func (m *Connector) Add(ts ToolSet) *Connector {
	if ts != nil {
		m.extra = append(m.extra, ts)
	}
	return m
}

// NewConnector builds the tools. places may be nil, and then list refuses a
// department filter rather than ignoring it.
func NewConnector(a *Service, d Spine, f Finder, p Places, suggestOnly bool) *Connector {
	return &Connector{agents: a, docs: d, find: f, places: p, suggestOnly: suggestOnly}
}

// Tools is the catalogue.
//
// Four, and the descriptions are written for the model rather than for a
// person: an agent chooses a tool from its description, and a vague one is how
// it picks search when it meant read.
func (m *Connector) Tools(c *Caller) []Tool {
	tools := []Tool{
		{
			Name: "search",
			Description: "Search work orders, assets, documents, contacts, and " +
				"attachments. Returns only what this agent has been granted.",
			InputSchema: object(map[string]any{
				"query": prop("string", "What to look for."),
				"kind": prop("string", "Optional. Restrict to one kind: "+
					"work_order, asset, task, doc, wiki, note, purchase_order."),
			}, "query"),
		},
		{
			Name: "read",
			Description: "Read one entity in full by its id, including its body " +
				"and task fields.",
			InputSchema: object(map[string]any{
				"id": prop("string", "The entity id, as returned by search."),
			}, "id"),
		},
		{
			Name: "list",
			Description: "List entities, most recently updated first, narrowed by " +
				"kind, assignee, status, and department. Open work only unless a " +
				"status is given. Use this for \"what is on my plate\" or \"what is " +
				"open in Engineering\"; use search when looking for something by name.",
			InputSchema: object(map[string]any{
				"kind": prop("string", "Optional. work_order, asset, task, doc, wiki, "+
					"note, or purchase_order. Empty lists every kind."),
				"assigned_to_me": prop("boolean", "Optional. Only what is assigned "+
					"to the caller."),
				"status": prop("string", "Optional. open (the default: anything not "+
					"done or cancelled), all, todo, in_progress, blocked, done, or cancelled."),
				"department": prop("string", "Optional. A department or other space, "+
					"by name. Includes everything filed under it."),
				"limit": prop("integer", "How many, up to 100. Default 20."),
			}),
		},
	}

	for _, ts := range m.extra {
		tools = append(tools, ts.Tools(c, m.canWriteDirectly(c))...)
	}

	// The write tools appear only when the token carries a scope for them, so
	// a read-only agent is not told about doors it cannot open and does not
	// spend a turn discovering that.
	if c.Can(ScopeSuggest) {
		verb := "Propose"
		tail := " The change is recorded for a person to accept and nothing is " +
			"altered until they do."
		if m.canWriteDirectly(c) {
			verb, tail = "Create", ""
		}
		tools = append(tools,
			Tool{
				Name:        "create",
				Description: verb + " a new work order, task, or note." + tail,
				InputSchema: object(map[string]any{
					"kind":  prop("string", "work_order, task, or note."),
					"title": prop("string", "A short title."),
					"body":  prop("string", "Optional detail."),
				}, "kind", "title"),
			},
			Tool{
				Name:        "update",
				Description: verb + " a change to an existing entity's status or title." + tail,
				InputSchema: object(map[string]any{
					"id":     prop("string", "The entity id."),
					"status": prop("string", "Optional. todo, in_progress, blocked, done, or cancelled."),
					"title":  prop("string", "Optional. A new title."),
					"reason": prop("string", "Why this change is being proposed."),
				}, "id"),
			})
	}
	return tools
}

// canWriteDirectly reports whether this caller's writes land immediately.
func (m *Connector) canWriteDirectly(c *Caller) bool {
	if c.Person {
		return true
	}
	return !m.suggestOnly && c.Can(ScopeWrite)
}

// CallTool runs one tool for an already-authenticated caller and returns its
// answer as text, flagged when it is a refusal. The protocol layer
// (internal/mcpserver) is everything around this; the decisions are all here.
func (m *Connector) CallTool(ctx context.Context, c *Caller, name string, args map[string]any) (string, bool) {
	r := m.call(ctx, c, name, args)
	var b strings.Builder
	for _, part := range r.Content {
		b.WriteString(part.Text)
	}
	return b.String(), r.IsError
}

// Handle answers one JSON-RPC request.
func (m *Connector) Handle(ctx context.Context, c *Caller, body []byte) *Response {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return rpcErr(nil, codeParse, "that is not JSON")
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return rpcErr(req.ID, codeInvalidRequest, "this speaks JSON-RPC 2.0")
	}

	switch req.Method {
	case "initialize":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "hearth",
				"version": "1",
			},
			// Said out loud, because an agent that knows it is proposing rather
			// than doing writes better reasons.
			"instructions": m.instructions(c),
		}}

	case "notifications/initialized", "ping":
		// A notification has no id and wants no response body beyond an empty
		// result. Answering with an error here is how a client decides the
		// server is broken before it has asked anything.
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}

	case "tools/list":
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"tools": m.Tools(c),
		}}

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return rpcErr(req.ID, codeInvalidParams, "those parameters do not parse")
			}
		}
		result := m.call(ctx, c, p.Name, p.Arguments)
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: result}

	default:
		return rpcErr(req.ID, codeMethodNotFound, "no method "+req.Method)
	}
}

func (m *Connector) instructions(c *Caller) string {
	var b strings.Builder
	b.WriteString("Hearth is a maintenance workspace. You are acting as the agent " +
		c.AgentName + ", and every action is recorded against that name.\n\n")
	b.WriteString("You can only see what this agent has been granted. If a search " +
		"returns nothing, the thing may exist and not be yours to read; say so " +
		"rather than guessing.\n\n")
	switch {
	case !c.Can(ScopeSuggest):
		b.WriteString("This token is read only. You cannot change anything.")
	case !m.canWriteDirectly(c):
		b.WriteString("Your changes are proposals. Each one waits for a person to " +
			"accept it, so give a clear reason with every one: somebody has to " +
			"decide, and a proposal with no reason gets ignored or accepted " +
			"blindly, and both are bad.")
	default:
		b.WriteString("Your changes take effect immediately and are attributed to " +
			"you in the history of whatever you touch.")
	}
	return b.String()
}

// call runs one tool.
//
// Errors come back as a tool result with isError rather than as a JSON-RPC
// error, which is what the protocol asks for: a model can read and act on the
// first and can only give up on the second.
func (m *Connector) call(ctx context.Context, c *Caller, name string, args map[string]any) *ToolResult {
	if !c.Can(ScopeRead) {
		return toolErr("this token cannot read")
	}
	switch name {
	case "search":
		return m.toolSearch(ctx, c, args)
	case "read":
		return m.toolRead(ctx, c, args)
	case "list":
		return m.toolList(ctx, c, args)
	case "create":
		return m.toolCreate(ctx, c, args)
	case "update":
		return m.toolUpdate(ctx, c, args)
	default:
		for _, ts := range m.extra {
			if r, ok := ts.Call(ctx, c, name, args, m.canWriteDirectly(c)); ok {
				return r
			}
		}
		return toolErr("no tool called " + name)
	}
}

func (m *Connector) toolSearch(ctx context.Context, c *Caller, args map[string]any) *ToolResult {
	q := str(args, "query")
	if q == "" {
		return toolErr("search needs a query")
	}
	kinds, err := c.kindsFor(str(args, "kind"))
	if err != nil {
		return toolErr(err.Error())
	}
	res := &search.Results{}
	for _, k := range kinds {
		r, err := m.find.Search(ctx, c.OrgID, c.Subjects, search.Query{Text: q, Kind: k, Limit: 20})
		if err != nil {
			return toolErr("search failed: " + err.Error())
		}
		res.Hits = append(res.Hits, r.Hits...)
	}
	if len(kinds) > 1 {
		slices.SortStableFunc(res.Hits, func(a, b search.Hit) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
		if len(res.Hits) > 20 {
			res.Hits = res.Hits[:20]
		}
	}
	if len(res.Hits) == 0 {
		return toolText("Nothing found. It may not exist, or it may not be " +
			"granted to this agent.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results:\n", len(res.Hits))
	for _, h := range res.Hits {
		fmt.Fprintf(&b, "- %s [%s] id=%s\n", h.Title, h.Kind, h.EntityID)
		if h.Snippet != "" {
			fmt.Fprintf(&b, "  %s\n", stripMarks(h.Snippet))
		}
	}
	return toolText(b.String())
}

func (m *Connector) toolRead(ctx context.Context, c *Caller, args map[string]any) *ToolResult {
	id := str(args, "id")
	if id == "" {
		return toolErr("read needs an id")
	}
	d, err := m.docs.Get(ctx, c.OrgID, c.Subjects, id)
	if err != nil {
		// Not-found whether it is missing or not granted, exactly as a person
		// gets. Telling an agent that an id is real is a disclosure, and an
		// agent repeats what it is told.
		return toolErr("no such entity, or it is not granted to this agent")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s]\nid: %s\nupdated: %s\n",
		d.Title, d.Kind, d.ID, d.UpdatedAt.Format("2006-01-02 15:04"))
	if d.Task != nil {
		fmt.Fprintf(&b, "status: %s\npriority: %d\n", d.Task.Status, d.Task.Priority)
		if d.Task.DueAt != nil {
			fmt.Fprintf(&b, "due: %s\n", d.Task.DueAt.Format("2006-01-02"))
		}
	}
	if d.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", d.Body)
	}
	return toolText(b.String())
}

// listStatuses are the single statuses list accepts besides open and all.
var listStatuses = []string{docs.StatusTodo, docs.StatusInProgress,
	docs.StatusBlocked, docs.StatusDone, docs.StatusCancelled}

func (m *Connector) toolList(ctx context.Context, c *Caller, args map[string]any) *ToolResult {
	limit := num(args, "limit", 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	kinds, err := c.kindsFor(str(args, "kind"))
	if err != nil {
		return toolErr(err.Error())
	}
	q := docs.Query{
		Sorts: []docs.Sort{{Field: docs.Col("updated"), Desc: true}},
		Limit: limit,
	}

	if boolean(args, "assigned_to_me") {
		q.Filters = append(q.Filters, docs.Filter{
			Field: docs.Col("assignee"), Op: docs.OpIs, Values: []string{c.AgentID}})
	}

	// Open by default, the way the Views page hides closed work. A super admin
	// asking for "my tasks" got every task ever closed in the org, newest
	// first, which buried the open ones.
	status := strings.ToLower(str(args, "status"))
	switch {
	case status == "" || status == "open":
		for _, s := range docs.ClosedStatuses {
			q.Filters = append(q.Filters, docs.Filter{
				Field: docs.Col("status"), Op: docs.OpIsNot, Values: []string{s}})
		}
	case status == "all":
	case slices.Contains(listStatuses, status):
		q.Filters = append(q.Filters, docs.Filter{
			Field: docs.Col("status"), Op: docs.OpIs, Values: []string{status}})
	default:
		return toolErr("status must be open, all, or one of: " + strings.Join(listStatuses, ", "))
	}

	if name := str(args, "department"); name != "" {
		id, problem := m.findSpace(ctx, c, name)
		if problem != "" {
			return toolErr(problem)
		}
		q.SpaceID = &id
	}

	var rows []*docs.Row
	for _, k := range kinds {
		q.Kind = k
		part, err := m.docs.Search(ctx, c.OrgID, c.Subjects, q)
		if err != nil {
			return toolErr("list failed: " + err.Error())
		}
		rows = append(rows, part...)
	}
	if len(kinds) > 1 {
		slices.SortStableFunc(rows, func(a, b *docs.Row) int {
			return b.Document.UpdatedAt.Compare(a.Document.UpdatedAt)
		})
		if len(rows) > limit {
			rows = rows[:limit]
		}
	}
	if len(rows) == 0 {
		return toolText("Nothing matches. It may not exist, or it may not be " +
			"granted to this agent.")
	}
	var b strings.Builder
	for _, r := range rows {
		d := r.Document
		fmt.Fprintf(&b, "- %s [%s]", d.Title, d.Kind)
		if d.Task != nil {
			fmt.Fprintf(&b, " %s", d.Task.Status)
			if d.Task.DueAt != nil {
				fmt.Fprintf(&b, " due %s", d.Task.DueAt.Format("2006-01-02"))
			}
		}
		fmt.Fprintf(&b, " id=%s\n", d.ID)
	}
	if len(rows) == limit {
		fmt.Fprintf(&b, "\nShowing the first %d. There may be more: narrow the "+
			"filters or raise the limit.\n", limit)
	}
	return toolText(b.String())
}

// findSpace turns a department name into a space id, from the tree the caller
// can see. Shallowest match wins, so the department beats a project that
// happens to share its name. When nothing matches, the answer names the
// departments that do exist, so the model can correct itself rather than guess.
func (m *Connector) findSpace(ctx context.Context, c *Caller, name string) (string, string) {
	if m.places == nil {
		return "", "filtering by department is not available here"
	}
	tree, err := m.places.SpaceTree(ctx, c.OrgID, c.Subjects)
	if err != nil {
		return "", "could not load departments: " + err.Error()
	}
	want := strings.ToLower(strings.TrimSpace(name))
	var best *plant.Space
	var top []string
	for _, sp := range tree {
		if sp.Depth == 0 {
			top = append(top, sp.Name)
		}
		if sp.ID != name && strings.ToLower(strings.TrimSpace(sp.Name)) != want {
			continue
		}
		if best == nil || sp.Depth < best.Depth {
			best = sp
		}
	}
	if best == nil {
		return "", "no department called " + name + ". Departments: " + strings.Join(top, ", ")
	}
	return best.ID, ""
}

func (m *Connector) toolCreate(ctx context.Context, c *Caller, args map[string]any) *ToolResult {
	if !c.Can(ScopeSuggest) {
		return toolErr("this token cannot create anything")
	}
	kind, title := str(args, "kind"), str(args, "title")
	if title == "" {
		return toolErr("create needs a title")
	}
	if kind == "" {
		kind = docs.KindTask
		if len(c.Kinds) > 0 && !c.coversKind(kind) {
			kind = c.Kinds[0]
		}
	}
	if !c.coversKind(kind) {
		return toolErr("this tool cannot create a " + kind + ": it covers " + strings.Join(c.Kinds, ", "))
	}

	if !m.canWriteDirectly(c) {
		g, err := m.agents.Suggest(ctx, c, ActionCreate, string(core.EntityDocument), nil,
			map[string]any{"kind": kind, "title": title, "body": str(args, "body")},
			str(args, "reason"))
		if err != nil {
			return toolErr("could not record the proposal: " + err.Error())
		}
		return toolText("Proposed. A person has to accept it before anything " +
			"changes. Reference " + g.ID + ".")
	}

	// The ordinary create, with the agent's own subject set. It gets admin on
	// what it made, exactly as a person does, and the history says the agent
	// created it. Its other subjects come too, or the work order is invisible
	// to every human being on site.
	create := m.docs.CreateAsAgent
	if c.Person {
		create = m.docs.Create
	}
	d, err := create(ctx, c.OrgID, c.Subjects, docs.CreateParams{
		Kind: kind, Title: title, Body: str(args, "body"),
		Task: taskFor(kind),
	})
	if err != nil {
		return toolErr("could not create it: " + err.Error())
	}
	return toolText("Created " + d.ID + ".")
}

func (m *Connector) toolUpdate(ctx context.Context, c *Caller, args map[string]any) *ToolResult {
	if !c.Can(ScopeSuggest) {
		return toolErr("this token cannot change anything")
	}
	id := str(args, "id")
	if id == "" {
		return toolErr("update needs an id")
	}
	status, title := str(args, "status"), str(args, "title")
	if status == "" && title == "" {
		return toolErr("update needs a status or a title")
	}

	// Read it first, with the agent's subjects, so a proposal about something
	// the agent cannot see is refused here rather than becoming a suggestion
	// that leaks an id to whoever reviews the queue.
	current, err := m.docs.Get(ctx, c.OrgID, c.Subjects, id)
	if err != nil {
		return toolErr("no such entity, or it is not granted to this agent")
	}
	if !c.coversKind(current.Kind) {
		return toolErr("no such entity, or it is not granted to this agent")
	}

	if !m.canWriteDirectly(c) {
		payload := map[string]any{}
		if status != "" {
			payload["status"] = status
		}
		if title != "" {
			payload["title"] = title
		}
		g, err := m.agents.Suggest(ctx, c, actionFor(status), string(core.EntityDocument),
			&id, payload, str(args, "reason"))
		if err != nil {
			return toolErr("could not record the proposal: " + err.Error())
		}
		return toolText("Proposed. A person has to accept it before anything " +
			"changes. Reference " + g.ID + ".")
	}

	p := docs.UpdateParams{ExpectedVersion: current.Version}
	if title != "" {
		p.Title = &title
	}
	if status != "" {
		p.Task = &docs.TaskPatch{Status: &status}
	}
	cctx, closed := docs.WithClosedSink(ctx)
	if _, err := m.docs.Update(cctx, c.OrgID, c.Subjects, id, p); err != nil {
		return toolErr("could not change it: " + err.Error())
	}
	if n := len(closed.IDs); n > 0 {
		if closed.Status == docs.StatusDone {
			return toolText(fmt.Sprintf("Updated %s. Closed with %d open subtask(s): %s.", id, n, strings.Join(closed.IDs, ", ")))
		}
		return toolText(fmt.Sprintf("Updated %s. Moved %d open subtask(s) to %s with it: %s.", id, n, docs.StatusLabel(closed.Status), strings.Join(closed.IDs, ", ")))
	}
	return toolText("Updated " + id + ".")
}

// coversKind reports whether a document of this kind is within the caller's
// confinement. An unconfined caller covers every kind.
func (c *Caller) coversKind(kind string) bool {
	return len(c.Kinds) == 0 || slices.Contains(c.Kinds, kind)
}

// kindsFor turns the kind a call asked for into the kinds to query. An
// unconfined caller gets what it asked for, empty meaning all of them. A
// confined one gets its own kinds when it asked for none, and a refusal that
// names them when it asked for one outside.
func (c *Caller) kindsFor(requested string) ([]string, error) {
	if len(c.Kinds) == 0 {
		return []string{requested}, nil
	}
	if requested == "" {
		return c.Kinds, nil
	}
	if !slices.Contains(c.Kinds, requested) {
		return nil, fmt.Errorf("this tool does not cover %q: it covers %s", requested, strings.Join(c.Kinds, ", "))
	}
	return []string{requested}, nil
}

func actionFor(status string) string {
	if status != "" {
		return ActionSetStatus
	}
	return ActionCreate
}

func taskFor(kind string) *docs.TaskFields {
	if docs.HasTaskFields(kind) {
		return &docs.TaskFields{Status: docs.StatusTodo}
	}
	return nil
}

// -----------------------------------------------------------------------------

func rpcErr(id json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

func toolText(s string) *ToolResult {
	return &ToolResult{Content: []Content{{Type: "text", Text: s}}}
}

func toolErr(s string) *ToolResult {
	return &ToolResult{Content: []Content{{Type: "text", Text: s}}, IsError: true}
}

func str(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func num(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return fallback
}

// boolean reads a flag. A model sometimes sends "true" as a string, and
// treating that as false would silently widen the list it asked to narrow.
func boolean(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

// object builds a JSON Schema for a tool's arguments.
func object(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type": "object", "properties": props, "required": required,
	}
}

func prop(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}

// stripMarks removes the highlight markers search puts round a match. They are
// for a browser, and a model reads them as content.
func stripMarks(s string) string {
	s = strings.ReplaceAll(s, "<mark>", "")
	return strings.ReplaceAll(s, "</mark>", "")
}

// ErrIsNotFound reports whether an error should be reported to an agent as
// not-found rather than as a failure, so a permission refusal and a missing row
// are indistinguishable from outside.
func ErrIsNotFound(err error) bool {
	return errors.Is(err, docs.ErrNotFound) || errors.Is(err, ErrNotFound)
}
