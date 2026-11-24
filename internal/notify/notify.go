// Package notify tells people the things they need and nothing else.
//
// The measure of success is that nobody turns it off. Every decision here is
// downstream of that: your own actions never notify you, being assigned the
// same job twice produces one row, and an automatic watch comes with one
// obvious way to stop.
//
// Everything is driven by the outbox, so a notification cannot describe a
// change that was rolled back. That is not a theoretical concern: "your work
// order was completed" for a completion that failed to commit is the message
// that teaches somebody to stop reading these.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/push"
)

// Reasons somebody is being told something.
const (
	ReasonAssigned  = "assigned"
	ReasonMentioned = "mentioned"
	ReasonCommented = "commented"
	ReasonChanged   = "changed"
	ReasonDueSoon   = "due_soon"
)

// Watch reasons.
const (
	WatchManual    = "manual"
	WatchAssigned  = "assigned"
	WatchCommented = "commented"
	WatchCreated   = "created"
)

// Email preferences.
const (
	EmailOff       = "off"
	EmailImmediate = "immediate"
	EmailDigest    = "digest"
)

// Notification is one row of somebody's inbox.
type Notification struct {
	ID         string
	Reason     string
	EntityType string
	EntityID   string
	Title      string
	Excerpt    string
	ActorID    *string
	ActorName  string
	ReadAt     *time.Time
	CreatedAt  time.Time
}

// Href is where clicking it goes.
func (n *Notification) Href() string { return hrefOf(n.EntityType, n.EntityID) }

func hrefOf(entityType, entityID string) string {
	switch core.EntityType(entityType) {
	case core.EntityChannel:
		return "/channels/" + entityID
	case core.EntityDepartmentRequest:
		return "/departments/requests/" + entityID
	case core.EntityTimeOff:
		return "/time-off#r-" + entityID
	case core.EntityDeal:
		return "/deals/" + entityID
	case core.EntityCompany:
		return "/companies/" + entityID
	case core.EntityContact:
		return "/contacts/" + entityID
	default:
		return "/documents/" + entityID
	}
}

// Prefs is how somebody wants to be told.
type Prefs struct {
	Email string
	// QuietFrom and QuietTo are minutes past local midnight, or nil for none.
	QuietFrom *int
	QuietTo   *int
	Timezone  string
	DigestAt  int
}

// DefaultPrefs is what somebody gets before they have chosen.
//
// Immediate rather than digest. People arrive here from tools where being
// assigned something emails you at once, and a day's wait for the 8am digest
// reads as "Hearth didn't tell me" -- the exact complaint that moved a team off
// the inbox and back to chasing work by chat. Someone drowning in mail still
// has the digest and off to go to; the digest stays the fallback for quiet
// hours, so a 2am assignment waits for morning either way. DigestAt is kept so
// that fallback, and a later switch to digest, both land at 8am.
func DefaultPrefs() Prefs {
	return Prefs{Email: EmailImmediate, Timezone: "UTC", DigestAt: 8 * 60}
}

