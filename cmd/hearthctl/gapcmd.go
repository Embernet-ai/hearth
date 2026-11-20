package main

// Filling what the first ClickUp import left out.
//
//	hearthctl clickup-gap snapshot                 read ClickUp, save the read, print the comparison
//	hearthctl clickup-gap apply -dry-run           compare a saved read with Hearth, write nothing
//	hearthctl clickup-gap apply                    write what is missing
//
// snapshot only reads. It saves everything it read to the object store as one
// compressed file, so the comparison, the dry run and the apply all work from
// the same moment and ClickUp is asked once. The token comes from
// CLICKUP_TOKEN, which a Job fills from a secret; nothing here prints it.
//
// Rules for apply are in internal/importer/clickup/gap.go.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/files"
	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/blob"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// The last two runs of the first import finished at 14:42 UTC on 2026-08-27.
// Anything changed in ClickUp after that is new to Hearth, and anything edited
// in Hearth after the verify and team-sync jobs (15:08) was edited by a person.
var (
	gapCutoff      = time.Date(2026, 8, 27, 14, 45, 0, 0, time.UTC)
	gapEditedAfter = time.Date(2026, 8, 27, 15, 15, 0, 0, time.UTC)
)

func runClickUpGap(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("clickup-gap needs snapshot or apply")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("clickup-gap "+sub, flag.ExitOnError)
	token := fs.String("token", os.Getenv("CLICKUP_TOKEN"), "ClickUp token, or CLICKUP_TOKEN")
	key := fs.String("snapshot-key", "", "object store key of a saved read (apply), or where to save one (snapshot)")
	dry := fs.Bool("dry-run", false, "apply: compare and report, write nothing")
	fallbackEmail := fs.String("fallback", "yara@example.com", "who gets work and time with no active person to go to")
	archived := fs.Bool("archived", true, "read archived lists too")
	skipUpdates := fs.Bool("skip-updates", false, "apply: do not bring changed tasks up to date")
	skipComments := fs.Bool("skip-comments", false, "apply: skip comments")
	skipFiles := fs.Bool("skip-attachments", false, "apply: skip attachments")
	skipTime := fs.Bool("skip-time", false, "apply: skip time entries")
	reportOut := fs.String("report", "", "write the comparison (markdown) to this file as well as printing it")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	logf := func(format string, a ...any) {
		fmt.Printf("%s  %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, a...))
	}

	pools, store, err := open(ctx, "", "")
	if err != nil {
		return err
	}
	defer pools.Close()

	switch sub {
	case "snapshot":
		if strings.TrimSpace(*token) == "" {
			return errors.New("a ClickUp token is required: CLICKUP_TOKEN")
		}
		c := clickup.NewClient(*token)
		c.Archived = *archived
		c.MinInterval = 700 * time.Millisecond
		snap, err := clickup.TakeSnapshot(ctx, c, logf)
		if err != nil {
			return err
		}
		k := *key
		if k == "" {
			k = "imports/clickup-snapshot-" + snap.TakenAt.Format("20060102-150405") + ".json.gz"
		}
		if err := putSnapshot(ctx, store, k, snap); err != nil {
			return err
		}
		logf("snapshot saved as %s", k)
		fmt.Printf("SNAPSHOT_KEY=%s\n", k)
		return compareAndPrint(ctx, pools.Web, snap, *reportOut)

	case "apply":
		if *key == "" {
			return errors.New("apply needs -snapshot-key")
		}
		snap, err := getSnapshot(ctx, store, *key)
		if err != nil {
			return err
		}
		if *dry {
			return compareAndPrint(ctx, pools.Web, snap, *reportOut)
		}
		return applyGap(ctx, pools.Web, store, snap, *fallbackEmail, *token, applyFlags{
			skipUpdates: *skipUpdates, skipComments: *skipComments, skipFiles: *skipFiles, skipTime: *skipTime,
		}, logf)
	}
	return fmt.Errorf("clickup-gap: %q is not snapshot or apply", sub)
}

type applyFlags struct{ skipUpdates, skipComments, skipFiles, skipTime bool }

func compareAndPrint(ctx context.Context, db *pgxpool.Pool, snap *clickup.Snapshot, reportPath string) error {
	orgID, _, err := soleOwnerPool(ctx, db)
	if err != nil {
		return err
	}
	cmp, err := clickup.Compare(ctx, snap, sqlGap{db, orgID}, gapCutoff, gapEditedAfter)
	if err != nil {
		return err
	}
	md := cmp.Markdown()
	fmt.Println()
	fmt.Println(md)
	if reportPath != "" {
		return os.WriteFile(reportPath, []byte(md), 0o644)
	}
	return nil
}

func applyGap(ctx context.Context, db *pgxpool.Pool, st *blob.Store, snap *clickup.Snapshot,
	fallbackEmail, token string, fl applyFlags, logf func(string, ...any)) error {

	orgID, ownerID, err := soleOwnerPool(ctx, db)
	if err != nil {
		return err
	}
	var fallback string
	if err := db.QueryRow(ctx, `
		SELECT p.id::text FROM principals p
		  LEFT JOIN user_identities u ON u.principal_id = p.id
		  LEFT JOIN local_credentials l ON l.principal_id = p.id
		 WHERE p.org_id = $1 AND p.status = 'active'
		   AND lower(COALESCE(u.email::text, l.email::text)) = lower($2) LIMIT 1`, orgID, fallbackEmail).Scan(&fallback); err != nil {
		return fmt.Errorf("find %s: %w", fallbackEmail, err)
	}
	az := authz.New(db)
	actor, err := az.SubjectsFor(ctx, orgID, ownerID)
	if err != nil {
		return err
	}
	docSvc := docs.New(db, az)
	is := importStore{
		Store: docSvc, plant: plant.New(db, az), auth: auth.New(db, az, false),
		time: timelog.New(db, az), chat: channels.New(db, az), notify: notify.New(db),
	}
	fsvc := files.New(db, st)

	// Undo lines go to the log as they are written and to the object store at
	// the end, so the record survives the pod.
	var undoBuf bytes.Buffer
	undo := io.MultiWriter(&undoBuf, prefixWriter{os.Stdout, "UNDO "})
	stamp := time.Now().UTC().Format("20060102-150405")
	undoKey := "imports/clickup-gap-undo-" + stamp + ".log"
	flush := func() {
		if undoBuf.Len() == 0 {
			return
		}
		if _, err := st.Put(ctx, undoKey, bytes.NewReader(undoBuf.Bytes()), int64(undoBuf.Len()), "text/plain"); err != nil {
			logf("could not save the undo log: %v", err)
		}
	}
	defer flush()

	c := clickup.NewClient(token)
	c.MinInterval = 300 * time.Millisecond
	deps := clickup.Deps{
		Store: is, Data: sqlGap{db, orgID},
		Files: gapFiles{files: fsvc, docs: docSvc, actor: actor},
		Time:  gapTime{db}, Download: c,
		Assign: gapAssign{docSvc},
		OrgID:  orgID, Actor: actor, ActorID: ownerID, Fallback: fallback,
		Cutoff: gapCutoff, EditedAfter: gapEditedAfter,
		Undo: undo, Logf: logf,
		SkipUpdates: fl.skipUpdates, SkipComments: fl.skipComments, SkipAttachments: fl.skipFiles, SkipTime: fl.skipTime,
	}
	logf("applying; undo log will be saved as %s", undoKey)
	res, err := clickup.Apply(ctx, deps, snap)
	flush()
	if res != nil {
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Printf("\nRESULT\n%s\n", out)
	}
	return err
}

// ---------------------------------------------------------------------------
// Saving a read
// ---------------------------------------------------------------------------

func putSnapshot(ctx context.Context, st *blob.Store, key string, s *clickup.Snapshot) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(s); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	_, err := st.Put(ctx, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), "application/gzip")
	return err
}

