// Package channels is conversation, and it is also the permission model doing
// its job in public for the first time.
//
// "Channel membership IS the permission model" has been true in the database
// since the substrate landed and unreachable from the interface ever since.
// Attaching a document to a channel already granted that channel access inside
// docs.Create; there was simply no way to make a channel, join one, or attach
// anything to one. This is that gap closed.
//
// A comment is a message on an entity rather than a second system. Three tables
// for channel messages, entity comments, and replies would be three sets of
// mentions, three notification paths, and three chances to get access wrong.
package channels

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
	"github.com/Embernet-ai/hearth/internal/jobs"
)

var (
	// ErrNotFound covers both "there is no such channel" and "there is one and
	// it is not yours to see", for the same reason documents do: telling
	// somebody a private channel exists is most of what private means.
	ErrNotFound  = errors.New("channel not found")
	ErrNotMember = errors.New("you are not in that channel")
	ErrEmpty     = errors.New("a message needs something in it")
	ErrSlug      = errors.New("that channel name is already taken")

	// ErrDeleted means the message exists but has been removed -- restore it
	// before editing it. Same rule timelog uses for a deleted time entry: an
	// edit on something marked gone is a real conflict, not a stale click.
	ErrDeleted = errors.New("channels: that message has been removed")
)

// Visibility values.
const (
	Public  = "public"
	Private = "private"
)

// Channel is one conversation.
type Channel struct {
	ID         string
	OrgID      string
	Slug       string
	Name       string
	Topic      string
	Visibility string
	CreatedBy  string
	CreatedAt  time.Time
	ArchivedAt *time.Time

	// Member reports whether the caller is in it. Membership is what grants
	// access to everything attached, so this is the single most important
	// thing about a channel from any one person's point of view.
	Member bool
	// Members is how many people are in it.
	Members int
	// Unread is how many messages have arrived since this person last looked,
	// from channel_members.last_read_at, which has been a column with nothing
	// reading it since the beginning.
	Unread int
	// Attached is how many documents this channel grants.
	Attached int
}

// Message is one thing somebody said.
type Message struct {
	ID         string
	OrgID      string
	ChannelID  *string
	EntityType *string
	EntityID   *string
	ParentID   *string
	AuthorID   string
	AuthorName string
	Body       string
	CreatedAt  time.Time
	EditedAt   *time.Time

	// Replies is how many messages hang off this one.
	Replies int
	// Mentions are the entities this message pointed at, resolved for display.
	Mentions []Mention
}

// Mention is one thing a message referred to.
type Mention struct {
	Type  core.EntityType
	ID    string
	Label string
}

// Authorizer is the slice of access control this package needs, declared here
// by the consumer so the dependency points one way.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
	Grant(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, level core.AccessLevel, grantedBy string) error
}

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
}

func New(db *pgxpool.Pool, az Authorizer) *Service { return &Service{db: db, authz: az} }

// -----------------------------------------------------------------------------
// Channels
// -----------------------------------------------------------------------------