type Service struct {
	db     *pgxpool.Pool
	pusher Pusher
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Pusher rings a person's devices. It is the slice of the push package this one needs, so the inbox works
// exactly as before when no push service is configured.
type Pusher interface {
	Notify(ctx context.Context, m push.Message)
}

// SetPusher makes every notification that reaches an inbox also ring the person's devices. Called once at
// start, before anything is delivered.
func (s *Service) SetPusher(p Pusher) { s.pusher = p }

// pushKind files a notification under the kind a person chooses between. Requests that wait on a decision
// (time off, departments) and the answers to them are approvals; the rest keep the reason the inbox gave.
func pushKind(reason string, entityType core.EntityType) string {
	if entityType == core.EntityTimeOff || entityType == core.EntityDepartmentRequest {
		return push.KindApprovals
	}
	switch reason {
	case ReasonAssigned:
		return push.KindAssigned
	case ReasonMentioned:
		return push.KindMentioned
	case ReasonCommented:
		return push.KindCommented
	case ReasonDueSoon:
		return push.KindDueSoon
	default:
		return push.KindChanged
	}
}

// ring sends the push that copies a new inbox row. Quiet hours hold it, as they hold email: the row is in
// the inbox for when the quiet ends, and nobody is woken for a work order raised at three in the morning.
func (s *Service) ring(ctx context.Context, d Delivery) {
	if s.pusher == nil {
		return
	}
	p, err := s.Prefs(ctx, d.PrincipalID)
	if err == nil && p.Quiet(time.Now()) {
		return
	}
	n := &Notification{Reason: d.Reason, Title: d.Title}
	if d.ActorID != "" {
		_ = s.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(display_name,''), '') FROM principals WHERE id = $1`,
			d.ActorID).Scan(&n.ActorName)
	}
	s.pusher.Notify(ctx, push.Message{
		PrincipalID: d.PrincipalID,
		Kind:        pushKind(d.Reason, d.Ref.Type),
		Body:        n.Sentence(),
		URL:         hrefOf(string(d.Ref.Type), d.Ref.ID),
		Tag:         "hearth-" + string(d.Ref.Type) + "-" + d.Ref.ID,
	})
}

// -----------------------------------------------------------------------------
// Watching
// -----------------------------------------------------------------------------

// Watch records that somebody wants to hear about an entity.
//
// Idempotent, and an existing manual watch is never downgraded to an automatic
// one: somebody who deliberately followed a job should not have that quietly
// turned into something the system will unfollow for them later.
func (s *Service) Watch(ctx context.Context, principalID string, ref core.Ref, reason string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO watches (principal_id, entity_type, entity_id, reason)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (principal_id, entity_type, entity_id) DO UPDATE
		    SET reason = CASE WHEN watches.reason = 'manual' THEN 'manual'
		                      ELSE EXCLUDED.reason END`,
		principalID, string(ref.Type), ref.ID, reason)
	if err != nil {
		return fmt.Errorf("notify: watch: %w", err)
	}
	return nil
}

// Unwatch stops delivery. Immediately: the next event finds no row, so there is
// no window where one more arrives.
func (s *Service) Unwatch(ctx context.Context, principalID string, ref core.Ref) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM watches WHERE principal_id=$1 AND entity_type=$2 AND entity_id=$3`,
		principalID, string(ref.Type), ref.ID)
	if err != nil {
		return fmt.Errorf("notify: unwatch: %w", err)
	}
	return nil
}

// IsWatching reports whether somebody is following an entity, for the control
// that turns it off.
func (s *Service) IsWatching(ctx context.Context, principalID string, ref core.Ref) (bool, error) {
	var yes bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM watches
		               WHERE principal_id=$1 AND entity_type=$2 AND entity_id=$3)`,
		principalID, string(ref.Type), ref.ID).Scan(&yes)
	if err != nil {
		return false, fmt.Errorf("notify: is watching: %w", err)
	}
	return yes, nil
}

