package main

// Putting a person's past time into Hearth from a file, as that person.
//
//	hearthctl time-import -as EMAIL -file ENTRIES.csv              check it, write nothing
//	hearthctl time-import -as EMAIL -file ENTRIES.csv -apply       write it
//
// The file is a CSV with a header row of start,minutes,note: start is an
// RFC 3339 time, minutes a whole number, note the words that go on the entry.
//
// Every entry goes through timelog.LogOwn, the call the Log time form makes, as
// the named person, so the entry is theirs, stands on its own with no document,
// and lands Pending for a manager to approve. Nothing here can approve it, and
// the person cannot approve their own.
//
// It will not import twice. Before anything is written every entry is checked
// against the person's live entries: if any already starts at the same instant
// the run stops with nothing written. -skip-existing is for finishing a run that
// died partway, and skips those entries instead of stopping.
//
// A note has to say what the entry is. Time rebuilt from somewhere else must
// never read like time somebody timed, so a note has to begin with
// "Reconstructed from" (time taken from a record) or "Added on" (time the person
// says they worked and no record shows, in their own words), and anything else
// is refused. Each run writes one time.import audit row
// naming the person, the file's checksum, the count, the minutes, and the span.

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// importedNotePrefixes are the two ways an imported note may begin.
var importedNotePrefixes = []string{"Reconstructed from", "Added on"}

func hasImportedPrefix(note string) bool {
	for _, p := range importedNotePrefixes {
		if strings.HasPrefix(note, p) {
			return true
		}
	}
	return false
}

// timeImportRow is one entry in the file.
type timeImportRow struct {
	Start   time.Time
	Minutes int
	Note    string
}

// readTimeImport parses the file and returns its rows and the SHA-256 of its
// bytes. Every problem is reported with its line number, and any problem stops
// the run: a file half understood is not imported at all.
func readTimeImport(r io.Reader, now time.Time) ([]timeImportRow, string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	cr := csv.NewReader(strings.NewReader(string(raw)))
	cr.FieldsPerRecord = 3
	head, err := cr.Read()
	if err != nil {
		return nil, "", fmt.Errorf("read the header: %w", err)
	}
	if len(head) != 3 || head[0] != "start" || head[1] != "minutes" || head[2] != "note" {
		return nil, "", errors.New("the header has to be start,minutes,note")
	}
	var rows []timeImportRow
	seen := map[int64]int{}
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("line %d: %w", line, err)
		}
		start, err := time.Parse(time.RFC3339, strings.TrimSpace(rec[0]))
		if err != nil {
			return nil, "", fmt.Errorf("line %d: %q is not an RFC 3339 time", line, rec[0])
		}
		mins, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil || mins <= 0 || mins > 100000 {
			return nil, "", fmt.Errorf("line %d: minutes %q must be a whole number from 1 to 100000", line, rec[1])
		}
		note := strings.TrimSpace(rec[2])
		if !hasImportedPrefix(note) {
			return nil, "", fmt.Errorf("line %d: the note has to begin with one of %q so the record says what it is", line, importedNotePrefixes)
		}
		if start.After(now) {
			return nil, "", fmt.Errorf("line %d: %s is in the future", line, rec[0])
		}
		if first, dup := seen[start.Unix()]; dup {
			return nil, "", fmt.Errorf("line %d: starts at the same instant as line %d", line, first)
		}
		seen[start.Unix()] = line
		rows = append(rows, timeImportRow{Start: start, Minutes: mins, Note: note})
	}
	if len(rows) == 0 {
		return nil, "", errors.New("the file has no entries")
	}
	return rows, hex.EncodeToString(sum[:]), nil
}

// timeImporter holds what an import needs, so the command and its test run the
// same code.
type timeImporter struct {
	db  *pgxpool.Pool // the audit row stays in Postgres in both back ends
	az  subjectsFinder
	svc timelog.Store
	org string
	who string
}

// existing returns the rows whose start the person already has a live entry at. It asks the store the person's time is in
// (Postgres, or EmberOps in that back end), over the span of the file, and compares starts to the second.
func (t *timeImporter) existing(ctx context.Context, rows []timeImportRow) (map[int64]bool, error) {
	have := map[int64]bool{}
	if len(rows) == 0 {
		return have, nil
	}
	from, to := rows[0].Start, rows[0].Start
	for _, r := range rows {
		if r.Start.Before(from) {
			from = r.Start
		}
		if r.Start.After(to) {
			to = r.Start
		}
	}
	got, err := t.svc.Mine(ctx, t.org, t.who, from.Add(-time.Minute), to.Add(time.Minute), false)
	if err != nil {
		return nil, fmt.Errorf("check existing time: %w", err)
	}
	starts := map[int64]bool{}
	for _, e := range got {
		starts[e.StartedAt.Unix()] = true
	}
	for _, r := range rows {
		if starts[r.Start.Unix()] {
			have[r.Start.Unix()] = true
		}
	}
	return have, nil
}

// timeImportResult is what a run did.
type timeImportResult struct {
	Written, Skipped, Minutes int
	First, Last               time.Time
}

