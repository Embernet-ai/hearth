// Package timelog answers three questions a maintenance team actually asks:
// who is overloaded, what did this job cost, and was the estimate ever
// realistic.
//
// A running timer is a row with no end. Nothing is held in the process, so a
// timer survives a deploy by construction rather than by anybody remembering
// to persist it, and that matters because a rollout in the middle of an
// afternoon should not cost somebody their afternoon.
//
// Manual correction is a first-class path rather than an admin escape hatch.
// The timer is always forgotten at least once, and a system that only accepts
// timed work quietly under-records everything that happened after four o'clock.
package timelog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
)

var (
	ErrNotFound = errors.New("time entry not found")
	// ErrAlreadyRunning means this person already has a timer going. Two open
	// timers is the same hour billed to two jobs.
	ErrAlreadyRunning = errors.New("you already have a timer running")
	ErrNotRunning     = errors.New("no timer is running")
	// ErrForbidden is correcting or removing an entry that is neither yours
	// nor on a document you administer.
	ErrForbidden = errors.New("you may not change that entry")
)

// Entry is one piece of recorded work.
type Entry struct {
	ID          string
	DocumentID  string
	DocTitle    string
	PrincipalID string
	PersonName  string
	StartedAt   time.Time
	EndedAt     *time.Time
	// Minutes is nil while the timer is running.
	Minutes *int
	Note    string
	// Tags is empty for a running timer's entry -- there is nothing to tag
	// yet, only something still happening.
	Tags []Tag

	// The approval decision, or its absence. Nil Approved with a nil DecidedAt
	// is an entry nobody has looked at yet; the schema will not let those two
	// disagree.
	Approved    *bool
	DecidedAt   *time.Time
	DecidedBy   string
	DeciderName string
	DecidedNote string

	// DeletedAt is nil for every entry an ordinary list shows. Non-nil only
	// reaches an Entry when a caller deliberately asked to see removed ones
	// (Mine's showRemoved) -- nothing renders this by accident.
	DeletedAt *time.Time
}

// Deleted reports whether Remove has been called on this entry.
func (e Entry) Deleted() bool { return e.DeletedAt != nil }

// Running reports whether the clock is still going.
func (e Entry) Running() bool { return e.EndedAt == nil }

// Pending reports whether this is finished work still waiting on a manager. A
// running timer is not pending: there is nothing to approve until it stops.
func (e Entry) Pending() bool { return e.EndedAt != nil && e.DecidedAt == nil }

// WasApproved reports a decision that went the person's way.
func (e Entry) WasApproved() bool { return e.Approved != nil && *e.Approved }

// SentBack reports a decision that did not, which is a question rather than a
// deletion: the hours stay, with a reason attached, and the person can correct
// them.
func (e Entry) SentBack() bool { return e.Approved != nil && !*e.Approved }

// Unattached reports work recorded against no job at all.
func (e Entry) Unattached() bool { return e.DocumentID == "" }

// Elapsed is how long this entry represents, counting a running timer up to
// now. A page showing a running timer as zero minutes is a page nobody
// believes.
func (e Entry) Elapsed() int {
	if e.Minutes != nil {
		return *e.Minutes
	}
	return int(time.Since(e.StartedAt).Minutes())
}

// Authorizer is the slice of access control this package needs.
//
// Two questions and no more. Require is about one entity; HasCapability is
// about whether somebody may see a section of the application at all, which is
// the question Workload has to ask and could not, because it never asked
// anything. The capability is a plain string rather than the authz type so this
// package does not import that one for a bool.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
	HasCapability(ctx context.Context, orgID, principalID, capability string) (bool, error)
}

// The capabilities this package asks about. They match the values in the
// capability_grants CHECK constraint, and there is a test that fails if these
// drift from the ones authz defines.
const (
	capTimeAll = "time_all"
	capFinance = "finance"
	capPeople  = "people"
)

// ErrNotAllowed is returned when somebody asks for a view of other people's
// time that they do not hold the capability for.
var ErrNotAllowed = errors.New("you can only see your own time")

// ErrDeleted means the entry exists but has been removed -- restore it
// before correcting it or changing its tags.
var ErrDeleted = errors.New("timelog: that entry has been removed")

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
}

func New(db *pgxpool.Pool, az Authorizer) *Service { return &Service{db: db, authz: az} }