// Watchers lists who is following something, excluding one person.
//
// The exclusion is the actor, and it is not an optimisation. Being told about
// your own change is the single fastest way to teach somebody that these
// messages are noise.
func (s *Service) Watchers(ctx context.Context, ref core.Ref, except string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT principal_id::text FROM watches
		WHERE entity_type=$1 AND entity_id=$2 AND principal_id <> $3`,
		string(ref.Type), ref.ID, orZero(except))
	if err != nil {
		return nil, fmt.Errorf("notify: watchers: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("notify: scan watcher: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// orZero substitutes a uuid that cannot exist, so "except nobody" is still a
// valid comparison rather than a second query shape.
func orZero(id string) string {
	if id == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return id
}

// -----------------------------------------------------------------------------
// Delivering
// -----------------------------------------------------------------------------

// Delivery is one notification about to be written.
type Delivery struct {
	OrgID       string
	PrincipalID string
	Reason      string
	Ref         core.Ref
	Title       string
	Excerpt     string
	ActorID     string
	// DedupeKey names the EVENT and the recipient rather than the attempt, so
	// a job that runs twice writes one row.
	DedupeKey string
}

// Deliver writes one notification, or does nothing if it has already been
// written.
//
// Returns whether a row was created, so a caller can decide whether to send an
// email without asking the database a second time.
func (s *Service) Deliver(ctx context.Context, d Delivery) (bool, error) {
	if d.PrincipalID == "" || d.PrincipalID == d.ActorID {
		// Your own change never notifies you.
		return false, nil
	}
	var actor any
	if d.ActorID != "" {
		actor = d.ActorID
	}
	ct, err := s.db.Exec(ctx, `
		INSERT INTO notifications
		    (id, org_id, principal_id, reason, entity_type, entity_id,
		     title, excerpt, actor_id, dedupe_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (principal_id, dedupe_key) DO NOTHING`,
		uuid.NewString(), d.OrgID, d.PrincipalID, d.Reason,
		string(d.Ref.Type), d.Ref.ID, d.Title, d.Excerpt, actor, d.DedupeKey)
	if err != nil {
		return false, fmt.Errorf("notify: deliver: %w", err)
	}
	created := ct.RowsAffected() > 0
	if created {
		s.ring(ctx, d)
	}
	return created, nil
}

// Inbox returns somebody's notifications, newest first.
func (s *Service) Inbox(ctx context.Context, principalID string, unreadOnly bool, limit int) ([]*Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sql := `
		SELECT n.id::text, n.reason, n.entity_type, n.entity_id::text, n.title,
		       n.excerpt, n.actor_id::text,
		       COALESCE(NULLIF(a.display_name,''), ''),
		       n.read_at, n.created_at
		FROM notifications n
		LEFT JOIN principals a ON a.id = n.actor_id
		WHERE n.principal_id = $1`
	if unreadOnly {
		sql += ` AND n.read_at IS NULL`
	}
	sql += ` ORDER BY n.created_at DESC LIMIT $2`

	rows, err := s.db.Query(ctx, sql, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("notify: inbox: %w", err)
	}
	defer rows.Close()

	var out []*Notification
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.Reason, &n.EntityType, &n.EntityID, &n.Title,
			&n.Excerpt, &n.ActorID, &n.ActorName, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("notify: scan notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// seenClause keeps a notification about a document only while the person can
// still open the document. The row was written when it happened, with the title
// and a line of the text; a grant that has ended, been taken back, or never
// followed the work when it moved out of a folder must not leave that behind.
// $keys is the placeholder of the caller's subject keys.
func seenClause(keys string) string {
	return ` AND (n.entity_type <> 'document' OR EXISTS (
	    SELECT 1 FROM grants g
	     WHERE g.entity_type = 'document' AND g.entity_id = n.entity_id
	       AND g.subject_key = ANY(` + keys + `)
	       AND (g.expires_at IS NULL OR g.expires_at > now())))`
}

// InboxFor is Inbox for a signed in person, hiding what they can no longer open.
// A super admin reaches everything, so nothing is hidden from them.
func (s *Service) InboxFor(ctx context.Context, actor core.Subjects, unreadOnly bool, limit int) ([]*Notification, error) {
	if actor.Super() {
		return s.Inbox(ctx, actor.Principal(), unreadOnly, limit)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sql := `
		SELECT n.id::text, n.reason, n.entity_type, n.entity_id::text, n.title,
		       n.excerpt, n.actor_id::text,
		       COALESCE(NULLIF(a.display_name,''), ''),
		       n.read_at, n.created_at
		FROM notifications n
		LEFT JOIN principals a ON a.id = n.actor_id
		WHERE n.principal_id = $1` + seenClause("$3")
	if unreadOnly {
		sql += ` AND n.read_at IS NULL`
	}
	sql += ` ORDER BY n.created_at DESC LIMIT $2`
	rows, err := s.db.Query(ctx, sql, actor.Principal(), limit, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("notify: inbox: %w", err)
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.Reason, &n.EntityType, &n.EntityID, &n.Title,
			&n.Excerpt, &n.ActorID, &n.ActorName, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("notify: scan notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadFor is the badge for a signed in person, counting only what they can
// still open.
func (s *Service) UnreadFor(ctx context.Context, actor core.Subjects) (int, error) {
	if actor.Super() {
		return s.Unread(ctx, actor.Principal())
	}
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM notifications n WHERE n.principal_id = $1 AND n.read_at IS NULL`+seenClause("$2"),
		actor.Principal(), actor.Keys()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("notify: unread: %w", err)
	}
	return n, nil
}

