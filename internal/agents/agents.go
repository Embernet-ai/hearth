// Package agents is machines using the product through the same door people
// do.
//
// # The one idea
//
// An agent is a principal. principals.kind has permitted 'agent' since the
// first migration, and spending that rather than inventing an API-key table
// with its own permission logic is what makes every claim in this package free:
// an agent has grants, an agent appears in history, an agent can be mentioned,
// and an agent that was never granted a document cannot read it because the
// same authz call says no.
//
// The alternative, a service account with a bearer token and its own rules, is
// how integrations end up able to read everything. It happens because the
// second permission system is written in a hurry by somebody who is really
// trying to ship an integration.
//
// # Tokens narrow, never widen
//
// A token's scopes bound what it may do BELOW what its principal may do. Read
// only means read only even for an agent that can edit. The grants remain the
// ceiling, always, and there is a test that says so.
//
// # Proposing rather than writing
//
// An agent that writes directly is one bad call away from reassigning every
// work order on site, and "it is attributable afterwards" does not help the
// shift that lost its morning. Writes that are not obviously safe become
// suggestions, and nothing in that table has changed anything until a person
// presses accept.
package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("that is not something an agent can do")
	// ErrNoToken and ErrScope are separate because they mean different things
	// to whoever is debugging an integration at eleven at night: one is a bad
	// credential, the other is a credential doing something it was not given.
	ErrNoToken = errors.New("no valid token")
	ErrScope   = errors.New("that token does not carry the scope for this")
)

// Scopes. Deliberately three, and deliberately coarse: a permission system
// with thirty scopes is one nobody configures correctly, and the real
// enforcement is the grants underneath.
const (
	ScopeRead    = "read"
	ScopeWrite   = "write"
	ScopeSuggest = "suggest"
)

// Scopes in the order the form offers them.
var Scopes = []string{ScopeRead, ScopeSuggest, ScopeWrite}

var validScopes = map[string]bool{ScopeRead: true, ScopeWrite: true, ScopeSuggest: true}

// Agent is a non-human principal.
type Agent struct {
	ID          string
	OrgID       string
	Name        string
	Handle      string
	Status      string
	Tokens      int
	Suggestions int
	// Channels is how many conversations this agent belongs to, and it is on
	// the list page for one reason: an agent in none of them produces work
	// only it can see. That is the honest consequence of channel membership
	// being the permission model, and it is invisible unless a page says so.
	Channels  int
	CreatedAt time.Time
}

// Isolated reports whether anything this agent creates will be invisible to
// everybody else.
func (a Agent) Isolated() bool { return a.Channels == 0 }

// Active reports whether this agent may still act.
func (a Agent) Active() bool { return a.Status == "active" }

// Token is a credential. The secret itself exists exactly once, at creation.
type Token struct {
	ID         string
	OrgID      string
	AgentID    string
	AgentName  string
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	// Secret is set only by Mint, and only on the call that created it. There
	// is nowhere to read it back from, which is the point.
	Secret string
}

// Can reports whether this token carries a scope.
//
// The three are ordered rather than independent: write implies suggest implies
// read. A token allowed to change something is allowed to propose the change
// and obviously allowed to look at what it is changing, and update has to read
// the current version before it can write one. Making somebody tick all three
// boxes to get a working token is a footgun dressed as a safeguard, and the
// first person to hit it ticks every box on every token forever.
func (t Token) Can(scope string) bool {
	for _, s := range t.Scopes {
		if rank(s) >= rank(scope) && rank(scope) > 0 {
			return true
		}
	}
	return false
}

func rank(scope string) int {
	switch scope {
	case ScopeRead:
		return 1
	case ScopeSuggest:
		return 2
	case ScopeWrite:
		return 3
	default:
		return 0
	}
}

// Suggestion is a proposed write waiting for a person.
type Suggestion struct {
	ID          string
	OrgID       string
	AgentID     string
	AgentName   string
	Action      string
	EntityType  string
	EntityID    *string
	EntityTitle string
	Payload     map[string]any
	Reason      string
	Status      string
	CreatedAt   time.Time
	DecidedAt   *time.Time
	DecidedBy   string
	Result      string
}