// Start begins a timer, against a document or against nothing.
//
// An empty docID is a person starting their own clock without first choosing a
// job, which is the ordinary case for anybody who is not standing at a machine.
// It carries no authorisation check because there is nothing to authorise: the
// row is their own time and it points at nothing.
//
// With a document it needs comment access, not edit. Recording that you worked
// on something is doing the work rather than changing what the work is, which
// is the same line checklists draw.
func (s *Service) Start(ctx context.Context, orgID string, actor core.Subjects, docID, note string) (*Entry, error) {
	if docID != "" {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
			return nil, err
		}
	}
	principal := actor.Principal()

	e := &Entry{ID: uuid.NewString(), DocumentID: docID, PrincipalID: principal,
		Note: strings.TrimSpace(note)}
	err := s.db.QueryRow(ctx, `
		INSERT INTO time_entries (id, org_id, document_id, principal_id, note)
		VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5)
		RETURNING started_at`, e.ID, orgID, docID, principal, e.Note).Scan(&e.StartedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("timelog: start: %w", err)
	}
	return e, nil
}

// AutoApprovedNote is what an auto-approved entry's decision says, so the
// record reads as what it is: the system's rule, not a person signing off.
const AutoApprovedNote = "Auto-approved by the system: this person's time is approved automatically."

// autoApproveIf is the test for approving an entry on the way in: the person
// (principals p) has the setting on, and the entry's work starts at or after the
// moment it was turned on. startExpr is the entry's start. Never null.
func autoApproveIf(startExpr string) string {
	return "(p.time_auto_approve_since IS NOT NULL AND " + startExpr + " >= p.time_auto_approve_since)"
}

// autoApproveColumns is the five approval columns for an INSERT ... SELECT from
// principals p, with $9 the note.
func autoApproveColumns(startExpr string) string {
	c := autoApproveIf(startExpr)
	return "NULLIF(" + c + ", false), CASE WHEN " + c + " THEN now() END, CASE WHEN " + c +
		" THEN p.id END, CASE WHEN " + c + " THEN $9::text ELSE '' END, " + c
}

// Stop ends whichever timer this person has running.
//
// The duration is computed from the clock rather than trusted from the caller,
// because the browser's clock is not the one the rest of the record uses.
func (s *Service) Stop(ctx context.Context, orgID, principalID string) (*Entry, error) {
	e := &Entry{PrincipalID: principalID}
	err := s.db.QueryRow(ctx, `
		UPDATE time_entries t
		   SET ended_at = now(),
		       minutes = GREATEST(0, ROUND(EXTRACT(EPOCH FROM (now() - t.started_at)) / 60))::int,
		       approved = NULLIF(`+autoApproveIf("t.started_at")+`, false),
		       decided_at = CASE WHEN `+autoApproveIf("t.started_at")+` THEN now() END,
		       decided_by = CASE WHEN `+autoApproveIf("t.started_at")+` THEN p.id END,
		       decided_note = CASE WHEN `+autoApproveIf("t.started_at")+` THEN $3::text ELSE '' END,
		       auto_approved = `+autoApproveIf("t.started_at")+`
		  FROM principals p
		 WHERE t.principal_id = $1 AND t.org_id = $2 AND t.ended_at IS NULL AND p.id = t.principal_id
		RETURNING t.id::text, COALESCE(t.document_id::text,''), t.started_at, t.ended_at, t.minutes, t.note`,
		principalID, orgID, AutoApprovedNote).
		Scan(&e.ID, &e.DocumentID, &e.StartedAt, &e.EndedAt, &e.Minutes, &e.Note)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotRunning
	case err != nil:
		return nil, fmt.Errorf("timelog: stop: %w", err)
	}
	return e, nil
}

// Running returns this person's open timer, if any.
func (s *Service) Running(ctx context.Context, orgID, principalID string) (*Entry, error) {
	e := &Entry{PrincipalID: principalID}
	// LEFT JOIN, because a timer no longer has to point at anything. An inner
	// join here would make an unattached running timer invisible to the top
	// bar, so the one control that stops it would never render and the clock
	// would run all night. That is the exact failure the top bar exists to
	// prevent.
	err := s.db.QueryRow(ctx, `
		SELECT t.id::text, COALESCE(t.document_id::text,''),
		       COALESCE(NULLIF(d.title,''), '') , t.started_at, t.note
		FROM time_entries t
		LEFT JOIN documents d ON d.id = t.document_id
		WHERE t.principal_id = $1 AND t.org_id = $2 AND t.ended_at IS NULL
		  AND t.deleted_at IS NULL`,
		principalID, orgID).
		Scan(&e.ID, &e.DocumentID, &e.DocTitle, &e.StartedAt, &e.Note)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("timelog: running: %w", err)
	}
	return e, nil
}

