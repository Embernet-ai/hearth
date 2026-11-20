package search

// The indexer: the jobs that keep the external index current.
//
// Fed by the outbox, so an edit that committed is an edit that will be indexed,
// and an index run that failed is retried rather than lost. The alternative,
// indexing inside the request, means a slow cluster makes saving a work order
// slow and a down cluster makes saving one fail.
//
// Every handler here is idempotent, because delivery is at-least-once. Indexing
// the same document twice writes the same document twice, which is free.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Job kinds.
const (
	KindIndex    = "search.index"
	KindDelete   = "search.delete"
	KindExtract  = "search.extract"
	KindBackfill = "search.backfill"
)

// Blobs is the slice of object storage the extractor needs.
//
// Declared here, by the consumer, so this package does not depend on the whole
// storage client to read one object.
type Blobs interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Pipeline runs the search jobs.
//
// target may be nil, and that is a legal configuration rather than a broken
// one: with no external engine the Postgres index is the only one, it is a
// generated column, and there is nothing to push.
type Pipeline struct {
	db     *pgxpool.Pool
	target Indexer
	blobs  Blobs
	queue  *jobs.Queue
	log    *slog.Logger
}

func NewPipeline(db *pgxpool.Pool, target Indexer, blobs Blobs, queue *jobs.Queue, log *slog.Logger) *Pipeline {
	return &Pipeline{db: db, target: target, blobs: blobs, queue: queue, log: log}
}

// Register wires the handlers onto a worker.
func (ix *Pipeline) Register(h interface {
	Handle(kind string, fn jobs.Handler)
}) {
	h.Handle(KindIndex, ix.handleIndex)
	h.Handle(KindDelete, ix.handleDelete)
	h.Handle(KindExtract, ix.handleExtract)
	h.Handle(KindBackfill, ix.handleBackfill)
}

// Routes are the outbox subscriptions that keep the index current.
//
// The unique key collapses duplicates while a reindex is pending, so a document
// edited four times in a minute is one index job rather than four. That is the
// difference between a queue that drains and one that grows all afternoon.
func Routes() []jobs.Route {
	const (
		doc     = string(core.EntityDocument)
		file    = string(core.EntityFile)
		contact = string(core.EntityContact)
		company = string(core.EntityCompany)
	)
	return []jobs.Route{
		{Event: jobs.EventCreated, EntityType: doc, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventUpdated, EntityType: doc, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventDeleted, EntityType: doc, JobKind: KindDelete},
		// A grant change alters who may see a document, and the external index
		// carries that list inside each document rather than joining to it. So
		// sharing something is a reindex, and forgetting this is how a
		// colleague you just shared a runbook with cannot find it.
		{Event: authz.EventGranted, EntityType: doc, JobKind: KindIndex, UniqueKey: "index"},

		// A new attachment gets read before it gets indexed. The extract job
		// enqueues the index job when it is done, rather than both running now
		// and the index one storing a filename with no contents.
		{Event: jobs.EventCreated, EntityType: file, JobKind: KindExtract, UniqueKey: "extract"},
		{Event: jobs.EventUpdated, EntityType: file, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventDeleted, EntityType: file, JobKind: KindDelete},

		// Contacts and companies, on exactly the same three subscriptions.
		// They are ordinary entities, so being findable is a route rather than
		// a second search implementation.
		{Event: jobs.EventCreated, EntityType: contact, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventUpdated, EntityType: contact, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventDeleted, EntityType: contact, JobKind: KindDelete},
		{Event: authz.EventGranted, EntityType: contact, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventCreated, EntityType: company, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventUpdated, EntityType: company, JobKind: KindIndex, UniqueKey: "index"},
		{Event: jobs.EventDeleted, EntityType: company, JobKind: KindDelete},
		{Event: authz.EventGranted, EntityType: company, JobKind: KindIndex, UniqueKey: "index"},
	}
}