// Create makes a channel and puts its creator in it.
//
// One transaction, for the same reason creating a document grants it in one:
// a channel nobody is in is a channel nobody can find, including the person who
// just made it.
func (s *Service) Create(ctx context.Context, orgID, principalID, name, topic, visibility string) (*Channel, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("channels: a channel needs a name")
	}
	if visibility != Public && visibility != Private {
		visibility = Private
	}
	slug := slugify(name)
	if slug == "" {
		slug = "channel"
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("channels: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	c := &Channel{
		ID: uuid.NewString(), OrgID: orgID, Slug: slug, Name: name,
		Topic: topic, Visibility: visibility, CreatedBy: principalID, Member: true,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO channels (id, org_id, slug, name, topic, visibility, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING created_at`,
		c.ID, orgID, slug, name, topic, visibility, principalID).Scan(&c.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlug
		}
		return nil, fmt.Errorf("channels: create: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_members (channel_id, principal_id, role, last_read_at)
		VALUES ($1,$2,'admin',now())`, c.ID, principalID); err != nil {
		return nil, fmt.Errorf("channels: add creator: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("channels: commit: %w", err)
	}
	return c, nil
}

// List returns the channels this person can see: everything they are in, plus
// every public one.
//
// A private channel they are not in does not appear at all. Listing it with a
// lock icon tells them it exists, which is the thing "private" is for.
func (s *Service) List(ctx context.Context, orgID, principalID string) ([]*Channel, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id::text, c.org_id::text, c.slug::text, c.name, COALESCE(c.topic,''),
		       c.visibility, c.created_by::text, c.created_at,
		       (me.principal_id IS NOT NULL) AS member,
		       (SELECT count(*) FROM channel_members m WHERE m.channel_id = c.id),
		       (SELECT count(*) FROM messages msg
		         WHERE msg.channel_id = c.id AND msg.deleted_at IS NULL
		           AND (me.last_read_at IS NULL OR msg.created_at > me.last_read_at)
		           AND msg.author_id <> $2),
		       (SELECT count(*) FROM grants g
		         WHERE g.entity_type = 'document'
		           AND g.subject_key = 'channel:' || c.id::text)
		FROM channels c
		LEFT JOIN channel_members me ON me.channel_id = c.id AND me.principal_id = $2
		WHERE c.org_id = $1 AND c.archived_at IS NULL
		  AND (c.visibility = 'public' OR me.principal_id IS NOT NULL)
		ORDER BY member DESC, c.name`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("channels: list: %w", err)
	}
	defer rows.Close()

	var out []*Channel
	for rows.Next() {
		c := &Channel{}
		if err := rows.Scan(&c.ID, &c.OrgID, &c.Slug, &c.Name, &c.Topic,
			&c.Visibility, &c.CreatedBy, &c.CreatedAt,
			&c.Member, &c.Members, &c.Unread, &c.Attached); err != nil {
			return nil, fmt.Errorf("channels: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one channel if this person may see it.
func (s *Service) Get(ctx context.Context, orgID, principalID, id string) (*Channel, error) {
	c := &Channel{}
	err := s.db.QueryRow(ctx, `
		SELECT c.id::text, c.org_id::text, c.slug::text, c.name, COALESCE(c.topic,''),
		       c.visibility, c.created_by::text, c.created_at,
		       (me.principal_id IS NOT NULL),
		       (SELECT count(*) FROM channel_members m WHERE m.channel_id = c.id),
		       0,
		       (SELECT count(*) FROM grants g
		         WHERE g.entity_type = 'document'
		           AND g.subject_key = 'channel:' || c.id::text)
		FROM channels c
		LEFT JOIN channel_members me ON me.channel_id = c.id AND me.principal_id = $3
		WHERE c.id = $1 AND c.org_id = $2 AND c.archived_at IS NULL
		  AND (c.visibility = 'public' OR me.principal_id IS NOT NULL)`,
		id, orgID, principalID).
		Scan(&c.ID, &c.OrgID, &c.Slug, &c.Name, &c.Topic, &c.Visibility,
			&c.CreatedBy, &c.CreatedAt, &c.Member, &c.Members, &c.Unread, &c.Attached)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("channels: get %s: %w", id, err)
	}
	return c, nil
}

// Join adds somebody to a channel.
//
// This is the whole permission model in one statement. The row in
// channel_members is what puts 'channel:<id>' in their subject set on the next
// request, and every document granted to that channel becomes visible without
// anything being copied anywhere.
//
// A private channel cannot be joined by walking up to it. Somebody already in
// it has to add you, which is Invite.
func (s *Service) Join(ctx context.Context, orgID, principalID, channelID string) error {
	var visibility string
	err := s.db.QueryRow(ctx,
		`SELECT visibility FROM channels
		  WHERE id=$1 AND org_id=$2 AND archived_at IS NULL`, channelID, orgID).Scan(&visibility)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("channels: read visibility: %w", err)
	}
	if visibility != Public {
		return ErrNotFound
	}
	return s.addMember(ctx, channelID, principalID)
}

// Invite adds somebody else, and requires the inviter to be in the channel
// already. Membership is access, so handing it out is not something a
// bystander does.
func (s *Service) Invite(ctx context.Context, orgID, actorID, channelID, principalID string) error {
	member, err := s.IsMember(ctx, channelID, actorID)
	if err != nil {
		return err
	}
	if !member {
		return ErrNotFound
	}
	// The invitee has to be in the same org, or a uuid from anywhere would
	// grant a stranger everything the channel holds.
	var sameOrg bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM principals WHERE id=$1 AND org_id=$2)`,
		principalID, orgID).Scan(&sameOrg); err != nil {
		return fmt.Errorf("channels: check principal: %w", err)
	}
	if !sameOrg {
		return ErrNotFound
	}
	return s.addMember(ctx, channelID, principalID)
}

func (s *Service) addMember(ctx context.Context, channelID, principalID string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO channel_members (channel_id, principal_id, last_read_at)
		VALUES ($1,$2,now())
		ON CONFLICT (channel_id, principal_id) DO NOTHING`, channelID, principalID)
	if err != nil {
		return fmt.Errorf("channels: join: %w", err)
	}
	return nil
}

// Leave removes somebody, and with them every document the channel was
// granting. That is the model working, not a side effect.
func (s *Service) Leave(ctx context.Context, channelID, principalID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM channel_members WHERE channel_id=$1 AND principal_id=$2`,
		channelID, principalID)
	if err != nil {
		return fmt.Errorf("channels: leave: %w", err)
	}
	return nil
}

