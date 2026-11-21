// Package auth owns sessions and who the caller is.
//
// Real identity is OIDC against Entra, which is not wired yet. What is here is
// the session machinery that identity will hang off: creating, reading, and
// revoking a server-side session, plus the middleware that turns a cookie into
// a set of permission subjects.
//
// There is deliberately NO way to create a session in a release build. The only
// constructor that does not require an already-authenticated identity lives
// behind the `dev` build tag, so a production binary literally cannot mint one
// until the OIDC path lands. That is stronger than a runtime flag: the code is
// not in the artifact.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/Embernet-ai/hearth/internal/platform/httpx"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops"
)

const (
	// CookieName is prefixed __Host- so the browser enforces the rules we want:
	// Secure only, no Domain attribute, and path /. A subdomain cannot set or
	// overwrite it, which removes a whole class of session fixation.
	CookieName = "__Host-hearth_session"

	// Two clocks, and they do different jobs.
	//
	// sessionIdle is how long a session survives WITHOUT activity. It slides
	// forward every time you use the app, so somebody working all day is not
	// thrown out mid-sentence at the twelve hour mark.
	//
	// sessionAbsolute is the hard ceiling from first sign-in, and it does NOT
	// slide. Without it a session that is used daily lives forever, which means
	// a stolen cookie also lives forever.
	sessionIdle     = 12 * time.Hour
	sessionAbsolute = 30 * 24 * time.Hour

	tokenBytes = 32

	// Refresh is throttled. Extending on literally every request turns a
	// read-mostly path into a write on the hottest table in the system.
	refreshAfter = 15 * time.Minute
)

var ErrNoSession = errors.New("no session")

// Identity is who the caller is, resolved once per request.
type Identity struct {
	PrincipalID string
	OrgID       string
	// DisplayName is what to call this person on a page.
	//
	// Read with the session rather than looked up per render, because the top
	// bar wants it on every single page and a second query for a string is a
	// second query on every single page.
	//
	// It was not here at all, so the top bar rendered the first eight
	// characters of a uuid at people and called it identification.
	DisplayName  string
	Subjects     core.Subjects
	Impersonated bool
	// MustChangePassword is true while the account is still on a password
	// somebody else chose. The web layer sends such a person to their account
	// page before anything else.
	MustChangePassword bool
	// Contractor marks an account that reaches only what it was given: a
	// folder, and the work in it. The web layer refuses it every page that is
	// not about that work.
	Contractor bool
	// ActingFor is the real person behind an impersonated session -- the one
	// a playground session belongs to, and the one "Back to Hearth" returns
	// to. Empty for an ordinary session.
	ActingFor string

	// token and renewedUntil let the middleware re-issue the cookie when the
	// session's expiry has slid forward. Unexported: nothing outside this
	// package has any business reading a session token.
	token        string
	renewedUntil *time.Time
	sessionID    []byte
}

// SubjectResolver computes a principal's permission subjects.
//
// Declared here, by the consumer, so auth does not import authz.
type SubjectResolver interface {
	SubjectsFor(ctx context.Context, orgID, principalID string) (core.Subjects, error)
}

// Service manages sessions.
type Service struct {
	db       *pgxpool.Pool
	subjects SubjectResolver
	secure   bool
	// owners caches which principal claimed each org. Keyed by org id, and
	// never invalidated because the answer is the earliest row and that does
	// not move. See OwnerID.
	owners sync.Map
	// vord keeps the person's Vord tokens for EmberOPS; nil unless that
	// back end is on. See vordtokens.go.
	vord *VordTokens
}

// New returns a session service. secure=false is only for local HTTP; it drops
// the Secure attribute, which a __Host- cookie requires, so the cookie name
// changes with it.
func New(db *pgxpool.Pool, subjects SubjectResolver, secure bool) *Service {
	return &Service{db: db, subjects: subjects, secure: secure}
}

func (s *Service) cookieName() string {
	if s.secure {
		return CookieName
	}
	// __Host- REQUIRES Secure, so a browser silently rejects the cookie over
	// plain HTTP. Using a different name locally is better than a login that
	// appears to work and never persists.
	return "hearth_session"
}

// Create issues a session for an already-authenticated principal.
//
// Unexported inputs on purpose: everything that calls this must have proven the
// identity first. See devlogin.go for the only exception, which is not compiled
// into release builds.
func (s *Service) Create(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID, principalID string) error {
	return s.create(ctx, w, r, orgID, principalID, "", nil)
}

// Switch replaces the caller's session with one for another principal, and
// records whose session it really is in impersonated_by: the playground's
// way in (actingFor = the real person) and out (actingFor = "", back to the
// real person's own). The old session is revoked first, so there is only
// ever one live session per browser.
//
// Same rule as Create: the caller must already have proven who the person
// is. The playground handlers only ever switch somebody to their own
// playground self, or back to the real principal recorded on the session.
func (s *Service) Switch(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID, principalID, actingFor string) error {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		if raw, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			sum := sha256.Sum256(raw)
			if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sum[:]); err != nil {
				return fmt.Errorf("auth: revoke session: %w", err)
			}
		}
	}
	return s.create(ctx, w, r, orgID, principalID, actingFor, nil)
}

