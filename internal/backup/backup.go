// Package backup takes Postgres and the object store away together, and puts
// them back together.
//
// # Why both, in one archive
//
// Because restoring one without the other gives a database full of rows that
// point at files nobody has, or a bucket full of files nothing references.
// Neither of those is a restore, and both of them look like one until somebody
// clicks an attachment. Two separate backup jobs on two separate schedules are
// two things that can be out of step, and they will be, because the schedules
// drift and one of them fails quietly on a Tuesday.
//
// One archive, one moment, one manifest that says what is inside it.
//
// # What is in it
//
//	manifest.json          what this is, when, from which build and migration
//	postgres/<n>-<table>   every row of every table, CSV, in dependency order
//	objects/<key>          every object in the bucket, byte for byte
//
// A tar file, because an operator has to be able to look inside a backup with
// tools they already have. A backup nobody can inspect is a backup nobody
// trusts, and an unverifiable format is how people end up with three years of
// archives and no idea whether any of them work.
//
// # A backup that has never been restored is not a backup
//
// So this package can do three things, not one: write an archive, verify one
// against the deployment it came from, and restore one into an empty
// deployment. The drill is timed, because the number that matters on the day is
// how long the restore takes, and "we have backups" is not that number.
package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/blob"
)

// Format is the archive layout version.
//
// A number in the manifest, checked on restore. It exists so that a future
// change to the layout produces "this archive is version 2 and this build reads
// version 1" rather than a restore that half works.
const Format = 1

const (
	manifestName = "manifest.json"
	tablePrefix  = "postgres/"
	objectPrefix = "objects/"
)

// Manifest is what the archive says about itself.
type Manifest struct {
	Format int `json:"format"`
	// TakenAt is when the archive was written, in UTC.
	TakenAt time.Time `json:"taken_at"`
	// Revision is the build that wrote it, so a restore can say what version of
	// the application produced the rows it is putting back.
	Revision string `json:"revision"`
	// Migration is the schema version. A restore into a database at a different
	// version is refused: it is the difference between a restore and a mess.
	Migration int64  `json:"migration"`
	Database  string `json:"database"`
	Bucket    string `json:"bucket"`

	Tables  []TableEntry  `json:"tables"`
	Objects []ObjectEntry `json:"objects"`

	// Totals, so a person reading the manifest can see the shape of the thing
	// without adding up two hundred lines.
	TotalRows  int64 `json:"total_rows"`
	TotalBytes int64 `json:"total_bytes"`
}

// TableEntry is one table in the archive.
type TableEntry struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
	Bytes   int64    `json:"bytes"`
	// SHA256 of the CSV as written. Verifying reads it back and recomputes,
	// which is the only way to know an archive on a disk is still the archive
	// that was written.
	SHA256    string     `json:"sha256"`
	Sequences []Sequence `json:"sequences,omitempty"`
}

// ObjectEntry is one stored file.
type ObjectEntry struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Result is what a run of any of the three operations reports.
type Result struct {
	Manifest Manifest
	Rows     int64
	Objects  int64
	Bytes    int64
	Took     time.Duration
	// Notes are things worth saying out loud that are not failures.
	Notes []string
}