// Log records work that has already happened.
//
// The manual path, and the one most entries come through. It takes a duration
// and a day rather than a start and an end, because that is what somebody
// remembers: "about forty minutes, yesterday".
func (s *Service) Log(ctx context.Context, orgID string, actor core.Subjects,
	docID string, minutes int, on time.Time, note string) (*Entry, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
		return nil, err
	}
	if minutes <= 0 {
		return nil, errors.New("timelog: how long did it take?")
	}
	if minutes > 100000 {
		return nil, errors.New("timelog: that is more than two months of work")
	}
	if on.IsZero() {
		on = time.Now()
	}

	e := &Entry{
		ID: uuid.NewString(), DocumentID: docID, PrincipalID: actor.Principal(),
		StartedAt: on, Minutes: &minutes, Note: strings.TrimSpace(note),
	}
	end := on.Add(time.Duration(minutes) * time.Minute)
	e.EndedAt = &end

	tag, err := s.db.Exec(ctx, `
		INSERT INTO time_entries (id, org_id, document_id, principal_id,
		                          started_at, ended_at, minutes, note,
		                          approved, decided_at, decided_by, decided_note, auto_approved)
		SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::timestamptz,$6::timestamptz,$7::int,$8::text,`+autoApproveColumns("$5::timestamptz")+`
		  FROM principals p WHERE p.id = $4::uuid`,
		e.ID, orgID, docID, e.PrincipalID, on, end, minutes, e.Note, AutoApprovedNote)
	if err != nil {
		return nil, fmt.Errorf("timelog: log: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, errors.New("timelog: no such person")
	}
	return e, nil
}

// Correct changes a finished entry's duration, day, and note. Its author, or
// an admin of the document, because a timesheet nobody can fix is a
// timesheet nobody trusts.
//
// on is nil when the day is not changing -- most corrections are "I typed the
// wrong number," not "I logged it against the wrong day," and a form that
// forced a date choice every time would make the ordinary case slower for
// the sake of the rare one.
func (s *Service) Correct(ctx context.Context, orgID string, actor core.Subjects,
	entryID string, minutes int, on *time.Time, note string) error {

	deletedAt, err := s.entryAuthority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	// Unlike Remove/Restore, this is not idempotent-away: editing the content
	// of something currently marked gone is a real conflict, not a stale
	// double-click, so it stays a refusal rather than silently reviving it as
	// a side effect of a correction.
	if deletedAt != nil {
		return ErrDeleted
	}
	if minutes < 0 || minutes > 100000 {
		return errors.New("timelog: that duration is not plausible")
	}

	// make_interval rather than concatenating the number into a string. The
	// same parameter is a count here and a duration on the next line, and
	// building the interval by concatenation makes Postgres deduce two
	// different types for one placeholder and refuse the whole statement.
	//
	// started_at moves only when a new day was given (COALESCE keeps it
	// otherwise), but ended_at is now recomputed unconditionally from
	// whichever started_at applies plus the corrected minutes -- previously
	// it only moved the first time, while the entry still had none, so a
	// second correction left ended_at quietly wrong against the minutes it
	// was supposed to describe.
	_, err = s.db.Exec(ctx, `
		UPDATE time_entries
		   SET minutes = $2::int,
		       started_at = COALESCE($4::timestamptz, started_at),
		       ended_at = COALESCE($4::timestamptz, started_at) + make_interval(mins => $2::int),
		       note = COALESCE(NULLIF($3::text, ''), note)
		 WHERE id = $1`, entryID, minutes, strings.TrimSpace(note), on)
	if err != nil {
		return fmt.Errorf("timelog: correct: %w", err)
	}
	return nil
}

// entryAuthority is the permission Correct, Remove, and Restore all share:
// the entry's own author, or an admin of the document it is filed against.
// An unattached entry has no document to check admin on, so nobody but its
// author may touch it. Checked before looking at deleted_at at all, so a
// stranger without the authority to act on an entry cannot learn whether it
// is deleted either.
func (s *Service) entryAuthority(ctx context.Context, orgID string, actor core.Subjects, entryID string) (deletedAt *time.Time, err error) {
	var author string
	var docID *string
	err = s.db.QueryRow(ctx,
		`SELECT principal_id::text, document_id::text, deleted_at FROM time_entries
		  WHERE id=$1 AND org_id=$2`, entryID, orgID).Scan(&author, &docID, &deletedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("timelog: load entry: %w", err)
	}
	if author == actor.Principal() {
		return deletedAt, nil
	}
	if docID == nil {
		return nil, ErrForbidden
	}
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: *docID}, core.AccessAdmin); err != nil {
		return nil, err
	}
	return deletedAt, nil
}

