// Package calls is the other half of how work arrives.
//
// Half of everything a maintenance team is asked to do arrives by phone, and
// none of it is anywhere afterwards. A call log is not a feature anybody asks
// for and it is the difference between "we were never told" and "you rang at
// half past three on Tuesday and spoke to Dana".
//
// A recording is an ordinary attachment, verified by the same SHA-256 as every
// other file. A second storage path for audio would be a second thing to back
// up and a second place for a checksum to go unchecked.
package calls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/files"
)

var (
	ErrNotFound = errors.New("call not found")
	ErrInvalid  = errors.New("that is not a call")
)

// Directions and outcomes.
const (
	In  = "in"
	Out = "out"

	Spoke     = "spoke"
	Voicemail = "voicemail"
	NoAnswer  = "no_answer"
	Engaged   = "engaged"
)

// Outcomes in the order a picker offers them.
var Outcomes = []string{Spoke, Voicemail, NoAnswer, Engaged}

var validOutcomes = map[string]bool{
	Spoke: true, Voicemail: true, NoAnswer: true, Engaged: true,
}

// Call is one conversation.
type Call struct {
	ID          string
	OrgID       string
	ContactID   *string
	ContactName string
	EntityID    *string
	EntityTitle string
	Direction   string
	Number      string
	StartedAt   time.Time
	DurationS   int
	Outcome     string
	Note        string
	RecordingID *string
	Recording   string
	LoggedBy    string
	ByName      string
}

// Minutes is the duration as a person says it.
func (c Call) Minutes() int { return (c.DurationS + 59) / 60 }

// Answered reports whether anybody picked up, which is the distinction a
// follow-up list is built on.
func (c Call) Answered() bool { return c.Outcome == Spoke }

// Authorizer is the slice of access control this package needs.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
}

// Files is the slice of storage this package needs, so a recording goes
// through the same upload and the same digest as every other attachment.
type Files interface {
	Upload(ctx context.Context, orgID, uploaderID, filename, contentType string, size int64, content io.Reader) (*files.File, error)
}

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
	files Files
}

func New(db *pgxpool.Pool, az Authorizer, f Files) *Service {
	return &Service{db: db, authz: az, files: f}
}

// Log records a call.
//
// Comment access on the work it concerns, the same line time entries and parts
// draw. Saying who rang is reporting what happened, not deciding what happens.
func (s *Service) Log(ctx context.Context, orgID string, actor core.Subjects, c Call) (*Call, error) {
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("calls: no acting principal")
	}
	if c.Direction != In && c.Direction != Out {
		return nil, fmt.Errorf("%w: a call goes in or out", ErrInvalid)
	}
	if c.Outcome == "" {
		c.Outcome = Spoke
	}
	if !validOutcomes[c.Outcome] {
		return nil, fmt.Errorf("%w: %q is not an outcome", ErrInvalid, c.Outcome)
	}
	if c.DurationS < 0 {
		c.DurationS = 0
	}
	if c.StartedAt.IsZero() {
		c.StartedAt = time.Now()
	}
	if c.StartedAt.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("%w: it cannot have happened in the future", ErrInvalid)
	}
	if c.ContactID != nil && *c.ContactID == "" {
		c.ContactID = nil
	}
	if c.EntityID != nil && *c.EntityID == "" {
		c.EntityID = nil
	}
	if c.ContactID == nil && strings.TrimSpace(c.Number) == "" {
		return nil, fmt.Errorf("%w: say who it was with, by contact or by number", ErrInvalid)
	}
	if c.EntityID != nil {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: *c.EntityID}, core.AccessComment); err != nil {
			return nil, err
		}
	}
	if c.ContactID != nil {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityContact, ID: *c.ContactID}, core.AccessView); err != nil {
			return nil, err
		}
	}

	c.ID, c.OrgID, c.LoggedBy = uuid.NewString(), orgID, principal
	_, err := s.db.Exec(ctx, `
		INSERT INTO calls (id, org_id, contact_id, entity_type, entity_id, direction,
		                   number, started_at, duration_s, outcome, note,
		                   recording_file_id, logged_by)
		VALUES ($1,$2,$3,'document',$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		c.ID, orgID, c.ContactID, c.EntityID, c.Direction, strings.TrimSpace(c.Number),
		c.StartedAt, c.DurationS, c.Outcome, strings.TrimSpace(c.Note),
		c.RecordingID, principal)
	if err != nil {
		return nil, fmt.Errorf("calls: log: %w", err)
	}
	return &c, nil
}

// AttachRecording stores audio against a call.
//
// Through the ordinary file service, so it gets the same SHA-256 on the way in
// and the same verification on the way back out. Audio is evidence more often
// than any other attachment, and evidence with an unchecked checksum is a
// recording somebody can argue with.
func (s *Service) AttachRecording(ctx context.Context, orgID string, actor core.Subjects,
	callID, filename, contentType string, size int64, content io.Reader) (*files.File, error) {

	c, err := s.Get(ctx, orgID, actor, callID)
	if err != nil {
		return nil, err
	}
	// Edit on the work it concerns, not comment. Attaching a recording of a
	// conversation is a heavier act than saying it happened.
	if c.EntityID != nil {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: *c.EntityID}, core.AccessEdit); err != nil {
			return nil, err
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	f, err := s.files.Upload(ctx, orgID, actor.Principal(), filename, contentType, size, content)
	if err != nil {
		return nil, fmt.Errorf("calls: upload recording: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE calls SET recording_file_id=$3 WHERE id=$1 AND org_id=$2`,
		callID, orgID, f.ID); err != nil {
		return nil, fmt.Errorf("calls: attach recording: %w", err)
	}
	return f, nil
}