// Write streams a whole backup into w.
//
// One repeatable read transaction for the whole of Postgres, so every table is
// read at the same instant. Without it, a document written between the
// documents table and the task_fields table produces an archive that will not
// restore because of a foreign key that was satisfied at neither moment.
//
// The objects are read after the snapshot, which means the bucket can gain a
// file the database does not mention yet. That direction is harmless: an
// unreferenced object is invisible. The other direction, a row pointing at a
// file that was not backed up, is the one that matters, and it cannot happen
// because every object that existed when the snapshot was taken is still there
// when the listing runs.
func Write(ctx context.Context, db *pgxpool.Pool, store *blob.Store, revision string, w io.Writer) (*Result, error) {
	started := time.Now()

	tables, err := Discover(ctx, db)
	if err != nil {
		return nil, err
	}

	tw := tar.NewWriter(w)
	m := Manifest{
		Format: Format, TakenAt: started.UTC(), Revision: revision,
		Bucket: store.Bucket(),
	}
	if err := db.QueryRow(ctx, `SELECT current_database()`).Scan(&m.Database); err != nil {
		return nil, fmt.Errorf("backup: read database name: %w", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(max(version_id), 0) FROM goose_db_version`).Scan(&m.Migration); err != nil {
		return nil, fmt.Errorf("backup: read migration version: %w", err)
	}

	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: acquire connection: %w", err)
	}
	defer conn.Release()

	// REPEATABLE READ rather than SERIALIZABLE: this only reads, so there is
	// nothing to serialise against, and repeatable read is exactly the promise
	// needed, which is that every statement sees the same instant.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, table := range tables {
		entry, err := writeTable(ctx, tx, tw, i, table)
		if err != nil {
			return nil, err
		}
		m.Tables = append(m.Tables, entry)
		m.TotalRows += entry.Rows
		m.TotalBytes += entry.Bytes
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, context.Canceled) {
		// A read-only snapshot has nothing to commit, and rolling it back is
		// how it ends. Reported rather than ignored, because a rollback that
		// fails means the connection is in a state worth knowing about.
		if !strings.Contains(err.Error(), "closed") {
			return nil, fmt.Errorf("backup: end snapshot: %w", err)
		}
	}

	objects, err := writeObjects(ctx, store, tw)
	if err != nil {
		return nil, err
	}
	m.Objects = objects
	for _, o := range objects {
		m.TotalBytes += o.Bytes
	}

	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: encode manifest: %w", err)
	}
	// Last, because it describes everything before it and cannot be written
	// until they are all counted. A reader has to reach the end of the archive
	// to verify it, which is the honest requirement anyway: a truncated backup
	// with a manifest at the front looks complete.
	if err := writeFile(tw, manifestName, int64(len(body)), strings.NewReader(string(body))); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("backup: close archive: %w", err)
	}

	return &Result{
		Manifest: m, Rows: m.TotalRows, Objects: int64(len(m.Objects)),
		Bytes: m.TotalBytes, Took: time.Since(started),
	}, nil
}

// writeTable copies one table out as CSV.
//
// Through a temporary file rather than a buffer in memory, because tar needs
// the length of a member before its bytes and a documents table on a real site
// is not something to hold in a pod's memory to find out how long it is. The
// file is removed the moment it has been copied in.
func writeTable(ctx context.Context, tx pgx.Tx, tw *tar.Writer, index int, table Table) (TableEntry, error) {
	entry := TableEntry{
		Name: table.Name, Columns: table.Columns, Sequences: table.Sequences,
	}

	tmp, err := os.CreateTemp("", "hearth-backup-*.csv")
	if err != nil {
		return entry, fmt.Errorf("backup: temporary file: %w", err)
	}
	defer func() {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
	}()

	sum := sha256.New()
	// COPY straight out of the server, which is orders of magnitude faster than
	// selecting rows and formatting them here, and produces the exact escaping
	// COPY FROM expects on the way back in.
	tag, err := tx.Conn().PgConn().CopyTo(ctx, io.MultiWriter(tmp, sum),
		fmt.Sprintf(`COPY (SELECT %s FROM %s) TO STDOUT WITH (FORMAT csv)`,
			columnList(table.Columns), quote(table.Name)))
	if err != nil {
		return entry, fmt.Errorf("backup: copy %s: %w", table.Name, err)
	}
	entry.Rows = tag.RowsAffected()
	entry.SHA256 = hex.EncodeToString(sum.Sum(nil))

	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return entry, fmt.Errorf("backup: size %s: %w", table.Name, err)
	}
	entry.Bytes = size
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return entry, fmt.Errorf("backup: rewind %s: %w", table.Name, err)
	}
	// The index keeps the members in dependency order inside the archive, so a
	// restore can stream through it in one pass and an operator listing it can
	// see the order at a glance.
	name := fmt.Sprintf("%s%03d-%s.csv", tablePrefix, index, table.Name)
	if err := writeFile(tw, name, size, tmp); err != nil {
		return entry, err
	}
	return entry, nil
}

func writeObjects(ctx context.Context, store *blob.Store, tw *tar.Writer) ([]ObjectEntry, error) {
	var keys []blob.Object
	if err := store.List(ctx, func(o blob.Object) error {
		keys = append(keys, o)
		return nil
	}); err != nil {
		return nil, err
	}
	// Sorted, so two archives of the same bucket are the same archive.
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })

	out := make([]ObjectEntry, 0, len(keys))
	for _, o := range keys {
		rc, err := store.Get(ctx, o.Key)
		if err != nil {
			return nil, fmt.Errorf("backup: read object %s: %w", o.Key, err)
		}
		sum := sha256.New()
		// Through the hash on the way past, so the digest is of the bytes that
		// actually went into the archive rather than of a second read that
		// could have found something different.
		err = writeFile(tw, objectPrefix+o.Key, o.Size, io.TeeReader(rc, sum))
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, ObjectEntry{
			Key: o.Key, Bytes: o.Size, SHA256: hex.EncodeToString(sum.Sum(nil)),
		})
	}
	return out, nil
}

func writeFile(tw *tar.Writer, name string, size int64, r io.Reader) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: size, Format: tar.FormatPAX,
		ModTime: time.Unix(0, 0).UTC(),
	}); err != nil {
		return fmt.Errorf("backup: header for %s: %w", name, err)
	}
	written, err := io.Copy(tw, r)
	if err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	if written != size {
		// tar would refuse this itself on the next header, with a message about
		// the archive rather than about the thing that changed underneath.
		return fmt.Errorf("backup: %s changed while it was being read: "+
			"%d bytes where %d were expected", name, written, size)
	}
	return nil
}

func columnList(columns []string) string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = quote(c)
	}
	return strings.Join(out, ", ")
}
