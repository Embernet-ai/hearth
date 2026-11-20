package emberops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenSource yields a bearer token for EmberOPS.
//
// Two kinds exist. A person's source returns the Vord access token from their
// session, refreshed before it expires, so EmberOPS applies that person's own
// permissions. The service source is a client-credentials token for work with
// no person behind it.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Invalidator is implemented by sources that can be told a token was refused,
// so the next call fetches a fresh one instead of replaying it.
type Invalidator interface {
	Invalidate()
}

type userKey struct{}

// WithUser attaches a person's token source to a context. Calls made with that
// context act as that person.
func WithUser(ctx context.Context, ts TokenSource) context.Context {
	return context.WithValue(ctx, userKey{}, ts)
}

// UserFrom returns the person's token source on ctx, or nil for a background
// call.
func UserFrom(ctx context.Context) TokenSource {
	ts, _ := ctx.Value(userKey{}).(TokenSource)
	return ts
}

// ClientCredentials is a Vord service client. Its token is cached until shortly
// before it expires, and concurrent callers share one fetch.
type ClientCredentials struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	// Scope is the client scope that adds the EmberOPS audience.
	Scope string
	HTTP  *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	now     func() time.Time
}

const tokenSkew = 30 * time.Second

func (c *ClientCredentials) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Token implements TokenSource.
func (c *ClientCredentials) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.clock().Add(tokenSkew).Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.ClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("emberops: service token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("emberops: service token: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", errors.New("emberops: service token response had no access_token")
	}
	c.token = tr.AccessToken
	c.expires = c.clock().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return c.token, nil
}

// Invalidate drops the cached token.
func (c *ClientCredentials) Invalidate() {
	c.mu.Lock()
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
}

// StaticToken is a fixed token, for tests.
type StaticToken string

// Token implements TokenSource.
func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

type watchKey struct{}

// AuthWatch records why a call made on behalf of a person could not even get a
// token. The web layer reads it after a handler has failed to tell "the Vord
// session ended" (send the person to /login) from any other failure, because a
// handler turns the error into a status code and the cause is gone by then.
type AuthWatch struct {
	mu  sync.Mutex
	err error
}

// WithAuthWatch attaches a fresh watch to ctx.
func WithAuthWatch(ctx context.Context) (context.Context, *AuthWatch) {
	w := &AuthWatch{}
	return context.WithValue(ctx, watchKey{}, w), w
}

// Err is the first token failure seen on this request, or nil.
func (w *AuthWatch) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func noteTokenError(ctx context.Context, err error) {
	if w, _ := ctx.Value(watchKey{}).(*AuthWatch); w != nil {
		w.mu.Lock()
		if w.err == nil {
			w.err = err
		}
		w.mu.Unlock()
	}
}
