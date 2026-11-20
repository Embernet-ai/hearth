package groupsync

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Scope is the one Admin SDK scope the sync needs: read and change group
// membership, nothing about people, mail or calendars.
const Scope = "https://www.googleapis.com/auth/admin.directory.group.member"

// Directory is the part of Google Workspace the sync talks to.
type Directory interface {
	Members(ctx context.Context, group string) ([]Member, error)
	Add(ctx context.Context, group, email string) error
	Remove(ctx context.Context, group, email string) error
}

// Google is the Admin SDK Directory API with domain-wide delegation: a service
// account key, acting as an admin, asking for Scope.
type Google struct {
	HTTP     *http.Client
	APIBase  string // default https://admin.googleapis.com/admin/directory/v1
	TokenURL string // default https://oauth2.googleapis.com/token
	Account  string // service account email
	Subject  string // the admin it acts as
	Key      *rsa.PrivateKey
	// Sign, when set, signs the delegation assertion instead of Key. It is how
	// a keyless deployment does it: Google's IAM signs on behalf of the account.
	Sign func(ctx context.Context, claims []byte) (string, error)
	// Static is a ready access token (an operator's one hour run). Never stored.
	Static string

	token   string
	expires time.Time
}

// NewGoogle reads a service account key file's JSON.
func NewGoogle(keyJSON []byte, subject string) (*Google, error) {
	var k struct {
		Email string `json:"client_email"`
		Key   string `json:"private_key"`
		Token string `json:"token_uri"`
	}
	if err := json.Unmarshal(keyJSON, &k); err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	b, _ := pem.Decode([]byte(k.Key))
	if b == nil {
		return nil, errors.New("service account key: no PEM private key")
	}
	pk, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	rk, ok := pk.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("service account key: not an RSA key")
	}
	if k.Email == "" || strings.TrimSpace(subject) == "" {
		return nil, errors.New("service account key and an admin subject are both required")
	}
	return &Google{Account: k.Email, Subject: subject, Key: rk, TokenURL: k.Token}, nil
}

func (g *Google) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *Google) api() string {
	if g.APIBase != "" {
		return strings.TrimRight(g.APIBase, "/")
	}
	return "https://admin.googleapis.com/admin/directory/v1"
}

func (g *Google) tokenURL() string {
	if g.TokenURL != "" {
		return g.TokenURL
	}
	return "https://oauth2.googleapis.com/token"
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (g *Google) accessToken(ctx context.Context) (string, error) {
	if g.Static != "" {
		return g.Static, nil
	}
	if g.token != "" && time.Now().Before(g.expires.Add(-time.Minute)) {
		return g.token, nil
	}
	now := time.Now()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	cl, _ := json.Marshal(map[string]any{
		"iss": g.Account, "sub": g.Subject, "scope": Scope, "aud": g.tokenURL(),
		"iat": now.Unix(), "exp": now.Add(50 * time.Minute).Unix(),
	})
	var assertion string
	if g.Sign != nil {
		a, err := g.Sign(ctx, cl)
		if err != nil {
			return "", err
		}
		assertion = a
	} else {
		in := b64(hdr) + "." + b64(cl)
		h := sha256.Sum256([]byte(in))
		sig, err := rsa.SignPKCS1v15(rand.Reader, g.Key, crypto.SHA256, h[:])
		if err != nil {
			return "", err
		}
		assertion = in + "." + b64(sig)
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL(), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		// Name the error code, never the body: it can echo the assertion.
		return "", fmt.Errorf("google token: %d %s (is %s delegated to this service account?)", resp.StatusCode, tr.Error, Scope)
	}
	g.token, g.expires = tr.AccessToken, now.Add(time.Duration(tr.ExpiresIn)*time.Second)
	return g.token, nil
}

func (g *Google) do(ctx context.Context, method, u string, body any) ([]byte, int, error) {
	tok, err := g.accessToken(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequestWithContext(ctx, method, u, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return b, resp.StatusCode, nil
}

// Members lists every member of a group, following pages.
func (g *Google) Members(ctx context.Context, group string) ([]Member, error) {
	var out []Member
	page := ""
	for {
		u := g.api() + "/groups/" + url.PathEscape(group) + "/members?maxResults=200"
		if page != "" {
			u += "&pageToken=" + url.QueryEscape(page)
		}
		b, code, err := g.do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if code != 200 {
			return nil, fmt.Errorf("list %s: %d", group, code)
		}
		var r struct {
			Members []struct {
				Email string `json:"email"`
				Role  string `json:"role"`
				Type  string `json:"type"`
			} `json:"members"`
			Next string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		for _, m := range r.Members {
			if m.Type == "USER" {
				out = append(out, Member{Email: m.Email, Role: m.Role})
			}
		}
		if r.Next == "" {
			return out, nil
		}
		page = r.Next
	}
}

// Add puts a person in a group as a plain member. Already a member is success.
func (g *Google) Add(ctx context.Context, group, email string) error {
	_, code, err := g.do(ctx, http.MethodPost, g.api()+"/groups/"+url.PathEscape(group)+"/members",
		map[string]string{"email": email, "role": "MEMBER"})
	if err != nil {
		return err
	}
	if code != 200 && code != 409 {
		return fmt.Errorf("add %s to %s: %d", email, group, code)
	}
	return nil
}

// Remove takes a person out of a group. Already gone is success.
func (g *Google) Remove(ctx context.Context, group, email string) error {
	_, code, err := g.do(ctx, http.MethodDelete,
		g.api()+"/groups/"+url.PathEscape(group)+"/members/"+url.PathEscape(email), nil)
	if err != nil {
		return err
	}
	if code != 204 && code != 200 && code != 404 {
		return fmt.Errorf("remove %s from %s: %d", email, group, code)
	}
	return nil
}
