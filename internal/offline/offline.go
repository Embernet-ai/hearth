// Package offline is work done at the machine, with no signal.
//
// # The one idea
//
// There is no offline data model. A queued action is the request the browser
// would have made, kept until it can be made, and replayed through the same
// handler a person's browser reaches. Nothing here re-implements setting a
// status or logging forty minutes, so nothing here can drift away from what
// the product actually does when the signal is fine.
//
// The alternative is a second write path with its own validation, and it goes
// wrong in a way that is very hard to see: the offline version accepts a
// reading the online form refuses, or misses the rule that raises the next
// preventive job, and the difference only shows up for the people who are
// least able to report it.
//
// # Exactly once, or an honest answer
//
// A phone at the edge of a signal sends a batch, loses the reply, and sends it
// again. Every operation carries a key generated when the person pressed the
// button, and this package claims that key before doing anything. A second
// arrival reads the first attempt's answer back instead of doing the work
// twice, which matters most for the operations where twice is silently wrong:
// time logged, parts fitted, meter readings.
//
// If a replay starts and never records its outcome, the honest answer is that
// nobody knows whether it landed. That is a status of its own, and it goes on
// a page for a person to settle rather than being retried on the assumption
// that a repeat is harmless.
//
// # Conflicts are questions, not errors
//
// An edit made offline can lose the version check, because somebody at a desk
// changed the same job in the meantime. Silently overwriting them is the one
// outcome that cannot be undone, and silently dropping the queued edit throws
// away work somebody did standing in front of the machine. So the operation is
// recorded as a conflict and stays there until a person decides. The record is
// here rather than in the browser because a conflict raised on a phone that
// then fell in a drum of coolant is still a conflict.
package offline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("no such queued operation")
	ErrInvalid  = errors.New("that is not something that can be queued")
)

// Statuses. The five the server writes, and the two a person writes.
const (
	StatusPending   = "pending"
	StatusApplied   = "applied"
	StatusConflict  = "conflict"
	StatusRejected  = "rejected"
	StatusUnknown   = "unknown"
	StatusResolved  = "resolved"
	StatusDiscarded = "discarded"
)

// MaxOps bounds one batch.
//
// A hundred is a long shift with no signal at all, and a batch bigger than that
// is either a bug or somebody replaying a file. The client sends the rest in
// the next batch, in order, so nothing is lost by the limit.
const MaxOps = 100

// MaxBody bounds the whole request, photographs included.
//
// Generous because a photograph of a cracked weld is the point, and bounded
// because the body is read into memory before any of it is trusted.
const MaxBody int64 = 48 << 20

// Op is one thing somebody did with no signal.
type Op struct {
	// Key is the client's idempotency key, made when the button was pressed.
	Key string
	// Path is where the request would have gone.
	Path string
	// Body is the form encoding, exactly as the browser would have sent it.
	Body string
	// At is the phone's clock when it happened. Kept for the record and for the
	// client's own ordering; never trusted for anything a decision rests on,
	// because a phone that has been in a drawer since Tuesday is confidently
	// wrong about the time.
	At time.Time
	// File is a photograph taken with no signal.
	File *File
}

// File is a photograph, carried inline.
//
// Inline rather than a second upload endpoint, because the second endpoint
// needs its own authentication, its own resume story, and its own way of
// binding the bytes to the operation they belong to, and a plant floor phone
// on one bar will drop between the two requests.
type File struct {
	Name        string
	ContentType string
	// Digest is the SHA-256 the phone computed when the photograph was taken.
	// It is checked here against the bytes that arrived, which is what turns
	// "the upload returned 200" into "the photograph on the server is the
	// photograph that was taken".
	Digest string
	Data   []byte
}