// run checks the rows and, with apply, writes them. tagName, when set, tags
// each written entry; a person who may not define tags still gets the entries.
func (t *timeImporter) run(ctx context.Context, rows []timeImportRow, sum, tagName string, apply, skipExisting bool, log io.Writer) (*timeImportResult, error) {
	have, err := t.existing(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(have) > 0 && !skipExisting {
		return nil, fmt.Errorf("%d of these entries already exist for this person at the same start; nothing was written (-skip-existing finishes a run that died partway)", len(have))
	}
	res := &timeImportResult{Skipped: len(have)}
	for _, r := range rows {
		if have[r.Start.Unix()] {
			continue
		}
		res.Minutes += r.Minutes
		if res.First.IsZero() || r.Start.Before(res.First) {
			res.First = r.Start
		}
		if end := r.Start.Add(time.Duration(r.Minutes) * time.Minute); end.After(res.Last) {
			res.Last = end
		}
	}
	if !apply {
		res.Written = len(rows) - len(have)
		return res, nil
	}

	actor, err := t.az.SubjectsFor(ctx, t.org, t.who)
	if err != nil {
		return nil, fmt.Errorf("resolve the person's access: %w", err)
	}
	var tagID string
	if tagName != "" {
		tagID = t.findOrMakeTag(ctx, tagName, log)
	}
	var runErr error
	res.Minutes = 0
	for _, r := range rows {
		if have[r.Start.Unix()] {
			continue
		}
		e, err := t.svc.LogOwn(ctx, t.org, actor, "", r.Minutes, r.Start, r.Note)
		if err != nil {
			runErr = fmt.Errorf("entry at %s: %w (%d written before it)", r.Start.Format(time.RFC3339), err, res.Written)
			break
		}
		res.Written++
		res.Minutes += r.Minutes
		if tagID != "" {
			if err := t.svc.TagEntry(ctx, t.org, actor, e.ID, tagID); err != nil {
				fmt.Fprintf(log, "could not tag entry %s: %v\n", e.ID, err)
			}
		}
	}
	// One audit row per run, written even when the run died partway, so the
	// record says what did get in.
	if t.db != nil && (res.Written > 0 || runErr == nil) {
		if err := audit.Record(ctx, t.db, audit.Entry{
			OrgID: t.org, ActorID: t.who, Action: audit.ActionTimeImport,
			EntityType: "person", EntityID: t.who,
			After: map[string]any{
				"via": "hearthctl time-import", "file_sha256": sum, "entries": res.Written,
				"skipped": res.Skipped, "minutes": res.Minutes,
				"first": res.First.Format(time.RFC3339), "last": res.Last.Format(time.RFC3339),
				"complete": runErr == nil,
			},
		}); err != nil && runErr == nil {
			runErr = fmt.Errorf("entries written but the audit row failed: %w", err)
		}
	}
	return res, runErr
}

// findOrMakeTag returns the tag's id, making it if it does not exist. An empty
// answer means the entries go in untagged, and the reason is printed.
func (t *timeImporter) findOrMakeTag(ctx context.Context, name string, log io.Writer) string {
	tags, err := t.svc.ListTags(ctx, t.org)
	if err != nil {
		fmt.Fprintf(log, "no tag: %v\n", err)
		return ""
	}
	for _, g := range tags {
		if strings.EqualFold(g.Name, name) {
			return g.ID
		}
	}
	g, err := t.svc.CreateTag(ctx, t.org, t.who, name,
		"Imported time, not timed by the person. The entry's note says where it came from.")
	if err != nil {
		fmt.Fprintf(log, "no tag: %v\n", err)
		return ""
	}
	return g.ID
}

func runTimeImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("time-import", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres URL, or HEARTH_DATABASE_URL")
	as := fs.String("as", "", "email of the person the time belongs to (required)")
	file := fs.String("file", "", "the CSV: start,minutes,note (required)")
	tag := fs.String("tag", "reconstructed", "tag to put on each entry; empty for none")
	apply := fs.Bool("apply", false, "write it. Without this nothing is written")
	skip := fs.Bool("skip-existing", false, "skip entries already there instead of stopping, to finish a run that died partway")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*as) == "" || *file == "" {
		return errors.New("-as and -file are required")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	rows, sum, err := readTimeImport(f, time.Now())
	f.Close()
	if err != nil {
		return err
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
	tl, _, err := backendStores(ctx, pools.Web, az)
	if err != nil {
		return err
	}
	imp := &timeImporter{db: pools.Web, az: az, svc: tl, org: orgID, who: who}
	res, err := imp.run(ctx, rows, sum, *tag, *apply, *skip, os.Stderr)
	if res != nil {
		verb := "would write"
		if *apply {
			verb = "wrote"
		}
		fmt.Printf("%s %d entries, %.1f hours, %s to %s, %d skipped as already there, as %s (file sha256 %s)\n",
			verb, res.Written, float64(res.Minutes)/60, res.First.Format("2006-01-02"), res.Last.Format("2006-01-02"), res.Skipped, *as, sum)
		if !*apply {
			fmt.Println("nothing was written; add -apply")
		}
	}
	return err
}
