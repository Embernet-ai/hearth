// Package api is the public REST interface, and it has no privileged back
// door.
//
// # The whole design
//
// Every handler here resolves a token to a principal, builds that principal's
// subject set with the same call the session middleware uses, and then calls
// the same service method the HTML page calls. There is no "internal" variant,
// no service account, and no bypass flag.
//
// That is why "the API refuses what the interface refuses, for the same
// caller" is true by construction rather than by a check somebody remembered
// to write into each endpoint. The way that claim usually stops being true is
// an endpoint added in a hurry that reaches for the database directly, and the
// interface this package holds does not offer a method that would let one.
//
// # Why a token belongs to a person
//
// An API token acts AS somebody. It is not a second identity with its own
// permissions, because the moment it is, two people have to be kept in step
// and one of them is a row nobody looks at. Somebody who leaves and is
// suspended takes their tokens with them, which is the behaviour an auditor
// asks about first.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

var (
	ErrNoToken  = errors.New("api: no valid token")
	ErrNotFound = errors.New("api: not found")
	ErrInvalid  = errors.New("api: that request cannot be answered")
)

// Scopes. Two, and the grants underneath are the real answer.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

// Scopes in the order the form offers them.
var Scopes = []string{ScopeRead, ScopeWrite}

// Token is a credential belonging to a person.
type Token struct {
	ID          string
	OrgID       string
	PrincipalID string
	OwnerName   string
	Name        string
	Scopes      []string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	ExpiresAt   *time.Time
	// Secret exists once, on the call that minted it.
	Secret string
}

// Can reports whether this token carries a scope. Write implies read, for the
// same reason it does for agents: a write endpoint reads the current version
// first, and a token that cannot is a token that cannot write.
func (t Token) Can(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope || (s == ScopeWrite && scope == ScopeRead) {
			return true
		}
	}
	return false
}

// Caller is an authenticated request, ready to be checked exactly like a
// session.
type Caller struct {
	OrgID       string
	PrincipalID string
	Name        string
	TokenID     string
	Scopes      []string
	Subjects    core.Subjects
}

func (c *Caller) Can(scope string) bool { return Token{Scopes: c.Scopes}.Can(scope) }

// Subjects resolves a principal's permission subjects.
type Subjects interface {
	SubjectsFor(ctx context.Context, orgID, principalID string) (core.Subjects, error)
}

// Spine is the slice of the product the endpoints reach.
//
// Every method takes a subject set. That is not a convention, it is the
// mechanism: there is no method on this interface that could be called without
// one, so an endpoint that skips the permission check cannot be written here
// without changing this declaration, which is the sort of change somebody
// notices in review.
type Spine interface {
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*docs.Document, error)
	List(ctx context.Context, orgID string, actor core.Subjects, p docs.ListParams) ([]*docs.Document, error)
	Create(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error)
	Update(ctx context.Context, orgID string, actor core.Subjects, id string, p docs.UpdateParams) (*docs.Document, error)
	Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error
	Search(ctx context.Context, orgID string, actor core.Subjects, q docs.Query) ([]*docs.Row, error)
}

type Service struct {
	db       *pgxpool.Pool
	subjects Subjects
	docs     Spine
	// crmStore and sales are the CRM endpoints' services (WithCRM). Nil leaves
	// those routes answering 404.
	crmStore crm.Store
	sales    crm.Sales
	// time is the admin time removal (WithTime). Nil leaves that route 404.
	time timelog.Store
	// timeoff is the time off routes and tools (WithTimeOff). Nil leaves them 404.
	timeoff timeoff.Store
	// spaces is the space routes (WithSpaces). Nil leaves them 404.
	spaces SpaceStore
}

func New(db *pgxpool.Pool, s Subjects, d Spine) *Service {
	return &Service{db: db, subjects: s, docs: d}
}

// -----------------------------------------------------------------------------
// Tokens
// -----------------------------------------------------------------------------

// Mint issues a token that acts as one person.
func (s *Service) Mint(ctx context.Context, orgID, principalID, name string,
	scopes []string, ttl time.Duration) (*Token, error) {

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: a token needs a name so it can be revoked "+
			"without guessing which one it is", ErrInvalid)
	}
	clean := make([]string, 0, 2)
	for _, sc := range scopes {
		if sc == ScopeRead || sc == ScopeWrite {
			clean = append(clean, sc)
		}
	}
	if len(clean) == 0 {
		clean = []string{ScopeRead}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("api: generate token: %w", err)
	}
	// The prefix makes a leaked token recognisable in a log or a repository
	// scan as something to revoke rather than as noise.
	secret := "hearth_api_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))

	t := &Token{
		ID: uuid.NewString(), OrgID: orgID, PrincipalID: principalID,
		Name: name, Scopes: clean, Secret: secret,
	}
	var expires *time.Time
	if ttl > 0 {
		e := time.Now().Add(ttl)
		expires, t.ExpiresAt = &e, &e
	}
	if err := s.db.QueryRow(ctx, `
		INSERT INTO api_tokens (id, org_id, principal_id, name, hash, scopes, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`,
		t.ID, orgID, principalID, name, sum[:], clean, expires).Scan(&t.CreatedAt); err != nil {
		return nil, fmt.Errorf("api: mint: %w", err)
	}
	return t, nil
}

