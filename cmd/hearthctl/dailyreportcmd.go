package main

// Filing a daily report for a day that has already gone, as the person it
// belongs to.
//
//	hearthctl daily-report -as EMAIL -date YYYY-MM-DD [-file PATH]     read the body from PATH, or standard input
//	hearthctl daily-report -as EMAIL -dir DIR                          every DIR/YYYY-MM-DD.md, one report per file
//	hearthctl daily-report ... -apply                                  write it; without -apply nothing is written
//	hearthctl daily-report ... -append                                 the day is already filed: add the text to the end of that report
//
// The web form files today and the API has no day field, so a report that was
// owed last week has no way in. This goes through the same docs.Create call the
// form uses, as the named person, with that day as the report's date. So the
// one-a-day rule is the database's own (ErrReportExists), the department is the
// same DefaultReportSpace the form starts from, and the report is the person's:
// they are its author, owner and admin, exactly as if they had filed it that day.
//
// -append is for a day that grew after it was filed, an afternoon written once
// the morning's report was in. It goes through docs.Service.Update as the same
// person, against the version it just read, so an edit made in the meantime
// stops it with ErrConflict instead of being written over. The text already
// there is kept exactly and the new text follows a blank line. Running it twice
// with the same text adds nothing the second time.
//
// Nothing is faked. The document's created_at is when this ran, not the day it
// is for, and every report writes a daily_report.file audit row naming the
// person it was filed as and the program that filed it.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// reportFileName is a day's file in -dir mode.
var reportFileName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.md$`)

// parseReportDay reads YYYY-MM-DD and returns an instant inside that day in the
// office, which is what docs.CreateParams.ReportDate wants. Noon UTC is the
// same calendar day in New York in every season. A day after today in the
// office is refused: a report is about work that happened.
func parseReportDay(day string, now time.Time) (time.Time, error) {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a day, want YYYY-MM-DD", day)
	}
	at := time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.UTC)
	if docs.ReportDay(at) > docs.ReportDay(now) {
		return time.Time{}, fmt.Errorf("%s is after today (%s) in the office; a report is for a day that has happened", day, docs.ReportDay(now))
	}
	return at, nil
}

// reportJob is one report to file.
type reportJob struct {
	Day  string
	Body string
}

// dailyReportFiler holds what filing needs, so the command and its test run the
// same code.
type dailyReportFiler struct {
	db  *pgxpool.Pool
	az  subjectsFinder
	svc docs.Store
	org string
	// who is the person the reports are filed as.
	who string
	now func() time.Time
}

// file files one report. With apply false it checks everything and writes
// nothing: the day, the body, whether the person already has that day, and the
// department it would go to.
func (f *dailyReportFiler) file(ctx context.Context, j reportJob, apply bool) (docID, space string, err error) {
	at, err := parseReportDay(j.Day, f.now())
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(j.Body) == "" {
		return "", "", fmt.Errorf("%s: the report is empty", j.Day)
	}
	if existing, err := f.svc.ReportOn(ctx, f.org, f.who, j.Day); err != nil {
		return "", "", err
	} else if existing != "" {
		return existing, "", fmt.Errorf("%s: %w (document %s)", j.Day, docs.ErrReportExists, existing)
	}
	space, err = f.svc.DefaultReportSpace(ctx, f.org, f.who)
	if err != nil {
		return "", "", err
	}
	if !apply {
		return "", space, nil
	}
	actor, err := f.az.SubjectsFor(ctx, f.org, f.who)
	if err != nil {
		return "", "", fmt.Errorf("resolve the person's access: %w", err)
	}
	d, err := f.svc.Create(ctx, f.org, actor, docs.CreateParams{
		Kind: docs.KindDailyReport, Title: "Daily report, " + j.Day, Body: j.Body,
		SpaceID: &space, ReportDate: &at,
	})
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", j.Day, err)
	}
	if f.db == nil { // a test with no database; the command always has one (the audit row stays in Postgres in both back ends)
		return d.ID, space, nil
	}
	if err := audit.Record(ctx, f.db, audit.Entry{
		OrgID: f.org, ActorID: f.who, Action: audit.ActionDailyReportFile,
		EntityType: "document", EntityID: d.ID,
		After: map[string]string{"day": j.Day, "space_id": space, "via": "hearthctl daily-report"},
	}); err != nil {
		return d.ID, space, fmt.Errorf("%s: filed as document %s but the audit row failed: %w", j.Day, d.ID, err)
	}
	return d.ID, space, nil
}

// errAlreadyAppended says the report already holds the text being added.
var errAlreadyAppended = errors.New("the report already holds this text")

// appendSeparator is what goes between the report as it stands and the text
// added to it: a blank line, however the old text ended.
func appendSeparator(old string) string {
	switch {
	case old == "", strings.HasSuffix(old, "\n\n"):
		return ""
	case strings.HasSuffix(old, "\n"):
		return "\n"
	}
	return "\n\n"
}

// appendResult is what an append did, or with apply false would do.
type appendResult struct {
	DocID      string
	Version    int64
	OldLen     int
	AddedLen   int
	NewLen     int
	NewVersion int64
}

// appendTo adds j.Body to the end of the person's report for j.Day. With apply
// false it reads and checks and writes nothing. The write is docs Update with
// ExpectedVersion set to the version this read, so a concurrent edit fails with
// docs.ErrConflict. The audit row is written after it, as file does.
func (f *dailyReportFiler) appendTo(ctx context.Context, j reportJob, apply bool) (appendResult, error) {
	var r appendResult
	if _, err := parseReportDay(j.Day, f.now()); err != nil {
		return r, err
	}
	add := strings.TrimSpace(j.Body)
	if add == "" {
		return r, fmt.Errorf("%s: the text to append is empty", j.Day)
	}
	id, err := f.svc.ReportOn(ctx, f.org, f.who, j.Day)
	if err != nil {
		return r, err
	}
	if id == "" {
		return r, fmt.Errorf("%s: there is no report filed for that day to append to (leave off -append to file it)", j.Day)
	}
	actor, err := f.az.SubjectsFor(ctx, f.org, f.who)
	if err != nil {
		return r, fmt.Errorf("resolve the person's access: %w", err)
	}
	cur, err := f.svc.Get(ctx, f.org, actor, id)
	if err != nil {
		return r, fmt.Errorf("%s: read document %s: %w", j.Day, id, err)
	}
	r = appendResult{DocID: id, Version: cur.Version, OldLen: len(cur.Body)}
	if strings.Contains(cur.Body, add) {
		return r, fmt.Errorf("%s: %w (document %s)", j.Day, errAlreadyAppended, id)
	}
	body := cur.Body + appendSeparator(cur.Body) + add + "\n"
	r.AddedLen, r.NewLen = len(body)-len(cur.Body), len(body)
	if !apply {
		return r, nil
	}
	d, err := f.svc.Update(ctx, f.org, actor, id, docs.UpdateParams{Body: &body, ExpectedVersion: cur.Version})
	if err != nil {
		return r, fmt.Errorf("%s: %w", j.Day, err)
	}
	if d != nil {
		r.NewVersion = d.Version
	}
	if f.db == nil { // a test with no database
		return r, nil
	}
	if err := audit.Record(ctx, f.db, audit.Entry{
		OrgID: f.org, ActorID: f.who, Action: audit.ActionDailyReportAppend,
		EntityType: "document", EntityID: id,
		Before: map[string]any{"version": cur.Version, "length": r.OldLen},
		After:  map[string]any{"day": j.Day, "version": r.NewVersion, "length": r.NewLen, "added": r.AddedLen, "via": "hearthctl daily-report -append"},
	}); err != nil {
		return r, fmt.Errorf("%s: appended to document %s but the audit row failed: %w", j.Day, id, err)
	}
	return r, nil
}

// readReportDir returns the reports in dir, oldest day first. A file whose name
// is not a day is an error, so a typo does not file nothing silently.
func readReportDir(dir string) ([]reportJob, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var jobs []reportJob
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		m := reportFileName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("%s: a file here has to be named YYYY-MM-DD.md", filepath.Join(dir, e.Name()))
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, reportJob{Day: m[1], Body: string(b)})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Day < jobs[j].Day })
	return jobs, nil
}

func runDailyReport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daily-report", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the person the report is filed as (required)")
	day := fs.String("date", "", "the day the report is for, YYYY-MM-DD, in the office's time zone")
	file := fs.String("file", "", "read the body from this file; default standard input (with -date)")
	dir := fs.String("dir", "", "file every YYYY-MM-DD.md in this directory (instead of -date)")
	apply := fs.Bool("apply", false, "write it. Without this nothing is written")
	appendTo := fs.Bool("append", false, "the day's report is already filed: add the text to the end of it, as that person, instead of filing a new one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*as) == "" {
		return errors.New("-as is required: the person the report belongs to")
	}
	if (*day == "") == (*dir == "") {
		return errors.New("give either -date (one report) or -dir (a folder of YYYY-MM-DD.md files)")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}

	var jobs []reportJob
	if *dir != "" {
		var err error
		if jobs, err = readReportDir(*dir); err != nil {
			return err
		}
	} else {
		var b []byte
		var err error
		if *file != "" {
			b, err = os.ReadFile(*file)
		} else {
			b, err = io.ReadAll(os.Stdin)
		}
		if err != nil {
			return err
		}
		jobs = []reportJob{{Day: *day, Body: string(b)}}
	}
	if len(jobs) == 0 {
		return errors.New("no reports to file")
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	orgID, _, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	var who string
	if err := pools.Web.QueryRow(ctx, `
		SELECT p.id::text FROM principals p
		  LEFT JOIN user_identities u ON u.principal_id = p.id
		  LEFT JOIN local_credentials l ON l.principal_id = p.id
		 WHERE p.org_id = $1 AND p.status = 'active'
		   AND lower(COALESCE(u.email::text, l.email::text)) = lower($2) LIMIT 1`, orgID, *as).Scan(&who); err != nil {
		return fmt.Errorf("find %s: %w", *as, err)
	}
	az := authz.New(pools.Web)
	_, ds, err := backendStores(ctx, pools.Web, az)
	if err != nil {
		return err
	}
	f := &dailyReportFiler{db: pools.Web, az: az, svc: ds, org: orgID, who: who, now: time.Now}

	if *appendTo {
		return appendReports(ctx, f, jobs, *as, *apply)
	}

	// Check every report before the first is written, so one bad day stops the
	// run with nothing filed rather than leaving half a month.
	taken := 0
	for _, j := range jobs {
		if _, _, err := f.file(ctx, j, false); err != nil {
			if errors.Is(err, docs.ErrReportExists) && *dir != "" {
				taken++
				fmt.Printf("already filed, will skip: %v\n", err)
				continue
			}
			return err
		}
	}
	if !*apply {
		fmt.Printf("%d reports checked as %s, %d already filed; nothing was written, add -apply\n", len(jobs), *as, taken)
		return nil
	}
	filed := 0
	for _, j := range jobs {
		id, space, err := f.file(ctx, j, true)
		switch {
		case errors.Is(err, docs.ErrReportExists) && *dir != "":
			fmt.Printf("skipped %s: already filed (%s)\n", j.Day, id)
		case err != nil:
			return fmt.Errorf("%w (filed %d before this)", err, filed)
		default:
			filed++
			fmt.Printf("filed %s document %s space %s\n", j.Day, id, space)
		}
	}
	fmt.Printf("%d reports filed as %s\n", filed, *as)
	return nil
}

// appendReports is -append: every day must already have a report and the text
// must not already be in it, checked before the first is written, so one bad
// day stops the run with nothing changed.
func appendReports(ctx context.Context, f *dailyReportFiler, jobs []reportJob, as string, apply bool) error {
	var todo []reportJob
	for _, j := range jobs {
		r, err := f.appendTo(ctx, j, false)
		if errors.Is(err, errAlreadyAppended) {
			fmt.Printf("already appended, will skip: %v\n", err)
			continue
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s document %s version %d: would append %d bytes to %d, resulting length %d bytes\n", j.Day, r.DocID, r.Version, r.AddedLen, r.OldLen, r.NewLen)
		todo = append(todo, j)
	}
	if !apply {
		fmt.Printf("%d reports checked as %s, %d to append to; nothing was written, add -apply\n", len(jobs), as, len(todo))
		return nil
	}
	done := 0
	for _, j := range todo {
		r, err := f.appendTo(ctx, j, true)
		if err != nil {
			return fmt.Errorf("%w (appended %d before this)", err, done)
		}
		done++
		fmt.Printf("appended %s document %s version %d to %d, length %d to %d bytes\n", j.Day, r.DocID, r.Version, r.NewVersion, r.OldLen, r.NewLen)
	}
	fmt.Printf("%d reports appended to as %s\n", done, as)
	return nil
}