func (s *Service) create(ctx context.Context, w http.ResponseWriter, r *http.Request, orgID, principalID, actingFor string, tok *oauth2.Token) error {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("auth: generate session token: %w", err)
	}
	// Store the hash. A stolen database backup must not be a pile of working
	// sessions.
	sum := sha256.Sum256(raw)
	expires := time.Now().Add(sessionIdle)

	ip := clientHost(ctx, r)
	ua := r.UserAgent()

	if tok != nil {
		// EmberOPS back end: the person's Vord tokens ride along, sealed.
		sealed, err := s.vord.seal(sum[:], tok.AccessToken, tok.RefreshToken, tok.Expiry)
		if err != nil {
			return fmt.Errorf("auth: seal session tokens: %w", err)
		}
		if _, err := s.db.Exec(ctx, `
			INSERT INTO sessions (id, principal_id, org_id, expires_at, user_agent, ip, impersonated_by, vord_tokens)
			VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8)`,
			sum[:], principalID, orgID, expires, ua, ip, actingFor, sealed); err != nil {
			return fmt.Errorf("auth: store session: %w", err)
		}
	} else if _, err := s.db.Exec(ctx, `
		INSERT INTO sessions (id, principal_id, org_id, expires_at, user_agent, ip, impersonated_by)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid)`,
		sum[:], principalID, orgID, expires, ua, ip, actingFor); err != nil {
		return fmt.Errorf("auth: store session: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(),
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
	return nil
}

// Destroy revokes the caller's session and clears the cookie.
func (s *Service) Destroy(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		if raw, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			sum := sha256.Sum256(raw)
			// One row. This is why sessions are not JWTs.
			_, _ = s.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sum[:])
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/",
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: -1,
	})
	return nil
}

// resolve reads the cookie and returns the caller's identity.
func (s *Service) resolve(ctx context.Context, r *http.Request) (*Identity, error) {
	c, err := r.Cookie(s.cookieName())
	if err != nil || c.Value == "" {
		return nil, ErrNoSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != tokenBytes {
		return nil, ErrNoSession
	}
	sum := sha256.Sum256(raw)

	var principalID, orgID, displayName string
	var lastSeen, createdAt time.Time
	var impersonatedBy *string
	var mustChange, contractor bool
	// Joined to principals for the display name. One query still, and the top
	// bar can say who somebody is rather than showing them eight characters of
	// their own uuid.
	err = s.db.QueryRow(ctx, `
		SELECT s.principal_id::text, s.org_id::text, s.last_seen_at, s.created_at,
		       s.impersonated_by::text,
		       COALESCE(NULLIF(p.display_name, ''), ''),
		       COALESCE(lc.must_change_password, false), p.contractor
		FROM sessions s
		JOIN principals p ON p.id = s.principal_id
		LEFT JOIN local_credentials lc ON lc.principal_id = s.principal_id
		WHERE s.id = $1 AND s.expires_at > now()`, sum[:]).
		Scan(&principalID, &orgID, &lastSeen, &createdAt, &impersonatedBy, &displayName, &mustChange, &contractor)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNoSession
	case err != nil:
		return nil, fmt.Errorf("auth: read session: %w", err)
	}

	// Slide the expiry forward on activity, capped by the absolute lifetime.
	// The cookie is re-issued by the middleware so the BROWSER's copy moves too:
	// updating only the database would leave the browser discarding a cookie the
	// server still considers valid, which logs people out while the session is
	// alive and is maddening to diagnose.
	var renewed *time.Time
	if time.Since(lastSeen) > refreshAfter {
		deadline := time.Now().Add(sessionIdle)
		if cap := createdAt.Add(sessionAbsolute); deadline.After(cap) {
			deadline = cap
		}
		if _, err := s.db.Exec(ctx,
			`UPDATE sessions SET last_seen_at = now(), expires_at = $1 WHERE id = $2`,
			deadline, sum[:]); err == nil {
			renewed = &deadline
		}
	}

	subjects, err := s.subjects.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("auth: resolve subjects: %w", err)
	}
	return &Identity{
		PrincipalID:  principalID,
		OrgID:        orgID,
		DisplayName:  displayName,
		Subjects:     subjects,
		Impersonated: impersonatedBy != nil,
		// An administrator viewing as somebody else is not that person and
		// cannot change their password, so the gate leaves it alone.
		MustChangePassword: mustChange && impersonatedBy == nil,
		// A super admin is never narrowed, whatever the account says.
		Contractor:   contractor && !subjects.Super(),
		ActingFor:    deref(impersonatedBy),
		token:        c.Value,
		renewedUntil: renewed,
		sessionID:    sum[:],
	}, nil
}

type ctxKey int

const identityKey ctxKey = iota

// sessionErrKey carries a store failure from Middleware to Require.
type sessionErrKey struct{}