func getSnapshot(ctx context.Context, st *blob.Store, key string) (*clickup.Snapshot, error) {
	rc, err := st.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read the snapshot %s: %w", key, err)
	}
	defer rc.Close()
	zr, err := gzip.NewReader(rc)
	if err != nil {
		return nil, err
	}
	var s clickup.Snapshot
	if err := json.NewDecoder(zr).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// prefixWriter puts a marker on every line, so the undo lines can be pulled out
// of a Job's log with grep.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if _, err := fmt.Fprintf(p.w, "%s%s\n", p.prefix, line); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func soleOwnerPool(ctx context.Context, db *pgxpool.Pool) (string, string, error) {
	var orgs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM orgs WHERE playground_of IS NULL`).Scan(&orgs); err != nil {
		return "", "", err
	}
	if orgs != 1 {
		return "", "", fmt.Errorf("expected exactly one organisation, found %d", orgs)
	}
	var orgID, principalID string
	err := db.QueryRow(ctx, `
		SELECT p.org_id::text, p.id::text FROM principals p
		  JOIN local_credentials lc ON lc.principal_id = p.id
		  JOIN orgs o ON o.id = p.org_id AND o.playground_of IS NULL
		 ORDER BY p.created_at LIMIT 1`).Scan(&orgID, &principalID)
	return orgID, principalID, err
}

// ---------------------------------------------------------------------------
// What Hearth holds
// ---------------------------------------------------------------------------

// sqlGap reads Hearth for one organisation. Personal playgrounds are
// organisations too, with copies of the same people, so every lookup is pinned
// to the real one.
type sqlGap struct {
	db  *pgxpool.Pool
	org string
}

// mo is the organisation as an SQL literal. It is a validated uuid, so there is
// nothing to inject.
func (g sqlGap) mo() string {
	if uuid.Validate(g.org) != nil {
		return "'00000000-0000-0000-0000-000000000000'::uuid"
	}
	return "'" + g.org + "'::uuid"
}

func (g sqlGap) pairs(ctx context.Context, sql string, args ...any) (map[string]string, error) {
	rows, err := g.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (g sqlGap) TaskDocs(ctx context.Context) (map[string]string, error) {
	return g.pairs(ctx, `
		SELECT external_id, entity_id::text FROM external_refs
		 WHERE org_id = `+g.mo()+` AND source = 'clickup' AND entity_type = 'document'
		   AND external_id NOT LIKE 'comment:%' AND external_id NOT LIKE 'attachment:%'`)
}

func (g sqlGap) DocUpdated(ctx context.Context) (map[string]time.Time, error) {
	rows, err := g.db.Query(ctx, `
		SELECT d.id::text, d.updated_at FROM documents d
		  JOIN external_refs e ON e.entity_id = d.id AND e.source = 'clickup'
		 WHERE d.org_id = `+g.mo()+` AND d.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

