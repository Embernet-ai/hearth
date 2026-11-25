// Command hearthctl is the operator's side of Hearth: back it up, check a
// backup, put one back, and rehearse the whole thing against a spare database.
//
// A separate binary from the server on purpose. The server's job is to serve,
// and a restore that runs inside it is a restore that competes with live
// traffic for the pool it is emptying. This one runs as a CronJob, as a Job, or
// from a laptop, and it takes its configuration from the same environment
// variables the server does so there is no second thing to keep in step.
//
//	hearthctl backup   -out /backups/hearth-2026-08-13.tar
//	hearthctl verify   -in  /backups/hearth-2026-08-13.tar
//	hearthctl restore  -in  /backups/hearth-2026-08-13.tar
//	hearthctl drill    -to  postgres://... -bucket hearth-drill
//
// The drill is the one that matters. A backup that has never been restored is
// not a backup, and the number that decides whether a bad morning is survivable
// is how long the restore takes, which this prints.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Embernet-ai/hearth/internal/backup"
	"github.com/Embernet-ai/hearth/internal/platform/blob"
	"github.com/Embernet-ai/hearth/internal/platform/buildinfo"
	"github.com/Embernet-ai/hearth/internal/platform/config"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "backup":
		err = runBackup(ctx, os.Args[2:])
	case "verify":
		err = runVerify(ctx, os.Args[2:])
	case "restore":
		err = runRestore(ctx, os.Args[2:])
	case "nightly":
		err = runNightly(ctx, os.Args[2:])
	case "drill":
		err = runDrill(ctx, os.Args[2:])
	case "import-clickup":
		err = runImportClickUp(ctx, os.Args[2:])
	case "verify-clickup":
		err = runVerifyClickUp(ctx, os.Args[2:])
	case "remap-org":
		err = runRemapOrg(ctx, os.Args[2:])
	case "sync-teams":
		err = runSyncTeams(ctx, os.Args[2:])
	case "service-history":
		err = runServiceHistory(ctx, os.Args[2:])
	case "reassign":
		err = runReassign(ctx, os.Args[2:])
	case "clickup-gap":
		err = runClickUpGap(ctx, os.Args[2:])
	case "crm-sync":
		err = runCRMSync(ctx, os.Args[2:])
	case "mautic-sync":
		err = runMauticSync(ctx, os.Args[2:])
	case "crm-delete":
		err = runCRMDelete(ctx, os.Args[2:])
	case "doc-delete":
		err = runDocDelete(ctx, os.Args[2:])
	case "space-remove":
		err = runSpaceRemove(ctx, os.Args[2:])
	case "space-create":
		err = runSpaceCreate(ctx, os.Args[2:])
	case "space-move":
		err = runSpaceMove(ctx, os.Args[2:])
	case "hubspot-import":
		err = runHubSpotImport(ctx, os.Args[2:])
	case "zoho-contacts-import":
		err = runZohoContactsImport(ctx, os.Args[2:])
	case "daily-report":
		err = runDailyReport(ctx, os.Args[2:])
	case "time-import":
		err = runTimeImport(ctx, os.Args[2:])
	case "time-remove":
		err = runTimeRemove(ctx, os.Args[2:])
	case "time-approve":
		err = runTimeApprove(ctx, os.Args[2:])
	case "service-token":
		err = runServiceToken(ctx, os.Args[2:])
	case "person-time":
		err = runPersonTime(ctx, os.Args[2:])
	case "person-suspend":
		err = runPersonSuspend(ctx, os.Args[2:])
	case "channel-remove":
		err = runChannelRemove(ctx, os.Args[2:])
	case "groups-sync":
		err = runGroupsSync(ctx, os.Args[2:])
	case "blob-migrate":
		err = runBlobMigrate(ctx, os.Args[2:])
	case "emberops-migrate":
		err = runEmberOPSMigrate(ctx, os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "hearthctl: %q is not a command\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hearthctl: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hearthctl takes Hearth away and puts it back.

  backup  -out FILE          write Postgres and the object store into one archive
  nightly -dir DIR [-keep N] the scheduled version: stamp, verify, and prune
  verify  -in FILE           read an archive back and check it against its manifest
  restore -in FILE           put an archive into an empty, migrated deployment
  drill   -to DSN -bucket B  back up, restore into a spare, compare, and time it
  service-history -under NAME -out DIR  one PDF per customer and one CSV, read only
  clickup-gap snapshot|apply  read ClickUp and add what Hearth is missing, additively
  crm-sync [-write]           send the CRM to EmberOps as Customers, Suppliers and Contacts (dry run by default)
  mautic-sync [-write]        send contacts to Mautic and bring its activity and opt-outs back (dry run by default)
  doc-delete -source S -ids A,B [-go]  remove documents that carry a reference from S (checks first, nothing deleted without -go)
  space-create -as ADMIN -name N [-kind project] [-parent ID] [-agent HANDLE] [-team NAME]  make a space (or find it) and share it with a service and a team
  space-move -as ADMIN -space ID -parent ID [-agent HANDLE] [-team NAME]  move a space inside another and share it with a service and a team
  space-remove -space ID [-go]         cancel the open tasks in a space, take every item out of it, remove the space (nothing changes without -go)
  zoho-contacts-import -file X.xlsx -url U -owner ID [-dry-run]  add a Zoho Contacts export to the CRM through the API
  hubspot-import [-apply] [-since T]  bring what changed in HubSpot since the last import into the CRM (dry run by default)
  reassign -csv PLAN [-apply -reverse-out FILE]  put work on its assignees from a plan
  daily-report -as EMAIL (-date D [-file F] | -dir DIR) [-apply] [-append]  file a past day's report as that person, or add to one already filed (dry run by default)
  time-import -as EMAIL -file F.csv [-apply]  put a person's past time in as reconstructed entries, once (dry run by default)
  service-token -as ADMIN -name NAME [-team Sales] [-scope write|read] [-days N] -out FILE  make a service its own agent principal on a team with an API token (write by default, -scope read for read only)
  person-time -as ADMIN -person EMAIL [-approver EMAIL|none] [-auto-approve on|off]  set who signs off a person's time, and whether it approves itself
  time-remove -as EMAIL -file IDS -reason WHY [-apply]  remove time entries as a super admin, audited, dry run by default
  time-approve -person EMAIL -reason WHY [-apply] [-limit N]  approve a salaried person's waiting time by their auto-approve, audited, dry run by default
  person-suspend -as ADMIN -email A,B -reason WHY [-apply]  suspend accounts (off their teams, sessions ended), audited, dry run by default
  channel-remove -as ADMIN -channel ID -reason WHY [-apply]  archive a channel so it is in no list, audited, dry run by default
  groups-sync -config FILE [-apply]  make Google Workspace group members follow Hearth teams and departments (dry run by default)
  blob-migrate [-apply]  copy every object from the store in HEARTH_SRC_* into the configured one and prove it (dry run by default, see docs/storage/migrating.md)
  emberops-migrate [--dry-run]  copy Postgres data into EmberOps (see docs/emberops-backend.md)

Configuration comes from the same environment the server reads:
HEARTH_DATABASE_URL and the object store settings (HEARTH_STORAGE_BACKEND,
HEARTH_STORAGE_BUCKET, and HEARTH_S3_*, HEARTH_AZURE_* or HEARTH_GCS_* for the
backend in use; see docs/storage/).
`)
}

// open connects to whatever the environment points at.
func open(ctx context.Context, dsn, bucket string) (*pg.Pools, *blob.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if dsn == "" {
		dsn = cfg.DatabaseURL
	}
	if bucket == "" {
		bucket = cfg.S3Bucket
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		return nil, nil, fmt.Errorf("connect to postgres: %w", err)
	}
	bc := cfg.Blob()
	bc.Bucket = bucket
	store, err := blob.Open(ctx, bc)
	if err != nil {
		pools.Close()
		return nil, nil, fmt.Errorf("connect to the object store: %w", err)
	}
	return pools, store, nil
}

func runBackup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	out := fs.String("out", "", "where to write the archive, or - for standard output")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("backup needs -out")
	}

	pools, store, err := open(ctx, "", "")
	if err != nil {
		return err
	}
	defer pools.Close()

	w := os.Stdout
	if *out != "-" {
		// Written to a temporary name and renamed at the end, so a crash
		// halfway through leaves no file that looks like a backup. A partial
		// archive in the backup directory is worse than a missing one: the
		// missing one gets noticed.
		tmp, err := os.CreateTemp(dirOf(*out), ".hearth-backup-*")
		if err != nil {
			return fmt.Errorf("create %s: %w", *out, err)
		}
		defer func() { _ = os.Remove(tmp.Name()) }()
		w = tmp
	}

	res, err := backup.Write(ctx, pools.Web, store, buildinfo.Get().Revision, w)
	if err != nil {
		return err
	}
	if *out != "-" {
		if err := w.Sync(); err != nil {
			return fmt.Errorf("flush the archive: %w", err)
		}
		name := w.Name()
		if err := w.Close(); err != nil {
			return fmt.Errorf("close the archive: %w", err)
		}
		if err := os.Rename(name, *out); err != nil {
			return fmt.Errorf("put the archive in place: %w", err)
		}
	}

	report("backed up", res)
	return nil
}

func runVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	in := fs.String("in", "", "the archive to check")
	_ = fs.Parse(args)
	if *in == "" {
		return errors.New("verify needs -in")
	}

	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()

	res, err := backup.Verify(ctx, f)
	if err != nil {
		return err
	}
	report("verified", res)
	fmt.Printf("taken %s from revision %s at migration %d\n",
		res.Manifest.TakenAt.Format(time.RFC3339),
		short(res.Manifest.Revision), res.Manifest.Migration)
	return nil
}

func runRestore(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	in := fs.String("in", "", "the archive to restore")
	dsn := fs.String("to", "", "the database to restore into, defaulting to the configured one")
	bucket := fs.String("bucket", "", "the bucket to restore into, defaulting to the configured one")
	_ = fs.Parse(args)
	if *in == "" {
		return errors.New("restore needs -in")
	}

	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()

	pools, store, err := open(ctx, *dsn, *bucket)
	if err != nil {
		return err
	}
	defer pools.Close()

	res, err := backup.Restore(ctx, pools.Web, store, f)
	if err != nil {
		return err
	}
	report("restored", res)

	problems, err := backup.Compare(ctx, pools.Web, store, &res.Manifest)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		return fmt.Errorf("the restore finished and does not match the archive")
	}
	fmt.Println("every table and every file matches the archive")
	return nil
}

// runDrill is the whole point of the package.
//
// Back up the live deployment, restore it into a spare database and a spare
// bucket, compare the two, and print how long each part took. Nothing about
// the live deployment is touched: it is read, and the restore goes somewhere
// else entirely.
//
// It is a command rather than a document because a documented restore drill is
// a drill nobody runs, and the recovery window has to be a number somebody
// measured this quarter.
func runDrill(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("drill", flag.ExitOnError)
	dsn := fs.String("to", "", "a spare, empty, migrated database to restore into")
	bucket := fs.String("bucket", "", "a spare, empty bucket to restore into")
	keep := fs.String("keep", "", "write the archive here instead of a temporary file")
	_ = fs.Parse(args)
	if *dsn == "" || *bucket == "" {
		return errors.New("a drill needs -to and -bucket, pointing at a spare " +
			"database and a spare bucket. Pointing them at the live ones would " +
			"be a restore, not a rehearsal")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	whole := time.Now()

	live, liveStore, err := open(ctx, "", "")
	if err != nil {
		return err
	}
	defer live.Close()

	name := *keep
	if name == "" {
		tmp, err := os.CreateTemp("", "hearth-drill-*.tar")
		if err != nil {
			return err
		}
		name = tmp.Name()
		_ = tmp.Close()
		defer func() { _ = os.Remove(name) }()
	}

	f, err := os.Create(name)
	if err != nil {
		return err
	}
	taken, err := backup.Write(ctx, live.Web, liveStore, buildinfo.Get().Revision, f)
	if err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	report("backed up", taken)

	f, err = os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()

	checked, err := backup.Verify(ctx, f)
	if err != nil {
		return err
	}
	report("verified", checked)

	spare, spareStore, err := open(ctx, *dsn, *bucket)
	if err != nil {
		return err
	}
	defer spare.Close()

	// The spare has to have the schema on it. Migrating here rather than
	// insisting somebody did it first, because an empty database is exactly
	// what a real recovery starts from and the drill has to rehearse that.
	migrating := time.Now()
	if err := pg.Migrate(ctx, spare, log); err != nil {
		return fmt.Errorf("migrate the spare: %w", err)
	}
	fmt.Printf("migrated the spare in %s\n", round(time.Since(migrating)))

	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	put, err := backup.Restore(ctx, spare.Web, spareStore, f)
	if err != nil {
		return err
	}
	report("restored", put)

	problems, err := backup.Compare(ctx, spare.Web, spareStore, &put.Manifest)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		return errors.New("the restored copy does not match the archive")
	}

	fmt.Printf("\nthe drill passed in %s\n", round(time.Since(whole)))
	fmt.Printf("recovery window: %s to restore %d rows and %d files\n",
		round(put.Took), put.Rows, put.Objects)
	fmt.Println("\nRun the acceptance suite against a server pointed at the spare " +
		"to finish the drill. A restore that loads is not the same as a restore " +
		"that works.")
	return nil
}

func report(what string, res *backup.Result) {
	fmt.Printf("%s %d rows across %d tables and %d files, %s, in %s\n",
		what, res.Rows, len(res.Manifest.Tables), res.Objects,
		humanBytes(res.Bytes), round(res.Took))
	for _, note := range res.Notes {
		fmt.Println("  " + note)
	}
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	}
}

// round trims a duration to something a person reads as a number rather than
// as a measurement. A restore that took 4.203918ms is a restore that took 4ms.
func round(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond)
	case d < time.Minute:
		return d.Round(10 * time.Millisecond)
	default:
		return d.Round(time.Second)
	}
}

func short(v string) string {
	if len(v) > 8 {
		return v[:8]
	}
	return v
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
