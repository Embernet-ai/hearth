package google

// Reading the calendar and the mailbox.
//
// Both go through the full calendar and mail scopes, so reading, writing and sending are all
// available to the person who connected.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// base64URLDecode accepts both padded and unpadded, which Google mixes: id
// token segments are unpadded and message bodies are padded and URL-safe.
func base64URLDecode(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// get calls a Google API as one person and decodes the result.
func (s *Service) get(ctx context.Context, principalID, path string, q url.Values, into any) error {
	tok, err := s.token(ctx, principalID)
	if err != nil {
		return err
	}
	full := s.apiBase + path
	if len(q) > 0 {
		full += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("google: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(into)
	case http.StatusUnauthorized:
		// The token was refreshed moments ago, so a 401 here means the grant
		// itself is gone rather than that it expired.
		_, _ = s.db.Exec(ctx, `
			UPDATE google_connections SET revoked_at = now(),
			       last_error = 'Google refused the connection'
			 WHERE principal_id = $1`, principalID)
		return ErrReconnect
	case http.StatusForbidden:
		// Usually the API is not enabled on the project, or the scope was not
		// granted. Both are configuration rather than faults, and both read as
		// mysterious without this sentence.
		return fmt.Errorf("google: refused %s. The API may not be enabled on the "+
			"Google project, or this scope was not granted", path)
	default:
		return fmt.Errorf("google: GET %s returned %s", path, resp.Status)
	}
}

// -----------------------------------------------------------------------------
// Calendar
// -----------------------------------------------------------------------------

// SyncCalendar pulls a window of somebody's primary calendar into google_events.
//
// A window rather than everything: a calendar going back years is not what
// anybody is looking at, and singleEvents expands recurrence so a weekly
// stand-up arrives as the instances a person actually has rather than as one
// rule this would then have to interpret.
func (s *Service) SyncCalendar(ctx context.Context, orgID, principalID string,
	from, to time.Time) (int, error) {

	var out struct {
		Items []struct {
			ID       string `json:"id"`
			Summary  string `json:"summary"`
			Location string `json:"location"`
			HTMLLink string `json:"htmlLink"`
			Status   string `json:"status"`
			Start    struct {
				DateTime time.Time `json:"dateTime"`
				Date     string    `json:"date"`
			} `json:"start"`
			End struct {
				DateTime time.Time `json:"dateTime"`
				Date     string    `json:"date"`
			} `json:"end"`
		} `json:"items"`
	}
	q := url.Values{}
	q.Set("timeMin", from.Format(time.RFC3339))
	q.Set("timeMax", to.Format(time.RFC3339))
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	q.Set("maxResults", "250")

	if err := s.get(ctx, principalID, "/calendar/v3/calendars/primary/events", q, &out); err != nil {
		return 0, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("google: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The window is replaced rather than merged, so an event somebody deleted
	// in Google disappears here instead of lingering forever. A merge-only sync
	// is how a cancelled meeting stays on a calendar for months.
	if _, err := tx.Exec(ctx, `
		DELETE FROM google_events
		 WHERE principal_id = $1 AND starts_at >= $2 AND starts_at < $3`,
		principalID, from, to); err != nil {
		return 0, fmt.Errorf("google: clear window: %w", err)
	}

	n := 0
	for _, it := range out.Items {
		if it.Status == "cancelled" {
			continue
		}
		var start, end time.Time
		allDay := it.Start.Date != ""
		if allDay {
			// A date, not an instant. Parsed in the server's location so it
			// lands on the day it says rather than the previous evening.
			start, _ = time.ParseInLocation("2006-01-02", it.Start.Date, time.Local)
			end, _ = time.ParseInLocation("2006-01-02", it.End.Date, time.Local)
			// Google's all-day end is exclusive, same as iCalendar's. Stored
			// inclusive here to match how hearth's own events are held.
			if end.After(start) {
				end = end.AddDate(0, 0, -1)
			}
		} else {
			start, end = it.Start.DateTime, it.End.DateTime
		}
		if start.IsZero() {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO google_events
			    (principal_id, google_id, org_id, title, location,
			     starts_at, ends_at, all_day, html_link, synced_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
			ON CONFLICT (principal_id, google_id) DO UPDATE SET
			    title = EXCLUDED.title, location = EXCLUDED.location,
			    starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at,
			    all_day = EXCLUDED.all_day, html_link = EXCLUDED.html_link,
			    synced_at = now()`,
			principalID, it.ID, orgID, it.Summary, it.Location,
			start, end, allDay, it.HTMLLink); err != nil {
			return 0, fmt.Errorf("google: store event: %w", err)
		}
		n++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("google: commit: %w", err)
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// Mail
// -----------------------------------------------------------------------------

// Message is one message, headers only unless the body was asked for.
type Message struct {
	ID      string
	From    string
	Subject string
	Snippet string
	Date    time.Time
	Unread  bool
}

// Inbox lists recent messages.
//
// Two calls per message is what the Gmail API costs: list returns ids and
// nothing else. Bounded hard at a small number for that reason, because this is
// a glance at what is waiting rather than a mail client.
func (s *Service) Inbox(ctx context.Context, principalID string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 25 {
		limit = 10
	}
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	q := url.Values{}
	q.Set("maxResults", fmt.Sprint(limit))
	q.Set("labelIds", "INBOX")
	if err := s.get(ctx, principalID, "/gmail/v1/users/me/messages", q, &list); err != nil {
		return nil, err
	}

	out := make([]Message, 0, len(list.Messages))
	for _, m := range list.Messages {
		var full struct {
			ID           string   `json:"id"`
			Snippet      string   `json:"snippet"`
			LabelIDs     []string `json:"labelIds"`
			InternalDate string   `json:"internalDate"`
			Payload      struct {
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"payload"`
		}
		mq := url.Values{}
		mq.Set("format", "metadata")
		mq.Add("metadataHeaders", "From")
		mq.Add("metadataHeaders", "Subject")
		if err := s.get(ctx, principalID, "/gmail/v1/users/me/messages/"+m.ID, mq, &full); err != nil {
			// One unreadable message does not empty the list. A mailbox that
			// shows nine of ten is more use than one that shows an error.
			continue
		}
		msg := Message{ID: full.ID, Snippet: full.Snippet}
		for _, h := range full.Payload.Headers {
			switch h.Name {
			case "From":
				msg.From = h.Value
			case "Subject":
				msg.Subject = h.Value
			}
		}
		for _, l := range full.LabelIDs {
			if l == "UNREAD" {
				msg.Unread = true
			}
		}
		if ms, err := parseMillis(full.InternalDate); err == nil {
			msg.Date = ms
		}
		out = append(out, msg)
	}
	return out, nil
}

func parseMillis(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	var ms int64
	if _, err := fmt.Sscanf(s, "%d", &ms); err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}
