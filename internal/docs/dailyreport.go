package docs

// Daily reports: one per person per day, filed under a department, readable by
// the people who can reach that department.
//
// A report is still a document (kind daily_report), so versioning, photos,
// comments, search and permissions all come with it. What this file adds is
// the day it is for, the rule that there is one a day, and the roll-up that
// reads many of them at once.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Embernet-ai/hearth/internal/core"

	// The service image is distroless and carries no zoneinfo. A report's day
	// is the office's day, so the zone data travels inside the binary.
	_ "time/tzdata"
)

// ErrReportExists is returned when somebody files a second report for a day.
var ErrReportExists = errors.New("you already filed a daily report for that day")

// ReportSpaceName is the department every account can file under.
const ReportSpaceName = "Daily reports"

// reportZoneName is the office's time zone. A report filed at 9pm Eastern is
// that day's report, not tomorrow's, whatever the server's clock says.
const reportZoneName = "America/New_York"

func reportZone() *time.Location {
	loc, err := time.LoadLocation(reportZoneName)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ReportDay is the calendar date, as YYYY-MM-DD, that t falls on in the office.
func ReportDay(t time.Time) string {
	return t.In(reportZone()).Format("2006-01-02")
}

// isReportDayTaken reports whether err is the one-report-a-day index firing.
func isReportDayTaken(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" &&
		pg.ConstraintName == "documents_one_report_per_person_per_day"
}

// EnsureReportSpace returns the org's open "Daily reports" department, making
// it if the org has none yet. Migration 00044 makes it for real orgs; this
// covers personal playgrounds and organisations created later, so nobody ever
// has to set it up by hand.
func (s *Service) EnsureReportSpace(ctx context.Context, orgID, creatorID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id::text FROM spaces
		 WHERE org_id = $1 AND open_to_all AND name = $2
		 ORDER BY created_at LIMIT 1`, orgID, ReportSpaceName).Scan(&id)
	if err == nil {
		return id, nil
	}
	err = s.db.QueryRow(ctx, `
		INSERT INTO spaces (id, org_id, parent_id, name, kind, created_by, open_to_all)
		VALUES (gen_random_uuid(), $1, NULL, $2, 'area', $3, true)
		RETURNING id::text`, orgID, ReportSpaceName, creatorID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("docs: make the daily reports department: %w", err)
	}
	return id, nil
}

// DefaultReportSpace is where a person's report is filed unless they choose
// otherwise: their own department if they have one, else the open "Daily
// reports" department that every account can use.
func (s *Service) DefaultReportSpace(ctx context.Context, orgID, principalID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT space_id::text FROM principal_departments
		 WHERE org_id = $1 AND principal_id = $2
		 ORDER BY created_at LIMIT 1`, orgID, principalID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("docs: default report space: %w", err)
	}
	return s.EnsureReportSpace(ctx, orgID, principalID)
}