// handleIndex pushes one entity into the external index.
func (ix *Pipeline) handleIndex(ctx context.Context, j *jobs.Job) error {
	var p jobs.EventPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	if ix.target == nil {
		// No external engine configured. Postgres is already current, so this
		// is a success rather than a job that retries forever.
		return nil
	}

	switch p.EntityType {
	case string(core.EntityDocument):
		return ix.indexDocument(ctx, j.OrgID, p.EntityID)
	case string(core.EntityFile):
		return ix.indexFile(ctx, j.OrgID, p.EntityID)
	case string(core.EntityContact):
		return ix.indexParty(ctx, j.OrgID, core.EntityContact, p.EntityID)
	case string(core.EntityCompany):
		return ix.indexParty(ctx, j.OrgID, core.EntityCompany, p.EntityID)
	default:
		// A type nothing indexes. Not an error: the outbox carries everything
		// and subscribers are allowed to ignore what is not theirs.
		return nil
	}
}

func (ix *Pipeline) indexDocument(ctx context.Context, orgID, id string) error {
	var d Document
	var deleted *time.Time
	err := ix.db.QueryRow(ctx, `
		SELECT d.org_id::text, d.kind, d.title, d.body, d.version, d.updated_at, d.deleted_at,
		       COALESCE(array_agg(g.subject_key) FILTER (WHERE g.subject_key IS NOT NULL), '{}')
		FROM documents d
		LEFT JOIN grants g
		       ON g.entity_type = 'document' AND g.entity_id = d.id
		      AND (g.expires_at IS NULL OR g.expires_at > now())
		WHERE d.id = $1
		GROUP BY d.id`, id).
		Scan(&d.OrgID, &d.Kind, &d.Title, &d.Body, &d.Version, &d.UpdatedAt, &deleted, &d.Subjects)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Gone entirely, so make sure the index agrees. A hard delete that
		// raced the job would otherwise leave a permanent ghost in results.
		return ix.removeAndForget(ctx, string(core.EntityDocument), id)
	case err != nil:
		return fmt.Errorf("search: load document %s: %w", id, err)
	}
	if deleted != nil {
		return ix.removeAndForget(ctx, string(core.EntityDocument), id)
	}

	d.EntityType = string(core.EntityDocument)
	d.EntityID = id
	if err := ix.target.Index(ctx, d); err != nil {
		ix.recordFailure(ctx, orgID, d.EntityType, id, err)
		return err
	}
	return ix.recordIndexed(ctx, orgID, d.EntityType, id, d.Version)
}

// indexFile indexes an attachment's extracted text.
//
// Its permission subjects are the ones on the document it hangs off, not its
// own: a file id grants nothing by itself, and copying the parent's list in is
// what makes a hit on an attachment obey the same rules as a hit on the work
// order it belongs to.
func (ix *Pipeline) indexFile(ctx context.Context, orgID, id string) error {
	var d Document
	var status string
	var text *string
	var deleted *time.Time
	err := ix.db.QueryRow(ctx, `
		SELECT f.org_id::text, f.filename, f.extracted_text, f.extraction_status,
		       f.created_at, f.deleted_at,
		       COALESCE(l.from_id::text, ''),
		       COALESCE(array_agg(g.subject_key) FILTER (WHERE g.subject_key IS NOT NULL), '{}')
		FROM files f
		LEFT JOIN entity_links l
		       ON l.to_type = 'file' AND l.to_id = f.id AND l.from_type = 'document'
		LEFT JOIN grants g
		       ON g.entity_type = 'document' AND g.entity_id = l.from_id
		      AND (g.expires_at IS NULL OR g.expires_at > now())
		WHERE f.id = $1
		GROUP BY f.id, l.from_id`, id).
		Scan(&d.OrgID, &d.Title, &text, &status, &d.UpdatedAt, &deleted, &d.ParentID, &d.Subjects)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ix.removeAndForget(ctx, string(core.EntityFile), id)
	case err != nil:
		return fmt.Errorf("search: load file %s: %w", id, err)
	}
	if deleted != nil || d.ParentID == "" {
		// An orphan file has nothing to inherit permissions from, so it must
		// not be searchable at all. Indexing it with an empty subject list
		// would be invisible today and a leak the day somebody widened the
		// filter.
		return ix.removeAndForget(ctx, string(core.EntityFile), id)
	}
	if status == StatusPending {
		// Extraction has not run. Indexing now would store the filename and
		// then never come back for the contents, because the extract job's
		// completion is what triggers the reindex.
		return nil
	}

	d.EntityType = string(core.EntityFile)
	d.EntityID = id
	d.Kind = "file"
	if text != nil {
		d.Body = *text
	}
	if err := ix.target.Index(ctx, d); err != nil {
		ix.recordFailure(ctx, orgID, d.EntityType, id, err)
		return err
	}
	return ix.recordIndexed(ctx, orgID, d.EntityType, id, 1)
}

