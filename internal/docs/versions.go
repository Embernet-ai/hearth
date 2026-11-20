package docs

// History, diffs, and restore.
//
// Every edit since the substrate landed has written a full snapshot into
// document_versions, and nothing has ever read one. This is the read path, and
// it is the whole of the work: the expensive half was done a long time ago by
// somebody who decided snapshots beat deltas at this scale.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Version is one snapshot.
type Version struct {
	Version    int64
	Title      string
	Body       string
	AuthorID   string
	AuthorName string
	CreatedAt  time.Time
	// Current marks the version the document is on now.
	Current bool
}

// History returns a document's versions, newest first.
func (s *Service) History(ctx context.Context, orgID string, actor core.Subjects, docID string, limit int) ([]*Version, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT v.version, v.title, v.body, v.author_id::text,
		       COALESCE(NULLIF(p.display_name,''), v.author_id::text),
		       v.created_at, (v.version = d.version)
		FROM document_versions v
		JOIN documents d ON d.id = v.document_id
		JOIN principals p ON p.id = v.author_id
		WHERE v.document_id = $1 AND d.org_id = $2
		ORDER BY v.version DESC LIMIT $3`, docID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("docs: history: %w", err)
	}
	defer rows.Close()

	var out []*Version
	for rows.Next() {
		v := &Version{}
		if err := rows.Scan(&v.Version, &v.Title, &v.Body, &v.AuthorID,
			&v.AuthorName, &v.CreatedAt, &v.Current); err != nil {
			return nil, fmt.Errorf("docs: scan version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VersionAt returns one snapshot.
func (s *Service) VersionAt(ctx context.Context, orgID string, actor core.Subjects, docID string, version int64) (*Version, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	v := &Version{}
	err := s.db.QueryRow(ctx, `
		SELECT v.version, v.title, v.body, v.author_id::text,
		       COALESCE(NULLIF(p.display_name,''), v.author_id::text),
		       v.created_at, (v.version = d.version)
		FROM document_versions v
		JOIN documents d ON d.id = v.document_id
		JOIN principals p ON p.id = v.author_id
		WHERE v.document_id = $1 AND d.org_id = $2 AND v.version = $3`,
		docID, orgID, version).
		Scan(&v.Version, &v.Title, &v.Body, &v.AuthorID, &v.AuthorName, &v.CreatedAt, &v.Current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("docs: version %d: %w", version, err)
	}
	return v, nil
}

// Restore puts an old version back.
//
// It writes a NEW version rather than rewinding the counter. Rewinding would
// make version 4 mean two different things on two different days, break every
// optimistic concurrency check that had already seen the old 4, and lose the
// record that somebody restored anything. Undoing an edit is itself an edit.
func (s *Service) Restore(ctx context.Context, orgID string, actor core.Subjects, docID string, version int64) (*Document, error) {
	old, err := s.VersionAt(ctx, orgID, actor, docID, version)
	if err != nil {
		return nil, err
	}
	return s.Update(ctx, orgID, actor, docID, UpdateParams{
		Title: &old.Title, Body: &old.Body,
		// No expected version. The person restoring is looking at history
		// rather than at an editor, so there is no version they were holding,
		// and refusing because somebody else saved in the meantime would mean
		// restore only worked on a quiet system.
	})
}

// -----------------------------------------------------------------------------
// Diff
// -----------------------------------------------------------------------------

// DiffOp is one line of a comparison.
type DiffOp struct {
	// Kind is " " for unchanged, "-" for removed, "+" for added.
	Kind string
	Text string
	// Line numbers in the old and new texts, zero where the line is absent.
	Old int
	New int
}

// Diff compares two versions line by line.
//
// Lines rather than words. A maintenance runbook is a list of steps, and "step
// four changed" is what somebody needs to see; a word-level diff of a procedure
// is a wall of colour that takes longer to read than the whole document.
func Diff(before, after string) []DiffOp {
	a := splitLines(before)
	b := splitLines(after)
	return diffLines(a, b)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			out = append(out, trimCR(line))
			start = i + 1
		}
	}
	out = append(out, trimCR(s[start:]))
	return out
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}

// diffLines is a longest common subsequence, computed with the classic table.
//
// Quadratic, and bounded rather than clever. A document that reaches the bound
// falls back to reporting the whole thing as replaced, which is honest: the
// interesting case is a procedure of a few dozen lines, and spending a second
// of CPU to render a diff of a hundred thousand is a way to make one page take
// the server down.
const maxDiffLines = 4000

func diffLines(a, b []string) []DiffOp {
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		out := make([]DiffOp, 0, len(a)+len(b))
		for i, line := range a {
			out = append(out, DiffOp{Kind: "-", Text: line, Old: i + 1})
		}
		for i, line := range b {
			out = append(out, DiffOp{Kind: "+", Text: line, New: i + 1})
		}
		return out
	}

	// lcs[i][j] is the length of the longest common subsequence of a[i:] and
	// b[j:], built backwards so the walk forwards produces the ops in order.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out []DiffOp
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffOp{Kind: " ", Text: a[i], Old: i + 1, New: j + 1})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffOp{Kind: "-", Text: a[i], Old: i + 1})
			i++
		default:
			out = append(out, DiffOp{Kind: "+", Text: b[j], New: j + 1})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, DiffOp{Kind: "-", Text: a[i], Old: i + 1})
	}
	for ; j < len(b); j++ {
		out = append(out, DiffOp{Kind: "+", Text: b[j], New: j + 1})
	}
	return out
}

// Changed reports whether a diff contains anything, so a history page can say
// "title only" rather than showing an empty comparison.
func Changed(ops []DiffOp) bool {
	for _, op := range ops {
		if op.Kind != " " {
			return true
		}
	}
	return false
}