// Revoke stops a token.
func (s *Service) Revoke(ctx context.Context, orgID, id string) error {
	ct, err := s.db.Exec(ctx,
		`UPDATE api_tokens SET revoked_at=now() WHERE id=$1 AND org_id=$2 AND revoked_at IS NULL`,
		id, orgID)
	if err != nil {
		return fmt.Errorf("api: revoke: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Tokens lists the live credentials in an org.
func (s *Service) Tokens(ctx context.Context, orgID string) ([]*Token, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, t.principal_id::text, COALESCE(p.display_name,''), t.name,
		       t.scopes, t.created_at, t.last_used_at, t.expires_at
		FROM api_tokens t
		JOIN principals p ON p.id = t.principal_id
		WHERE t.org_id = $1 AND t.revoked_at IS NULL
		ORDER BY p.display_name, t.name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("api: list tokens: %w", err)
	}
	defer rows.Close()

	var out []*Token
	for rows.Next() {
		t := &Token{OrgID: orgID}
		if err := rows.Scan(&t.ID, &t.PrincipalID, &t.OwnerName, &t.Name, &t.Scopes,
			&t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, fmt.Errorf("api: scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Authenticate resolves a bearer token to a caller.
//
// The principal's status is checked here on every call, not cached. Somebody
// who leaves and is suspended has to lose their tokens on the next request
// rather than on the next restart, and that is the first thing an auditor asks
// about.
func (s *Service) Authenticate(ctx context.Context, header string) (*Caller, error) {
	secret := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if secret == "" {
		return nil, ErrNoToken
	}
	sum := sha256.Sum256([]byte(secret))

	c := &Caller{}
	var status string
	err := s.db.QueryRow(ctx, `
		SELECT t.id::text, t.org_id::text, t.principal_id::text,
		       COALESCE(p.display_name,''), t.scopes, p.status
		FROM api_tokens t
		JOIN principals p ON p.id = t.principal_id
		WHERE t.hash = $1 AND t.revoked_at IS NULL
		  AND (t.expires_at IS NULL OR t.expires_at > now())`, sum[:]).
		Scan(&c.TokenID, &c.OrgID, &c.PrincipalID, &c.Name, &c.Scopes, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNoToken
	case err != nil:
		return nil, fmt.Errorf("api: authenticate: %w", err)
	}
	if status != "active" {
		return nil, ErrNoToken
	}

	// The same call the session middleware makes. Not a copy of it: the same
	// one, so a change to how subjects are computed cannot apply to the
	// interface and not to the API.
	subjects, err := s.subjects.SubjectsFor(ctx, c.OrgID, c.PrincipalID)
	if err != nil {
		return nil, fmt.Errorf("api: subjects: %w", err)
	}
	c.Subjects = subjects

	_, _ = s.db.Exec(ctx, `UPDATE api_tokens SET last_used_at=now() WHERE id=$1`, c.TokenID)
	return c, nil
}

// -----------------------------------------------------------------------------
// The endpoints
// -----------------------------------------------------------------------------

// WithDocs makes the endpoints read and write documents through the store the
// pages use. On the EmberOps back end that is the one that sends every write on
// to EmberOps; without it an API write changes Hearth's own row only and the
// next read shows EmberOps's older record over it.
func (s *Service) WithDocs(d Spine) *Service {
	s.docs = d
	return s
}

// Routes returns the API handler, already authenticated.
func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/documents", s.list)
	mux.HandleFunc("POST /v1/documents", s.create)
	mux.HandleFunc("GET /v1/documents/{id}", s.get)
	mux.HandleFunc("PATCH /v1/documents/{id}", s.update)
	mux.HandleFunc("DELETE /v1/documents/{id}", s.remove)
	mux.HandleFunc("GET /v1/export", s.export)
	mux.HandleFunc("GET /v1/whoami", s.whoami)
	s.crmRoutes(mux)
	s.timeRoutes(mux)
	s.timeOffRoutes(mux)
	s.spaceRoutes(mux)
	return s.authenticated(mux)
}

// authenticated turns a bearer token into a caller and puts it on the context.
type callerKey struct{}

func (s *Service) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil && !errors.Is(err, ErrNoToken) {
			// The store could not be read, which says nothing about the token.
			// A caller told 401 treats a good token as dead; 503 says try again.
			w.Header().Set("Retry-After", "2")
			writeErr(w, http.StatusServiceUnavailable, "could not check that token, try again")
			return
		}
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hearth"`)
			writeErr(w, http.StatusUnauthorized, "no valid token")
			return
		}
		// A write method needs a write token. Checked once here rather than in
		// every handler, because the one handler that forgets is the whole
		// hole.
		switch r.Method {
		case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
			if !c.Can(ScopeWrite) {
				writeErr(w, http.StatusForbidden, "this token is read only")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

func from(r *http.Request) *Caller {
	c, _ := r.Context().Value(callerKey{}).(*Caller)
	return c
}

func (s *Service) whoami(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"principal_id": c.PrincipalID, "name": c.Name,
		"org_id": c.OrgID, "scopes": c.Scopes,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	list, err := s.docs.List(r.Context(), c.OrgID, c.Subjects, docs.ListParams{
		Kind: r.URL.Query().Get("kind"), Limit: limit,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		out = append(out, render(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": out})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	d, err := s.docs.Get(r.Context(), c.OrgID, c.Subjects, r.PathValue("id"))
	if err != nil {
		// Not-found whether it is missing or not granted, exactly as the
		// interface answers. An API that distinguishes them is an oracle for
		// which ids exist.
		writeErr(w, http.StatusNotFound, "no such document")
		return
	}
	writeJSON(w, http.StatusOK, render(d))
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var body struct {
		Kind     string  `json:"kind"`
		Title    string  `json:"title"`
		Body     string  `json:"body"`
		Status   string  `json:"status"`
		Priority int16   `json:"priority"`
		SpaceID  *string `json:"space_id"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeErr(w, http.StatusBadRequest, "a document needs a title")
		return
	}
	p := docs.CreateParams{
		Kind: body.Kind, Title: body.Title, Body: body.Body, SpaceID: body.SpaceID,
	}
	if docs.HasTaskFields(body.Kind) {
		status := body.Status
		if status == "" {
			status = docs.StatusTodo
		}
		p.Task = &docs.TaskFields{Status: status, Priority: body.Priority}
	}
	d, err := s.docs.Create(r.Context(), c.OrgID, c.Subjects, p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "docs: "))
		return
	}
	writeJSON(w, http.StatusCreated, render(d))
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	var body struct {
		Title    *string `json:"title"`
		Body     *string `json:"body"`
		Status   *string `json:"status"`
		Priority *int16  `json:"priority"`
		Version  int64   `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")

	current, err := s.docs.Get(r.Context(), c.OrgID, c.Subjects, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such document")
		return
	}
	// The version is optional and defaults to what is there now. An API client
	// that wants the optimistic check sends one and gets a conflict; one that
	// does not is not forced to read before every write.
	expected := body.Version
	if expected == 0 {
		expected = current.Version
	}
	p := docs.UpdateParams{Title: body.Title, Body: body.Body, ExpectedVersion: expected}
	if body.Status != nil || body.Priority != nil {
		p.Task = &docs.TaskPatch{Status: body.Status, Priority: body.Priority}
	}

	ctx, closed := docs.WithClosedSink(r.Context())
	d, err := s.docs.Update(ctx, c.OrgID, c.Subjects, id, p)
	switch {
	case errors.Is(err, docs.ErrConflict):
		writeErr(w, http.StatusConflict, "it has changed since you read it")
		return
	case errors.Is(err, docs.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such document")
		return
	case err != nil:
		// A caller who can view but not edit gets a forbidden, exactly as the
		// interface gives them: they already know it exists.
		writeErr(w, http.StatusForbidden, "you cannot change this")
		return
	}
	out := render(d)
	if n := len(closed.IDs); n > 0 {
		// A status change on a task moved its open subtasks too. The first
		// version only closed, and closed_subtasks keeps that meaning (done or
		// cancelled); cascaded_subtasks and cascaded_to say it for any status.
		out["cascaded_subtasks"] = closed.IDs
		out["cascaded_to"] = closed.Status
		if docs.IsClosedStatus(closed.Status) {
			out["closed_subtasks"] = closed.IDs
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	if err := s.docs.Delete(r.Context(), c.OrgID, c.Subjects, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusForbidden, "you cannot remove this")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------

func render(d *docs.Document) map[string]any {
	out := map[string]any{
		"id": d.ID, "kind": d.Kind, "title": d.Title, "body": d.Body,
		"version": d.Version, "created_at": d.CreatedAt, "updated_at": d.UpdatedAt,
	}
	if d.SpaceID != nil {
		out["space_id"] = *d.SpaceID
	}
	if d.Task != nil {
		task := map[string]any{"status": d.Task.Status, "priority": d.Task.Priority}
		if d.Task.AssigneeID != nil {
			task["assignee_id"] = *d.Task.AssigneeID
		}
		if d.Task.DueAt != nil {
			task["due_at"] = d.Task.DueAt
		}
		out["task"] = task
	}
	return out
}

// maxBody bounds a request. A document body is measured in kilobytes.
const maxBody = 1 << 20

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	// Unknown fields are refused rather than ignored. A client that misspells
	// "title" and gets a 201 back has created something empty and been told it
	// worked, and they find out a week later.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("that body does not parse: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
