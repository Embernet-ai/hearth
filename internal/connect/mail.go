package connect

// Mail: messages linked to the people in the CRM, read-only.
//
// Hearth never sends, moves or deletes anything in a mailbox through this, and it does not keep mail. For a
// message that names somebody the person already has in the CRM it keeps the sender, the recipients, the subject,
// a short preview and the link back to the message at the provider. Everything else in the mailbox is read and
// dropped in the same pass.

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	mailFirstLookback = 14 * 24 * time.Hour
	mailLimit         = 200
	mailKeep          = 90 * 24 * time.Hour
)

// LinkedMail is one message shown on a contact.
type LinkedMail struct {
	ID       string
	Provider Provider
	Subject  string
	From     string
	To       []string
	Snippet  string
	Received time.Time
	WebLink  string
}

// bareAddress takes "Name <a@b.c>" or "a@b.c" and returns the lowercase address, or "".
func bareAddress(s string) string {
	if a, err := mail.ParseAddress(strings.TrimSpace(s)); err == nil {
		return strings.ToLower(a.Address)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(s, "@") && !strings.ContainsAny(s, " <>") {
		return s
	}
	return ""
}

// SyncMail reads recent mail and links what names a contact. It returns how many links it added.
func (s *Service) SyncMail(ctx context.Context, orgID, principalID string, p Provider) (int, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Mail)
	if err != nil {
		return 0, err
	}
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return 0, err
	}
	cs, err := s.crm.Contacts(ctx, orgID, actor, "", "")
	if err != nil {
		return 0, err
	}
	byAddr := map[string]string{}
	for _, c := range cs {
		if a := bareAddress(c.Email); a != "" {
			byAddr[a] = c.ID
		}
	}
	since := s.now().Add(-mailFirstLookback)
	var last *time.Time
	_ = s.db.QueryRow(ctx, `SELECT last_ok_at FROM connector_status WHERE principal_id=$1 AND provider=$2 AND connector='mail'`,
		principalID, string(p)).Scan(&last)
	if last != nil {
		// A day of overlap, so a message that arrived while the last run was reading is not missed.
		since = last.Add(-24 * time.Hour)
	}
	if len(byAddr) == 0 {
		_, _ = s.db.Exec(ctx, `DELETE FROM connector_mail WHERE principal_id=$1 AND provider=$2 AND received_at < $3`,
			principalID, string(p), s.now().Add(-mailKeep))
		return 0, nil
	}
	msgs, err := sess.Mail().Messages(ctx, since, mailLimit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range msgs {
		hit := map[string]bool{}
		for _, a := range append([]string{m.From}, m.To...) {
			if id, ok := byAddr[bareAddress(a)]; ok {
				hit[id] = true
			}
		}
		for contactID := range hit {
			ct, err := s.db.Exec(ctx, `
				INSERT INTO connector_mail (id, org_id, principal_id, provider, message_id, contact_id, subject,
				                            from_addr, to_addrs, snippet, received_at, web_link)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
				ON CONFLICT (principal_id, provider, message_id, contact_id) DO NOTHING`,
				uuid.NewString(), orgID, principalID, string(p), m.ID, contactID, clip(m.Subject, 300),
				clip(m.From, 300), clipAll(m.To, 20), clip(m.Snippet, 240), m.Received, m.WebLink)
			if err != nil {
				return n, fmt.Errorf("connect: link message: %w", err)
			}
			n += int(ct.RowsAffected())
		}
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM connector_mail WHERE principal_id=$1 AND provider=$2 AND received_at < $3`,
		principalID, string(p), s.now().Add(-mailKeep))
	return n, nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func clipAll(in []string, n int) []string {
	if len(in) > n {
		in = in[:n]
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = clip(v, 300)
	}
	return out
}

// MailFor lists the linked messages on a contact, newest first. Only the person who connected the mailbox sees
// what came from it: a message in somebody's mailbox is not visible to a colleague because they can see the
// contact.
func (s *Service) MailFor(ctx context.Context, orgID, principalID, contactID string, limit int) ([]LinkedMail, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, provider, subject, from_addr, to_addrs, snippet, received_at, web_link
		  FROM connector_mail
		 WHERE org_id=$1 AND principal_id=$2 AND contact_id=$3
		 ORDER BY received_at DESC LIMIT $4`, orgID, principalID, contactID, limit)
	if err != nil {
		return nil, fmt.Errorf("connect: read linked mail: %w", err)
	}
	defer rows.Close()
	var out []LinkedMail
	for rows.Next() {
		var m LinkedMail
		var p string
		if err := rows.Scan(&m.ID, &p, &m.Subject, &m.From, &m.To, &m.Snippet, &m.Received, &m.WebLink); err != nil {
			return nil, err
		}
		m.Provider = Provider(p)
		out = append(out, m)
	}
	return out, rows.Err()
}
