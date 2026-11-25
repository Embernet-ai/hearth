package timelog

// A person's own time, and the manager who signs it off.
//
// Everything in timelog.go is organised around a document: what did this job
// cost, was the estimate realistic, who is overloaded. That is the right shape
// for a maintenance system and the wrong shape for the question "what did I
// work on this week", which nothing here could answer until now.
//
// The routing is deliberately three rules and not a hierarchy. A hierarchy
// invites a recursive query, a cycle, and a Sunday afternoon working out why
// somebody's timesheet has no approver.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// ErrNotYours is returned when somebody tries to decide time they do not
// approve, including their own.
var ErrNotYours = errors.New("that time is not yours to approve")

// approverClause is the whole of the routing, in one place so the queue and the
// decision cannot disagree about who may act.
//
// $1 org, $2 the actor, and the query it is spliced into must have `p` bound to
// the person whose time it is.
//
//	super admin      sees and decides everything
//	p.approver_id    the individual override, for somebody who reports across
//	                 a department boundary
//	space_managers   the ordinary case: a manager of ANY department they are
//	                 in. Somebody in four departments can be approved by a
//	                 manager of any one of them, which is the same answer a
//	                 person would give and avoids inventing a primary
//	                 department that the org chart does not have.
//
// Self-approval is excluded by the callers rather than here, because the reason
// is different: the first two are about authority and that one is about it not
// being an approval at all.
//
// The COALESCE around the middle rule is load-bearing and was put here by a
// failing test. approver_id is null for almost everybody, and `null = $2` is
// null rather than false, so the whole OR chain evaluates to null whenever both
// EXISTS are false and no override is set. In a WHERE that behaves like false
// and nothing looks wrong; read back into a bool by CanApprove it is not false,
// it is an error. Same clause, two contexts, and only one of them noticed.
const approverClause = `(
	EXISTS (SELECT 1 FROM capability_grants g
	         WHERE g.org_id = $1 AND g.principal_id = $2
	           AND g.capability = 'super_admin')
	OR COALESCE(p.approver_id = $2, false)
	OR EXISTS (SELECT 1
	             FROM principal_departments pd
	             JOIN space_managers m ON m.space_id = pd.space_id
	            WHERE pd.principal_id = p.id AND m.principal_id = $2)
)`

// entryColumns is the full read of an entry with everything a page shows.
// `t` is the entry, `d` the document it may point at, `dp` whoever decided it.
//
// Tags come back as two parallel arrays, ids and names, rather than one
// aggregate of structs -- pgx scans a Postgres array straight into a Go
// slice, and the same ORDER BY on both keeps them lined up index for index.
// scanEntries below is what zips them back into []Tag.
const entryColumns = `
	t.id::text,
	COALESCE(t.document_id::text, ''),
	COALESCE(NULLIF(d.title, ''), ''),
	t.principal_id::text,
	COALESCE(NULLIF(p.display_name, ''), p.id::text),
	t.started_at, t.ended_at, t.minutes, t.note,
	t.approved, t.decided_at,
	COALESCE(t.decided_by::text, ''),
	COALESCE(NULLIF(dp.display_name, ''), ''),
	t.decided_note, t.deleted_at,
	COALESCE((SELECT array_agg(tg.id::text ORDER BY tg.name)
	            FROM time_entry_tag_links l JOIN time_entry_tags tg ON tg.id = l.tag_id
	           WHERE l.entry_id = t.id), '{}'),
	COALESCE((SELECT array_agg(tg.name ORDER BY tg.name)
	            FROM time_entry_tag_links l JOIN time_entry_tags tg ON tg.id = l.tag_id
	           WHERE l.entry_id = t.id), '{}')`

// entryJoins matches entryColumns. LEFT on all three: the document is now
// optional, and the decider only exists once somebody has decided.
const entryJoins = `
	FROM time_entries t
	JOIN principals p ON p.id = t.principal_id
	LEFT JOIN documents d ON d.id = t.document_id
	LEFT JOIN principals dp ON dp.id = t.decided_by`

