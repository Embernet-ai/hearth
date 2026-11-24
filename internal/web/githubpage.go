package web

// The GitHub page: a person connects with a token they made themselves, chooses up to twelve repositories, and sees what
// happened in them lately. It only reads. Nothing here writes to GitHub, and nothing is shared: everybody's connection and
// numbers are their own, taken from the session and never from the request.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/github"
)

// GitHubView is the whole GitHub page.
type GitHubView struct {
	Configured bool
	Notice     string
	Connected  bool
	Login      string
	LastError  string
	Chosen     []string
	// Choices is what the token can see, with the chosen ones marked, for the picker.
	Choices []GitHubChoice
	Days    int
	Total   *github.Metrics
	Repos   []github.Metrics
}

// GitHubChoice is one repository in the picker.
type GitHubChoice struct {
	Name    string
	Private bool
	Chosen  bool
}

func (s *Server) getGitHub(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	v := &GitHubView{Configured: s.github != nil, Notice: r.URL.Query().Get("notice"), Days: 30}
	if v.Configured {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		conn, err := s.github.Get(ctx, id.PrincipalID)
		switch {
		case errors.Is(err, github.ErrNotConnected):
		case err != nil:
			v.Notice = areaErr(err)
		default:
			v.Connected, v.Login, v.LastError, v.Chosen = true, conn.Login, conn.LastError, conn.Repos
			chosen := map[string]bool{}
			for _, c := range conn.Repos {
				chosen[strings.ToLower(c)] = true
			}
			if all, err := s.github.Repos(ctx, id.PrincipalID); err == nil {
				for _, repo := range all {
					v.Choices = append(v.Choices, GitHubChoice{Name: repo.FullName, Private: repo.Private, Chosen: chosen[strings.ToLower(repo.FullName)]})
				}
			} else if v.Notice == "" {
				v.Notice = areaErr(err)
			}
			if rep, err := s.github.Measure(ctx, id.PrincipalID, v.Days); err == nil {
				t := rep.Total()
				v.Total, v.Repos = &t, rep.Repos
			}
		}
	}
	s.render(w, r, "github", page{Title: "GitHub", GitHubArea: v})
}

func (s *Server) postGitHubConnect(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		s.fail(w, r, http.StatusNotFound, "GitHub is not available on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	id := auth.From(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	conn, err := s.github.Connect(ctx, id.OrgID, id.PrincipalID, r.FormValue("token"))
	if err != nil {
		areaBack(w, r, "/github", nil, "", err)
		return
	}
	areaBack(w, r, "/github", nil, fmt.Sprintf("Connected as %s. Choose the repositories you want numbers for.", conn.Login), nil)
}

func (s *Server) postGitHubRepos(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		s.fail(w, r, http.StatusNotFound, "GitHub is not available on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	id := auth.From(r.Context())
	repos := append([]string{}, r.Form["repo"]...)
	// A repository typed in, for one the picker did not list (the first hundred only).
	for _, extra := range strings.FieldsFunc(r.FormValue("more"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		repos = append(repos, extra)
	}
	if err := s.github.Choose(r.Context(), id.PrincipalID, repos); err != nil {
		areaBack(w, r, "/github", nil, "", err)
		return
	}
	areaBack(w, r, "/github", url.Values{}, "Saved.", nil)
}

func (s *Server) postGitHubDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		s.fail(w, r, http.StatusNotFound, "GitHub is not available on this installation")
		return
	}
	id := auth.From(r.Context())
	if err := s.github.Disconnect(r.Context(), id.PrincipalID); err != nil {
		areaBack(w, r, "/github", nil, "", err)
		return
	}
	areaBack(w, r, "/github", nil, "Disconnected. Your token is gone from Hearth.", nil)
}

// githubSection is the person's own GitHub numbers on the dashboard, nil when they have not connected or chosen a
// repository. It is read per person (never shared the way the team parts are) and cached by the service for ten minutes.
func (s *Server) githubSection(ctx context.Context, id *auth.Identity) *teamSection {
	if s.github == nil {
		return nil
	}
	conn, err := s.github.Get(ctx, id.PrincipalID)
	if err != nil || len(conn.Repos) == 0 {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	sec := &teamSection{Key: "github", Title: "GitHub, last 30 days", Href: "/github"}
	rep, err := s.github.Measure(rctx, id.PrincipalID, 30)
	if err != nil {
		sec.Notice = "GitHub did not answer: " + areaErr(err)
		return sec
	}
	t := rep.Total()
	ci := "no runs"
	if t.Runs > 0 {
		ci = pct(int64(t.RunsPassed), int64(t.Runs)) + " passed"
	}
	sec.Tiles = []statTile{
		{Label: "Commits", Value: num(int64(t.Commits)), Period: fmt.Sprintf("in %d repositories", len(rep.Repos)), Href: "/github"},
		{Label: "Pull requests", Value: num(int64(t.PRsMerged)), Detail: num(int64(t.PRsOpened)) + " opened", Period: "merged", Href: "/github"},
		{Label: "Issues", Value: num(int64(t.IssuesClosed)), Detail: num(int64(t.IssuesOpened)) + " opened", Period: "closed", Href: "/github"},
		{Label: "CI runs", Value: num(int64(t.Runs)), Detail: ci, Period: fmt.Sprintf("%d failed", t.RunsFailed), Href: "/github", Tone: toneIf(t.RunsFailed > 0)},
	}
	for _, m := range rep.Repos {
		if len(sec.Rows) >= 5 {
			break
		}
		d := fmt.Sprintf("%d commits, %d PRs merged", m.Commits, m.PRsMerged)
		if m.LatestRelease != "" {
			d += ", " + m.LatestRelease
		}
		if m.Err != "" {
			d = "could not be read"
		}
		sec.Rows = append(sec.Rows, teamRow{Title: m.Repo, Detail: d, Href: "https://github.com/" + m.Repo})
	}
	return sec
}
