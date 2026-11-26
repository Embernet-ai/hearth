// Package migrate copies a Hearth Postgres database into EmberOPS through the
// emberops_hearth addon, so a standalone deployment can switch to the EmberOPS
// back end without losing what it holds.
//
// Every record is written with api.bulk_upsert keyed by its Hearth id, so the
// copy is idempotent: running it twice updates what it wrote the first time and
// makes no duplicates. Progress is checkpointed per step in Postgres, so a run
// that stops (a deploy, a dropped connection, an EmberOPS restart) resumes where
// it left off, and a run that failed on a few records can simply be run again.
// The steps follow the addon's MIGRATION_ORDER, so a record is only written
// after everything it references.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// Options configure a run.
type Options struct {
	// DryRun reads and maps everything but writes nothing, in EmberOPS or in
	// Postgres, and does not need EmberOPS to be reachable.
	DryRun bool
	// Restart ignores saved progress and starts every step from the top.
	Restart bool
	// Only limits the run to the named steps.
	Only []string
	// BatchSize is rows read per page; at most emberops.MaxBulk.
	BatchSize int
	// Loc is the EmberOPS site's time zone.
	Loc *time.Location
	// ActivityType is the Activity Type for a time entry with no tag; the
	// addon falls back to Hearth Settings' default when empty.
	ActivityType string
	// Warehouse is where stock movements are booked; empty uses the default
	// warehouse in Hearth Settings on the EmberOPS site.
	Warehouse string
	// CurrencyPerCent is the value of one cent in the site's currency
	// (0.01 for dollars). Zero means 0.01.
	CurrencyUnit float64
	// CRMParties says companies and contacts live in EmberOps (HEARTH_EMBEROPS_CRM_PARTIES=emberops). They do not by
	// default: Hearth keeps the CRM and the crm sync projects the accounting roles into EmberOps as Customers and
	// Suppliers, so copying all 6668 of Fireball's companies here would make a Customer or Supplier of every prospect,
	// next to the ones the sync made. Those two steps run only when this is set.
	CRMParties bool
	Log        *slog.Logger
}

// CurrencyPerCent is CurrencyUnit with its default.
func (o Options) CurrencyPerCent() float64 {
	if o.CurrencyUnit == 0 {
		return 0.01
	}
	return o.CurrencyUnit
}

type builder struct {
	z mapping.TZ
	// known is the emails EmberOPS already holds a User for, read once when the people steps run (nil in a dry run, which
	// calls nothing). A person who is not active is carried only when they are in it: a suspended colleague already in
	// EmberOPS stays up to date, and a throwaway or proof principal is never made into a User.
	known map[string]bool
	// built is the emails this run has already written a User or an Employee for. Principals can share an address (the
	// proof tooling makes a fresh one each run), and only one record can carry it.
	built map[string]bool
}

// person decides whether a principal becomes a User or an Employee, and why not. kind is "user" or "employee", so each
// step keeps its own record of the addresses it has taken.
func (b *builder) person(r mapping.Rec, kind string) string {
	email := strings.ToLower(r.S("email"))
	if s := r.S("status"); s != "" && s != "active" && !b.known[email] {
		return "not active and not in EmberOps, so no " + kind + " is made for them"
	}
	if b.built == nil {
		b.built = map[string]bool{}
	}
	if b.built[kind+"\x00"+email] {
		return "another principal with the same email already carries this " + kind
	}
	b.built[kind+"\x00"+email] = true
	return ""
}

// StepReport is the outcome of one step.
type StepReport struct {
	Name     string
	Doctypes []string
	// Source is how many rows Hearth holds for the step.
	Source int
	// Mapped is how many became records; Skipped is the rest, with reasons.
	Mapped  int
	Skipped int
	// Created and Updated are what EmberOPS reported; Failed are records it refused.
	Created, Updated, Failed int
	// Remote is how many records EmberOPS now holds with a hearth_id in the
	// step's doctypes, or -1 when it could not be counted.
	Remote int
	// Notes holds the first few skip reasons and failures.
	Notes []string
	// Classes counts refused records by what EmberOps said, with ids and numbers taken out so the same
	// cause is one line however many records it hit. Notes stops at a few lines, and skips come first,
	// which on a real site hid every refusal behind the reasons for skipping.
	Classes map[string]int
	Sample  map[string]string
	Resumed bool
}

// Report is the whole run.
type Report struct {
	DryRun bool
	Steps  []StepReport
}

// Ok reports whether every record Hearth holds is accounted for: written, or
// deliberately skipped.
func (r Report) Ok() bool {
	for _, s := range r.Steps {
		if s.Failed > 0 {
			return false
		}
	}
	return true
}

