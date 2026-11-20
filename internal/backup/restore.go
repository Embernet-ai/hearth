package backup

// Reading an archive back: checking one, and putting one back.
//
// A backup that has never been restored is not a backup. Both halves of that
// live here, and the drill in cmd/hearthctl runs them in order and times them,
// so the recovery window is a number somebody has measured rather than a hope.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/blob"
)

// ErrIncomplete means the archive ended before it said what was in it.
var ErrIncomplete = errors.New("backup: this archive has no manifest, so it is truncated")

// Verify reads an archive from end to end and checks it against its own
// manifest.
//
// Every member is hashed as it goes past and compared with what the manifest
// says, so this catches a truncated upload, a corrupted disk, and a file
// somebody edited. It reads the whole thing, which is the point: a verification
// that only reads the manifest verifies the manifest.
func Verify(ctx context.Context, r io.Reader) (*Result, error) {
	started := time.Now()
	seen := map[string]string{}
	sizes := map[string]int64{}
	var manifest *Manifest

	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: read archive: %w", err)
		}
		if header.Name == manifestName {
			body, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("backup: read manifest: %w", err)
			}
			var m Manifest
			if err := json.Unmarshal(body, &m); err != nil {
				return nil, fmt.Errorf("backup: parse manifest: %w", err)
			}
			manifest = &m
			continue
		}
		sum := sha256.New()
		n, err := io.Copy(sum, tr)
		if err != nil {
			return nil, fmt.Errorf("backup: read %s: %w", header.Name, err)
		}
		seen[header.Name] = hex.EncodeToString(sum.Sum(nil))
		sizes[header.Name] = n
	}
	if manifest == nil {
		return nil, ErrIncomplete
	}
	if manifest.Format != Format {
		return nil, fmt.Errorf(
			"backup: this archive is format %d and this build reads format %d",
			manifest.Format, Format)
	}

	res := &Result{Manifest: *manifest}
	var problems []string
	for i, t := range manifest.Tables {
		name := fmt.Sprintf("%s%03d-%s.csv", tablePrefix, i, t.Name)
		got, ok := seen[name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s is missing from the archive", t.Name))
		case got != t.SHA256:
			problems = append(problems, fmt.Sprintf(
				"%s does not match its digest: %s in the archive, %s in the manifest",
				t.Name, got[:12], t.SHA256[:12]))
		case sizes[name] != t.Bytes:
			problems = append(problems, fmt.Sprintf(
				"%s is %d bytes and the manifest says %d", t.Name, sizes[name], t.Bytes))
		}
		res.Rows += t.Rows
		res.Bytes += t.Bytes
		delete(seen, name)
	}
	for _, o := range manifest.Objects {
		name := objectPrefix + o.Key
		got, ok := seen[name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("the file %s is missing from the archive", o.Key))
		case got != o.SHA256:
			problems = append(problems, fmt.Sprintf(
				"the file %s does not match its digest", o.Key))
		}
		res.Objects++
		res.Bytes += o.Bytes
		delete(seen, name)
	}
	// Anything left over is a member the manifest does not mention, which means
	// the two disagree about what this archive is.
	for name := range seen {
		problems = append(problems, name+" is in the archive and not in the manifest")
	}
	sort.Strings(problems)

	res.Took = time.Since(started)
	if len(problems) > 0 {
		return res, fmt.Errorf("backup: this archive does not verify:\n  %s",
			strings.Join(problems, "\n  "))
	}
	return res, nil
}