// Unread is the badge.
func (s *Service) Unread(ctx context.Context, principalID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE principal_id=$1 AND read_at IS NULL`,
		principalID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("notify: unread: %w", err)
	}
	return n, nil
}

// MarkRead marks one, or all of them when the id is empty.
func (s *Service) MarkRead(ctx context.Context, principalID, id string) error {
	var err error
	if id == "" {
		_, err = s.db.Exec(ctx,
			`UPDATE notifications SET read_at = now()
			  WHERE principal_id=$1 AND read_at IS NULL`, principalID)
	} else {
		_, err = s.db.Exec(ctx,
			`UPDATE notifications SET read_at = now()
			  WHERE principal_id=$1 AND id=$2 AND read_at IS NULL`, principalID, id)
	}
	if err != nil {
		return fmt.Errorf("notify: mark read: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Preferences
// -----------------------------------------------------------------------------

// Prefs returns somebody's settings, or the defaults if they have never chosen.
func (s *Service) Prefs(ctx context.Context, principalID string) (Prefs, error) {
	p := DefaultPrefs()
	err := s.db.QueryRow(ctx, `
		SELECT email, quiet_from, quiet_to, timezone, digest_at
		FROM notification_prefs WHERE principal_id = $1`, principalID).
		Scan(&p.Email, &p.QuietFrom, &p.QuietTo, &p.Timezone, &p.DigestAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return DefaultPrefs(), nil
	case err != nil:
		return p, fmt.Errorf("notify: prefs: %w", err)
	}
	return p, nil
}

// SetPrefs saves them.
func (s *Service) SetPrefs(ctx context.Context, principalID string, p Prefs) error {
	switch p.Email {
	case EmailOff, EmailImmediate, EmailDigest:
	default:
		return fmt.Errorf("notify: %q is not an email setting", p.Email)
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		// A name the runtime cannot resolve means every quiet-hours comparison
		// silently falls back to UTC, which is worse than refusing it here.
		return fmt.Errorf("notify: %q is not a known time zone", p.Timezone)
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO notification_prefs
		    (principal_id, email, quiet_from, quiet_to, timezone, digest_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (principal_id) DO UPDATE
		    SET email = EXCLUDED.email, quiet_from = EXCLUDED.quiet_from,
		        quiet_to = EXCLUDED.quiet_to, timezone = EXCLUDED.timezone,
		        digest_at = EXCLUDED.digest_at, updated_at = now()`,
		principalID, p.Email, p.QuietFrom, p.QuietTo, p.Timezone, p.DigestAt)
	if err != nil {
		return fmt.Errorf("notify: set prefs: %w", err)
	}
	return nil
}