func (ix *Pipeline) handleDelete(ctx context.Context, j *jobs.Job) error {
	var p jobs.EventPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	if ix.target == nil {
		return nil
	}
	return ix.removeAndForget(ctx, p.EntityType, p.EntityID)
}

func (ix *Pipeline) removeAndForget(ctx context.Context, entityType, id string) error {
	if ix.target != nil {
		if err := ix.target.Delete(ctx, entityType, id); err != nil {
			return err
		}
	}
	_, err := ix.db.Exec(ctx,
		`DELETE FROM search_state WHERE entity_type=$1 AND entity_id=$2`, entityType, id)
	if err != nil {
		return fmt.Errorf("search: clear index state: %w", err)
	}
	return nil
}

func (ix *Pipeline) recordIndexed(ctx context.Context, orgID, entityType, id string, version int64) error {
	_, err := ix.db.Exec(ctx, `
		INSERT INTO search_state (entity_type, entity_id, org_id, indexed_version, indexed_at, error)
		VALUES ($1,$2,$3,$4,now(),NULL)
		ON CONFLICT (entity_type, entity_id) DO UPDATE
		    SET indexed_version = EXCLUDED.indexed_version,
		        indexed_at = now(), error = NULL`,
		entityType, id, orgID, version)
	if err != nil {
		return fmt.Errorf("search: record index state: %w", err)
	}
	return nil
}

// recordFailure leaves a trail for a row that will not index.
//
// Best effort, and deliberately not returned: the job's own failure is what
// matters, and losing the note must not turn a retryable index error into a
// different error about recording it.
func (ix *Pipeline) recordFailure(ctx context.Context, orgID, entityType, id string, cause error) {
	_, _ = ix.db.Exec(ctx, `
		INSERT INTO search_state (entity_type, entity_id, org_id, indexed_version, indexed_at, error)
		VALUES ($1,$2,$3,0,now(),$4)
		ON CONFLICT (entity_type, entity_id) DO UPDATE
		    SET indexed_at = now(), error = EXCLUDED.error`,
		entityType, id, orgID, cause.Error())
}

// -----------------------------------------------------------------------------
// Attachment text
// -----------------------------------------------------------------------------

// ExtractPayload names the file to read.
type ExtractPayload struct {
	FileID string `json:"file_id"`
}