func (g sqlGap) DocVersions(ctx context.Context) (map[string]int64, error) {
	rows, err := g.db.Query(ctx, `
		SELECT d.id::text, d.version FROM documents d
		  JOIN external_refs e ON e.entity_id = d.id AND e.source = 'clickup'
		 WHERE d.org_id = `+g.mo())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var v int64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

func (g sqlGap) set(ctx context.Context, sql string) (map[string]bool, error) {
	m, err := g.pairs(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out, nil
}

func (g sqlGap) KnownComments(ctx context.Context) (map[string]bool, error) {
	return g.set(ctx, `SELECT external_id, '' FROM external_refs WHERE org_id = `+g.mo()+` AND source = 'clickup' AND external_id LIKE 'comment:%'`)
}

func (g sqlGap) KnownAttachments(ctx context.Context) (map[string]string, error) {
	m, err := g.pairs(ctx, `SELECT external_id, entity_id::text FROM external_refs WHERE org_id = `+g.mo()+` AND source = 'clickup' AND external_id LIKE 'attachment:%'`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.TrimPrefix(k, "attachment:")] = v
	}
	return out, nil
}

func (g sqlGap) KnownTimeEntries(ctx context.Context) (map[string]bool, error) {
	rows, err := g.db.Query(ctx, `SELECT note FROM time_entries WHERE org_id = `+g.mo()+` AND note LIKE $1`, clickup.TimeEntryNotePrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			return nil, err
		}
		rest := strings.TrimPrefix(note, clickup.TimeEntryNotePrefix)
		if i := strings.IndexAny(rest, ": "); i > 0 {
			rest = rest[:i]
		}
		out[rest] = true
	}
	return out, rows.Err()
}

