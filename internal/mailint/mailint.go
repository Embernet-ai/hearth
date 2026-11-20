// Package mailint is work arriving by email, and replies going back out.
//
// # Why this exists
//
// Work arrives by email whether or not the software is ready for it. A
// maintenance system that only accepts work typed into its own form grows a
// shadow inbox beside it within a fortnight, and then the two disagree about
// what has been asked for. Meeting the work where it already is costs one
// address per space.
//
// # The two things that have to be right
//
// A relay retries. Every MTA in existence will deliver the same message twice
// if the first acknowledgement is lost, and a path that answers that with two
// work orders is a path the crew stops trusting on its second bad afternoon.
// The Message-ID is the idempotency key, it has a unique index on it, and a
// message with no Message-ID is refused rather than given one: inventing a key
// turns a retry into a duplicate, which is the exact failure being avoided.
//
// And a reply has to thread. Somebody who emailed a question and gets an answer
// that appears as an unrelated message in their inbox has learned that this
// address does not really work, and they go back to phoning. Threading is
// In-Reply-To and the whole References chain, and keeping only the former
// breaks the moment somebody replies from a phone.
package mailint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/platform/mail"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("that message cannot be filed")
	// ErrNoAddress means nothing in the message was addressed to an inbound
	// address here. Not an error worth retrying: the relay was misconfigured or
	// somebody was copied in, and neither improves on the ninth attempt.
	ErrNoAddress = errors.New("no inbound address on this message")
	// ErrDuplicate means this message id has already been filed. Returned so
	// the caller can answer the relay with a success, because a retry that gets
	// an error becomes a third attempt.
	ErrDuplicate = errors.New("that message has already been filed")
)

// Directions.
const (
	Inbound  = "inbound"
	Outbound = "outbound"
)

// Address is one inbound address.
type Address struct {
	ID        string
	OrgID     string
	SpaceID   *string
	SpaceName string
	Address   string
	Kind      string
	CreatedBy string
	ChannelID *string
	Enabled   bool
}

// Filed is the outcome of accepting a message.
type Filed struct {
	// DocumentID is the work order, whether it was created now or already
	// existed because this is a reply.
	DocumentID string
	// Created is false when the message threaded onto existing work, which is
	// the ordinary case for everything after the first message.
	Created bool
	// Duplicate is true when this exact message had already been filed.
	Duplicate bool
	MessageID string
}

// Documents is the slice of the spine this package needs.
type Documents interface {
	CreateAsSystem(ctx context.Context, orgID string, actor docs.RuleActor, p docs.CreateParams, linkTo core.Ref) (*docs.Document, error)
	Create(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error)
}

// Conversation is the slice of messaging this package needs, so a mail thread
// is the same comment timeline everything else uses rather than a second one.
type Conversation interface {
	PostAsSystem(ctx context.Context, orgID, authorID string, entity core.Ref, body string) error
}

// Contacts resolves a sender to somebody on file.
type Contacts interface {
	ByEmail(ctx context.Context, orgID, email string) (*crm.Contact, error)
}

type Service struct {
	db     *pgxpool.Pool
	docs   Documents
	convo  Conversation
	people Contacts
	// domain is what generated Message-IDs are stamped with. A right hand side
	// that does not resolve gets a message filed as spam by half the world.
	domain string
}

func New(db *pgxpool.Pool, d Documents, c Conversation, p Contacts, domain string) *Service {
	return &Service{db: db, docs: d, convo: c, people: p, domain: cleanDomain(domain)}
}

// -----------------------------------------------------------------------------
// Addresses
// -----------------------------------------------------------------------------