// ReportOn returns the id of the caller's own report for a day, or "".
func (s *Service) ReportOn(ctx context.Context, orgID, principalID, day string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id::text FROM documents
		 WHERE org_id = $1 AND kind = 'daily_report' AND deleted_at IS NULL
		   AND created_by = $2 AND report_date = $3::date`,
		orgID, principalID, day).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("docs: find today's report: %w", err)
	}
	return id, nil
}

// ReportFilter narrows a roll-up. All fields are optional.
type ReportFilter struct {
	// SpaceID is a department; reports filed anywhere beneath it count too.
	SpaceID string
	// From and To are inclusive YYYY-MM-DD days.
	From, To string
	// PersonID limits the roll-up to one author.
	PersonID string
	// DocID narrows to one report, for the single-report PDF.
	DocID string
}

// ReportEntry is one report as the roll-up and the PDF read it.
type ReportEntry struct {
	ID         string
	Title      string
	Body       string
	BodyFormat string
	Day        string
	AuthorID   string
	Author     string
	SpaceID    string
	SpacePath  string
	UpdatedAt  time.Time
}

// maxReports bounds one roll-up, so a wide date range cannot become a request
// that holds the database for a minute.
const maxReports = 500

// Reports returns the reports the caller may read that match the filter,
// newest day first and then by author.
func (s *Service) Reports(ctx context.Context, orgID string, actor core.Subjects, f ReportFilter) ([]*ReportEntry, error) {
	args := []any{orgID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	// The same access test every other document list uses, so a super admin
	// and an ordinary reader get exactly the answers they get everywhere else.
	vis := actor.GrantsExists(core.EntityDocument, "d.id", arg)

	where := []string{"d.org_id = $1", "d.kind = 'daily_report'", "d.deleted_at IS NULL", vis}
	with := ""
	if f.SpaceID != "" {
		args = append(args, f.SpaceID)
		with = fmt.Sprintf(`WITH RECURSIVE tree AS (
			SELECT id FROM spaces WHERE id = $%d::uuid AND org_id = $1
			UNION ALL
			SELECT c.id FROM spaces c JOIN tree t ON c.parent_id = t.id) `, len(args))
		where = append(where, "d.space_id IN (SELECT id FROM tree)")
	}
	if f.From != "" {
		args = append(args, f.From)
		where = append(where, fmt.Sprintf("d.report_date >= $%d::date", len(args)))
	}
	if f.To != "" {
		args = append(args, f.To)
		where = append(where, fmt.Sprintf("d.report_date <= $%d::date", len(args)))
	}
	if f.DocID != "" {
		args = append(args, f.DocID)
		where = append(where, fmt.Sprintf("d.id = $%d::uuid", len(args)))
	}
	if f.PersonID != "" {
		args = append(args, f.PersonID)
		where = append(where, fmt.Sprintf("d.created_by = $%d::uuid", len(args)))
	}

	rows, err := s.db.Query(ctx, with+`
		SELECT d.id::text, d.title, d.body, d.body_format, COALESCE(d.space_id::text, ''),
		       COALESCE(d.report_date::text, ''), d.updated_at, d.created_by::text,
		       COALESCE(NULLIF(p.display_name, ''), d.created_by::text)
		  FROM documents d
		  JOIN principals p ON p.id = d.created_by
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY d.report_date DESC NULLS LAST, 9, d.created_at
		 LIMIT `+fmt.Sprint(maxReports), args...)
	if err != nil {
		return nil, fmt.Errorf("docs: reports: %w", err)
	}
	defer rows.Close()

	var out []*ReportEntry
	for rows.Next() {
		r := &ReportEntry{}
		if err := rows.Scan(&r.ID, &r.Title, &r.Body, &r.BodyFormat, &r.SpaceID,
			&r.Day, &r.UpdatedAt, &r.AuthorID, &r.Author); err != nil {
			return nil, fmt.Errorf("docs: scan report: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	paths, err := s.spacePaths(ctx, orgID, actor)
	if err != nil {
		return nil, err
	}
	for _, r := range out {
		if r.SpaceID == "" {
			r.SpacePath = "No department"
		} else if p, ok := paths[r.SpaceID]; ok {
			r.SpacePath = p
		} else {
			r.SpacePath = "Elsewhere"
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day > out[j].Day
		}
		return out[i].Author < out[j].Author
	})
	return out, nil
}

// spacePaths maps every space id in the org to its slash-separated path, as the
// actor may see it: the path starts at the highest folder they may open, so a
// person given one subfolder is never told what the folders above it are
// called.
func (s *Service) spacePaths(ctx context.Context, orgID string, actor core.Subjects) (map[string]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id::text, COALESCE(parent_id::text, ''), name FROM spaces WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: space paths: %w", err)
	}
	defer rows.Close()
	type node struct{ parent, name string }
	nodes := map[string]node{}
	for rows.Next() {
		var id string
		var n node
		if err := rows.Scan(&id, &n.parent, &n.name); err != nil {
			return nil, err
		}
		nodes[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var open map[string]bool
	if !actor.Super() {
		open = map[string]bool{}
		vr, err := s.db.Query(ctx, `
			WITH RECURSIVE `+core.MemberSpaces("$3")+`
			SELECT entity_id::text FROM grants
			 WHERE entity_type = 'space' AND org_id = $1 AND subject_key = ANY($2)
			   AND (expires_at IS NULL OR expires_at > now())
			UNION SELECT id::text FROM member_spaces`, orgID, actor.Keys(), actor.Principal())
		if err != nil {
			return nil, fmt.Errorf("docs: space paths visibility: %w", err)
		}
		for vr.Next() {
			var id string
			if err := vr.Scan(&id); err != nil {
				vr.Close()
				return nil, err
			}
			open[id] = true
		}
		vr.Close()
		if err := vr.Err(); err != nil {
			return nil, err
		}
	}
	paths := make(map[string]string, len(nodes))
	for id := range nodes {
		var parts []string
		for cur, hops := id, 0; cur != "" && hops < 12; hops++ {
			n, ok := nodes[cur]
			if !ok {
				break
			}
			if open != nil && cur != id && !open[cur] {
				break
			}
			parts = append([]string{n.name}, parts...)
			cur = n.parent
		}
		paths[id] = strings.Join(parts, " / ")
	}
	return paths, nil
}