// Middleware attaches the caller's identity to the request context when a valid
// session exists. It does NOT reject anonymous requests; that is Require's job,
// so that public pages and the login flow can share the same chain.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, rerr := s.resolve(r.Context(), r)
		if rerr != nil && !errors.Is(rerr, ErrNoSession) {
			// The store could not be read. That says nothing about the session,
			// so the request is NOT treated as anonymous: Require answers 503,
			// not a redirect to the sign-in page, and a database that blinks
			// does not send everyone to sign in. Public pages carry on.
			r = r.WithContext(context.WithValue(r.Context(), sessionErrKey{}, rerr))
		}
		if rerr == nil {
			// Re-issue the cookie whenever the expiry moved, so the browser's
			// copy tracks the server's. This is what makes a session survive an
			// ordinary working day, and a redeploy, without anyone signing in
			// again: sessions are rows in Postgres, so a new binary inherits
			// them, and the cookie no longer quietly ages out underneath.
			if id.renewedUntil != nil {
				http.SetCookie(w, &http.Cookie{
					Name:     s.cookieName(),
					Value:    id.token,
					Path:     "/",
					HttpOnly: true,
					Secure:   s.secure,
					SameSite: http.SameSiteLaxMode,
					Expires:  *id.renewedUntil,
				})
			}
			ctx := context.WithValue(r.Context(), identityKey, id)
			if s.vord != nil {
				// Calls to EmberOPS made while serving this request act as
				// this person, and a page asking twice asks once.
				ctx = emberops.WithRequestMemo(emberops.WithUser(ctx, s.vord.TokenSource(id.sessionID)))
				var watch *emberops.AuthWatch
				ctx, watch = emberops.WithAuthWatch(ctx)
				r = r.WithContext(ctx)
				next.ServeHTTP(&expiryWriter{ResponseWriter: w, s: s, r: r, watch: watch}, r)
				return
			}
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// Require rejects anonymous callers.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if From(r.Context()) == nil {
			if err, _ := r.Context().Value(sessionErrKey{}).(error); err != nil {
				// Could not read the session: try again, don't sign in again.
				w.Header().Set("Retry-After", "2")
				http.Error(w, "Hearth could not check your sign-in just now. Try again in a moment.", http.StatusServiceUnavailable)
				return
			}
			// HTMX needs a header to redirect; a browser needs a 303. Sending
			// a plain redirect to an HTMX request swaps the login page into a
			// fragment, which looks like the app has fallen apart.
			to := "/login"
			// Carry where they were going, so scanning an asset code on a
			// phone that signed out last week lands on the machine rather
			// than on a document list. GET only, and only a path on this
			// site: a next parameter that accepts anything is an open
			// redirect wearing a helpful face.
			if next := returnTo(r); next != "" {
				to += "?next=" + url.QueryEscape(next)
			}
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// returnTo is the path worth coming back to after signing in.
//
// A GET, on this site, and not the login page itself. Everything else returns
// empty and the caller lands on the front page, which is the safe answer.
func returnTo(r *http.Request) string {
	if r.Method != http.MethodGet {
		return ""
	}
	return SafeNext(r.URL.RequestURI())
}

// SafeNext filters a return path down to something that cannot leave the site.
//
// A leading single slash and no scheme, no host, and no protocol-relative
// "//host" prefix. Returns empty for anything else, so a caller can use the
// result directly.
func SafeNext(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return ""
	}
	// A backslash is a slash to some browsers, so "/\evil.example" is a
	// protocol-relative URL wearing a path. Carriage returns and newlines
	// would split the Location header.
	if strings.ContainsAny(v, "\\\r\n") {
		return ""
	}
	if strings.HasPrefix(v, "/login") || strings.HasPrefix(v, "/logout") ||
		strings.HasPrefix(v, "/auth/") {
		return ""
	}
	return v
}

// From returns the caller's identity, or nil if anonymous.
func From(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// PurgeExpired deletes sessions that have lapsed. Called on a timer.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	ct, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("auth: purge sessions: %w", err)
	}
	return ct.RowsAffected(), nil
}

// SecretEqual compares a presented secret against the configured one without
// leaking its length or contents through timing.
//
// Here rather than in the caller because there is exactly one right way to do
// it and every place that compares a secret should reach for the same one. The
// length check is deliberately not an early return on mismatch: ConstantTimeEq
// folds it in, so a wrong length costs the same as a wrong byte.
func SecretEqual(got, want string) bool {
	if want == "" {
		return false
	}
	g, w := []byte(got), []byte(want)
	same := subtle.ConstantTimeCompare(g, w)
	lengthOK := subtle.ConstantTimeEq(int32(len(g)), int32(len(w)))
	return same&lengthOK == 1
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// WithIdentity puts an identity on a context, as Middleware does after it has
// read a session. It exists so the layers above can be tested without a
// database behind the session; nothing in the server calls it.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// clientHost is the address a sign-in record keeps: the client the proxies saw
// when the deployment lists them and the connection is one of theirs
// (httpx.ClientIP), the socket address otherwise. Nil when neither is known.
func clientHost(ctx context.Context, r *http.Request) *string {
	if host := httpx.ClientIP(ctx); host != "" {
		return &host
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return &host
	}
	return nil
}