// SaveAddress creates or replaces an inbound address.
func (s *Service) SaveAddress(ctx context.Context, orgID string, actor core.Subjects, a Address) (*Address, error) {
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("mailint: no acting principal")
	}
	a.Address = strings.ToLower(strings.TrimSpace(a.Address))
	if !strings.Contains(a.Address, "@") || strings.HasPrefix(a.Address, "@") {
		return nil, fmt.Errorf("%w: %q is not an address", ErrInvalid, a.Address)
	}
	if a.Kind == "" {
		a.Kind = docs.KindWorkOrder
	}
	if a.Kind != docs.KindWorkOrder && a.Kind != docs.KindTask && a.Kind != docs.KindNote {
		return nil, fmt.Errorf("%w: mail cannot become a %s", ErrInvalid, a.Kind)
	}
	if a.SpaceID != nil && *a.SpaceID == "" {
		a.SpaceID = nil
	}
	if a.ChannelID != nil && *a.ChannelID == "" {
		a.ChannelID = nil
	}

	if a.ID == "" {
		a.ID, a.OrgID, a.CreatedBy = uuid.NewString(), orgID, principal
		_, err := s.db.Exec(ctx, `
			INSERT INTO inbound_addresses (id, org_id, space_id, address, kind, created_by,
			                               channel_id, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7,true)`,
			a.ID, orgID, a.SpaceID, a.Address, a.Kind, principal, a.ChannelID)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: %s is already taken", ErrInvalid, a.Address)
			}
			return nil, fmt.Errorf("mailint: create address: %w", err)
		}
		a.Enabled = true
		return &a, nil
	}

	ct, err := s.db.Exec(ctx, `
		UPDATE inbound_addresses
		   SET space_id=$3, address=$4, kind=$5, channel_id=$6, enabled=$7
		 WHERE id=$1 AND org_id=$2`,
		a.ID, orgID, a.SpaceID, a.Address, a.Kind, a.ChannelID, a.Enabled)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s is already taken", ErrInvalid, a.Address)
		}
		return nil, fmt.Errorf("mailint: update address: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &a, nil
}