// Result is what happened to one operation.
type Result struct {
	Key     string `json:"key"`
	Status  string `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	// Repeat says this answer was read back rather than done again, so a client
	// that sent the same batch twice can tell the difference between its work
	// landing and its work landing twice.
	Repeat bool `json:"repeat,omitempty"`
}

// Record is a queued operation as the server has it.
type Record struct {
	ID          string
	OrgID       string
	PrincipalID string
	Key         string
	Path        string
	Body        string
	EntityID    *string
	Kind        string
	QueuedAt    time.Time
	ReceivedAt  time.Time
	Status      string
	Code        int
	Message     string
}

// Late reports whether the gap between doing it and it arriving was long
// enough to be worth showing. A queue that drained in four seconds is not a
// story; one that drained after two hours in a basement is.
func (r Record) Late() bool { return r.ReceivedAt.Sub(r.QueuedAt) > 2*time.Minute }

// Waited is how long the signal was out for this operation.
func (r Record) Waited() time.Duration {
	d := r.ReceivedAt.Sub(r.QueuedAt)
	if d < 0 {
		return 0
	}
	return d
}

// Service stores what the phones did.
type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// ---------------------------------------------------------------------------
// What may be replayed
// ---------------------------------------------------------------------------

// replayable is the list of actions worth queueing.
//
// It is short on purpose, and it is the security boundary rather than a
// convenience. A batch endpoint that will replay any path is a way to reach
// every handler on the site with one request and no referer, and the reason to
// have one at all is a technician holding a phone: a status, a reading, a
// photograph, time, a note, and ticking a step off a procedure. That is what
// people do standing at a machine.
//
// The service worker carries the same list so it does not queue something that
// will only be refused later. That copy is a courtesy. This one decides.
var replayable = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`^/documents/` + uuidPat + `/status$`), "a status change"},
	{regexp.MustCompile(`^/documents/` + uuidPat + `/readings$`), "a meter reading"},
	{regexp.MustCompile(`^/documents/` + uuidPat + `/time$`), "time logged"},
	{regexp.MustCompile(`^/documents/` + uuidPat + `/files$`), "a photograph"},
	{regexp.MustCompile(`^/documents/` + uuidPat + `/comments$`), "a note"},
	{regexp.MustCompile(`^/documents/` + uuidPat + `/checklist/` + uuidPat + `$`), "a step ticked off"},
}

const uuidPat = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

var docIDRe = regexp.MustCompile(`^/documents/(` + uuidPat + `)/`)

// Replayable reports whether a path may be replayed, and says what it is in
// the words the outbox page uses.
func Replayable(path string) (kind string, ok bool) {
	// A query string on a queued action is not something any of these forms
	// produce, and allowing one widens what the patterns above actually match.
	if path == "" || strings.ContainsAny(path, "?#") {
		return "", false
	}
	for _, r := range replayable {
		if r.re.MatchString(path) {
			return r.kind, true
		}
	}
	return "", false
}

// Subject is the entity a queued operation is about, so the outbox can name it.
func Subject(path string) string {
	m := docIDRe.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// CheckDigest compares what arrived against what the phone said it sent.
//
// This is the whole of the photograph criterion. An upload that returns 200
// having stored a truncated file is the failure being prevented, and it is
// exactly what a flaky link produces.
func CheckDigest(f *File) error {
	if f == nil {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(f.Digest))
	if want == "" {
		return fmt.Errorf("%w: a photograph has to arrive with the digest taken when it was captured", ErrInvalid)
	}
	if len(want) != 64 {
		return fmt.Errorf("%w: that is not a SHA-256", ErrInvalid)
	}
	sum := sha256.Sum256(f.Data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%w: the photograph did not arrive intact, %d bytes hash to %s and the phone said %s",
			ErrInvalid, len(f.Data), got[:12], want[:12])
	}
	return nil
}

// ---------------------------------------------------------------------------
// Claiming, and reading an answer back
// ---------------------------------------------------------------------------

// Claim takes ownership of a key before any work is done.
//
// Returns claimed when this call is the one that will do the work. Returns a
// prior record when the key has been seen before, and the caller answers from
// that instead of doing anything, which is what makes a repeated batch safe.
//
// A prior record still sitting at pending is a replay that started and never
// finished, almost always because the process died mid-batch. It is moved to
// unknown here rather than being retried: whether it landed is a question
// about the world, and the only correct thing to do with a question is ask it.
func (s *Service) Claim(ctx context.Context, orgID, principalID string, op Op) (id string, prior *Record, err error) {
	key := strings.TrimSpace(op.Key)
	switch {
	case key == "":
		return "", nil, fmt.Errorf("%w: every queued operation needs a key", ErrInvalid)
	case len(key) > 128:
		return "", nil, fmt.Errorf("%w: that key is too long to be one", ErrInvalid)
	}
	kind, ok := Replayable(op.Path)
	if !ok {
		return "", nil, fmt.Errorf("%w: %s is not queued for later", ErrInvalid, op.Path)
	}
	if err := CheckDigest(op.File); err != nil {
		return "", nil, err
	}

	var entity *string
	if v := Subject(op.Path); v != "" {
		entity = &v
	}
	// The body of a photograph operation is not kept: the bytes are in the
	// object store once it lands, and a base64 copy of every photograph in a
	// bookkeeping table is how a small table becomes the largest one.
	body := op.Body
	if op.File != nil {
		body = ""
	}

	newID := uuid.NewString()
	tag, err := s.db.Exec(ctx, `
		INSERT INTO offline_ops
		    (id, org_id, principal_id, key, path, body, entity_id, kind,
		     queued_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending')
		ON CONFLICT (principal_id, key) DO NOTHING`,
		newID, orgID, principalID, key, op.Path, body, entity, kind, sane(op.At))
	if err != nil {
		return "", nil, fmt.Errorf("offline: claim: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return newID, nil, nil
	}

	// Somebody has been here before. Read what happened.
	rec, err := s.byKey(ctx, principalID, key)
	if err != nil {
		return "", nil, err
	}
	if rec.Status == StatusPending {
		if _, err := s.db.Exec(ctx, `
			UPDATE offline_ops
			   SET status = 'unknown',
			       message = 'a replay of this started and never said how it ended'
			 WHERE id = $1 AND status = 'pending'`, rec.ID); err != nil {
			return "", nil, fmt.Errorf("offline: mark unknown: %w", err)
		}
		rec.Status = StatusUnknown
		rec.Message = "a replay of this started and never said how it ended"
	}
	return "", rec, nil
}

// sane clamps a clock this service does not control.
//
// A phone that has been off since the summer comes back with a date, and it is
// occasionally 1970 or the middle of next year. The value is only ever shown to
// a person and used to order a list, so a wild one is replaced with now rather
// than rejected: refusing the work because the clock is wrong helps nobody
// standing at a machine.
func sane(t time.Time) time.Time {
	now := time.Now()
	if t.IsZero() || t.Before(now.AddDate(-1, 0, 0)) || t.After(now.Add(24*time.Hour)) {
		return now
	}
	return t
}

// Finish records how a replay ended.
func (s *Service) Finish(ctx context.Context, id, status string, code int, message string) error {
	switch status {
	case StatusApplied, StatusConflict, StatusRejected, StatusUnknown:
	default:
		return fmt.Errorf("%w: %s is not an outcome", ErrInvalid, status)
	}
	resolved := status == StatusApplied || status == StatusRejected
	_, err := s.db.Exec(ctx, `
		UPDATE offline_ops
		   SET status = $2, code = $3, message = $4,
		       resolved_at = CASE WHEN $5 THEN now() ELSE resolved_at END
		 WHERE id = $1`, id, status, code, trim(message, 2000), resolved)
	if err != nil {
		return fmt.Errorf("offline: finish: %w", err)
	}
	return nil
}

// Decide records a person's answer to a conflict or an unknown.
func (s *Service) Decide(ctx context.Context, principalID, id, status, message string) error {
	switch status {
	case StatusResolved, StatusDiscarded:
	default:
		return fmt.Errorf("%w: %s is not a decision", ErrInvalid, status)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE offline_ops
		   SET status = $3, message = $4, resolved_at = now()
		 WHERE id = $1 AND principal_id = $2
		   AND status IN ('conflict', 'unknown')`,
		id, principalID, status, trim(message, 2000))
	if err != nil {
		return fmt.Errorf("offline: decide: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Undecided is what this person still has to answer.
//
// Their own, only. Somebody else's unresolved edit is not a decision anybody
// can make for them: only the person who was standing at the machine knows
// whether their reading or the one at the desk is the true one.
func (s *Service) Undecided(ctx context.Context, orgID, principalID string) ([]*Record, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, org_id::text, principal_id::text, key, path, body,
		       entity_id::text, kind, queued_at, received_at, status, code, message
		  FROM offline_ops
		 WHERE org_id = $1 AND principal_id = $2
		   AND status IN ('conflict', 'unknown')
		 ORDER BY queued_at DESC
		 LIMIT 200`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("offline: undecided: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// Recent is what this person's phone has sent lately, decided or not.
//
// On the page because "did my morning actually go in" is the question somebody
// asks after a shift in a basement, and an outbox that only shows problems
// cannot answer it.
func (s *Service) Recent(ctx context.Context, orgID, principalID string, limit int) ([]*Record, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, org_id::text, principal_id::text, key, path, body,
		       entity_id::text, kind, queued_at, received_at, status, code, message
		  FROM offline_ops
		 WHERE org_id = $1 AND principal_id = $2
		 ORDER BY received_at DESC
		 LIMIT $3`, orgID, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("offline: recent: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// Count is how many decisions are waiting, for the badge in the top bar.
func (s *Service) Count(ctx context.Context, orgID, principalID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM offline_ops
		 WHERE org_id = $1 AND principal_id = $2
		   AND status IN ('conflict', 'unknown')`, orgID, principalID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("offline: count: %w", err)
	}
	return n, nil
}

// Get reads one of this person's operations.
func (s *Service) Get(ctx context.Context, principalID, id string) (*Record, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, org_id::text, principal_id::text, key, path, body,
		       entity_id::text, kind, queued_at, received_at, status, code, message
		  FROM offline_ops
		 WHERE id = $1 AND principal_id = $2`, id, principalID)
	if err != nil {
		return nil, fmt.Errorf("offline: get: %w", err)
	}
	defer rows.Close()
	recs, err := scanRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrNotFound
	}
	return recs[0], nil
}

// Prune drops the settled record after a while.
//
// Settled only. A conflict nobody answered stays forever, because the whole
// reason it is on the server is that the phone it came from is not a reliable
// place to keep a question.
func (s *Service) Prune(ctx context.Context, orgID string, olderThan time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM offline_ops
		 WHERE org_id = $1
		   AND received_at < now() - $2::interval
		   AND status IN ('applied', 'rejected', 'resolved', 'discarded')`,
		orgID, olderThan.String())
	if err != nil {
		return 0, fmt.Errorf("offline: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Service) byKey(ctx context.Context, principalID, key string) (*Record, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, org_id::text, principal_id::text, key, path, body,
		       entity_id::text, kind, queued_at, received_at, status, code, message
		  FROM offline_ops
		 WHERE principal_id = $1 AND key = $2`, principalID, key)
	if err != nil {
		return nil, fmt.Errorf("offline: by key: %w", err)
	}
	defer rows.Close()
	recs, err := scanRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		// The insert said the key was taken and the read says it is not, which
		// means it was pruned or deleted between the two statements. Rare, and
		// the honest answer is that this call cannot claim it either.
		return nil, ErrNotFound
	}
	return recs[0], nil
}

func scanRecords(rows pgx.Rows) ([]*Record, error) {
	var out []*Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.OrgID, &r.PrincipalID, &r.Key, &r.Path,
			&r.Body, &r.EntityID, &r.Kind, &r.QueuedAt, &r.ReceivedAt,
			&r.Status, &r.Code, &r.Message); err != nil {
			return nil, fmt.Errorf("offline: scan: %w", err)
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("offline: rows: %w", err)
	}
	return out, nil
}

func trim(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) > n {
		return v[:n]
	}
	return v
}