// Quiet reports whether now falls inside somebody's quiet hours.
//
// The window wraps midnight, which is the normal case: 22:00 to 07:00 is one
// range that spans two days, and treating it as from-less-than-to silently
// makes it mean nothing.
func (p Prefs) Quiet(at time.Time) bool {
	if p.QuietFrom == nil || p.QuietTo == nil {
		return false
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := at.In(loc)
	minutes := local.Hour()*60 + local.Minute()
	from, to := *p.QuietFrom, *p.QuietTo
	if from == to {
		return false
	}
	if from < to {
		return minutes >= from && minutes < to
	}
	return minutes >= from || minutes < to
}

// Recipient is somebody about to be emailed.
type Recipient struct {
	PrincipalID string
	Name        string
	Email       string
	Prefs       Prefs
}

// RecipientFor loads the address and settings for one person, or reports that
// there is nowhere to send.
func (s *Service) RecipientFor(ctx context.Context, principalID string) (*Recipient, error) {
	r := &Recipient{PrincipalID: principalID, Prefs: DefaultPrefs()}
	var email *string
	var prefEmail, tz *string
	var digestAt *int16
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(p.display_name,''), ''),
		       COALESCE(lc.email::text, ui.email::text),
		       np.email, np.quiet_from, np.quiet_to, np.timezone, np.digest_at
		FROM principals p
		LEFT JOIN local_credentials lc  ON lc.principal_id = p.id
		LEFT JOIN user_identities   ui  ON ui.principal_id = p.id
		LEFT JOIN notification_prefs np ON np.principal_id = p.id
		WHERE p.id = $1 AND p.status = 'active'`, principalID).
		Scan(&r.Name, &email, &prefEmail, &r.Prefs.QuietFrom, &r.Prefs.QuietTo, &tz, &digestAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("notify: recipient: %w", err)
	}
	if email == nil || *email == "" {
		return nil, nil
	}
	r.Email = *email
	if prefEmail != nil {
		r.Prefs.Email = *prefEmail
	}
	if tz != nil {
		r.Prefs.Timezone = *tz
	}
	if digestAt != nil {
		r.Prefs.DigestAt = int(*digestAt)
	}
	return r, nil
}

// Pending returns what has not been emailed yet for one person, for the digest.
func (s *Service) Pending(ctx context.Context, principalID string, limit int) ([]*Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `
		SELECT n.id::text, n.reason, n.entity_type, n.entity_id::text, n.title,
		       n.excerpt, n.actor_id::text,
		       COALESCE(NULLIF(a.display_name,''), ''),
		       n.read_at, n.created_at
		FROM notifications n
		LEFT JOIN principals a ON a.id = n.actor_id
		WHERE n.principal_id = $1 AND n.emailed_at IS NULL
		ORDER BY n.created_at LIMIT $2`, principalID, limit)
	if err != nil {
		return nil, fmt.Errorf("notify: pending: %w", err)
	}
	defer rows.Close()

	var out []*Notification
	for rows.Next() {
		n := &Notification{}
		if err := rows.Scan(&n.ID, &n.Reason, &n.EntityType, &n.EntityID, &n.Title,
			&n.Excerpt, &n.ActorID, &n.ActorName, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("notify: scan pending: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkEmailed records that these went out, so the next digest does not repeat
// them.
func (s *Service) MarkEmailed(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE notifications SET emailed_at = now() WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("notify: mark emailed: %w", err)
	}
	return nil
}

// Sentence renders one notification as the line an email or an inbox shows.
func (n *Notification) Sentence() string {
	who := n.ActorName
	if who == "" {
		who = "Somebody"
	}
	title := n.Title
	if title == "" {
		title = "an item"
	}
	switch n.Reason {
	case ReasonAssigned:
		return who + " assigned you " + title
	case ReasonMentioned:
		return who + " mentioned you in " + title
	case ReasonCommented:
		return who + " commented on " + title
	case ReasonDueSoon:
		return title + " is due soon"
	default:
		return who + " changed " + title
	}
}

// Subject is the one-line summary an immediate email uses.
func (n *Notification) Subject() string {
	s := n.Sentence()
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return strings.TrimSpace(s)
}