// Pending reports whether it is still waiting.
func (s Suggestion) Pending() bool { return s.Status == "pending" }

// Summary is the one line a person reads before deciding.
func (s Suggestion) Summary() string {
	switch s.Action {
	case ActionComment:
		return "comment on " + s.title()
	case ActionSetStatus:
		return "set " + s.title() + " to " + s.str("status")
	case ActionAssign:
		return "assign " + s.title()
	case ActionCreate:
		return "create a " + s.str("kind") + " called " + s.str("title")
	default:
		return s.Action + " on " + s.title()
	}
}

func (s Suggestion) title() string {
	if s.EntityTitle != "" {
		return s.EntityTitle
	}
	if s.EntityID != nil {
		return *s.EntityID
	}
	return "something"
}

func (s Suggestion) str(key string) string {
	if v, ok := s.Payload[key].(string); ok {
		return v
	}
	return ""
}

// Actions an agent can propose.
const (
	ActionCreate    = "create"
	ActionSetStatus = "set_status"
	ActionAssign    = "assign"
	ActionComment   = "comment"
)

var validActions = map[string]bool{
	ActionCreate: true, ActionSetStatus: true, ActionAssign: true, ActionComment: true,
}

// Subjects resolves an agent principal's permission subjects, so an agent is
// checked by exactly the machinery a person is.
type Subjects interface {
	SubjectsFor(ctx context.Context, orgID, principalID string) (core.Subjects, error)
}

type Service struct {
	db       *pgxpool.Pool
	subjects Subjects
}

func New(db *pgxpool.Pool, s Subjects) *Service { return &Service{db: db, subjects: s} }

// -----------------------------------------------------------------------------
// Agents
// -----------------------------------------------------------------------------

// Create adds an agent principal.
//
// A principal, not a row in a table of API consumers. That is the whole design:
// everything else in this package is short because the substrate already knows
// what a principal is.
func (s *Service) Create(ctx context.Context, orgID, actorID, name, handle string) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: an agent needs a name", ErrInvalid)
	}
	handle = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(handle, "@")))
	if handle == "" {
		handle = slug(name)
	}
	if actorID == "" {
		return nil, errors.New("agents: no acting principal")
	}

	a := &Agent{ID: uuid.NewString(), OrgID: orgID, Name: name, Handle: handle, Status: "active"}
	err := s.db.QueryRow(ctx, `
		INSERT INTO principals (id, org_id, kind, display_name, handle)
		VALUES ($1,$2,'agent',$3,$4)
		RETURNING created_at`, a.ID, orgID, name, handle).Scan(&a.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: the handle @%s is taken", ErrInvalid, handle)
		}
		return nil, fmt.Errorf("agents: create: %w", err)
	}
	return a, nil
}