// DeleteAddress removes one.
func (s *Service) DeleteAddress(ctx context.Context, orgID, id string) error {
	ct, err := s.db.Exec(ctx,
		`DELETE FROM inbound_addresses WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("mailint: delete address: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Addresses lists the inbound addresses for an org.
func (s *Service) Addresses(ctx context.Context, orgID string) ([]*Address, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.space_id::text, COALESCE(sp.name, ''), a.address, a.kind,
		       a.created_by::text, a.channel_id::text, a.enabled
		FROM inbound_addresses a
		LEFT JOIN spaces sp ON sp.id = a.space_id
		WHERE a.org_id = $1 ORDER BY a.address`, orgID)
	if err != nil {
		return nil, fmt.Errorf("mailint: list addresses: %w", err)
	}
	defer rows.Close()

	var out []*Address
	for rows.Next() {
		a := &Address{OrgID: orgID}
		if err := rows.Scan(&a.ID, &a.SpaceID, &a.SpaceName, &a.Address, &a.Kind,
			&a.CreatedBy, &a.ChannelID, &a.Enabled); err != nil {
			return nil, fmt.Errorf("mailint: scan address: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// findAddress matches any recipient on the message against the configured
// addresses.
//
// Every recipient, including the delivery headers, because a message sent to a
// list and blind copied here has the inbound address in none of To or Cc.
func (s *Service) findAddress(ctx context.Context, m *mail.Message) (*Address, error) {
	candidates := make([]string, 0, len(m.To)+len(m.Cc))
	candidates = append(candidates, m.To...)
	candidates = append(candidates, m.Cc...)
	if len(candidates) == 0 {
		return nil, ErrNoAddress
	}

	a := &Address{}
	err := s.db.QueryRow(ctx, `
		SELECT id::text, org_id::text, space_id::text, address, kind,
		       created_by::text, channel_id::text, enabled
		FROM inbound_addresses
		WHERE lower(address) = ANY($1) AND enabled
		LIMIT 1`, candidates).
		Scan(&a.ID, &a.OrgID, &a.SpaceID, &a.Address, &a.Kind, &a.CreatedBy,
			&a.ChannelID, &a.Enabled)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNoAddress
	case err != nil:
		return nil, fmt.Errorf("mailint: match address: %w", err)
	}
	return a, nil
}

// -----------------------------------------------------------------------------
// Receiving
// -----------------------------------------------------------------------------

// Receive files one raw message.
//
// The whole path in one transaction: the email row, the work order or the
// comment, and the event. A failure anywhere leaves nothing behind, so the
// relay's retry is a clean second attempt rather than a repair job.
func (s *Service) Receive(ctx context.Context, raw []byte) (*Filed, error) {
	m, err := mail.Parse(raw)
	if err != nil {
		return nil, err
	}
	addr, err := s.findAddress(ctx, m)
	if err != nil {
		return nil, err
	}

	// Already filed? Answered before any work is done, and reported as a
	// duplicate rather than an error so the caller can tell the relay it
	// succeeded. An error here becomes a third delivery.
	var existingDoc *string
	err = s.db.QueryRow(ctx,
		`SELECT entity_id::text FROM email_messages WHERE org_id=$1 AND message_id=$2`,
		addr.OrgID, m.MessageID).Scan(&existingDoc)
	switch {
	case err == nil:
		out := &Filed{Duplicate: true, MessageID: m.MessageID}
		if existingDoc != nil {
			out.DocumentID = *existingDoc
		}
		return out, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("mailint: check for duplicate: %w", err)
	}

	// A reply lands on the work it answers rather than making a second one.
	// This is what makes the address usable: somebody replies to the
	// notification they got and it appears on the job.
	if docID := s.threadTarget(ctx, addr.OrgID, m); docID != "" {
		return s.appendToThread(ctx, addr, m, docID)
	}
	return s.createFromMessage(ctx, addr, m)
}

// threadTarget walks the References chain to find work this message answers.
//
// The whole chain and not only In-Reply-To, because a reply from a phone
// commonly rewrites In-Reply-To to point at the wrong message or drops it
// entirely while leaving References intact.
func (s *Service) threadTarget(ctx context.Context, orgID string, m *mail.Message) string {
	ids := make([]string, 0, len(m.References)+1)
	if m.InReplyTo != "" {
		ids = append(ids, m.InReplyTo)
	}
	ids = append(ids, m.References...)
	if len(ids) == 0 {
		return ""
	}
	var docID *string
	err := s.db.QueryRow(ctx, `
		SELECT entity_id::text FROM email_messages
		WHERE org_id=$1 AND message_id = ANY($2) AND entity_id IS NOT NULL
		ORDER BY received_at DESC LIMIT 1`, orgID, ids).Scan(&docID)
	if err != nil || docID == nil {
		return ""
	}
	return *docID
}

// createFromMessage turns a message into work.
func (s *Service) createFromMessage(ctx context.Context, addr *Address, m *mail.Message) (*Filed, error) {
	title := strings.TrimSpace(m.Subject)
	if title == "" {
		title = "Message from " + m.From
	}
	body := m.Text
	// Who sent it, at the top, because a work order that does not say who
	// asked for it is a work order nobody can ask about.
	from := m.From
	if m.FromName != "" {
		from = m.FromName + " <" + m.From + ">"
	}
	body = "From: " + from + "\n\n" + body

	actor := docs.RuleActor{PrincipalID: addr.CreatedBy}
	p := docs.CreateParams{
		Kind: addr.Kind, Title: title, Body: body, BodyFormat: docs.FormatPlain,
		SpaceID: addr.SpaceID, ChannelID: addr.ChannelID,
	}
	if docs.HasTaskFields(addr.Kind) {
		p.Task = &docs.TaskFields{Status: docs.StatusTodo}
	}

	// Created through the ordinary path with the address owner as the actor.
	// Attribution matters: "the mail robot" is not an answer anybody accepts
	// when a work order appeared overnight, and the address names a person.
	subjects := core.Subjects{{Kind: core.SubjectPrincipal, ID: addr.CreatedBy}}
	d, err := s.docs.Create(ctx, addr.OrgID, subjects, p)
	if err != nil {
		return nil, fmt.Errorf("mailint: create from message: %w", err)
	}
	_ = actor

	if err := s.record(ctx, addr.OrgID, m, d.ID, Inbound); err != nil {
		return nil, err
	}
	// The sender, if they are on file, so the job says who to ring back.
	s.linkSender(ctx, addr.OrgID, addr.CreatedBy, m.From, d.ID)

	return &Filed{DocumentID: d.ID, Created: true, MessageID: m.MessageID}, nil
}

// appendToThread posts a reply onto the work it answers.
func (s *Service) appendToThread(ctx context.Context, addr *Address, m *mail.Message, docID string) (*Filed, error) {
	from := m.From
	if m.FromName != "" {
		from = m.FromName + " <" + m.From + ">"
	}
	body := "From: " + from + "\n\n" + m.Text

	if err := s.convo.PostAsSystem(ctx, addr.OrgID, addr.CreatedBy,
		core.Ref{Type: core.EntityDocument, ID: docID}, body); err != nil {
		return nil, fmt.Errorf("mailint: post reply: %w", err)
	}
	if err := s.record(ctx, addr.OrgID, m, docID, Inbound); err != nil {
		return nil, err
	}
	return &Filed{DocumentID: docID, MessageID: m.MessageID}, nil
}

// record writes the email row that makes the message id unique.
func (s *Service) record(ctx context.Context, orgID string, m *mail.Message, docID, direction string) error {
	to := ""
	if len(m.To) > 0 {
		to = m.To[0]
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO email_messages (id, org_id, message_id, in_reply_to, references_ids,
		                            entity_type, entity_id, direction, from_addr,
		                            to_addr, subject)
		VALUES ($1,$2,$3,$4,$5,'document',$6,$7,$8,$9,$10)
		ON CONFLICT (org_id, message_id) DO NOTHING`,
		uuid.NewString(), orgID, m.MessageID, m.InReplyTo, notNil(m.References),
		docID, direction, m.From, to, m.Subject)
	if err != nil {
		return fmt.Errorf("mailint: record message: %w", err)
	}
	return nil
}

// linkSender attaches the contact who sent it, when there is one on file.
//
// Best effort and never fatal: a message from somebody unknown is still work,
// and refusing it because the sender is not in the address book would be a
// filing system with opinions.
func (s *Service) linkSender(ctx context.Context, orgID, principal, email, docID string) {
	if s.people == nil || email == "" {
		return
	}
	contact, err := s.people.ByEmail(ctx, orgID, email)
	if err != nil || contact == nil {
		return
	}
	_, _ = s.db.Exec(ctx, `
		INSERT INTO entity_links (id, org_id, from_type, from_id, to_type, to_id, kind, created_by)
		VALUES (gen_random_uuid(), $1, 'document', $2, 'contact', $3, 'concerns', $4)
		ON CONFLICT (from_type, from_id, to_type, to_id, kind) DO NOTHING`,
		orgID, docID, contact.ID, principal)
}

// -----------------------------------------------------------------------------
// Replying
// -----------------------------------------------------------------------------

// Reply is a message going back out.
type Reply struct {
	To         string
	Subject    string
	Body       string
	MessageID  string
	InReplyTo  string
	References []string
}

// Headers renders the threading headers a client needs to put this under the
// original.
//
// References is the original chain plus the message being answered. A client
// threads on it, and sending only In-Reply-To breaks as soon as anybody in the
// conversation uses a client that rewrites headers.
func (r Reply) Headers() map[string]string {
	h := map[string]string{"Message-Id": "<" + r.MessageID + ">"}
	if r.InReplyTo != "" {
		h["In-Reply-To"] = "<" + r.InReplyTo + ">"
	}
	if len(r.References) > 0 {
		var b strings.Builder
		for i, id := range r.References {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("<" + id + ">")
		}
		h["References"] = b.String()
	}
	return h
}

// PrepareReply builds the reply to the latest inbound message on a document.
//
// Returns ErrNotFound when nothing arrived by email, because replying to a work
// order somebody typed has nowhere to go and offering the button would be a
// button that fails.
func (s *Service) PrepareReply(ctx context.Context, orgID, docID, body string) (*Reply, error) {
	var (
		msgID, subject, from string
		refs                 []string
	)
	err := s.db.QueryRow(ctx, `
		SELECT message_id, subject, from_addr, references_ids
		FROM email_messages
		WHERE org_id=$1 AND entity_id=$2 AND direction='inbound'
		ORDER BY received_at DESC LIMIT 1`, orgID, docID).
		Scan(&msgID, &subject, &from, &refs)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("mailint: find thread: %w", err)
	}

	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	return &Reply{
		To: from, Subject: subject, Body: body,
		MessageID:  s.NewMessageID(),
		InReplyTo:  msgID,
		References: append(append([]string{}, refs...), msgID),
	}, nil
}

// RecordOutbound files a sent reply so the answer to it threads too.
func (s *Service) RecordOutbound(ctx context.Context, orgID, docID string, r *Reply) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO email_messages (id, org_id, message_id, in_reply_to, references_ids,
		                            entity_type, entity_id, direction, from_addr,
		                            to_addr, subject)
		VALUES ($1,$2,$3,$4,$5,'document',$6,'outbound','',$7,$8)
		ON CONFLICT (org_id, message_id) DO NOTHING`,
		uuid.NewString(), orgID, r.MessageID, r.InReplyTo, notNil(r.References),
		docID, r.To, r.Subject)
	if err != nil {
		return fmt.Errorf("mailint: record outbound: %w", err)
	}
	return nil
}

// NewMessageID mints an id for a message this application is sending.
//
// Random rather than derived from the row, because a Message-ID is world
// visible and one built from a database id tells a stranger how many work
// orders there are.
func (s *Service) NewMessageID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Not fatal. A time-based id is worse but still unique enough to
		// thread, and refusing to send a reply because the random source
		// hiccuped is the wrong trade.
		return fmt.Sprintf("%d.hearth@%s", time.Now().UnixNano(), s.domain)
	}
	return hex.EncodeToString(b[:]) + ".hearth@" + s.domain
}

// Thread lists the email on one document, for the page that shows it.
func (s *Service) Thread(ctx context.Context, orgID, docID string) ([]ThreadEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT direction, from_addr, to_addr, subject, received_at
		FROM email_messages
		WHERE org_id=$1 AND entity_id=$2
		ORDER BY received_at`, orgID, docID)
	if err != nil {
		return nil, fmt.Errorf("mailint: thread: %w", err)
	}
	defer rows.Close()

	var out []ThreadEntry
	for rows.Next() {
		var e ThreadEntry
		if err := rows.Scan(&e.Direction, &e.From, &e.To, &e.Subject, &e.At); err != nil {
			return nil, fmt.Errorf("mailint: scan thread: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ThreadEntry is one email on a document.
type ThreadEntry struct {
	Direction string
	From      string
	To        string
	Subject   string
	At        time.Time
}

// Inbound reports whether this entry arrived rather than left.
func (e ThreadEntry) Inbound() bool { return e.Direction == Inbound }

// -----------------------------------------------------------------------------

// notNil turns a nil slice into an empty one.
//
// A nil Go slice reaches Postgres as NULL, and an explicit NULL overrides a
// column default, so a NOT NULL column with a default of '{}' still rejects
// the row. The failure is at the very end of a path that has already created
// a work order, which is the worst place for it.
func notNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func cleanDomain(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	if i := strings.IndexAny(v, "/:"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return "hearth.invalid"
	}
	return v
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

// SendKind is the job that pushes a reply through the relay.
const SendKind = "mail.reply"

// SendPayload is what the job carries.
type SendPayload struct {
	DocumentID string            `json:"document_id"`
	To         string            `json:"to"`
	Subject    string            `json:"subject"`
	Body       string            `json:"body"`
	Headers    map[string]string `json:"headers"`
}

// Enqueue puts a reply on the queue rather than sending it in the request.
//
// A relay that is slow makes saving a reply slow, and a relay that is down
// makes saving one fail. On the queue it is retried with backoff and lands in
// the dead letter table if it truly cannot be delivered, which is a page an
// operator can look at rather than a message that vanished.
func Enqueue(ctx context.Context, q *jobs.Queue, orgID string, p SendPayload) error {
	_, err := q.Enqueue(ctx, jobs.Spec{OrgID: orgID, Kind: SendKind, Payload: p})
	return err
}
