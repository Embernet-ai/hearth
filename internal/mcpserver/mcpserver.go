// Package mcpserver serves Hearth over the Model Context Protocol, on the
// official Go SDK, the way Thoth does (Fireball-Red-Team/thoth internal/mcp).
//
// Two kinds of caller come through the same door:
//
//   - A person, with a Vord access token: "connect Claude to Hearth" and sign
//     in. They act as themselves, with exactly the rights they have on the
//     website, and their writes land directly.
//   - An agent, with a Hearth agent token (hearth_agent_...), exactly as
//     before: its own principal and grants, and writes that may be
//     suggestion-only.
//
// Nothing here decides what a caller may do. The tools are internal/agents'
// (MCP.Tools / MCP.CallTool), every one of which takes the caller's subject
// set, so a person or an agent gets the answers the rest of the product would
// give them. What is here is the resource-server work the identity provider
// will not do for us -- the RFC 9728 metadata document, the WWW-Authenticate
// pointer to it, and CORS a browser-based client can read -- plus admission
// (a valid token, the required realm role, a linked account) and a log line
// per tool call.
//
// Identity is resolved before any MCP method runs, including tools/list: the
// tool list is built from the caller's rights, so answering it for somebody
// unidentified would be a map of the building handed out at the door.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/platform/buildinfo"
	"github.com/Embernet-ai/hearth/internal/platform/httpx"
	"github.com/Embernet-ai/hearth/internal/platform/tokens"
)

// Path is where the protocol endpoint lives.
const Path = "/mcp"

// metadataPath is where RFC 9728 says a resource's metadata lives: the
// well-known prefix followed by the resource's own path.
const metadataPath = "/.well-known/oauth-protected-resource" + Path

// maxBody bounds one request.
const maxBody = 1 << 20

// agentTokenPrefix marks Hearth's own agent tokens (agents.Mint). Anything
// else in a bearer header is treated as a Vord access token.
const agentTokenPrefix = "hearth_agent_"

// People turns a Vord identity into a Hearth person (auth.ResolveFederated).
type People interface {
	ResolveFederated(ctx context.Context, issuer, subject string) (principalID, orgID, name string, err error)
}

// Subjects is the permission set of a principal (authz.SubjectsFor).
type Subjects interface {
	SubjectsFor(ctx context.Context, orgID, principalID string) (core.Subjects, error)
}

// Options are the server's dependencies.
type Options struct {
	Log *slog.Logger

	// Verifier checks Vord access tokens for this resource's audience. Nil
	// turns person sign-in off; agent tokens still work.
	Verifier *tokens.Verifier

	// PublicURL is the externally reachable base of this deployment. Every
	// discovery document and WWW-Authenticate header is built from it, never
	// from the Host header: whoever controls Host would otherwise control where
	// clients are sent to authenticate.
	PublicURL *url.URL

	// Issuer is the authorization server clients are pointed at (Vord), and
	// the issuer a token's subject is looked up under.
	Issuer string

	// RequiredRole gates admission on a Vord realm role (fireball-staff). Not
	// an authorisation input: what a caller may do comes from their grants.
	RequiredRole string

	Agents   *agents.Service
	Tools    *agents.MCP
	People   People
	Subjects Subjects
}

// Server serves MCP over streamable HTTP.
type Server struct{ o Options }

func New(o Options) *Server { return &Server{o: o} }

func (s *Server) resourceURL() string {
	return strings.TrimSuffix(s.o.PublicURL.String(), "/") + Path
}

func (s *Server) metadataURL() string {
	return strings.TrimSuffix(s.o.PublicURL.String(), "/") + metadataPath
}

// Handlers are every path this server owns, for the root mux: the protocol
// endpoint (all methods -- a client probes with GET and ends with DELETE) and
// the metadata document in the three shapes clients actually request.
func (s *Server) Handlers() map[string]http.Handler {
	metadata := s.cors(http.HandlerFunc(s.metadata))
	protocol := s.cors(s.authenticate(s.transport()))
	return map[string]http.Handler{
		"/.well-known/oauth-protected-resource":        metadata,
		metadataPath:                                   metadata,
		Path + "/.well-known/oauth-protected-resource": metadata,
		Path:       protocol,
		Path + "/": protocol,
	}
}

// transport is stateless with JSON responses: Hearth runs behind a load
// balancer that does not pin clients to a pod, and nothing here streams.
func (s *Server) transport() http.Handler {
	return sdk.NewStreamableHTTPHandler(s.serverFor, &sdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: s.o.Log,
	})
}