func (s *Service) IsMember(ctx context.Context, channelID, principalID string) (bool, error) {
	var yes bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM channel_members WHERE channel_id=$1 AND principal_id=$2)`,
		channelID, principalID).Scan(&yes)
	if err != nil {
		return false, fmt.Errorf("channels: membership: %w", err)
	}
	return yes, nil
}

// Member is one person in a channel.
type Member struct {
	PrincipalID string
	Name        string
	Role        string
	JoinedAt    time.Time
}

func (s *Service) Members(ctx context.Context, channelID string) ([]Member, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, COALESCE(NULLIF(p.display_name,''), p.id::text), m.role, m.joined_at
		FROM channel_members m
		JOIN principals p ON p.id = m.principal_id
		WHERE m.channel_id = $1
		ORDER BY m.joined_at`, channelID)
	if err != nil {
		return nil, fmt.Errorf("channels: members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.PrincipalID, &m.Name, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("channels: scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkRead moves this person's watermark to now.
//
// last_read_at has been a column with nothing writing to it since the
// substrate. Unread counts are the difference between a channel list people
// glance at and one they scroll through wondering what is new.
func (s *Service) MarkRead(ctx context.Context, channelID, principalID string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE channel_members SET last_read_at = now()
		  WHERE channel_id=$1 AND principal_id=$2`, channelID, principalID)
	if err != nil {
		return fmt.Errorf("channels: mark read: %w", err)
	}
	return nil
}

// Attach grants a channel access to a document.
//
// The one call that makes "join a channel, gain what is attached to it" real
// from the interface. docs.Create has done this for a channel named at creation
// since the beginning; this is the same grant, made afterwards, for a document
// that already exists.
//
// It requires admin on the document, not edit. Widening who can see something
// is not the same authority as changing it.
func (s *Service) Attach(ctx context.Context, orgID string, actor core.Subjects, channelID, docID string) error {
	ref := core.Ref{Type: core.EntityDocument, ID: docID}
	if err := s.authz.Require(ctx, actor, ref, core.AccessAdmin); err != nil {
		return err
	}
	member, err := s.IsMember(ctx, channelID, actor.Principal())
	if err != nil {
		return err
	}
	if !member {
		// Attaching to a channel you are not in is handing something to a room
		// you cannot see into.
		return ErrNotFound
	}
	return s.authz.Grant(ctx, orgID, ref,
		core.Subject{Kind: core.SubjectChannel, ID: channelID},
		core.AccessEdit, actor.Principal())
}

// AttachedDocuments lists what a channel grants, for its page.
func (s *Service) AttachedDocuments(ctx context.Context, orgID, channelID string) ([]AttachedDoc, error) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id::text, d.kind, d.title, d.updated_at
		FROM grants g
		JOIN documents d ON d.id = g.entity_id
		WHERE g.entity_type = 'document'
		  AND g.subject_key = 'channel:' || $1
		  AND d.org_id = $2 AND d.deleted_at IS NULL
		ORDER BY d.updated_at DESC
		LIMIT 200`, channelID, orgID)
	if err != nil {
		return nil, fmt.Errorf("channels: attached documents: %w", err)
	}
	defer rows.Close()

	var out []AttachedDoc
	for rows.Next() {
		var d AttachedDoc
		if err := rows.Scan(&d.ID, &d.Kind, &d.Title, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("channels: scan attached: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AttachedDoc is a document a channel grants.
type AttachedDoc struct {
	ID        string
	Kind      string
	Title     string
	UpdatedAt time.Time
}

func slugify(s string) string {
	var b strings.Builder
	last := byte('-')
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			last = byte(r)
		case last != '-':
			b.WriteByte('-')
			last = '-'
		}
	}
	return strings.Trim(b.String(), "-")
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// EventMessage is published when somebody says something.
const EventMessage = "message"

// publishMessage records the event in the caller's transaction.
func publishMessage(ctx context.Context, tx pgx.Tx, orgID, messageID, authorID string,
	home core.Ref, payload map[string]any) error {

	if payload == nil {
		payload = map[string]any{}
	}
	payload["message_id"] = messageID
	return jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(home.Type), EntityID: home.ID,
		Event: EventMessage, ActorID: authorID, Payload: payload,
	})
}