// handleExtract reads an attachment's text and then asks for it to be indexed.
//
// Two jobs rather than one, because they fail for different reasons and should
// retry independently: a cluster that is down should not make the extraction
// run again, and extraction is the expensive half.
func (ix *Pipeline) handleExtract(ctx context.Context, j *jobs.Job) error {
	var p ExtractPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	if p.FileID == "" {
		// Routed from an outbox event rather than enqueued directly.
		var ev jobs.EventPayload
		if err := j.Decode(&ev); err != nil {
			return err
		}
		p.FileID = ev.EntityID
	}
	if p.FileID == "" {
		return errors.New("search: extract job has no file")
	}

	var key, contentType, filename, status string
	err := ix.db.QueryRow(ctx, `
		SELECT storage_key, content_type, filename, extraction_status
		FROM files WHERE id = $1 AND deleted_at IS NULL`, p.FileID).
		Scan(&key, &contentType, &filename, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Deleted between the upload and the job. Nothing to do, and not a
		// failure.
		return nil
	case err != nil:
		return fmt.Errorf("search: load file %s: %w", p.FileID, err)
	}
	if status != StatusPending {
		// Already done. At-least-once delivery means this is the normal case
		// for a redelivered job, not an anomaly.
		return nil
	}
	if ix.blobs == nil {
		return errors.New("search: no object storage configured for extraction")
	}

	rc, err := ix.blobs.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("search: open %s: %w", key, err)
	}
	defer rc.Close()

	text, outcome, err := Extract(contentType, filename, rc)
	if err != nil {
		// A read that failed is worth retrying. A format that has no text in it
		// is not, and Extract distinguishes them.
		_, _ = ix.db.Exec(ctx,
			`UPDATE files SET extraction_status = $1 WHERE id = $2`, StatusFailed, p.FileID)
		return err
	}

	if _, err := ix.db.Exec(ctx, `
		UPDATE files SET extracted_text = $1, extraction_status = $2 WHERE id = $3`,
		nullIfEmpty(text), outcome, p.FileID); err != nil {
		return fmt.Errorf("search: store extracted text: %w", err)
	}
	ix.log.InfoContext(ctx, "extracted attachment text",
		"file", p.FileID, "status", outcome, "characters", len(text))

	// Now it has contents, index it.
	_, err = ix.queue.Enqueue(ctx, jobs.Spec{
		OrgID: j.OrgID, Kind: KindIndex, UniqueKey: "index:" + p.FileID,
		Payload: jobs.EventPayload{
			Event: jobs.EventUpdated, EntityType: string(core.EntityFile), EntityID: p.FileID,
		},
	})
	return err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// -----------------------------------------------------------------------------
// Backfill
// -----------------------------------------------------------------------------

// BackfillPayload bounds one pass.
type BackfillPayload struct {
	// After is the last id of the previous batch, so the work resumes rather
	// than starting over. A backfill that cannot resume is a backfill nobody
	// dares start on a large instance.
	After string `json:"after"`
	// Entity is "document" or "file".
	Entity string `json:"entity"`
}

// backfillBatch is how many index jobs one pass enqueues. Large enough to get
// through a real instance quickly, small enough that the queue does not become
// a million rows in one transaction.
const backfillBatch = 500