// serverFor builds the MCP server this one caller gets: a fresh one per
// request, holding only the tools the caller's rights permit.
func (s *Server) serverFor(r *http.Request) *sdk.Server {
	c, ok := callerFrom(r.Context())
	if !ok {
		s.o.Log.ErrorContext(r.Context(), "mcp request reached the transport unauthenticated")
		return nil
	}
	srv := sdk.NewServer(&sdk.Implementation{
		Name: "hearth", Title: "Hearth", Version: buildinfo.Get().Revision,
	}, &sdk.ServerOptions{
		Logger: s.o.Log,
		Instructions: "Hearth is where Fireball's work lives: tasks, work orders, " +
			"documents and assets. Every tool answers only with what you are " +
			"allowed to see, so an empty result means you cannot see it rather " +
			"than that it does not exist. Use the ids one tool returns with the " +
			"others; do not construct them.",
	})
	requestID := httpx.RequestID(r.Context())
	clientID, _ := r.Context().Value(clientKey{}).(string)
	for _, t := range s.o.Tools.Tools(c) {
		name := t.Name
		srv.AddTool(&sdk.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema},
			func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				start := time.Now()
				args := map[string]any{}
				if req.Params != nil && len(req.Params.Arguments) > 0 {
					if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
						return toolResult("those arguments are not a JSON object", true), nil
					}
				}
				text, refused := s.o.Tools.CallTool(ctx, c, name, args)
				// One line per call: who, through which client, which tool, how
				// it went. The audit trail for anything an assistant did.
				s.o.Log.InfoContext(ctx, "mcp tool call",
					"principal", c.AgentID, "name", c.AgentName, "person", c.Person,
					"client", clientID, "tool", name, "refused", refused,
					"request_id", requestID, "duration_ms", time.Since(start).Milliseconds())
				return toolResult(text, refused), nil
			})
	}
	return srv
}

func toolResult(text string, isError bool) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}, IsError: isError}
}

// --- authentication ----------------------------------------------------------

type callerKey struct{}
type clientKey struct{}

func callerFrom(ctx context.Context) (*agents.Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(*agents.Caller)
	return c, ok && c != nil
}