func scanEntries(rows pgx.Rows) ([]Entry, error) {
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var tagIDs, tagNames []string
		if err := rows.Scan(&e.ID, &e.DocumentID, &e.DocTitle,
			&e.PrincipalID, &e.PersonName,
			&e.StartedAt, &e.EndedAt, &e.Minutes, &e.Note,
			&e.Approved, &e.DecidedAt, &e.DecidedBy, &e.DeciderName,
			&e.DecidedNote, &e.DeletedAt, &tagIDs, &tagNames); err != nil {
			return nil, err
		}
		// Same ORDER BY on both arrays in entryColumns is what makes this
		// zip valid -- index i in one is always index i in the other.
		for i := range tagIDs {
			e.Tags = append(e.Tags, Tag{ID: tagIDs[i], Name: tagNames[i]})
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Mine lists one person's own entries in a window, newest first.
//
// No authorisation check and none needed: the principal id is the caller's own,
// taken from their session by the handler rather than from the request. There
// is no parameter here that could be pointed at somebody else.
// showRemoved includes entries Remove has been called on, mixed in with
// active ones rather than as a separate list -- the only way anything
// removed before now was recoverable at all is finding it here and hitting
// Restore, so it has to actually be findable, not sitting behind a second
// page nobody thinks to visit.
func (s *Service) Mine(ctx context.Context, orgID, principalID string,
	from, to time.Time, showRemoved bool) ([]Entry, error) {

	if to.Before(from) {
		from, to = to, from
	}
	rows, err := s.db.Query(ctx, `SELECT `+entryColumns+entryJoins+`
		WHERE t.org_id = $1 AND t.principal_id = $2
		  AND t.started_at >= $3 AND t.started_at < $4
		  AND ($5::bool OR t.deleted_at IS NULL)
		ORDER BY t.started_at DESC, t.id`, orgID, principalID, from, to, showRemoved)
	if err != nil {
		return nil, fmt.Errorf("timelog: my time: %w", err)
	}
	return scanEntries(rows)
}

// MineTotal is the minutes one person recorded in a window, and how many of
// those are still waiting on somebody.
//
// Counted in SQL rather than by summing what Mine returned, so a page showing a
// week's total does not depend on having read every row of that week.
func (s *Service) MineTotal(ctx context.Context, orgID, principalID string,
	from, to time.Time) (total, pending int, err error) {

	if to.Before(from) {
		from, to = to, from
	}
	err = s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(minutes), 0)::int,
		       COALESCE(SUM(minutes) FILTER (WHERE decided_at IS NULL), 0)::int
		  FROM time_entries
		 WHERE org_id = $1 AND principal_id = $2
		   AND started_at >= $3 AND started_at < $4
		   AND ended_at IS NOT NULL AND deleted_at IS NULL`,
		orgID, principalID, from, to).Scan(&total, &pending)
	if err != nil {
		return 0, 0, fmt.Errorf("timelog: my total: %w", err)
	}
	return total, pending, nil
}

// LogOwn records finished work for the person doing the recording.
//
// The document is optional, which is the entire point of it existing beside
// Log. Log demands a document and checks comment access on it; here an empty
// docID means the hour stands on its own, and when one IS given the same access
// check applies, because attaching your time to somebody else's job is still
// writing to their job.
func (s *Service) LogOwn(ctx context.Context, orgID string, actor core.Subjects,
	docID string, minutes int, on time.Time, note string) (*Entry, error) {

	if docID != "" {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
			return nil, err
		}
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
		SELECT $1::uuid,$2::uuid,NULLIF($3::text,'')::uuid,$4::uuid,$5::timestamptz,$6::timestamptz,$7::int,$8::text,`+autoApproveColumns("$5::timestamptz")+`
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

// CanApprove reports whether the actor may decide this person's time.
//
// Self-approval is refused here and not left to the caller. Somebody signing
// off their own hours is not an approval, and a super admin is exactly the
// person for whom the rest of the clause would otherwise say yes.
func (s *Service) CanApprove(ctx context.Context, orgID, actorID, subjectID string) (bool, error) {
	if actorID == "" || actorID == subjectID {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT `+approverClause+`
		  FROM principals p WHERE p.id = $3 AND p.org_id = $1`,
		orgID, actorID, subjectID).Scan(&ok)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("timelog: can approve: %w", err)
	}
	return ok, nil
}

// Pending is the queue: finished work this actor may decide and has not.
//
// Oldest first, because the entry that has been waiting longest is the one
// somebody is chasing. A running timer never appears: there is nothing to
// approve until it stops, and the schema refuses a decision on one anyway.
func (s *Service) Pending(ctx context.Context, orgID, actorID string) ([]Entry, error) {
	rows, err := s.db.Query(ctx, `SELECT `+entryColumns+entryJoins+`
		WHERE t.org_id = $1
		  AND t.ended_at IS NOT NULL
		  AND t.decided_at IS NULL
		  AND t.deleted_at IS NULL
		  AND t.principal_id <> $2
		  AND `+approverClause+`
		ORDER BY t.started_at`, orgID, actorID)
	if err != nil {
		return nil, fmt.Errorf("timelog: pending: %w", err)
	}
	return scanEntries(rows)
}

