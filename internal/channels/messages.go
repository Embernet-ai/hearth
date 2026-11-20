package channels

// Messages, comments, and mentions.
//
// A comment is a message whose home is an entity instead of a channel, and a
// reply is a message with a parent. One table, one permission check per home,
// one mention parser, one event.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// PostParams describes something somebody is about to say.
type PostParams struct {
	// Exactly one home: a channel, or an entity.
	ChannelID string
	Entity    core.Ref
	// ParentID makes it a reply.
	ParentID string
	Body     string
}

// Post writes a message and records everything it mentioned.
//
// One transaction covering the message, its mention links, and its event. A
// mention written afterwards can fail and leave a work order that was talked
// about with no record of it, which is the exact thing "a work order knows
// every conversation that referenced it" was supposed to deliver.
func (s *Service) Post(ctx context.Context, orgID string, actor core.Subjects, p PostParams) (*Message, error) {
	body := strings.TrimSpace(p.Body)
	if body == "" {
		return nil, ErrEmpty
	}
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("channels: no acting principal")
	}

	home, err := s.authorisePost(ctx, actor, p)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("channels: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	m := &Message{
		ID: uuid.NewString(), OrgID: orgID, AuthorID: principal, Body: body,
	}
	var channelID, entityType, entityID, parentID any
	if p.ChannelID != "" {
		channelID = p.ChannelID
		m.ChannelID = &p.ChannelID
	} else {
		entityType, entityID = string(p.Entity.Type), p.Entity.ID
		t, i := string(p.Entity.Type), p.Entity.ID
		m.EntityType, m.EntityID = &t, &i
	}
	if p.ParentID != "" {
		parentID = p.ParentID
		m.ParentID = &p.ParentID
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO messages (id, org_id, channel_id, entity_type, entity_id,
		                      parent_id, author_id, body)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING created_at`,
		m.ID, orgID, channelID, entityType, entityID, parentID, principal, body).
		Scan(&m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("channels: post: %w", err)
	}

	mentions, err := s.recordMentions(ctx, tx, orgID, principal, m.ID, body)
	if err != nil {
		return nil, err
	}
	m.Mentions = mentions

	if err := publishMessage(ctx, tx, orgID, m.ID, principal, home, map[string]any{
		"parent_id": p.ParentID,
		"mentions":  mentionIDs(mentions),
		"excerpt":   excerpt(body),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("channels: commit: %w", err)
	}
	return m, nil
}

// PostAsRule leaves a comment on behalf of an automation.
//
// No permission check, because a rule is configured by somebody who can
// already see what it covers, and the event it publishes carries the rule's
// depth so nothing reacts to it. Attributed to whoever wrote the rule, because
// "the system said it" is not an answer anybody accepts.
func (s *Service) PostAsRule(ctx context.Context, orgID, principalID, ruleID string,
	ref core.Ref, body string) (*Message, error) {

	return s.postWithoutChecking(ctx, orgID, principalID, ruleID, 1, ref, body)
}

// PostAsSystem posts on behalf of machinery that is not a rule, at depth zero.
//
// Depth zero is the difference, and it is deliberate. A rule reacting to a
// change is stamped at depth one so nothing reacts to it again; an email
// arriving from a customer is an original event, and the people watching the
// work order should be told about it exactly as if a colleague had typed it.
// Stamping it at depth one would silently mute every message that came in by
// mail, which is the half of the conversation nobody is in the room for.
func (s *Service) PostAsSystem(ctx context.Context, orgID, principalID string,
	ref core.Ref, body string) error {

	_, err := s.postWithoutChecking(ctx, orgID, principalID, "", 0, ref, body)
	return err
}

func (s *Service) postWithoutChecking(ctx context.Context, orgID, principalID, ruleID string,
	depth int, ref core.Ref, body string) (*Message, error) {

	body = strings.TrimSpace(body)
	if body == "" {
		return nil, ErrEmpty
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("channels: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	m := &Message{ID: uuid.NewString(), OrgID: orgID, AuthorID: principalID, Body: body}
	t, i := string(ref.Type), ref.ID
	m.EntityType, m.EntityID = &t, &i
	if err := tx.QueryRow(ctx, `
		INSERT INTO messages (id, org_id, entity_type, entity_id, author_id, body)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING created_at`,
		m.ID, orgID, t, i, principalID, body).Scan(&m.CreatedAt); err != nil {
		return nil, fmt.Errorf("channels: post without checking: %w", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: t, EntityID: i, Event: EventMessage,
		ActorID: principalID, CauseRuleID: ruleID, Depth: depth,
		Payload: map[string]any{"message_id": m.ID, "excerpt": excerpt(body)},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("channels: commit: %w", err)
	}
	return m, nil
}

// authorisePost decides whether this person may say this here, and returns the
// thing the event will be about.
//
// A channel needs membership. An entity needs COMMENT access, which is the
// level that has existed in the enum since the beginning with nothing using it:
// somebody who may discuss a work order without being trusted to change it.
func (s *Service) authorisePost(ctx context.Context, actor core.Subjects, p PostParams) (core.Ref, error) {
	switch {
	case p.ChannelID != "":
		member, err := s.IsMember(ctx, p.ChannelID, actor.Principal())
		if err != nil {
			return core.Ref{}, err
		}
		if !member {
			return core.Ref{}, ErrNotMember
		}
		return core.Ref{Type: core.EntityChannel, ID: p.ChannelID}, nil

	case p.Entity.ID != "":
		if err := s.authz.Require(ctx, actor, p.Entity, core.AccessComment); err != nil {
			return core.Ref{}, err
		}
		return p.Entity, nil

	default:
		return core.Ref{}, errors.New("channels: a message needs a channel or an entity")
	}
}

// ChannelTimeline returns a channel's messages, newest last so it reads the way
// a conversation does.
func (s *Service) ChannelTimeline(ctx context.Context, orgID, principalID, channelID string, limit int) ([]*Message, error) {
	// The read check is the channel's own: a public channel is readable by
	// anybody in the org, a private one only by its members.
	if _, err := s.Get(ctx, orgID, principalID, channelID); err != nil {
		return nil, err
	}
	return s.messages(ctx, orgID, `m.channel_id = $2`, []any{channelID}, limit)
}

// Comments returns the conversation on one entity, if the caller may view it.
func (s *Service) Comments(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref, limit int) ([]*Message, error) {
	if err := s.authz.Require(ctx, actor, ref, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	return s.messages(ctx, orgID,
		`m.entity_type = $2 AND m.entity_id = $3`, []any{string(ref.Type), ref.ID}, limit)
}

// messages is the shared read: one query shape for a channel timeline and for
// a comment thread, because they differ only in which column names their home.
//
// The caller supplies the WHERE fragment and its bind values, numbered from $2
// because $1 is always the org.
func (s *Service) messages(ctx context.Context, orgID, where string, filter []any, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	args := append([]any{orgID}, filter...)
	args = append(args, limit)

	sql := fmt.Sprintf(`
		SELECT m.id::text, m.org_id::text, m.channel_id::text,
		       m.entity_type, m.entity_id::text, m.parent_id::text,
		       m.author_id::text,
		       COALESCE(NULLIF(p.display_name,''), m.author_id::text),
		       m.body, m.created_at, m.edited_at,
		       (SELECT count(*) FROM messages r
		         WHERE r.parent_id = m.id AND r.deleted_at IS NULL)
		FROM messages m
		JOIN principals p ON p.id = m.author_id
		WHERE m.org_id = $1 AND m.deleted_at IS NULL AND %s
		  AND m.parent_id IS NULL
		ORDER BY m.created_at
		LIMIT $%d`, where, len(args))

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("channels: read messages: %w", err)
	}
	defer rows.Close()

	var out []*Message
	var ids []string
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.OrgID, &m.ChannelID, &m.EntityType, &m.EntityID,
			&m.ParentID, &m.AuthorID, &m.AuthorName, &m.Body,
			&m.CreatedAt, &m.EditedAt, &m.Replies); err != nil {
			return nil, fmt.Errorf("channels: scan message: %w", err)
		}
		out = append(out, m)
		ids = append(ids, m.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.loadMentions(ctx, orgID, out, ids); err != nil {
		return nil, err
	}
	return out, nil
}

// Replies returns the messages hanging off one.
func (s *Service) Replies(ctx context.Context, orgID, parentID string, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `
		SELECT m.id::text, m.org_id::text, m.channel_id::text,
		       m.entity_type, m.entity_id::text, m.parent_id::text, m.author_id::text,
		       COALESCE(NULLIF(p.display_name,''), m.author_id::text),
		       m.body, m.created_at, m.edited_at, 0
		FROM messages m
		JOIN principals p ON p.id = m.author_id
		WHERE m.org_id = $1 AND m.parent_id = $2 AND m.deleted_at IS NULL
		ORDER BY m.created_at LIMIT $3`, orgID, parentID, limit)
	if err != nil {
		return nil, fmt.Errorf("channels: replies: %w", err)
	}
	defer rows.Close()

	var out []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.OrgID, &m.ChannelID, &m.EntityType, &m.EntityID,
			&m.ParentID, &m.AuthorID, &m.AuthorName, &m.Body,
			&m.CreatedAt, &m.EditedAt, &m.Replies); err != nil {
			return nil, fmt.Errorf("channels: scan reply: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// messageOwner loads who wrote a message, what it is attached to (if
// anything), and whether it is currently deleted -- the three facts every one
// of Edit/Delete/Restore needs before deciding whether the caller may act.
//
// Deliberately not itself an authorisation check: Delete and Restore allow an
// admin of the entity a comment is attached to as well as the author (the
// existing Delete rule, kept as-is), while Edit is author-only, full stop --
// rewriting words still attributed to somebody else's name is a different
// trust question than hiding them, and quietly gaining that power as a side
// effect of refactoring Delete would be worse than not sharing this query.
func (s *Service) messageOwner(ctx context.Context, orgID, id string) (author string, entityType, entityID *string, deletedAt *time.Time, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT author_id::text, entity_type, entity_id::text, deleted_at
		FROM messages WHERE id=$1 AND org_id=$2`, id, orgID).
		Scan(&author, &entityType, &entityID, &deletedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil, nil, nil, ErrNotFound
	case err != nil:
		return "", nil, nil, nil, fmt.Errorf("channels: load message: %w", err)
	}
	return author, entityType, entityID, deletedAt, nil
}