// Restore puts an archive back into an empty deployment.
//
// Empty is checked rather than assumed. Restoring over live data is a different
// operation with a different blast radius, and one flag away from it is too
// close: the way to restore over a running site is to empty it first, which is
// a thing somebody has to do on purpose.
//
// The schema has to already be there and at the same migration the backup was
// taken at. A restore is data, not structure, and a database at a different
// version would take some tables and quietly fail others.
func Restore(ctx context.Context, db *pgxpool.Pool, store *blob.Store, r io.Reader) (*Result, error) {
	started := time.Now()

	// The archive is read twice: once to find the manifest, which is at the
	// end, and once to put the data back. Reading it into memory would mean
	// holding a whole deployment in a pod, so the caller passes something
	// seekable in practice and this asks for it explicitly.
	seeker, ok := r.(io.ReadSeeker)
	if !ok {
		return nil, errors.New(
			"backup: restoring reads the archive twice, so it has to come from a " +
				"file rather than from a stream")
	}

	manifest, err := readManifest(ctx, seeker)
	if err != nil {
		return nil, err
	}
	if manifest.Format != Format {
		return nil, fmt.Errorf(
			"backup: this archive is format %d and this build reads format %d",
			manifest.Format, Format)
	}

	var version int64
	if err := db.QueryRow(ctx,
		`SELECT COALESCE(max(version_id), 0) FROM goose_db_version`).Scan(&version); err != nil {
		return nil, fmt.Errorf("backup: read migration version: %w", err)
	}
	if version != manifest.Migration {
		return nil, fmt.Errorf(
			"backup: this archive was taken at migration %d and this database is at "+
				"%d. Restore into a database migrated to the same version, or the "+
				"restore will take some tables and silently skip others",
			manifest.Migration, version)
	}

	if err := requireEmpty(ctx, db, manifest); err != nil {
		return nil, err
	}

	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("backup: rewind archive: %w", err)
	}

	conn, err := db.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: begin restore: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Constraints are deferred for the duration, so the archive's own ordering
	// is a convenience rather than a requirement and a self-referencing row
	// such as a space inside a space cannot fail on the way in.
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
		return nil, fmt.Errorf("backup: defer constraints: %w", err)
	}

	res := &Result{Manifest: *manifest}
	index := map[string]int{}
	for i, t := range manifest.Tables {
		index[fmt.Sprintf("%s%03d-%s.csv", tablePrefix, i, t.Name)] = i
	}

	// Two passes over the archive: Postgres first, inside a transaction, and
	// the objects afterwards once it has committed.
	//
	// Not one pass with the files held in memory, which is a whole deployment's
	// attachments in a pod. And not objects first, because an object put back
	// for a row that never lands is litter in a bucket, while a row that lands
	// with no object behind it is a dead attachment. Only one of those two is
	// survivable, so the order is the one that produces it.
	tr := tar.NewReader(seeker)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: read archive: %w", err)
		}
		switch {
		case strings.HasPrefix(header.Name, tablePrefix):
			i, ok := index[header.Name]
			if !ok {
				return nil, fmt.Errorf("backup: %s is not in the manifest", header.Name)
			}
			t := manifest.Tables[i]
			if err := copyIn(ctx, tx, t, tr); err != nil {
				return nil, err
			}
			res.Rows += t.Rows
		case header.Name == manifestName, strings.HasPrefix(header.Name, objectPrefix):
			// The second pass deals with these.
		default:
			return nil, fmt.Errorf("backup: %s is not something this reads", header.Name)
		}
	}

	// The counters, after the rows. Without this the first insert after a
	// restore collides with a row that came out of the backup, and it reads as
	// a corrupt database rather than as an unset number.
	for _, t := range manifest.Tables {
		for _, seq := range t.Sequences {
			if _, err := tx.Exec(ctx, fmt.Sprintf(
				`SELECT setval($1, COALESCE((SELECT max(%s) FROM %s), 0) + 1, false)`,
				quote(seq.Column), quote(t.Name)), seq.Name); err != nil {
				return nil, fmt.Errorf("backup: move sequence %s: %w", seq.Name, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("backup: commit restore: %w", err)
	}

	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("backup: rewind archive: %w", err)
	}
	sizes := map[string]int64{}
	for _, o := range manifest.Objects {
		sizes[o.Key] = o.Bytes
	}
	tr = tar.NewReader(seeker)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: read archive: %w", err)
		}
		if !strings.HasPrefix(header.Name, objectPrefix) {
			continue
		}
		key := strings.TrimPrefix(header.Name, objectPrefix)
		size, ok := sizes[key]
		if !ok {
			return nil, fmt.Errorf("backup: the file %s is not in the manifest", key)
		}
		// Straight from the archive into the store, so nothing larger than a
		// buffer is ever held. The content type is not in the archive because
		// it is on the files row that has just been restored, and the store is
		// not where this application reads it from.
		if _, err := store.Put(ctx, key, tr, size, "application/octet-stream"); err != nil {
			return nil, fmt.Errorf("backup: put object %s: %w", key, err)
		}
		res.Objects++
		res.Bytes += size
	}

	res.Took = time.Since(started)
	return res, nil
}