const callSelect = `
	SELECT c.id::text, c.contact_id::text, COALESCE(ct.name, ''),
	       c.entity_id::text, COALESCE(d.title, ''), c.direction, c.number,
	       c.started_at, c.duration_s, c.outcome, c.note,
	       c.recording_file_id::text, COALESCE(f.filename, ''),
	       c.logged_by::text, COALESCE(NULLIF(p.display_name,''), '')
	FROM calls c
	JOIN principals p ON p.id = c.logged_by
	LEFT JOIN contacts ct ON ct.id = c.contact_id AND ct.deleted_at IS NULL
	LEFT JOIN documents d ON d.id = c.entity_id AND d.deleted_at IS NULL
	LEFT JOIN files f ON f.id = c.recording_file_id AND f.deleted_at IS NULL`

// Get returns one call if the caller may see what it was about.
func (s *Service) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Call, error) {
	rows, err := s.db.Query(ctx, callSelect+` WHERE c.id=$1 AND c.org_id=$2`, id, orgID)
	if err != nil {
		return nil, fmt.Errorf("calls: get: %w", err)
	}
	list, err := scan(rows, orgID)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	c := list[0]
	if err := s.maySee(ctx, actor, c); err != nil {
		return nil, ErrNotFound
	}
	return c, nil
}

// maySee checks the caller against whatever the call hangs off.
//
// A call attached to a work order is visible to whoever can see the work order.
// A call attached only to a contact follows the contact. A call attached to
// neither cannot exist, which Log enforces.
func (s *Service) maySee(ctx context.Context, actor core.Subjects, c *Call) error {
	if c.EntityID != nil {
		return s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: *c.EntityID}, core.AccessView)
	}
	if c.ContactID != nil {
		return s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityContact, ID: *c.ContactID}, core.AccessView)
	}
	// A number and nothing else. Visible to whoever logged it, because there
	// is nothing else to hang the question on.
	if actor.Principal() == c.LoggedBy {
		return nil
	}
	return ErrNotFound
}

// ForDocument lists the calls about one piece of work.
func (s *Service) ForDocument(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]*Call, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx,
		callSelect+` WHERE c.entity_id=$1 AND c.org_id=$2 ORDER BY c.started_at DESC`,
		docID, orgID)
	if err != nil {
		return nil, fmt.Errorf("calls: for document: %w", err)
	}
	defer rows.Close()
	return scan(rows, orgID)
}

// ForContact lists the calls with one person, which is the history somebody
// wants before ringing them again.
func (s *Service) ForContact(ctx context.Context, orgID string, actor core.Subjects, contactID string) ([]*Call, error) {
	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityContact, ID: contactID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx,
		callSelect+` WHERE c.contact_id=$1 AND c.org_id=$2 ORDER BY c.started_at DESC LIMIT 100`,
		contactID, orgID)
	if err != nil {
		return nil, fmt.Errorf("calls: for contact: %w", err)
	}
	defer rows.Close()
	return scan(rows, orgID)
}

// Remove deletes a call. Edit on the work it concerns, because the log is a
// record of what was said and erasing one is heavier than writing it.
func (s *Service) Remove(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	c, err := s.Get(ctx, orgID, actor, id)
	if err != nil {
		return err
	}
	if c.EntityID != nil {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: *c.EntityID}, core.AccessEdit); err != nil {
			return err
		}
	} else if c.LoggedBy != actor.Principal() {
		return ErrNotFound
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM calls WHERE id=$1 AND org_id=$2`, id, orgID); err != nil {
		return fmt.Errorf("calls: remove: %w", err)
	}
	return nil
}

func scan(rows pgx.Rows, orgID string) ([]*Call, error) {
	var out []*Call
	for rows.Next() {
		c := &Call{OrgID: orgID}
		if err := rows.Scan(&c.ID, &c.ContactID, &c.ContactName, &c.EntityID,
			&c.EntityTitle, &c.Direction, &c.Number, &c.StartedAt, &c.DurationS,
			&c.Outcome, &c.Note, &c.RecordingID, &c.Recording,
			&c.LoggedBy, &c.ByName); err != nil {
			return nil, fmt.Errorf("calls: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