// requireModerator is the Delete/Restore rule: the author, or -- for a
// message attached to an entity -- somebody who administers that entity. A
// channel message somebody else wrote refuses outright: moderating a channel
// is a power this does not have yet, and inventing one quietly is worse than
// not having it.
func (s *Service) requireModerator(ctx context.Context, actor core.Subjects, author string, entityType, entityID *string) error {
	if author == actor.Principal() {
		return nil
	}
	if entityType == nil || entityID == nil {
		return ErrNotFound
	}
	return s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityType(*entityType), ID: *entityID}, core.AccessAdmin)
}

// Delete removes a message. Its author, or somebody who administers the thing
// it is attached to. Idempotent -- calling it on an already-deleted message is
// a no-op, not an error, same reasoning as timelog.Remove: a stale page
// re-submitting the same click should not surface as a failure.
func (s *Service) Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	author, entityType, entityID, deletedAt, err := s.messageOwner(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.requireModerator(ctx, actor, author, entityType, entityID); err != nil {
		return err
	}
	if deletedAt != nil {
		return nil
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE messages SET deleted_at = now() WHERE id=$1`, id); err != nil {
		return fmt.Errorf("channels: delete message: %w", err)
	}
	return nil
}

// Restore brings back a message Delete removed. Same authority as Delete,
// and idempotent the same way: restoring something already active is a
// no-op, not an error.
func (s *Service) Restore(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	author, entityType, entityID, deletedAt, err := s.messageOwner(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.requireModerator(ctx, actor, author, entityType, entityID); err != nil {
		return err
	}
	if deletedAt == nil {
		return nil
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE messages SET deleted_at = NULL WHERE id=$1`, id); err != nil {
		return fmt.Errorf("channels: restore message: %w", err)
	}
	return nil
}