// SetStatus suspends or restores an agent.
//
// Suspending is the stop button, and it has to work on the token that is
// already in somebody's script: Authenticate checks the principal's status on
// every call rather than caching it, so revoking an agent takes effect on its
// next request rather than on its next restart.
func (s *Service) SetStatus(ctx context.Context, orgID, id, status string) error {
	if status != "active" && status != "suspended" {
		return fmt.Errorf("%w: %q is not a status", ErrInvalid, status)
	}
	// One transaction, so a stopped agent is never still in a team. A team is
	// a grant subject, and restoring an agent does not put it back: that is an
	// admin's act, the same as for a person.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("agents: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `
		UPDATE principals SET status=$3, updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND kind='agent'`, id, orgID, status)
	if err != nil {
		return fmt.Errorf("agents: set status: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if status == "suspended" {
		if _, err := tx.Exec(ctx, `DELETE FROM team_members WHERE principal_id = $1`, id); err != nil {
			return fmt.Errorf("agents: take off teams: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// List returns the agents in an org.
func (s *Service) List(ctx context.Context, orgID string) ([]*Agent, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, COALESCE(NULLIF(p.display_name,''), ''), COALESCE(p.handle::text, ''),
		       p.status, p.created_at,
		       (SELECT count(*) FROM agent_tokens t
		         WHERE t.principal_id = p.id AND t.revoked_at IS NULL)::int,
		       (SELECT count(*) FROM agent_suggestions g
		         WHERE g.principal_id = p.id AND g.status = 'pending')::int,
		       (SELECT count(*) FROM channel_members cm
		          JOIN channels ch ON ch.id = cm.channel_id AND ch.archived_at IS NULL
		         WHERE cm.principal_id = p.id)::int
		FROM principals p
		WHERE p.org_id = $1 AND p.kind = 'agent' AND p.status <> 'deleted'
		ORDER BY p.display_name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("agents: list: %w", err)
	}
	defer rows.Close()

	var out []*Agent
	for rows.Next() {
		a := &Agent{OrgID: orgID}
		if err := rows.Scan(&a.ID, &a.Name, &a.Handle, &a.Status, &a.CreatedAt,
			&a.Tokens, &a.Suggestions, &a.Channels); err != nil {
			return nil, fmt.Errorf("agents: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Tokens
// -----------------------------------------------------------------------------

// Mint issues a credential and returns it once.
//
// Once, and there is nowhere to read it back from. A token that can be
// recovered from the interface is a token that leaks through a screen share,
// and "regenerate" is a smaller inconvenience than that.
func (s *Service) Mint(ctx context.Context, orgID, actorID, agentID, name string,
	scopes []string, ttl time.Duration) (*Token, error) {

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: a token needs a name, so it can be revoked "+
			"without guessing which one it is", ErrInvalid)
	}
	clean := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		if validScopes[sc] {
			clean = append(clean, sc)
		}
	}
	if len(clean) == 0 {
		// Read, because a token with no scopes at all is a token that fails
		// mysteriously rather than one that is obviously narrow.
		clean = []string{ScopeRead}
	}

	// 32 bytes. The prefix is there so a leaked token is recognisable in a log
	// or a repository scan as something to revoke rather than as noise.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("agents: generate token: %w", err)
	}
	secret := "hearth_agent_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))

	t := &Token{
		ID: uuid.NewString(), OrgID: orgID, AgentID: agentID, Name: name,
		Scopes: clean, Secret: secret,
	}
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires = &e
		t.ExpiresAt = expires
	}
	err := s.db.QueryRow(ctx, `
		INSERT INTO agent_tokens (id, org_id, principal_id, name, hash, scopes,
		                          created_by, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING created_at`,
		t.ID, orgID, agentID, name, sum[:], clean, actorID, expires).Scan(&t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("agents: mint: %w", err)
	}
	return t, nil
}

// Revoke stops a token.
func (s *Service) Revoke(ctx context.Context, orgID, id string) error {
	ct, err := s.db.Exec(ctx,
		`UPDATE agent_tokens SET revoked_at=now() WHERE id=$1 AND org_id=$2 AND revoked_at IS NULL`,
		id, orgID)
	if err != nil {
		return fmt.Errorf("agents: revoke: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Tokens lists the live credentials for an org.
func (s *Service) Tokens(ctx context.Context, orgID string) ([]*Token, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, t.principal_id::text, COALESCE(p.display_name,''), t.name,
		       t.scopes, t.created_at, t.last_used_at, t.expires_at
		FROM agent_tokens t
		JOIN principals p ON p.id = t.principal_id
		WHERE t.org_id = $1 AND t.revoked_at IS NULL
		ORDER BY p.display_name, t.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("agents: list tokens: %w", err)
	}
	defer rows.Close()

	var out []*Token
	for rows.Next() {
		t := &Token{OrgID: orgID}
		if err := rows.Scan(&t.ID, &t.AgentID, &t.AgentName, &t.Name, &t.Scopes,
			&t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, fmt.Errorf("agents: scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Caller is an authenticated agent, ready to be checked like a person.
type Caller struct {
	OrgID     string
	AgentID   string
	AgentName string
	TokenID   string
	Scopes    []string
	// Subjects is what authz takes. It is the agent's own subject set, so
	// every permission question in the product answers for an agent the same
	// way it answers for anybody else.
	Subjects core.Subjects
	// Person is a human signed in through Vord (internal/mcpserver) rather
	// than an agent token: AgentID and AgentName are then that person's
	// principal and name. A person acts as themselves -- their writes land
	// directly, with exactly the rights they have on the website, because a
	// suggestion queue for your own changes would be reviewing yourself.
	Person bool
	// Kinds, when set, confines the document tools to those kinds: search and
	// list cover only them, and read, create and update refuse anything else.
	// The Hub module (internal/hubmodule) sets it per tool so that holding the
	// ability for work orders does not open assets or documents. Empty is the
	// ordinary caller, unconfined.
	Kinds []string
}

// Can reports whether the token carries a scope.
func (c *Caller) Can(scope string) bool {
	return Token{Scopes: c.Scopes}.Can(scope)
}

// Authenticate resolves a bearer token to a caller.
//
// Every check happens here and on every call: the token exists, is not
// revoked, has not expired, and its principal is still active. Caching any of
// that would mean suspending an agent takes effect whenever the cache felt
// like it, and the stop button has to work now.
func (s *Service) Authenticate(ctx context.Context, secret string) (*Caller, error) {
	secret = strings.TrimSpace(strings.TrimPrefix(secret, "Bearer "))
	if secret == "" {
		return nil, ErrNoToken
	}
	sum := sha256.Sum256([]byte(secret))

	c := &Caller{}
	var status string
	err := s.db.QueryRow(ctx, `
		SELECT t.id::text, t.org_id::text, t.principal_id::text,
		       COALESCE(p.display_name,''), t.scopes, p.status
		FROM agent_tokens t
		JOIN principals p ON p.id = t.principal_id
		WHERE t.hash = $1
		  AND t.revoked_at IS NULL
		  AND (t.expires_at IS NULL OR t.expires_at > now())`, sum[:]).
		Scan(&c.TokenID, &c.OrgID, &c.AgentID, &c.AgentName, &c.Scopes, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNoToken
	case err != nil:
		return nil, fmt.Errorf("agents: authenticate: %w", err)
	}
	if status != "active" {
		return nil, ErrNoToken
	}

	subjects, err := s.subjects.SubjectsFor(ctx, c.OrgID, c.AgentID)
	if err != nil {
		return nil, fmt.Errorf("agents: subjects: %w", err)
	}
	c.Subjects = subjects

	// Best effort, and deliberately not in the hot path's error handling: a
	// failure to record that a token was used must not fail the call it was
	// used for.
	_, _ = s.db.Exec(ctx, `UPDATE agent_tokens SET last_used_at=now() WHERE id=$1`, c.TokenID)
	return c, nil
}

// -----------------------------------------------------------------------------
// Suggestions
// -----------------------------------------------------------------------------

// Suggest records a proposed write. Nothing changes until somebody accepts.
func (s *Service) Suggest(ctx context.Context, c *Caller, action, entityType string,
	entityID *string, payload map[string]any, reason string) (*Suggestion, error) {

	if !validActions[action] {
		return nil, fmt.Errorf("%w: %q", ErrInvalid, action)
	}
	if !c.Can(ScopeSuggest) {
		return nil, ErrScope
	}
	if entityType == "" {
		entityType = string(core.EntityDocument)
	}
	if entityID != nil && *entityID == "" {
		entityID = nil
	}
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("agents: encode suggestion: %w", err)
	}

	g := &Suggestion{
		ID: uuid.NewString(), OrgID: c.OrgID, AgentID: c.AgentID, AgentName: c.AgentName,
		Action: action, EntityType: entityType, EntityID: entityID,
		Payload: payload, Reason: strings.TrimSpace(reason), Status: "pending",
	}
	err = s.db.QueryRow(ctx, `
		INSERT INTO agent_suggestions (id, org_id, principal_id, action, entity_type,
		                               entity_id, payload, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING created_at`,
		g.ID, c.OrgID, c.AgentID, action, entityType, entityID, encoded, g.Reason).
		Scan(&g.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("agents: suggest: %w", err)
	}
	return g, nil
}

// Pending lists what is waiting for a person.
func (s *Service) Pending(ctx context.Context, orgID string) ([]*Suggestion, error) {
	return s.list(ctx, orgID, `AND g.status = 'pending'`)
}

// Recent lists the last decisions as well, so a page can show what happened to
// what somebody accepted yesterday.
func (s *Service) Recent(ctx context.Context, orgID string) ([]*Suggestion, error) {
	return s.list(ctx, orgID, `AND g.status <> 'pending'`)
}

func (s *Service) list(ctx context.Context, orgID, where string) ([]*Suggestion, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id::text, g.principal_id::text, COALESCE(p.display_name,''),
		       g.action, g.entity_type, g.entity_id::text,
		       COALESCE(d.title, ''), g.payload, g.reason, g.status,
		       g.created_at, g.decided_at, COALESCE(dp.display_name,''), g.result
		FROM agent_suggestions g
		JOIN principals p ON p.id = g.principal_id
		LEFT JOIN documents d ON d.id = g.entity_id AND d.deleted_at IS NULL
		LEFT JOIN principals dp ON dp.id = g.decided_by
		WHERE g.org_id = $1 `+where+`
		ORDER BY g.created_at DESC LIMIT 100`, orgID)
	if err != nil {
		return nil, fmt.Errorf("agents: list suggestions: %w", err)
	}
	defer rows.Close()

	var out []*Suggestion
	for rows.Next() {
		g := &Suggestion{OrgID: orgID}
		var payload []byte
		if err := rows.Scan(&g.ID, &g.AgentID, &g.AgentName, &g.Action, &g.EntityType,
			&g.EntityID, &g.EntityTitle, &payload, &g.Reason, &g.Status,
			&g.CreatedAt, &g.DecidedAt, &g.DecidedBy, &g.Result); err != nil {
			return nil, fmt.Errorf("agents: scan suggestion: %w", err)
		}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &g.Payload)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Get returns one suggestion.
func (s *Service) Get(ctx context.Context, orgID, id string) (*Suggestion, error) {
	list, err := s.list(ctx, orgID, `AND g.id = '`+safeUUID(id)+`'`)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

// Decide records the outcome. The write itself is the caller's job, because
// this package must not know how to change a work order.
func (s *Service) Decide(ctx context.Context, orgID, id, deciderID, status, result string) error {
	if status != "accepted" && status != "rejected" && status != "failed" {
		return fmt.Errorf("%w: %q is not a decision", ErrInvalid, status)
	}
	ct, err := s.db.Exec(ctx, `
		UPDATE agent_suggestions
		   SET status=$3, decided_at=now(), decided_by=$4, result=$5
		 WHERE id=$1 AND org_id=$2 AND status='pending'`,
		id, orgID, status, deciderID, result)
	if err != nil {
		return fmt.Errorf("agents: decide: %w", err)
	}
	if ct.RowsAffected() == 0 {
		// Already decided, or never existed. Both mean the same thing to the
		// caller: do not act on it.
		return ErrNotFound
	}
	return nil
}

// -----------------------------------------------------------------------------

// slug turns a name into a handle.
func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// safeUUID keeps an id that is interpolated rather than bound from being
// anything but an id.
//
// Interpolating at all is a compromise: the list query shares one WHERE
// fragment between three callers and threading a variadic argument list
// through it was worse. This is what makes the compromise safe, and it is a
// whitelist rather than an escape.
func safeUUID(v string) string {
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F', r == '-':
		default:
			return "00000000-0000-0000-0000-000000000000"
		}
	}
	if len(v) != 36 {
		return "00000000-0000-0000-0000-000000000000"
	}
	return v
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