// handleBackfill rebuilds the index from Postgres.
//
// It enqueues index jobs rather than indexing inline, so the work goes through
// the same path as an ordinary edit, gets the same retries, and can be watched
// on the same operations page. An index that can only be rebuilt by a special
// code path is an index nobody has actually rebuilt.
//
// It chains: each pass enqueues the next one. That is what makes it resumable
// and what stops one enormous job holding a lease for an hour.
func (ix *Pipeline) handleBackfill(ctx context.Context, j *jobs.Job) error {
	var p BackfillPayload
	if err := j.Decode(&p); err != nil {
		return err
	}
	if p.Entity == "" {
		p.Entity = string(core.EntityDocument)
	}
	if ix.target != nil {
		// A backfill into an index that does not exist yet silently creates one
		// with a guessed mapping, and then subjects is analysed as text and the
		// permission filter matches everything.
		if err := ix.target.EnsureIndex(ctx); err != nil {
			return err
		}
	}

	// One statement shape, four tables. Every entity that is indexed has an
	// id, an org, and a soft delete, which is what makes the sweep the same
	// for all of them.
	var table string
	switch p.Entity {
	case string(core.EntityFile):
		table = "files"
	case string(core.EntityContact):
		table = "contacts"
	case string(core.EntityCompany):
		table = "companies"
	default:
		table = "documents"
	}
	sql := `SELECT id::text FROM ` + table + `
	        WHERE org_id = $1 AND deleted_at IS NULL AND ($2 = '' OR id::text > $2)
	        ORDER BY id LIMIT $3`

	rows, err := ix.db.Query(ctx, sql, j.OrgID, p.After, backfillBatch)
	if err != nil {
		return fmt.Errorf("search: backfill scan: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("search: backfill scan row: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("search: backfill iterate: %w", err)
	}

	for _, id := range ids {
		if _, err := ix.queue.Enqueue(ctx, jobs.Spec{
			OrgID: j.OrgID, Kind: KindIndex, UniqueKey: "index:" + id,
			Payload: jobs.EventPayload{
				Event: jobs.EventUpdated, EntityType: p.Entity, EntityID: id,
			},
		}); err != nil {
			return fmt.Errorf("search: backfill enqueue %s: %w", id, err)
		}
	}

	switch {
	case len(ids) == backfillBatch:
		// More to do. The next pass picks up where this one stopped.
		_, err = ix.queue.Enqueue(ctx, jobs.Spec{
			OrgID: j.OrgID, Kind: KindBackfill,
			Payload: BackfillPayload{After: ids[len(ids)-1], Entity: p.Entity},
		})
		return err
	case backfillNext[p.Entity] != "":
		// This kind is done, so move on to the next. Chained rather than one
		// job per table, so a rebuild is one thing an operator can watch
		// finish rather than four they have to remember to start.
		next := backfillNext[p.Entity]
		ix.log.InfoContext(ctx, "search backfill finished a kind",
			"org", j.OrgID, "done", p.Entity, "next", next)
		_, err = ix.queue.Enqueue(ctx, jobs.Spec{
			OrgID: j.OrgID, Kind: KindBackfill,
			Payload: BackfillPayload{Entity: next},
		})
		return err
	default:
		ix.log.InfoContext(ctx, "search backfill finished", "org", j.OrgID)
		return nil
	}
}

// backfillNext is the order a rebuild walks the entity types. Documents first,
// because they are what somebody watching a rebuild is waiting for, and the
// phone book last because it is small.
var backfillNext = map[string]string{
	string(core.EntityDocument): string(core.EntityFile),
	string(core.EntityFile):     string(core.EntityContact),
	string(core.EntityContact):  string(core.EntityCompany),
}

// StartBackfill queues a rebuild of the whole index for one org.
func StartBackfill(ctx context.Context, q *jobs.Queue, orgID string) error {
	_, err := q.Enqueue(ctx, jobs.Spec{
		OrgID: orgID, Kind: KindBackfill, UniqueKey: "backfill",
		Payload: BackfillPayload{Entity: string(core.EntityDocument)},
	})
	return err
}

// IndexState is what the operations page shows about the index.
type IndexState struct {
	Indexed int
	Stale   int
	Failed  int
	// Pending extraction is separate, because a file whose text has not been
	// read yet is not a fault, it is work that has not come round.
	AwaitingExtraction int
	LastIndexedAt      *time.Time
}

// State reports how far behind the index is.
//
// Stale is counted by comparing versions rather than timestamps. Two machines'
// clocks disagree, so "indexed at 12:00:01, edited at 12:00:00" is not evidence
// of anything, while indexed_version < version is exact.
func State(ctx context.Context, db *pgxpool.Pool, orgID string) (*IndexState, error) {
	s := &IndexState{}
	err := db.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM search_state WHERE org_id = $1 AND error IS NULL),
		  (SELECT count(*) FROM documents d
		     LEFT JOIN search_state s
		            ON s.entity_type = 'document' AND s.entity_id = d.id
		    WHERE d.org_id = $1 AND d.deleted_at IS NULL
		      AND (s.entity_id IS NULL OR s.indexed_version < d.version)),
		  (SELECT count(*) FROM search_state WHERE org_id = $1 AND error IS NOT NULL),
		  (SELECT count(*) FROM files
		    WHERE org_id = $1 AND deleted_at IS NULL AND extraction_status = 'pending'),
		  (SELECT max(indexed_at) FROM search_state WHERE org_id = $1)`,
		orgID).Scan(&s.Indexed, &s.Stale, &s.Failed, &s.AwaitingExtraction, &s.LastIndexedAt)
	if err != nil {
		return nil, fmt.Errorf("search: index state: %w", err)
	}
	return s, nil
}