// authenticate turns the bearer token into a caller, or refuses -- before
// any MCP method runs.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A protocol message is a few kilobytes; an unbounded read is how one
		// caller takes the process down.
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		raw, err := tokens.BearerToken(r.Header.Get("Authorization"))
		if err != nil {
			// Not a failure: the first step of the handshake. A client connects
			// with no token to be told where to get one.
			s.challenge(w, "unauthorized",
				"present a bearer token from the authorization server named in the resource metadata")
			return
		}

		var c *agents.Caller
		client := ""
		if strings.HasPrefix(raw, agentTokenPrefix) {
			c, err = s.o.Agents.Authenticate(r.Context(), raw)
			if err != nil && !errors.Is(err, agents.ErrNoToken) {
				// The store failed, which says nothing about the token: a client
				// told "invalid_token" drops a good one. 503 says try again.
				s.o.Log.ErrorContext(r.Context(), "mcp agent token could not be checked", "error", err)
				w.Header().Set("Retry-After", "2")
				writeError(w, http.StatusServiceUnavailable, "try again", "the token could not be checked just now")
				return
			}
			if err != nil {
				s.o.Log.WarnContext(r.Context(), "mcp agent token refused", "error", err)
				s.challenge(w, "invalid_token", "that token was not accepted")
				return
			}
			client = "agent-token"
		} else {
			if s.o.Verifier == nil {
				s.challenge(w, "invalid_token", "sign-in with Vord is not configured here")
				return
			}
			claims, err := s.o.Verifier.Verify(r.Context(), raw)
			if errors.Is(err, tokens.ErrUnavailable) {
				s.o.Log.ErrorContext(r.Context(), "mcp token could not be checked", "error", err)
				w.Header().Set("Retry-After", "2")
				writeError(w, http.StatusServiceUnavailable, "try again", "the token could not be checked just now")
				return
			}
			if err != nil {
				s.o.Log.WarnContext(r.Context(), "mcp token refused", "error", err)
				s.challenge(w, "invalid_token", "that token was not accepted")
				return
			}
			// Before the subject is looked up, so a caller who is not admitted
			// learns nothing about which subjects have accounts.
			if !claims.HasRealmRole(s.o.RequiredRole) {
				s.o.Log.WarnContext(r.Context(), "mcp caller lacks the required realm role",
					"subject", claims.Subject, "client", claims.ClientID, "required_role", s.o.RequiredRole)
				writeError(w, http.StatusForbidden, "not permitted",
					fmt.Sprintf("this endpoint requires the %q realm role", s.o.RequiredRole))
				return
			}
			principal, org, name, err := s.o.People.ResolveFederated(r.Context(), s.o.Issuer, claims.Subject)
			if err != nil && !errors.Is(err, auth.ErrNotBound) {
				// The store failed; the person may well have an account.
				s.o.Log.ErrorContext(r.Context(), "mcp account lookup failed", "error", err)
				w.Header().Set("Retry-After", "2")
				writeError(w, http.StatusServiceUnavailable, "try again", "the account could not be checked just now")
				return
			}
			if err != nil {
				s.o.Log.WarnContext(r.Context(), "mcp subject has no Hearth account",
					"subject", claims.Subject, "client", claims.ClientID, "error", err)
				writeError(w, http.StatusForbidden, "not provisioned",
					"this Vord account has no Hearth account yet: sign in to Hearth in a browser once, then connect again")
				return
			}
			subjects, err := s.o.Subjects.SubjectsFor(r.Context(), org, principal)
			if err != nil {
				s.o.Log.ErrorContext(r.Context(), "mcp subjects", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error", "the request could not be completed")
				return
			}
			c = &agents.Caller{
				OrgID: org, AgentID: principal, AgentName: name, Person: true,
				Scopes:   []string{agents.ScopeRead, agents.ScopeSuggest, agents.ScopeWrite},
				Subjects: subjects,
			}
			client = claims.ClientID
		}

		ctx := context.WithValue(r.Context(), callerKey{}, c)
		ctx = context.WithValue(ctx, clientKey{}, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// challenge answers 401 with the header that bootstraps discovery: without
// resource_metadata a client has a refusal and no idea where to sign in.
//
// A request that carried no credentials at all gets no error code: RFC 6750
// section 3.1 says the server should not include error or error_description
// then, and error takes only the registered values (invalid_token and the
// rest), which "authentication required" is not. The scope parameter names what
// to ask for, so a client that reads it asks for the audience scope Vord needs
// to put hearth-mcp in the token (Anthropic's connector docs: Claude reads
// scope from this header before it falls back to scopes_supported).
func (s *Server) challenge(w http.ResponseWriter, code, detail string) {
	h := fmt.Sprintf(`Bearer realm="hearth", resource_metadata=%q, scope=%q`,
		s.metadataURL(), strings.Join(s.scopes(), " "))
	if code == "invalid_token" {
		h += fmt.Sprintf(`, error=%q, error_description=%q`, code, detail)
	}
	w.Header().Set("WWW-Authenticate", h)
	writeError(w, http.StatusUnauthorized, code, detail)
}

// --- discovery ---------------------------------------------------------------

// protectedResource is the RFC 9728 metadata document.
type protectedResource struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed",
			"the resource metadata document is read with GET")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, protectedResource{
		Resource:             s.resourceURL(),
		AuthorizationServers: []string{s.o.Issuer},
		// The audience is listed as a scope because that is how a client asks
		// for it: Vord attaches the audience through a client scope of that
		// name, and a token without it is refused here.
		ScopesSupported:        s.scopes(),
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Hearth",
	})
}

func (s *Server) scopes() []string {
	out := []string{"openid", "profile", "email"}
	if s.o.Verifier != nil && s.o.Verifier.Audience() != "" {
		out = append(out, s.o.Verifier.Audience())
	}
	return out
}

// --- CORS --------------------------------------------------------------------

// cors sits outside authentication: a preflight never carries a token, and a
// 401 has to be readable cross-origin or a browser-based client never sees
// the WWW-Authenticate it needs. Wildcard origin and never credentials: this
// surface authenticates with a token a client attaches on purpose, so there
// is no ambient credential for a hostile page to borrow.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers",
			"Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID")
		h.Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id, Mcp-Protocol-Version")
		h.Set("Access-Control-Max-Age", "600")
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- errors ------------------------------------------------------------------

// writeError answers a refusal before any MCP method ran: plain HTTP with a
// JSON body. A tool that runs and fails is different -- a normal protocol
// answer with isError set, so the model can read it and correct itself.
func writeError(w http.ResponseWriter, code int, title, detail string) {
	writeJSON(w, code, map[string]string{"error": title, "error_description": detail})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