// Write prints the report as a table.
func (r Report) Write(w io.Writer) {
	mode := "migration"
	if r.DryRun {
		mode = "dry run (nothing was written)"
	}
	fmt.Fprintf(w, "\nEmberOps %s\n\n", mode)
	fmt.Fprintf(w, "%-22s %8s %8s %8s %8s %8s %8s\n", "step", "hearth", "mapped", "skipped", "created", "updated", "failed")
	var src, mapped, skipped, failed int
	for _, s := range r.Steps {
		fmt.Fprintf(w, "%-22s %8d %8d %8d %8d %8d %8d\n", s.Name, s.Source, s.Mapped, s.Skipped, s.Created, s.Updated, s.Failed)
		src += s.Source
		mapped += s.Mapped
		skipped += s.Skipped
		failed += s.Failed
	}
	fmt.Fprintf(w, "%-22s %8d %8d %8d\n", "total", src, mapped, skipped)
	if !r.DryRun {
		fmt.Fprintf(w, "\nverification (records in EmberOps carrying a hearth_id):\n")
		for _, s := range r.Steps {
			remote := "unavailable"
			if s.Remote >= 0 {
				remote = fmt.Sprint(s.Remote)
			}
			fmt.Fprintf(w, "  %-22s %-30s expected >= %-6d in EmberOps %s\n", s.Name, strings.Join(s.Doctypes, "/"), s.Mapped-s.Failed, remote)
		}
	}
	for _, s := range r.Steps {
		for _, n := range s.Notes {
			fmt.Fprintf(w, "  %s: %s\n", s.Name, n)
		}
	}
	for _, s := range r.Steps {
		keys := make([]string, 0, len(s.Classes))
		for k := range s.Classes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  refused %s: %d x %s (e.g. %s)\n", s.Name, s.Classes[k], k, s.Sample[k])
		}
	}
	if failed > 0 {
		fmt.Fprintf(w, "\n%d record(s) failed. Fix the cause and run again with --restart to retry every step: records already written are updated, not duplicated.\n", failed)
	}
}

// Source is where Hearth's rows and the run's progress are read from. PG is
// the Postgres implementation; tests use an in-memory one.
type Source interface {
	Org(ctx context.Context) (string, error)
	Count(ctx context.Context, st Step, org string) (int, error)
	// Page returns rows with cur > cursor, ordered by cur, at most limit.
	Page(ctx context.Context, st Step, cursor string, limit int, org string) ([]Row, error)
	Progress(ctx context.Context, step string) (cursor string, done bool, err error)
	Save(ctx context.Context, step, cursor string, rows int) error
	Finish(ctx context.Context, step, cursor string) error
	Reset(ctx context.Context, step string) error
}

// Row is one source row: its paging cursor and its fields.
type Row struct {
	Cur string
	Rec mapping.Rec
}

// Runner does the copy.
type Runner struct {
	Src Source
	C   *emberops.Client // nil is allowed only for a dry run
}

// NewRunner reads from Postgres.
func NewRunner(db *pgxpool.Pool, c *emberops.Client) *Runner { return &Runner{Src: PG{DB: db}, C: c} }

// Run copies every step. A step that fails outright stops the run; records
// EmberOPS merely refuses are counted and the run goes on.
func (m *Runner) Run(ctx context.Context, opts Options) (Report, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.BatchSize <= 0 || opts.BatchSize > emberops.MaxBulk {
		opts.BatchSize = 200
	}
	if m.C == nil && !opts.DryRun {
		return Report{}, errors.New("migrate: an EmberOps client is required unless this is a dry run")
	}
	orgID, err := m.Src.Org(ctx)
	if err != nil {
		return Report{}, err
	}
	rep := Report{DryRun: opts.DryRun}
	b := &builder{z: mapping.TZ{Loc: opts.Loc}}
	if !opts.DryRun && m.C != nil {
		users, err := m.C.ListAll(ctx, emberops.ListQuery{Doctype: "User", Fields: []string{"name"}, Reference: true,
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}}, 0)
		if err != nil {
			return Report{}, fmt.Errorf("migrate: read the Users EmberOPS holds: %w", err)
		}
		b.known = map[string]bool{}
		for _, u := range users {
			b.known[strings.ToLower(u.S("name"))] = true
		}
	}
	all := append(steps(opts), documentSteps(opts)...)
	for _, st := range all {
		if len(opts.Only) > 0 && !contains(opts.Only, st.name) {
			continue
		}
		if (st.name == "companies" || st.name == "contacts") && !opts.CRMParties {
			opts.Log.Info("left in Hearth", "step", st.name, "why", "companies and contacts stay in Hearth's CRM, which the crm sync projects into EmberOps")
			continue
		}
		sr, err := m.runStep(ctx, b, st, orgID, opts)
		rep.Steps = append(rep.Steps, sr)
		if err != nil {
			return rep, fmt.Errorf("migrate: %s: %w", st.name, err)
		}
		opts.Log.Info("migrated", "step", st.name, "hearth", sr.Source, "mapped", sr.Mapped, "skipped", sr.Skipped,
			"created", sr.Created, "updated", sr.Updated, "failed", sr.Failed)
	}
	return rep, nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if strings.EqualFold(e, s) {
			return true
		}
	}
	return false
}