// Remove takes an entry out of every list that reads time_entries, without
// destroying it. A logged hour is somebody's paid work; a mis-click should
// not be able to make it unrecoverable the way DELETE FROM did before this.
//
// Idempotent: removing an already-removed entry is a no-op, not an error.
// The alternative punishes exactly the case a stale page produces -- two tabs
// open, or a page loaded before somebody else's Undo, and a second click on
// the same button that should just agree the entry is gone.
func (s *Service) Remove(ctx context.Context, orgID string, actor core.Subjects, entryID string) error {
	deletedAt, err := s.entryAuthority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if deletedAt != nil {
		return nil
	}
	// The removal and the record of who did it are one transaction: an entry
	// that is gone with nobody attached is what an audit log exists to answer.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timelog: remove: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before RemovedEntry
	err = tx.QueryRow(ctx, `
		UPDATE time_entries SET deleted_at = now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL
		RETURNING principal_id::text, document_id::text, minutes, started_at, note`,
		entryID, orgID).Scan(&before.Person, &before.Document, &before.Minutes, &before.StartedAt, &before.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // somebody else's click got there first
	}
	if err != nil {
		return fmt.Errorf("timelog: remove: %w", err)
	}
	if err := audit.Record(ctx, tx, removalEntry(orgID, actor.Principal(), entryID, "remove", "", before)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("timelog: remove: %w", err)
	}
	return nil
}