func copyIn(ctx context.Context, tx pgx.Tx, t TableEntry, r io.Reader) error {
	// The audit log refuses ordinary writes to protect history. Putting history
	// back is not rewriting it, and this is an INSERT rather than an UPDATE or
	// a DELETE, so the triggers do not apply at all: they guard the two
	// operations that would change what a row says.
	sql := fmt.Sprintf(`COPY %s (%s) FROM STDIN WITH (FORMAT csv)`,
		quote(t.Name), columnList(t.Columns))
	tag, err := tx.Conn().PgConn().CopyFrom(ctx, r, sql)
	if err != nil {
		return fmt.Errorf("backup: restore %s: %w", t.Name, err)
	}
	if tag.RowsAffected() != t.Rows {
		return fmt.Errorf("backup: restored %d rows into %s and the manifest says %d",
			tag.RowsAffected(), t.Name, t.Rows)
	}
	return nil
}

// requireEmpty refuses to restore over anything.
func requireEmpty(ctx context.Context, db *pgxpool.Pool, m *Manifest) error {
	var occupied []string
	for _, t := range m.Tables {
		var n int64
		if err := db.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*) FROM %s`, quote(t.Name))).Scan(&n); err != nil {
			return fmt.Errorf("backup: count %s: %w", t.Name, err)
		}
		if n > 0 {
			occupied = append(occupied, fmt.Sprintf("%s (%d)", t.Name, n))
		}
	}
	if len(occupied) > 0 {
		sort.Strings(occupied)
		return fmt.Errorf(
			"backup: this database is not empty, so restoring into it would mix two "+
				"deployments together: %s", strings.Join(occupied, ", "))
	}
	return nil
}

// readManifest finds the manifest, which is at the end of the archive.
func readManifest(ctx context.Context, r io.ReadSeeker) (*Manifest, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("backup: rewind archive: %w", err)
	}
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrIncomplete
		}
		if err != nil {
			return nil, fmt.Errorf("backup: read archive: %w", err)
		}
		if header.Name != manifestName {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("backup: read manifest: %w", err)
		}
		var m Manifest
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("backup: parse manifest: %w", err)
		}
		return &m, nil
	}
}

// Compare reports whether a live deployment matches an archive.
//
// This is what turns "the restore finished" into "the restore is right". Row
// counts per table and the digest of every object, read back out of the store
// rather than trusted from the manifest.
func Compare(ctx context.Context, db *pgxpool.Pool, store *blob.Store, m *Manifest) ([]string, error) {
	var problems []string
	for _, t := range m.Tables {
		var n int64
		if err := db.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*) FROM %s`, quote(t.Name))).Scan(&n); err != nil {
			return nil, fmt.Errorf("backup: count %s: %w", t.Name, err)
		}
		if n != t.Rows {
			problems = append(problems, fmt.Sprintf(
				"%s has %d rows and the archive has %d", t.Name, n, t.Rows))
		}
	}
	for _, o := range m.Objects {
		rc, err := store.Get(ctx, o.Key)
		if err != nil {
			problems = append(problems, fmt.Sprintf("the file %s is not in the store", o.Key))
			continue
		}
		sum := sha256.New()
		_, err = io.Copy(sum, rc)
		rc.Close()
		if err != nil {
			problems = append(problems, fmt.Sprintf("the file %s could not be read back", o.Key))
			continue
		}
		if got := hex.EncodeToString(sum.Sum(nil)); got != o.SHA256 {
			problems = append(problems, fmt.Sprintf(
				"the file %s is not the file that was backed up", o.Key))
		}
	}
	sort.Strings(problems)
	return problems, nil
}
