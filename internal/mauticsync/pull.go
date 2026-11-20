package mauticsync

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/mautic"
)

const pageSize = 500

// Where each pull stopped, in mautic_sync_state.
const (
	curSent  = "email_stats.sent"
	curRead  = "email_stats.read"
	curClick = "page_hits"
	curForm  = "form_submissions"
	curDNC   = "lead_donotcontact"
)

func (s *Syncer) cursor(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRow(ctx, `SELECT value FROM mautic_sync_state WHERE org_id=$1 AND key=$2`, s.org, key).Scan(&v)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Syncer) setCursor(ctx context.Context, key, v string) error {
	if !s.opt.Write {
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mautic_sync_state (org_id, key, value) VALUES ($1,$2,$3)
		ON CONFLICT (org_id, key) DO UPDATE SET value = EXCLUDED.value`, s.org, key, v)
	return err
}

// botPrincipal finds, or makes, the principal the timeline notes are written as, so Hearth reads "Mautic" and not a person.
func (s *Syncer) botPrincipal(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM principals WHERE org_id=$1 AND kind='bot' AND handle='mautic' AND status='active'`, s.org).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", fmt.Errorf("find the Mautic principal: %w", err)
	}
	id = uuid.NewString()
	if _, err := s.db.Exec(ctx, `INSERT INTO principals (id, org_id, kind, display_name, handle) VALUES ($1,$2,'bot','Mautic','mautic')
		ON CONFLICT DO NOTHING`, id, s.org); err != nil {
		return "", fmt.Errorf("make the Mautic principal: %w", err)
	}
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM principals WHERE org_id=$1 AND kind='bot' AND handle='mautic'`, s.org).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

// byMautic maps a Mautic contact id to the Hearth contact it was made from.
func (s *Syncer) byMautic(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.Query(ctx, `SELECT mautic_id, contact_id::text FROM mautic_links WHERE org_id=$1`, s.org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var mid int64
		var cid string
		if err := rows.Scan(&mid, &cid); err != nil {
			return nil, err
		}
		m[mid] = cid
	}
	return m, rows.Err()
}

// readAll pages through a stats table with an id greater than after and calls fn on each row, oldest first.
func (s *Syncer) readAll(ctx context.Context, table string, after int64, fn func(mautic.Row) error, extra ...mautic.Where) (int64, error) {
	last := after
	for {
		w := append([]mautic.Where{{Col: "id", Expr: "gt", Val: strconv.FormatInt(last, 10)}}, extra...)
		rows, err := s.mc.Stats(ctx, table, "id", pageSize, w...)
		if err != nil {
			return last, fmt.Errorf("read Mautic's %s: %w", table, err)
		}
		for _, r := range rows {
			if err := fn(r); err != nil {
				return last, err
			}
			if r.ID() > last {
				last = r.ID()
			}
		}
		if len(rows) < pageSize {
			return last, nil
		}
	}
}

// -----------------------------------------------------------------------------
// Do-not-contact, Mautic to Hearth
// -----------------------------------------------------------------------------

// pullDoNotContact reads every email do-not-contact Mautic holds (the table is small and a stale cursor must never hide one),
// records each on its Hearth contact as an opt-out, and remembers which Mautic contacts hold one so the push does not add it twice.
func (s *Syncer) pullDoNotContact(ctx context.Context, links map[string]*link) error {
	by, err := s.byMautic(ctx)
	if err != nil {
		return err
	}
	seen, err := s.cursor(ctx, curDNC)
	if err != nil {
		return err
	}
	seenID, _ := strconv.ParseInt(seen, 10, 64)
	maxID := seenID
	_, err = s.readAll(ctx, "lead_donotcontact", 0, func(r mautic.Row) error {
		if r["channel"] != "email" {
			return nil
		}
		lead := r.Int("lead_id")
		s.mu.Lock()
		s.dnc[lead] = true
		s.mu.Unlock()
		if r.ID() > maxID {
			maxID = r.ID()
		}
		cid, ok := by[lead]
		if !ok {
			if r.ID() > seenID {
				s.rep.add("skip", "opt-out", strconv.FormatInt(lead, 10), "", "a Mautic contact the sync did not send: nothing in Hearth to mark")
			}
			return nil
		}
		reason := map[int64]string{mautic.DNCUnsubscribed: "unsubscribed", mautic.DNCBounced: "bounced"}[r.Int("reason")]
		if reason == "" {
			reason = "manual"
		}
		when := r.Time("date_added")
		if when.IsZero() {
			when = time.Now().UTC()
		}
		var already *time.Time
		if err := s.db.QueryRow(ctx, `SELECT marketing_opt_out_at FROM contacts WHERE id=$1`, cid).Scan(&already); err != nil {
			return err
		}
		if already == nil {
			if s.opt.Write {
				if _, err := s.db.Exec(ctx, `UPDATE contacts SET marketing_opt_out_at=$2, marketing_opt_out_reason=$3 WHERE id=$1 AND marketing_opt_out_at IS NULL`,
					cid, when, reason); err != nil {
					return err
				}
			}
			s.rep.add("create", "opt-out", cid, "", reason+" in Mautic")
		}
		// A do-not-contact the sync itself put there for an opt-out Hearth already held says nothing new.
		if r.ID() > seenID && !strings.HasPrefix(r["comments"], "Hearth:") {
			title := map[string]string{"unsubscribed": "Unsubscribed from email", "bounced": "Email address bounced"}[reason]
			if title == "" {
				title = "Marked do not contact for email in Mautic"
			}
			if err := s.note(ctx, "dnc:"+strconv.FormatInt(r.ID(), 10), cid, title, r["comments"], when); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.setCursor(ctx, curDNC, strconv.FormatInt(maxID, 10))
}

// -----------------------------------------------------------------------------
// Activity, Mautic to Hearth
// -----------------------------------------------------------------------------

func (s *Syncer) emailName(ctx context.Context, id int64) string {
	if id == 0 {
		return "an email"
	}
	if v, ok := s.emailCch[id]; ok {
		return v
	}
	n, err := s.mc.Email(ctx, id)
	v := n.Subject
	if v == "" {
		v = n.Name
	}
	if err != nil || v == "" {
		v = "email " + strconv.FormatInt(id, 10)
	}
	s.emailCch[id] = v
	return v
}

func (s *Syncer) formName(ctx context.Context, id int64) string {
	if v, ok := s.formCch[id]; ok {
		return v
	}
	n, err := s.mc.Form(ctx, id)
	if err != nil || n == "" {
		n = "form " + strconv.FormatInt(id, 10)
	}
	s.formCch[id] = n
	return n
}

func (s *Syncer) pullActivity(ctx context.Context) error {
	by, err := s.byMautic(ctx)
	if err != nil {
		return err
	}
	cur := func(k string) int64 {
		v, _ := s.cursor(ctx, k)
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}

	// Sent.
	last, err := s.readAll(ctx, "email_stats", cur(curSent), func(r mautic.Row) error {
		cid, ok := by[r.Int("lead_id")]
		if !ok || r["is_failed"] == "1" {
			return nil
		}
		return s.note(ctx, "sent:"+r["id"], cid, "Email sent: "+s.emailName(ctx, r.Int("email_id")),
			"Sent to "+r["email_address"]+" through Mautic.", r.Time("date_sent"))
	})
	if err != nil {
		return err
	}
	if err := s.setCursor(ctx, curSent, strconv.FormatInt(last, 10)); err != nil {
		return err
	}

	// Opened. A stat row is made when the email is sent and filled in when it is first opened, so the id cursor cannot find it:
	// read by the time it was opened, and let the note's key (the stat id) refuse a repeat.
	readFrom, _ := s.cursor(ctx, curRead)
	maxRead := readFrom
	for {
		w := []mautic.Where{{Col: "is_read", Expr: "eq", Val: "1"}}
		if readFrom != "" {
			w = append(w, mautic.Where{Col: "date_read", Expr: "gte", Val: readFrom})
		}
		rows, err := s.mc.Stats(ctx, "email_stats", "date_read", pageSize, w...)
		if err != nil {
			return fmt.Errorf("read Mautic's opens: %w", err)
		}
		for _, r := range rows {
			if t := r.Time("date_read"); !t.IsZero() {
				if ts := t.Format("2006-01-02 15:04:05"); ts > maxRead {
					maxRead = ts
				}
			}
			if cid, ok := by[r.Int("lead_id")]; ok {
				if err := s.note(ctx, "open:"+r["id"], cid, "Email opened: "+s.emailName(ctx, r.Int("email_id")),
					"Opened by "+r["email_address"]+".", r.Time("date_read")); err != nil {
					return err
				}
			}
		}
		// A full page that did not move past the cursor would loop for ever; stop and let the next run carry on.
		if len(rows) < pageSize || maxRead == readFrom {
			break
		}
		readFrom = maxRead
	}
	if maxRead != "" {
		if err := s.setCursor(ctx, curRead, maxRead); err != nil {
			return err
		}
	}

	// Clicked a link in an email.
	last, err = s.readAll(ctx, "page_hits", cur(curClick), func(r mautic.Row) error {
		eid := r.Int("email_id")
		cid, ok := by[r.Int("lead_id")]
		if eid == 0 || !ok {
			return nil
		}
		return s.note(ctx, "click:"+r["id"], cid, "Clicked a link in: "+s.emailName(ctx, eid), r["url"], r.Time("date_hit"))
	})
	if err != nil {
		return err
	}
	if err := s.setCursor(ctx, curClick, strconv.FormatInt(last, 10)); err != nil {
		return err
	}

	// Submitted a form.
	last, err = s.readAll(ctx, "form_submissions", cur(curForm), func(r mautic.Row) error {
		cid, ok := by[r.Int("lead_id")]
		if !ok {
			return nil
		}
		return s.note(ctx, "form:"+r["id"], cid, "Submitted a form: "+s.formName(ctx, r.Int("form_id")), "", r.Time("date_submitted"))
	})
	if err != nil {
		return err
	}
	return s.setCursor(ctx, curForm, strconv.FormatInt(last, 10))
}

// note writes one line on a contact's timeline (and its company's), once. key names the Mautic row it came from.
func (s *Syncer) note(ctx context.Context, key, contactID, title, body string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if !s.opt.Write {
		s.rep.add("create", "activity", contactID, title, "")
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := uuid.NewString()
	tag, err := tx.Exec(ctx, `INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id)
		VALUES ($1,'mautic',$2,'activity',$3) ON CONFLICT DO NOTHING`, s.org, key, id)
	if err != nil {
		return fmt.Errorf("record %s: %w", key, err)
	}
	if tag.RowsAffected() == 0 {
		return nil // already on the timeline
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_activities (id, org_id, kind, title, body, occurred_at, created_by)
		VALUES ($1,$2,'note',$3,$4,$5,$6)`, id, s.org, strings.TrimSpace(title), strings.TrimSpace(body), at, s.bot); err != nil {
		return fmt.Errorf("write the note %s: %w", key, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_activity_targets (activity_id, target_type, target_id) VALUES ($1,'contact',$2)`, id, contactID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_activity_targets (activity_id, target_type, target_id)
		SELECT $1,'company',company_id FROM contacts WHERE id=$2 AND company_id IS NOT NULL ON CONFLICT DO NOTHING`, id, contactID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.rep.add("create", "activity", contactID, title, key)
	return nil
}