// Restore undoes a Remove. Same authority as Remove, and idempotent the same
// way: restoring an entry that is not removed leaves it exactly as it was
// rather than refusing, for the same reason -- a stale "Show removed" list
// still offering Restore on something already brought back by the page's own
// Undo banner is a real sequence, not a misuse, and it should just work.
func (s *Service) Restore(ctx context.Context, orgID string, actor core.Subjects, entryID string) error {
	deletedAt, err := s.entryAuthority(ctx, orgID, actor, entryID)
	if err != nil {
		return err
	}
	if deletedAt == nil {
		return nil
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE time_entries SET deleted_at = NULL WHERE id=$1`, entryID); err != nil {
		return fmt.Errorf("timelog: restore: %w", err)
	}
	return nil
}

// Entries lists the work recorded against one document.
func (s *Service) Entries(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]Entry, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, t.document_id::text, t.principal_id::text,
		       COALESCE(NULLIF(p.display_name,''), t.principal_id::text),
		       t.started_at, t.ended_at, t.minutes, t.note,
		       COALESCE((SELECT array_agg(tg.id::text ORDER BY tg.name)
		                   FROM time_entry_tag_links l JOIN time_entry_tags tg ON tg.id = l.tag_id
		                  WHERE l.entry_id = t.id), '{}'),
		       COALESCE((SELECT array_agg(tg.name ORDER BY tg.name)
		                   FROM time_entry_tag_links l JOIN time_entry_tags tg ON tg.id = l.tag_id
		                  WHERE l.entry_id = t.id), '{}')
		FROM time_entries t
		JOIN principals p ON p.id = t.principal_id
		WHERE t.document_id = $1 AND t.org_id = $2 AND t.deleted_at IS NULL
		ORDER BY t.started_at DESC, t.id`, docID, orgID)
	if err != nil {
		return nil, fmt.Errorf("timelog: entries: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var tagIDs, tagNames []string
		if err := rows.Scan(&e.ID, &e.DocumentID, &e.PrincipalID, &e.PersonName,
			&e.StartedAt, &e.EndedAt, &e.Minutes, &e.Note, &tagIDs, &tagNames); err != nil {
			return nil, fmt.Errorf("timelog: scan entry: %w", err)
		}
		for i := range tagIDs {
			e.Tags = append(e.Tags, Tag{ID: tagIDs[i], Name: tagNames[i]})
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Estimate against actual
// -----------------------------------------------------------------------------

// Effort is what a job was expected to take against what it did.
type Effort struct {
	EstimateMinutes *int
	ActualMinutes   int
	// CostCents is nil when nobody involved has a rate, which is different
	// from zero. "We do not measure this" and "this was free" must not look
	// the same.
	CostCents *int
	Entries   int
}

// HasEstimate reports whether anybody has said how long this should take.
//
// Separate from the percentage rather than returned alongside it, because a
// template can only call a method that returns one value, and "was there an
// estimate" is a question the page has to ask before showing a number.
func (e Effort) HasEstimate() bool {
	return e.EstimateMinutes != nil && *e.EstimateMinutes > 0
}

// OverPercent is how far past the estimate the work went. Zero when nobody
// estimated it, which HasEstimate exists to distinguish from on target.
func (e Effort) OverPercent() int {
	if !e.HasEstimate() {
		return 0
	}
	return (e.ActualMinutes - *e.EstimateMinutes) * 100 / *e.EstimateMinutes
}

// EffortFor reads one document's estimate and actual.
func (s *Service) EffortFor(ctx context.Context, orgID string, actor core.Subjects, docID string) (*Effort, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	e := &Effort{}
	err := s.db.QueryRow(ctx, `
		SELECT t.estimate_minutes,
		       COALESCE(SUM(te.minutes), 0)::int,
		       COUNT(te.id)::int,
		       -- Null unless somebody involved has a rate, so an unmeasured
		       -- job reads as unknown rather than as free.
		       SUM(te.minutes * c.rate_cents / 60.0)::int
		FROM documents d
		LEFT JOIN task_fields t  ON t.document_id = d.id
		LEFT JOIN time_entries te ON te.document_id = d.id AND te.minutes IS NOT NULL AND te.deleted_at IS NULL
		LEFT JOIN capacity c      ON c.principal_id = te.principal_id
		WHERE d.id = $1 AND d.org_id = $2
		GROUP BY t.estimate_minutes`, docID, orgID).
		Scan(&e.EstimateMinutes, &e.ActualMinutes, &e.Entries, &e.CostCents)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &Effort{}, nil
	case err != nil:
		return nil, fmt.Errorf("timelog: effort: %w", err)
	}
	return e, nil
}

// SetEstimate writes task_fields.estimate_minutes, which has been a column with
// nothing writing to it since the substrate landed.
func (s *Service) SetEstimate(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes *int) error {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessEdit); err != nil {
		return err
	}
	if minutes != nil && (*minutes < 0 || *minutes > 100000) {
		return errors.New("timelog: that estimate is not plausible")
	}
	ct, err := s.db.Exec(ctx,
		`UPDATE task_fields SET estimate_minutes = $2 WHERE document_id = $1`, docID, minutes)
	if err != nil {
		return fmt.Errorf("timelog: set estimate: %w", err)
	}
	if ct.RowsAffected() == 0 {
		// A kind with no lifecycle. An asset does not take an estimate, and
		// silently doing nothing would look like the field had saved.
		return errors.New("timelog: this kind of thing has no estimate")
	}
	return nil
}

// AssetEffort is the roll-up: everything spent on work orders that concern one
// asset.
type AssetEffort struct {
	AssetID    string
	AssetTitle string
	Minutes    int
	CostCents  *int
	WorkOrders int
}

// ForAsset totals the work done on everything linked to an asset.
//
// Through entity_links rather than a column, because the link already exists
// and was the whole point of having one table for relationships. An asset with
// a maintenance history and no cost cannot justify a replacement, which is the
// decision this number is for.
func (s *Service) ForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string) (*AssetEffort, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	out := &AssetEffort{AssetID: assetID}
	err := s.db.QueryRow(ctx, `
		WITH related AS (
		    SELECT from_id AS id FROM entity_links
		     WHERE org_id = $1 AND to_type = 'document' AND to_id = $2 AND from_type = 'document'
		    UNION
		    SELECT to_id AS id FROM entity_links
		     WHERE org_id = $1 AND from_type = 'document' AND from_id = $2 AND to_type = 'document'
		)
		SELECT COALESCE(NULLIF(a.title,''), 'Untitled'),
		       COALESCE(SUM(te.minutes), 0)::int,
		       SUM(te.minutes * c.rate_cents / 60.0)::int,
		       COUNT(DISTINCT te.document_id)::int
		FROM documents a
		LEFT JOIN related r ON true
		LEFT JOIN time_entries te ON te.document_id = r.id AND te.minutes IS NOT NULL AND te.deleted_at IS NULL
		LEFT JOIN capacity c ON c.principal_id = te.principal_id
		WHERE a.id = $2 AND a.org_id = $1
		GROUP BY a.title`, orgID, assetID).
		Scan(&out.AssetTitle, &out.Minutes, &out.CostCents, &out.WorkOrders)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return out, nil
	case err != nil:
		return nil, fmt.Errorf("timelog: asset effort: %w", err)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Workload
// -----------------------------------------------------------------------------

// Load is one person's week.
type Load struct {
	PrincipalID string
	Name        string
	// Assigned is the estimated minutes of open work pointed at them.
	Assigned int
	// Logged is what they have actually recorded in the window.
	Logged int
	// Capacity is their week, in minutes.
	Capacity int
	// Open is how many jobs are on them.
	Open      int
	RateCents *int
}

// Percent is assigned work against capacity. Over a hundred is the number
// somebody is looking for.
func (l Load) Percent() int {
	if l.Capacity <= 0 {
		return 0
	}
	return l.Assigned * 100 / l.Capacity
}

// Workload reports who is overloaded across a date range.
//
// Cancelled and completed work is excluded, and that exclusion is the whole
// correctness of the number: counting a cancelled work order towards somebody's
// week is how a planner concludes a person is full when they are free, and it
// is the mistake every homegrown version of this makes first.
// Two checks before anything is read, and this method had neither.
//
// It was the only one in the package that took no actor and asked nothing, and
// it is registered on a route any signed-in person can reach, so everybody
// could see every colleague's open jobs, assigned hours, capacity and hours
// logged. The rates were never rendered to them, because the template hid the
// only thing that printed one, but they were carried into the page struct all
// the same. Hiding a pay rate in the markup is not withholding it.
func (s *Service) Workload(ctx context.Context, orgID, actorID string, from, to time.Time) ([]Load, error) {
	seeAll, err := s.authz.HasCapability(ctx, orgID, actorID, capTimeAll)
	if err != nil {
		return nil, err
	}
	if !seeAll {
		return nil, ErrNotAllowed
	}
	// Money is a separate grant, so an operations manager can plan the week
	// without being shown what anybody is paid.
	seeMoney, err := s.authz.HasCapability(ctx, orgID, actorID, capFinance)
	if err != nil {
		return nil, err
	}

	if to.Before(from) {
		from, to = to, from
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text,
		       COALESCE(NULLIF(p.display_name,''), p.id::text),
		       -- d.id IS NOT NULL is load-bearing. task_fields joins straight to
		       -- the person, and the document join is what applies "not deleted"
		       -- and "due this week". Without it the sum counts estimates from
		       -- deleted work and from jobs due next quarter, so a planner reads
		       -- somebody as full over work that no longer exists.
		       COALESCE(SUM(t.estimate_minutes) FILTER (
		           WHERE d.id IS NOT NULL AND t.status NOT IN ('done','cancelled')), 0)::int,
		       COALESCE(cap.minutes_per_week, 2400),
		       COUNT(d.id) FILTER (WHERE t.status NOT IN ('done','cancelled'))::int,
		       cap.rate_cents,
		       COALESCE((SELECT SUM(te.minutes) FROM time_entries te
		                  WHERE te.principal_id = p.id AND te.org_id = $1
		                    AND te.minutes IS NOT NULL AND te.deleted_at IS NULL
		                    AND te.started_at >= $2 AND te.started_at < $3), 0)::int
		FROM principals p
		LEFT JOIN capacity cap ON cap.principal_id = p.id
		LEFT JOIN task_fields t ON t.assignee_id = p.id
		LEFT JOIN documents d
		       ON d.id = t.document_id AND d.org_id = $1 AND d.deleted_at IS NULL
		      AND (t.due_at IS NULL OR (t.due_at >= $2 AND t.due_at < $3))
		WHERE p.org_id = $1 AND p.kind = 'user' AND p.status = 'active'
		GROUP BY p.id, p.display_name, cap.minutes_per_week, cap.rate_cents
		ORDER BY 3 DESC, 2`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("timelog: workload: %w", err)
	}
	defer rows.Close()

	var out []Load
	for rows.Next() {
		var l Load
		if err := rows.Scan(&l.PrincipalID, &l.Name, &l.Assigned, &l.Capacity,
			&l.Open, &l.RateCents, &l.Logged); err != nil {
			return nil, fmt.Errorf("timelog: scan load: %w", err)
		}
		// Dropped here rather than left for the template to hide. A value that
		// reaches the page struct is one template edit away from the page, and
		// somebody's pay is not a thing to leave sitting there relying on an
		// {{if}} nobody is looking at.
		if !seeMoney {
			l.RateCents = nil
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ReportRow is one logged, finished entry as billing needs to see it: who,
// when, how long, what it says, and which space it was filed under -- close
// enough to "which customer" for an org that keeps one space per client, nowhere
// close for one that does not, so the caller decides what to do with it rather
// than this package pretending to know.
type ReportRow struct {
	ID          string
	PrincipalID string
	PersonName  string
	DocTitle    string
	SpaceName   string
	StartedAt   time.Time
	Minutes     int
	Note        string
	Tags        []string
}

// ReportTotal is one person's total for a range -- the roll-up billing
// actually wants first: who, and how much, before anybody reads a single
// entry. TimeReport's full detail is one click away, scoped to this person.
type ReportTotal struct {
	PrincipalID string
	PersonName  string
	Minutes     int
}

// requireBilling is the gate both report queries share: Finance alone, not
// TimeAll. Workload is a planning tool -- an operations manager needs it and
// does not need to be handed money. This is a billing tool, and the two
// audiences are not assumed to be the same people.
func (s *Service) requireBilling(ctx context.Context, orgID, actorID string) error {
	billing, err := s.authz.HasCapability(ctx, orgID, actorID, capFinance)
	if err != nil {
		return err
	}
	if !billing {
		return ErrNotAllowed
	}
	return nil
}

// TimeReportSummary totals finished, logged hours per person across the org
// for a date range -- the whole-org view: who logged what, before anybody
// reads a single entry.
func (s *Service) TimeReportSummary(ctx context.Context, orgID, actorID string, from, to time.Time) ([]ReportTotal, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return nil, err
	}
	if to.Before(from) {
		from, to = to, from
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, COALESCE(NULLIF(p.display_name,''), p.id::text),
		       SUM(t.minutes)::int
		  FROM time_entries t
		  JOIN principals p ON p.id = t.principal_id
		 WHERE t.org_id = $1 AND t.minutes IS NOT NULL AND t.deleted_at IS NULL
		   AND t.started_at >= $2 AND t.started_at < $3
		 GROUP BY p.id, p.display_name
		 ORDER BY p.display_name`, orgID, from, to)
	if err != nil {
		return nil, fmt.Errorf("timelog: report summary: %w", err)
	}
	defer rows.Close()

	var out []ReportTotal
	for rows.Next() {
		var r ReportTotal
		if err := rows.Scan(&r.PrincipalID, &r.PersonName, &r.Minutes); err != nil {
			return nil, fmt.Errorf("timelog: scan report total: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TimeReport lists finished, logged hours across the org for a date range,
// optionally narrowed to one person -- the drill-down from
// TimeReportSummary, or the same list unfiltered if principalID is empty.
func (s *Service) TimeReport(ctx context.Context, orgID, actorID, principalID string, from, to time.Time) ([]ReportRow, error) {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return nil, err
	}

	if to.Before(from) {
		from, to = to, from
	}
	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, p.id::text, COALESCE(NULLIF(p.display_name,''), p.id::text),
		       COALESCE(d.title, ''), COALESCE(sp.name, ''),
		       t.started_at, t.minutes, t.note,
		       -- A correlated subquery rather than a join, so this stays one
		       -- row per entry and does not need the whole SELECT list
		       -- turned into a GROUP BY just to aggregate tag names.
		       COALESCE((SELECT array_agg(tg.name ORDER BY tg.name)
		                   FROM time_entry_tag_links l
		                   JOIN time_entry_tags tg ON tg.id = l.tag_id
		                  WHERE l.entry_id = t.id), '{}')
		  FROM time_entries t
		  JOIN principals p ON p.id = t.principal_id
		  LEFT JOIN documents d ON d.id = t.document_id
		  LEFT JOIN spaces sp ON sp.id = d.space_id
		 WHERE t.org_id = $1 AND t.minutes IS NOT NULL AND t.deleted_at IS NULL
		   AND t.started_at >= $2 AND t.started_at < $3
		   AND ($4 = '' OR t.principal_id::text = $4)
		 ORDER BY p.display_name, t.started_at`, orgID, from, to, principalID)
	if err != nil {
		return nil, fmt.Errorf("timelog: report: %w", err)
	}
	defer rows.Close()

	var out []ReportRow
	for rows.Next() {
		var r ReportRow
		if err := rows.Scan(&r.ID, &r.PrincipalID, &r.PersonName, &r.DocTitle, &r.SpaceName,
			&r.StartedAt, &r.Minutes, &r.Note, &r.Tags); err != nil {
			return nil, fmt.Errorf("timelog: scan report row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TimeReportRows hands every finished, logged entry in the range to fn, one at
// a time, for the people named (everybody when principalIDs is empty). It is the
// detail export's read: the same rows, order and billing gate as TimeReport, but
// streamed off the cursor so a year of everybody's time never has to sit in
// memory, and so the first bytes of the file can leave before the last row is
// read. The gate runs before any row, so a refusal arrives before anything has
// been written.
func (s *Service) TimeReportRows(ctx context.Context, orgID, actorID string, principalIDs []string, from, to time.Time, fn func(ReportRow) error) error {
	if err := s.requireBilling(ctx, orgID, actorID); err != nil {
		return err
	}
	if to.Before(from) {
		from, to = to, from
	}
	if principalIDs == nil {
		principalIDs = []string{}
	}
	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, p.id::text, COALESCE(NULLIF(p.display_name,''), p.id::text),
		       COALESCE(d.title, ''), COALESCE(sp.name, ''),
		       t.started_at, t.minutes, t.note,
		       COALESCE((SELECT array_agg(tg.name ORDER BY tg.name)
		                   FROM time_entry_tag_links l
		                   JOIN time_entry_tags tg ON tg.id = l.tag_id
		                  WHERE l.entry_id = t.id), '{}')
		  FROM time_entries t
		  JOIN principals p ON p.id = t.principal_id
		  LEFT JOIN documents d ON d.id = t.document_id
		  LEFT JOIN spaces sp ON sp.id = d.space_id
		 WHERE t.org_id = $1 AND t.minutes IS NOT NULL AND t.deleted_at IS NULL
		   AND t.started_at >= $2 AND t.started_at < $3
		   AND (cardinality($4::text[]) = 0 OR t.principal_id::text = ANY($4::text[]))
		 ORDER BY p.display_name, p.id, t.started_at, t.id`, orgID, from, to, principalIDs)
	if err != nil {
		return fmt.Errorf("timelog: report rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ReportRow
		if err := rows.Scan(&r.ID, &r.PrincipalID, &r.PersonName, &r.DocTitle, &r.SpaceName,
			&r.StartedAt, &r.Minutes, &r.Note, &r.Tags); err != nil {
			return fmt.Errorf("timelog: scan report row: %w", err)
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SetCapacity records somebody's week and their rate.
func (s *Service) SetCapacity(ctx context.Context, orgID, principalID string, minutesPerWeek int, rateCents *int) error {
	if minutesPerWeek < 0 || minutesPerWeek > 10080 {
		return errors.New("timelog: that is not a week")
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO capacity (principal_id, org_id, minutes_per_week, rate_cents)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (principal_id) DO UPDATE
		    SET minutes_per_week = EXCLUDED.minutes_per_week,
		        rate_cents = EXCLUDED.rate_cents, updated_at = now()`,
		principalID, orgID, minutesPerWeek, rateCents)
	if err != nil {
		return fmt.Errorf("timelog: set capacity: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// DeleteImported removes entries an importer wrote against a document.
//
// Matched on the note the importer stamps, which is what keeps it from
// touching hours somebody logged here by hand. An import that replaced every
// entry on a document would quietly delete real timesheet rows the first time
// somebody re-ran it.
//
// No authorisation check: the caller is a migration running as the owner, and
// there is no requesting human to check against.
func (s *Service) DeleteImported(ctx context.Context, orgID, docID, note string) error {
	// The rows go and the record of them goes in the same transaction, so a
	// re-import that cleared hours leaves a row saying how many and which.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timelog: clear imported: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ids []string
	var minutes int
	rows, err := tx.Query(ctx,
		`DELETE FROM time_entries WHERE org_id=$1 AND document_id=$2 AND note=$3
		 RETURNING id::text, COALESCE(minutes,0)`, orgID, docID, note)
	if err != nil {
		return fmt.Errorf("timelog: clear imported: %w", err)
	}
	for rows.Next() {
		var id string
		var m int
		if err := rows.Scan(&id, &m); err != nil {
			rows.Close()
			return fmt.Errorf("timelog: clear imported: %w", err)
		}
		ids = append(ids, id)
		minutes += m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("timelog: clear imported: %w", err)
	}
	if len(ids) > 0 {
		if err := audit.Record(ctx, tx, audit.Entry{
			OrgID: orgID, Action: audit.ActionTimeRemoveImported, EntityType: "document", EntityID: docID,
			After: map[string]any{"via": "importer re-run", "note": note, "entries": len(ids), "minutes": minutes, "ids": ids},
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("timelog: clear imported: %w", err)
	}
	return nil
}
