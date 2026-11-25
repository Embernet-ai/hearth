// Package github is Hearth's read-only view of GitHub for one person: who they are, which repositories they chose, and what
// happened in them lately (commits, pull requests, issues, releases and CI runs). It only ever reads. The token is the
// person's own, sealed in the database, and the only calls are GET requests to api.github.com.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrBadToken means GitHub refused the token (revoked, expired or never valid).
var ErrBadToken = errors.New("github: GitHub did not accept that token")

// Client reads the GitHub REST API (https://docs.github.com/en/rest).
type Client struct {
	Base string
	HC   *http.Client
}

// NewClient reads api.github.com.
func NewClient() *Client {
	return &Client{Base: "https://api.github.com", HC: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) get(ctx context.Context, token, path string, q url.Values, out any) error {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return fmt.Errorf("github: %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrBadToken
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return errors.New("github: GitHub's hourly request limit for this token is used up; try again later")
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("github: %s was not found, or this token cannot see it", path)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("github: %s: %d", path, resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

// Who is the login a token belongs to.
func (c *Client) Who(ctx context.Context, token string) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, token, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", ErrBadToken
	}
	return u.Login, nil
}

// Repo is a repository a person could choose.
type Repo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	PushedAt string `json:"pushed_at"`
}

// Repos lists the repositories the token can see, most recently pushed first (the first hundred).
func (c *Client) Repos(ctx context.Context, token string) ([]Repo, error) {
	var out []Repo
	err := c.get(ctx, token, "/user/repos", url.Values{"per_page": {"100"}, "sort": {"pushed"}, "affiliation": {"owner,collaborator,organization_member"}}, &out)
	return out, err
}

// Metrics is what happened in one repository since a time.
type Metrics struct {
	Repo          string
	Commits       int
	PRsOpened     int
	PRsMerged     int
	IssuesOpened  int
	IssuesClosed  int
	Releases      int
	LatestRelease string
	Runs          int
	RunsPassed    int
	RunsFailed    int
	// Err is why a repository could not be read, so one bad name never hides the others.
	Err string
}

type dated struct {
	CreatedAt   time.Time  `json:"created_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	MergedAt    *time.Time `json:"merged_at"`
	PublishedAt *time.Time `json:"published_at"`
	PullRequest *struct{}  `json:"pull_request"`
	TagName     string     `json:"tag_name"`
	Conclusion  string     `json:"conclusion"`
	Status      string     `json:"status"`
}

// Measure reads one repository ("owner/name") for the period since.
func (c *Client) Measure(ctx context.Context, token, repo string, since time.Time) Metrics {
	m := Metrics{Repo: repo}
	if parts := strings.Split(repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		m.Err = "that is not an owner/name"
		return m
	}
	ts := since.UTC().Format(time.RFC3339)
	var commits []struct{}
	if err := c.get(ctx, token, "/repos/"+repo+"/commits", url.Values{"since": {ts}, "per_page": {"100"}}, &commits); err != nil {
		m.Err = err.Error()
		return m
	}
	m.Commits = len(commits)
	var prs []dated
	if c.get(ctx, token, "/repos/"+repo+"/pulls", url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"100"}}, &prs) == nil {
		for _, p := range prs {
			if p.CreatedAt.After(since) {
				m.PRsOpened++
			}
			if p.MergedAt != nil && p.MergedAt.After(since) {
				m.PRsMerged++
			}
		}
	}
	var issues []dated
	if c.get(ctx, token, "/repos/"+repo+"/issues", url.Values{"state": {"all"}, "since": {ts}, "per_page": {"100"}}, &issues) == nil {
		for _, i := range issues {
			if i.PullRequest != nil {
				continue
			}
			if i.CreatedAt.After(since) {
				m.IssuesOpened++
			}
			if i.ClosedAt != nil && i.ClosedAt.After(since) {
				m.IssuesClosed++
			}
		}
	}
	var rels []dated
	if c.get(ctx, token, "/repos/"+repo+"/releases", url.Values{"per_page": {"30"}}, &rels) == nil {
		for _, r := range rels {
			if m.LatestRelease == "" {
				m.LatestRelease = r.TagName
			}
			if r.PublishedAt != nil && r.PublishedAt.After(since) {
				m.Releases++
			}
		}
	}
	var runs struct {
		Runs []dated `json:"workflow_runs"`
	}
	if c.get(ctx, token, "/repos/"+repo+"/actions/runs", url.Values{"created": {">=" + since.UTC().Format("2006-01-02")}, "per_page": {"100"}, "status": {"completed"}}, &runs) == nil {
		for _, r := range runs.Runs {
			m.Runs++
			switch r.Conclusion {
			case "success":
				m.RunsPassed++
			case "failure", "timed_out", "startup_failure":
				m.RunsFailed++
			}
		}
	}
	return m
}
