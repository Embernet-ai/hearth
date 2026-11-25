package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sealer seals and opens a token with the key in the environment.
type Sealer interface {
	SealString(plain, context string) ([]byte, error)
	OpenString(sealed []byte, context string) (string, error)
}

// ErrNotConnected is a person with no GitHub connection.
var ErrNotConnected = errors.New("github: not connected")

// Connection is one person's link, without the token.
type Connection struct {
	Login       string
	Repos       []string
	ConnectedAt time.Time
	LastError   string
}

// Service keeps each person's connection and reads GitHub for them. It never writes to GitHub.
type Service struct {
	db     *pgxpool.Pool
	box    Sealer
	client *Client
	cache  *metricCache
}

// NewService builds the service. box may not be nil: a token is never stored in the clear.
func NewService(db *pgxpool.Pool, box Sealer, client *Client) *Service {
	if client == nil {
		client = NewClient()
	}
	return &Service{db: db, box: box, client: client, cache: newMetricCache(10 * time.Minute)}
}

func sealCtx(principal string) string { return "github:" + principal }

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Connect checks the token with GitHub, then keeps it sealed for the person.
func (s *Service) Connect(ctx context.Context, orgID, principalID, token string) (*Connection, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("github: paste the token first")
	}
	if s.box == nil {
		return nil, errors.New("github: this installation has no key to seal a token with")
	}
	login, err := s.client.Who(ctx, token)
	if err != nil {
		return nil, err
	}
	sealed, err := s.box.SealString(token, sealCtx(principalID))
	if err != nil {
		return nil, fmt.Errorf("github: seal: %w", err)
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO github_connections (principal_id, org_id, login, token) VALUES ($1,$2,$3,$4)
		ON CONFLICT (principal_id) DO UPDATE SET login = $3, token = $4, last_error = '', updated_at = now()`,
		principalID, orgID, login, sealed); err != nil {
		return nil, fmt.Errorf("github: keep connection: %w", err)
	}
	s.cache.forget(principalID)
	return s.Get(ctx, principalID)
}

// Get reads a person's connection, ErrNotConnected when they have none.
func (s *Service) Get(ctx context.Context, principalID string) (*Connection, error) {
	var c Connection
	err := s.db.QueryRow(ctx, `SELECT login, repos, connected_at, last_error FROM github_connections WHERE principal_id = $1`, principalID).
		Scan(&c.Login, &c.Repos, &c.ConnectedAt, &c.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("github: read connection: %w", err)
	}
	return &c, nil
}

// Disconnect forgets the token and the chosen repositories.
func (s *Service) Disconnect(ctx context.Context, principalID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM github_connections WHERE principal_id = $1`, principalID)
	s.cache.forget(principalID)
	return err
}

// Choose sets the repositories a person sees numbers for ("owner/name"), at most twelve.
func (s *Service) Choose(ctx context.Context, principalID string, repos []string) error {
	seen := map[string]bool{}
	var clean []string
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" || seen[strings.ToLower(r)] {
			continue
		}
		if !repoName.MatchString(r) {
			return fmt.Errorf("github: %q is not an owner/name", r)
		}
		seen[strings.ToLower(r)] = true
		clean = append(clean, r)
	}
	if len(clean) > 12 {
		return errors.New("github: choose twelve repositories or fewer")
	}
	tag, err := s.db.Exec(ctx, `UPDATE github_connections SET repos = $2, updated_at = now() WHERE principal_id = $1`, principalID, clean)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotConnected
	}
	s.cache.forget(principalID)
	return nil
}

func (s *Service) token(ctx context.Context, principalID string) (string, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT token FROM github_connections WHERE principal_id = $1`, principalID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", err
	}
	return s.box.OpenString(sealed, sealCtx(principalID))
}

// Repos lists what the person's token can see, for choosing.
func (s *Service) Repos(ctx context.Context, principalID string) ([]Repo, error) {
	t, err := s.token(ctx, principalID)
	if err != nil {
		return nil, err
	}
	rs, err := s.client.Repos(ctx, t)
	if errors.Is(err, ErrBadToken) {
		_, _ = s.db.Exec(ctx, `UPDATE github_connections SET last_error = $2 WHERE principal_id = $1`, principalID, "GitHub no longer accepts this token. Connect again with a new one.")
	}
	return rs, err
}

// Report is the numbers for a person's chosen repositories over the last days.
type Report struct {
	Days  int
	Repos []Metrics
}

// Measure reads (or serves from a ten minute cache) each chosen repository over the last days.
func (s *Service) Measure(ctx context.Context, principalID string, days int) (*Report, error) {
	conn, err := s.Get(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 30
	}
	if rep, ok := s.cache.get(principalID, days); ok {
		return rep, nil
	}
	rep := &Report{Days: days}
	if len(conn.Repos) > 0 {
		t, err := s.token(ctx, principalID)
		if err != nil {
			return nil, err
		}
		since := time.Now().AddDate(0, 0, -days)
		for _, r := range conn.Repos {
			m := s.client.Measure(ctx, t, r, since)
			if strings.Contains(m.Err, ErrBadToken.Error()) {
				_, _ = s.db.Exec(ctx, `UPDATE github_connections SET last_error = $2 WHERE principal_id = $1`, principalID, "GitHub no longer accepts this token. Connect again with a new one.")
			}
			rep.Repos = append(rep.Repos, m)
		}
	}
	s.cache.put(principalID, days, rep)
	return rep, nil
}

// Total adds the repositories up.
func (r *Report) Total() Metrics {
	var t Metrics
	for _, m := range r.Repos {
		t.Commits += m.Commits
		t.PRsOpened += m.PRsOpened
		t.PRsMerged += m.PRsMerged
		t.IssuesOpened += m.IssuesOpened
		t.IssuesClosed += m.IssuesClosed
		t.Releases += m.Releases
		t.Runs += m.Runs
		t.RunsPassed += m.RunsPassed
		t.RunsFailed += m.RunsFailed
	}
	return t
}