// PendingCount is the same set, counted, for the badge on the rail.
func (s *Service) PendingCount(ctx context.Context, orgID, actorID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		  FROM time_entries t
		  JOIN principals p ON p.id = t.principal_id
		 WHERE t.org_id = $1
		   AND t.ended_at IS NOT NULL
		   AND t.decided_at IS NULL
		   AND t.deleted_at IS NULL
		   AND t.principal_id <> $2
		   AND `+approverClause, orgID, actorID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("timelog: pending count: %w", err)
	}
	return n, nil
}

// Decide approves an entry or sends it back with a reason.
//
// The authority check is inside the UPDATE rather than a SELECT before it. Two
// statements would leave a window between deciding somebody may act and acting,
// and more practically they would let a caller that forgot the first one write
// anyway. Nothing here can be reached without the clause.
//
// Sending back does not delete anything. The hours stay, the reason is attached,
// and the person can correct them, because a rejection that destroys the record
// leaves nobody able to see what was disputed.
func (s *Service) Decide(ctx context.Context, orgID, actorID, entryID string,
	approve bool, note string) error {

	d := Decision{EntryID: entryID, DeciderID: actorID, Note: strings.TrimSpace(note)}
	err := s.db.QueryRow(ctx, `
		UPDATE time_entries t
		   SET approved = $4, decided_at = now(), decided_by = $2,
		       decided_note = $5
		  FROM principals p
		 WHERE t.id = $3
		   AND t.org_id = $1
		   AND p.id = t.principal_id
		   AND t.ended_at IS NOT NULL
		   AND t.deleted_at IS NULL
		   AND t.principal_id <> $2
		   AND `+approverClause+`
		RETURNING t.principal_id::text, t.started_at, COALESCE(t.minutes, 0)`,
		orgID, actorID, entryID, approve, d.Note).Scan(&d.PersonID, &d.StartedAt, &d.Minutes)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("timelog: decide: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Denied and absent are the same answer on purpose. Telling somebody
		// that an entry exists but is not theirs to decide is telling them
		// about work they are not entitled to know about.
		return ErrNotYours
	}
	if !approve && s.notify != nil {
		s.notify.HoursSentBack(ctx, orgID, d)
	}
	return nil
}

// Reopen clears a decision so it can be made again.
//
// Somebody has to be able to undo an approval given in error, and the same
// people who could decide it can undo it. The entry returns to pending rather
// than flipping to the other answer, so the next decision is a decision rather
// than a correction of a correction.
func (s *Service) Reopen(ctx context.Context, orgID, actorID, entryID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE time_entries t
		   SET approved = NULL, decided_at = NULL, decided_by = NULL,
		       decided_note = ''
		  FROM principals p
		 WHERE t.id = $3
		   AND t.org_id = $1
		   AND p.id = t.principal_id
		   AND t.decided_at IS NOT NULL
		   AND t.principal_id <> $2
		   AND `+approverClause,
		orgID, actorID, entryID)
	if err != nil {
		return fmt.Errorf("timelog: reopen: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotYours
	}
	return nil
}

// Decided is the record of time already signed off.
//
// Pat approves his two people and Yara approves everybody else, and the
// ask was that Yara can see the whole approved record without having to sign
// anything off a second time. So this is visibility rather than a second stage:
// nothing here changes a decision, and an entry appears once it has one.
//
// Somebody granted time_all sees the whole company. Everybody else sees what
// they themselves decided, which is their own record of what they signed.
func (s *Service) Decided(ctx context.Context, orgID, actorID string,
	from, to time.Time, seeAll bool) ([]Entry, error) {

	if to.Before(from) {
		from, to = to, from
	}
	// The clause differs by one line, so it is written once with a flag rather
	// than as two queries that would drift.
	scope := "AND t.decided_by = $2"
	if seeAll {
		// Cast, because this is the only place $2 appears in the seeAll
		// query, and Postgres refuses a parameter it cannot give a type.
		scope = "AND $2::text IS NOT NULL"
	}

	rows, err := s.db.Query(ctx, `SELECT `+entryColumns+entryJoins+`
		WHERE t.org_id = $1
		  AND t.decided_at IS NOT NULL
		  AND t.started_at >= $3 AND t.started_at < $4
		  `+scope+`
		ORDER BY t.decided_at DESC, t.started_at DESC, t.id
		LIMIT 200`, orgID, actorID, from, to)
	if err != nil {
		return nil, fmt.Errorf("timelog: decided: %w", err)
	}
	return scanEntries(rows)
}