func (g sqlGap) ImportedTotals(ctx context.Context) (map[string][]string, error) {
	rows, err := g.db.Query(ctx, `SELECT document_id::text, id::text FROM time_entries WHERE org_id = `+g.mo()+` AND note = $1 AND deleted_at IS NULL`, clickup.ImportedTimeNote)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var d, id string
		if err := rows.Scan(&d, &id); err != nil {
			return nil, err
		}
		out[d] = append(out[d], id)
	}
	return out, rows.Err()
}

func (g sqlGap) ActivePeople(ctx context.Context) (map[string]string, error) {
	rows, err := g.db.Query(ctx, `
		SELECT p.id::text, COALESCE(NULLIF(p.display_name, ''), ''), COALESCE(u.email::text, l.email::text, '')
		  FROM principals p
		  LEFT JOIN user_identities u ON u.principal_id = p.id
		  LEFT JOIN local_credentials l ON l.principal_id = p.id
		 WHERE p.org_id = `+g.mo()+` AND p.kind = 'user' AND p.status = 'active'
		 ORDER BY (p.source = 'scim') DESC, p.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name, email string
		if err := rows.Scan(&id, &name, &email); err != nil {
			return nil, err
		}
		if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
			if _, taken := out[e]; !taken {
				out[e] = id
			}
		}
		if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
			if _, taken := out[n]; !taken {
				out[n] = id
			}
		}
	}
	return out, rows.Err()
}

func (g sqlGap) TopSpaces(ctx context.Context) (map[string]string, error) {
	rows, err := g.db.Query(ctx, `
		SELECT DISTINCT ON (lower(s.name)) s.name, s.id::text
		  FROM spaces s
		 WHERE s.org_id = `+g.mo()+` AND s.parent_id IS NULL AND s.kind = 'area'
		 ORDER BY lower(s.name), (SELECT count(*) FROM spaces c WHERE c.parent_id = s.id) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, id string
		if err := rows.Scan(&n, &id); err != nil {
			return nil, err
		}
		out[n] = id
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// What it writes through
// ---------------------------------------------------------------------------

type gapFiles struct {
	files *files.Service
	docs  *docs.Service
	actor core.Subjects
}

func (g gapFiles) AttachNew(ctx context.Context, orgID, uploaderID, docID, name, contentType string, size int64, r io.Reader) (string, error) {
	f, err := g.files.Upload(ctx, orgID, uploaderID, name, contentType, size, r)
	if err != nil {
		return "", err
	}
	// Attached the way the page attaches: the file is only reachable through
	// the document it hangs on.
	if err := g.docs.Attach(ctx, orgID, g.actor, docID, f.ID); err != nil {
		return "", fmt.Errorf("stored the file but could not attach it: %w", err)
	}
	return f.ID, nil
}

type gapTime struct{ db *pgxpool.Pool }

func (g gapTime) LogFor(ctx context.Context, orgID, principalID, docID string, minutes int, start time.Time, note string) (string, error) {
	var id string
	end := start.Add(time.Duration(minutes) * time.Minute)
	err := g.db.QueryRow(ctx, `
		INSERT INTO time_entries (id, org_id, document_id, principal_id, started_at, ended_at, minutes, note)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
		orgID, docID, principalID, start, end, minutes, note).Scan(&id)
	return id, err
}

func (g gapTime) Retire(ctx context.Context, ids []string) error {
	_, err := g.db.Exec(ctx, `UPDATE time_entries SET deleted_at = now() WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL`, ids)
	return err
}

type gapAssign struct{ docs *docs.Service }

func (g gapAssign) AssignQuietly(ctx context.Context, orgID, actorID string, rows []docs.Reassignment) error {
	rep, err := g.docs.Reassign(ctx, orgID, actorID, rows, true, false)
	if err != nil {
		return err
	}
	fmt.Printf("assigned %d new tasks quietly (%d changed)\n", rep.Rows, rep.Changed)
	return nil
}