func (m *Runner) runStep(ctx context.Context, b *builder, st Step, orgID string, opts Options) (StepReport, error) {
	sr := StepReport{Name: st.name, Doctypes: st.doctypes, Remote: -1}
	n, err := m.Src.Count(ctx, st, orgID)
	if err != nil {
		return sr, fmt.Errorf("count: %w", err)
	}
	sr.Source = n

	cursor, done := "", false
	if !opts.DryRun && !opts.Restart {
		if cursor, done, err = m.Src.Progress(ctx, st.name); err != nil {
			return sr, err
		}
		sr.Resumed = cursor != "" || done
	}
	if opts.Restart && !opts.DryRun {
		if err := m.Src.Reset(ctx, st.name); err != nil {
			return sr, err
		}
	}

	if !done {
		for {
			rows, err := m.Src.Page(ctx, st, cursor, opts.BatchSize, orgID)
			if err != nil {
				return sr, fmt.Errorf("read: %w", err)
			}
			var items []emberops.BulkItem
			last, n := "", len(rows)
			for _, row := range rows {
				last = row.Cur
				built, why := st.build(b, row.Rec)
				if len(built) == 0 {
					sr.Skipped++
					note(&sr, fmt.Sprintf("skipped %s: %s", row.Rec.S("id"), why))
					continue
				}
				sr.Mapped += len(built)
				items = append(items, built...)
			}
			if n == 0 {
				break
			}
			if !opts.DryRun {
				if err := m.send(ctx, &sr, items); err != nil {
					return sr, err
				}
				if err := m.Src.Save(ctx, st.name, last, n); err != nil {
					return sr, err
				}
			}
			cursor = last
			if n < opts.BatchSize {
				break
			}
		}
		if !opts.DryRun {
			if err := m.Src.Finish(ctx, st.name, cursor); err != nil {
				return sr, err
			}
		}
	} else {
		// Finished on an earlier run: nothing to resend, and the counts below
		// still verify it.
		sr.Mapped = sr.Source
	}

	if !opts.DryRun {
		sr.Remote = m.remoteCount(ctx, st)
	}
	return sr, nil
}

var idLike = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9]+`)

// refused records a record EmberOps would not take, under its cause.
func refused(sr *StepReport, id, msg string) {
	class := strings.TrimSpace(idLike.ReplaceAllString(msg, "#"))
	if len(class) > 160 {
		class = class[:160]
	}
	if class == "" {
		class = "(no message)"
	}
	if sr.Classes == nil {
		sr.Classes, sr.Sample = map[string]int{}, map[string]string{}
	}
	sr.Classes[class]++
	if _, ok := sr.Sample[class]; !ok {
		sr.Sample[class] = id
	}
}

func note(sr *StepReport, s string) {
	if len(sr.Notes) < 5 {
		sr.Notes = append(sr.Notes, s)
	} else if len(sr.Notes) == 5 {
		sr.Notes = append(sr.Notes, "... and more")
	}
}

// send writes a page. Time entries go through upsert_time_entry, which files
// each into the right timesheet; everything else is one bulk_upsert.
func (m *Runner) send(ctx context.Context, sr *StepReport, items []emberops.BulkItem) error {
	var bulk []emberops.BulkItem
	for _, it := range items {
		if it.Doctype != "Timesheet Detail" {
			bulk = append(bulk, it)
			continue
		}
		var out map[string]any
		if err := m.C.Call(ctx, "emberops_hearth.api.upsert_time_entry", map[string]any{"values": it.Values}, &out); err != nil {
			sr.Failed++
			refused(sr, it.HearthID, err.Error())
			note(sr, fmt.Sprintf("failed %s: %v", it.HearthID, err))
			continue
		}
		sr.Created++ // upsert_time_entry does not say which; counted as written
	}
	if len(bulk) == 0 {
		return nil
	}
	res, err := m.C.BulkUpsert(ctx, bulk)
	if err != nil {
		return err
	}
	for _, d := range res.Done {
		if d.Created {
			sr.Created++
		} else {
			sr.Updated++
		}
	}
	for _, f := range res.Failed {
		sr.Failed++
		refused(sr, f.HearthID, f.Doctype+": "+f.Error)
		note(sr, fmt.Sprintf("failed %s %s: %s", f.Doctype, f.HearthID, f.Error))
	}
	return nil
}

func (m *Runner) remoteCount(ctx context.Context, st Step) int {
	total := 0
	for _, dt := range st.doctypes {
		n, err := m.C.Count(ctx, dt, []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}})
		if err != nil {
			return -1
		}
		total += n
	}
	return total
}