// Edit changes a message's body and marks it edited. Author-only, deliberately
// not extended to an admin the way Delete/Restore are -- see messageOwner.
// Refuses (ErrDeleted) on a message that is currently removed: restore it
// first, the same rule timelog.Correct uses for a deleted time entry.
func (s *Service) Edit(ctx context.Context, orgID string, actor core.Subjects, id, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return ErrEmpty
	}
	author, _, _, deletedAt, err := s.messageOwner(ctx, orgID, id)
	if err != nil {
		return err
	}
	if author != actor.Principal() {
		// Same disclosure rule as everywhere else here: not yours reads as
		// not found, not forbidden.
		return ErrNotFound
	}
	if deletedAt != nil {
		return ErrDeleted
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE messages SET body = $1, edited_at = now() WHERE id=$2`, body, id); err != nil {
		return fmt.Errorf("channels: edit message: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Mentions
// -----------------------------------------------------------------------------

// mentionPatterns are the two things people actually type.
//
// A handle, and a pasted link. There is no new syntax to learn for referring to
// a work order: paste its address, which is what somebody does anyway when they
// want a colleague to look at it. Inventing a #reference notation means teaching
// it, and it would be typed wrongly for a year.
var (
	handlePattern = regexp.MustCompile(`@([a-zA-Z0-9][a-zA-Z0-9._-]{0,62})`)
	docPattern    = regexp.MustCompile(`/documents/([0-9a-fA-F-]{36})`)
	channelLink   = regexp.MustCompile(`/channels/([0-9a-fA-F-]{36})`)
)

// recordMentions turns what a message said into real links.
func (s *Service) recordMentions(ctx context.Context, tx pgx.Tx, orgID, authorID, messageID, body string) ([]Mention, error) {
	seen := map[string]bool{}
	var out []Mention

	add := func(t core.EntityType, id, label string) error {
		key := string(t) + ":" + id
		if seen[key] {
			return nil
		}
		seen[key] = true
		out = append(out, Mention{Type: t, ID: id, Label: label})
		_, err := tx.Exec(ctx, `
			INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
			VALUES (gen_random_uuid(), $1, 'message', $2, $3, $4, 'mention', $5)
			ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
			orgID, messageID, string(t), id, authorID)
		if err != nil {
			return fmt.Errorf("channels: record mention: %w", err)
		}
		return nil
	}

	// People, by handle. A handle nobody has is left as plain text rather than
	// being an error: somebody typing an email address into a sentence has not
	// made a mistake.
	for _, m := range handlePattern.FindAllStringSubmatch(body, 20) {
		var id, name string
		err := tx.QueryRow(ctx, `
			SELECT id::text, COALESCE(NULLIF(display_name,''), handle::text)
			FROM principals WHERE org_id=$1 AND handle=$2 AND status='active'`,
			orgID, m[1]).Scan(&id, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("channels: resolve handle: %w", err)
		}
		if err := add(core.EntityPrincipal, id, name); err != nil {
			return nil, err
		}
	}

	// Documents, by pasted address. The existence check is deliberate: a link
	// to something that is not here would create a dangling row that every
	// reverse lookup then has to filter out.
	for _, m := range docPattern.FindAllStringSubmatch(body, 20) {
		var title, kind string
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(NULLIF(title,''), 'Untitled'), kind
			FROM documents WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
			m[1], orgID).Scan(&title, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("channels: resolve document mention: %w", err)
		}
		if err := add(core.EntityDocument, strings.ToLower(m[1]), title); err != nil {
			return nil, err
		}
	}

	for _, m := range channelLink.FindAllStringSubmatch(body, 10) {
		var name string
		err := tx.QueryRow(ctx,
			`SELECT name FROM channels WHERE id=$1 AND org_id=$2 AND archived_at IS NULL`,
			m[1], orgID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("channels: resolve channel mention: %w", err)
		}
		if err := add(core.EntityChannel, strings.ToLower(m[1]), name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// loadMentions attaches the mentions to a page of messages in one query.
func (s *Service) loadMentions(ctx context.Context, orgID string, messages []*Message, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT l.from_id::text, l.to_type, l.to_id::text,
		       COALESCE(
		         (SELECT COALESCE(NULLIF(d.title,''), 'Untitled') FROM documents d WHERE d.id = l.to_id),
		         (SELECT COALESCE(NULLIF(pr.display_name,''), pr.handle::text) FROM principals pr WHERE pr.id = l.to_id),
		         (SELECT c.name FROM channels c WHERE c.id = l.to_id),
		         '')
		FROM entity_links l
		WHERE l.org_id = $1 AND l.from_type = 'message' AND l.kind = 'mention'
		  AND l.from_id = ANY($2::uuid[])`, orgID, ids)
	if err != nil {
		return fmt.Errorf("channels: load mentions: %w", err)
	}
	defer rows.Close()

	byID := make(map[string]*Message, len(messages))
	for _, m := range messages {
		byID[m.ID] = m
	}
	for rows.Next() {
		var from, toType, toID, label string
		if err := rows.Scan(&from, &toType, &toID, &label); err != nil {
			return fmt.Errorf("channels: scan mention: %w", err)
		}
		if m, ok := byID[from]; ok {
			m.Mentions = append(m.Mentions, Mention{
				Type: core.EntityType(toType), ID: toID, Label: label,
			})
		}
	}
	return rows.Err()
}

// MentionedIn returns the conversations that referenced one entity.
//
// This is the payoff, and it reads in the direction people ask the question:
// standing on a work order, what has anybody said about this.
func (s *Service) MentionedIn(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref, limit int) ([]*Message, error) {
	if err := s.authz.Require(ctx, actor, ref, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	// Restricted to channels this person is in, plus comments they can already
	// read. A mention is a message, and a message in a room they are not in is
	// not theirs to see just because it named something they can.
	rows, err := s.db.Query(ctx, `
		SELECT m.id::text, m.org_id::text, m.channel_id::text, m.entity_type,
		       m.entity_id::text, m.parent_id::text, m.author_id::text,
		       COALESCE(NULLIF(p.display_name,''), m.author_id::text),
		       m.body, m.created_at, m.edited_at, 0
		FROM entity_links l
		JOIN messages m ON m.id = l.from_id AND m.deleted_at IS NULL
		JOIN principals p ON p.id = m.author_id
		LEFT JOIN channel_members cm
		       ON cm.channel_id = m.channel_id AND cm.principal_id = $4
		WHERE l.org_id = $1 AND l.from_type = 'message' AND l.kind = 'mention'
		  AND l.to_type = $2 AND l.to_id = $3
		  AND (m.channel_id IS NULL OR cm.principal_id IS NOT NULL)
		ORDER BY m.created_at DESC
		LIMIT $5`, orgID, string(ref.Type), ref.ID, actor.Principal(), limit)
	if err != nil {
		return nil, fmt.Errorf("channels: mentioned in: %w", err)
	}
	defer rows.Close()

	var out []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.OrgID, &m.ChannelID, &m.EntityType, &m.EntityID,
			&m.ParentID, &m.AuthorID, &m.AuthorName, &m.Body,
			&m.CreatedAt, &m.EditedAt, &m.Replies); err != nil {
			return nil, fmt.Errorf("channels: scan mention row: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func mentionIDs(m []Mention) []string {
	out := make([]string, 0, len(m))
	for _, x := range m {
		out = append(out, string(x.Type)+":"+x.ID)
	}
	return out
}

// excerpt is the first line, bounded, for a notification that has to fit on one.
func excerpt(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(line)
	if len(line) > 140 {
		// Cut on a rune boundary so the excerpt is never invalid UTF-8.
		cut := 140
		for cut > 0 && line[cut]&0xC0 == 0x80 {
			cut--
		}
		return line[:cut] + "..."
	}
	return line
}

// Unread counts what has arrived in a channel since somebody last looked.
func (s *Service) Unread(ctx context.Context, channelID, principalID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM messages m
		JOIN channel_members cm
		  ON cm.channel_id = m.channel_id AND cm.principal_id = $2
		WHERE m.channel_id = $1 AND m.deleted_at IS NULL
		  AND m.author_id <> $2
		  AND (cm.last_read_at IS NULL OR m.created_at > cm.last_read_at)`,
		channelID, principalID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("channels: unread: %w", err)
	}
	return n, nil
}

// PostImported writes a message that already happened somewhere else.
//
// Three things separate this from every other way to post, and each is the
// reason it cannot reuse one of them:
//
// It publishes NO event. Every other path raises one, which fans out to
// notifications, rules and the search index. A migration carrying a few
// thousand comments through that would put a few thousand unread notifications
// in front of people on their first morning, about conversations that finished
// months ago. The search index is rebuilt from Postgres by design, so nothing
// is permanently lost by skipping the event.
//
// It takes the author. The person who wrote it is a fact about the comment, not
// about who ran the import.
//
// It takes the time. A comment thread whose entries all share one timestamp is
// not a thread, it is a pile, and the order an argument happened in is usually
// the point of reading it back.
//
// No authorisation check, deliberately, for the same reason the importer runs
// as the owner: there is no requesting human, and the caller has already
// decided the whole workspace is coming across.
func (s *Service) PostImported(ctx context.Context, orgID, principalID string,
	ref core.Ref, body string, at time.Time, parentID string) (string, error) {

	body = strings.TrimSpace(body)
	if body == "" {
		return "", ErrEmpty
	}
	id := uuid.NewString()
	t, i := string(ref.Type), ref.ID
	var parent *string
	if parentID != "" {
		parent = &parentID
	}
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO messages (id, org_id, entity_type, entity_id, parent_id,
		                      author_id, body, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, orgID, t, i, parent, principalID, body, at); err != nil {
		return "", fmt.Errorf("channels: post imported: %w", err)
	}
	return id, nil
}
