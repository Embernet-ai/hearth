package google

// Google Workspace, through Google's own Go clients (google.golang.org/api): Calendar, People, Gmail and Drive.
//
// This is the connect.Backend for Google. The connection, its sealed tokens and its refresh are the ones already
// in this package; this file adds what the connectors need on top: the scopes each asks for, and a session whose
// calls are made as the person.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	gcal "google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"

	"github.com/Embernet-ai/hearth/internal/connect"
)

// Scopes the connectors ask for.
const (
	ScopeCalendarList = "https://www.googleapis.com/auth/calendar.calendarlist.readonly"
	ScopeContacts     = "https://www.googleapis.com/auth/contacts.readonly"
	ScopeGmailMeta    = "https://www.googleapis.com/auth/gmail.metadata"
	ScopeDriveRead    = "https://www.googleapis.com/auth/drive.readonly"

	// The scopes the connectors ask for. They are read and write, because Hearth is a company tool that works in
	// the person's own mailbox, calendar, address book and drive as that person.
	ScopeMailFull     = "https://mail.google.com/"
	ScopeCalendarFull = "https://www.googleapis.com/auth/calendar"
	ScopeContactsFull = "https://www.googleapis.com/auth/contacts"
	ScopeDriveFull    = "https://www.googleapis.com/auth/drive"
)

// apiEndpoints are overridden by tests, which serve a recorded Google from one address.
type apiEndpoints struct{ calendar, people, gmail, drive string }

var _ connect.Backend = (*Service)(nil)

func (s *Service) Provider() connect.Provider { return connect.Google }

// Configured reports whether the client is set up and there is somewhere safe to keep a token.
func (s *Service) Configured() bool { return s.cfg.Enabled() && s.box != nil }

// Scopes is what each connector asks for: Google's full scope for the product, read and write.
func (s *Service) Scopes(k connect.Kind) []string {
	switch k {
	case connect.Calendar:
		return []string{ScopeCalendarFull}
	case connect.Contacts:
		return []string{ScopeContactsFull}
	case connect.Mail:
		return []string{ScopeMailFull}
	case connect.Files:
		return []string{ScopeDriveFull}
	}
	return nil
}

// expand adds the narrower scopes a broader grant already covers, so a grant of the full scope is recognised for
// whatever narrower scope something asks about.
func expand(granted []string) []string {
	have := map[string]bool{}
	for _, g := range granted {
		have[g] = true
	}
	add := func(when string, then ...string) {
		if have[when] {
			for _, t := range then {
				have[t] = true
			}
		}
	}
	add(ScopeCalendarFull, ScopeCalendarWrite, ScopeCalendarRead, ScopeCalendarList)
	add(ScopeCalendarWrite, ScopeCalendarRead)
	add(ScopeCalendarRead, ScopeCalendarList)
	add(ScopeContactsFull, ScopeContacts)
	add(ScopeMailFull, ScopeGmailRead, ScopeGmailSend, ScopeGmailMeta)
	add(ScopeGmailRead, ScopeGmailMeta)
	add(ScopeDriveFull, ScopeDriveRead)
	out := make([]string, 0, len(have))
	for k := range have {
		out = append(out, k)
	}
	return out
}

// Granted reads what the person's connection covers.
func (s *Service) Granted(ctx context.Context, principalID string) ([]string, error) {
	c, err := s.Get(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if !c.Live() {
		return nil, ErrReconnect
	}
	return expand(c.Scopes), nil
}

// Connections lists the connections in an organisation, without any token.
func (s *Service) Connections(ctx context.Context, orgID string) ([]connect.Conn, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.principal_id::text, COALESCE(p.display_name,''), c.email::text, c.scopes, c.connected_at,
		       c.revoked_at IS NOT NULL, c.last_error
		  FROM google_connections c JOIN principals p ON p.id = c.principal_id
		 WHERE c.org_id = $1 ORDER BY c.connected_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("google: list connections: %w", err)
	}
	defer rows.Close()
	var out []connect.Conn
	for rows.Next() {
		c := connect.Conn{OrgID: orgID}
		if err := rows.Scan(&c.PrincipalID, &c.Name, &c.Email, &c.Scopes, &c.ConnectedAt, &c.Revoked, &c.LastError); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// tokenSource hands the Google clients the person's current token, asking this package for one each half minute:
// token() is a database read unless it has to refresh.
type tokenSource struct {
	s         *Service
	principal string
}

func (t tokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := t.s.token(ctx, t.principal)
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: tok, TokenType: "Bearer", Expiry: time.Now().Add(30 * time.Second)}, nil
}

// Session opens one person's Google clients.
func (s *Service) Session(ctx context.Context, principalID string) (connect.Session, error) {
	if _, err := s.Granted(ctx, principalID); err != nil {
		return nil, err
	}
	hc := &http.Client{
		Transport: &oauth2.Transport{Source: oauth2.ReuseTokenSource(nil, tokenSource{s: s, principal: principalID}), Base: s.http.Transport},
		Timeout:   60 * time.Second,
	}
	opts := func(base string) []option.ClientOption {
		o := []option.ClientOption{option.WithHTTPClient(hc)}
		if base != "" {
			o = append(o, option.WithEndpoint(base))
		}
		return o
	}
	cal, err := gcal.NewService(ctx, opts(s.api.calendar)...)
	if err != nil {
		return nil, err
	}
	ppl, err := people.NewService(ctx, opts(s.api.people)...)
	if err != nil {
		return nil, err
	}
	gm, err := gmail.NewService(ctx, opts(s.api.gmail)...)
	if err != nil {
		return nil, err
	}
	dr, err := drive.NewService(ctx, opts(s.api.drive)...)
	if err != nil {
		return nil, err
	}
	return &session{s: s, principal: principalID, cal: cal, ppl: ppl, gm: gm, dr: dr}, nil
}

type session struct {
	s         *Service
	principal string
	cal       *gcal.Service
	ppl       *people.Service
	gm        *gmail.Service
	dr        *drive.Service
}

type contactsAdapter struct{ x *session }

// mailAdapter and filesAdapter are separate types because a session already has a Delete for the calendar, and
// mail and files each have their own.
type mailAdapter struct{ *session }
type filesAdapter struct{ *session }

func (x *session) Calendar() connect.CalendarAPI { return x }
func (x *session) Contacts() connect.ContactsAPI { return contactsAdapter{x} }
func (x *session) Mail() connect.MailAPI         { return mailAdapter{x} }
func (x *session) Files() connect.FilesAPI       { return filesAdapter{x} }

// fail turns a Google API error into what the rest of Hearth understands.
func (x *session) fail(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, connect.ErrReconnect) || errors.Is(err, connect.ErrNotConnected) {
		return err
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		switch ge.Code {
		case http.StatusUnauthorized:
			_, _ = x.s.db.Exec(ctx, `UPDATE google_connections SET revoked_at = now(),
				last_error = 'Google refused the connection' WHERE principal_id = $1`, x.principal)
			return ErrReconnect
		case http.StatusForbidden:
			reason := ge.Message
			if len(ge.Errors) > 0 && ge.Errors[0].Reason != "" {
				reason = ge.Errors[0].Reason + ": " + ge.Message
			}
			return fmt.Errorf("google refused that (%s). The API may not be enabled on the Google project, or the scope was not granted: %w", reason, connect.ErrScope)
		case http.StatusNotFound, http.StatusGone:
			return connect.ErrNotFound
		}
	}
	return fmt.Errorf("google: %w", err)
}

// -----------------------------------------------------------------------------
// Calendar
// -----------------------------------------------------------------------------

func (x *session) Calendars(ctx context.Context) ([]connect.CalendarInfo, error) {
	var out []connect.CalendarInfo
	err := x.cal.CalendarList.List().Context(ctx).Pages(ctx, func(r *gcal.CalendarList) error {
		for _, c := range r.Items {
			name := c.Summary
			if c.SummaryOverride != "" {
				name = c.SummaryOverride
			}
			out = append(out, connect.CalendarInfo{ID: c.Id, Name: name, Primary: c.Primary,
				Writable: c.AccessRole == "owner" || c.AccessRole == "writer"})
		}
		return nil
	})
	return out, x.fail(ctx, err)
}

func calID(id string) string {
	if id == "" {
		return "primary"
	}
	return id
}

func eventFrom(e *gcal.Event) connect.Event {
	ev := connect.Event{ID: e.Id, Version: e.Etag, Title: e.Summary, Notes: e.Description, Place: e.Location, JoinURL: e.HangoutLink}
	if ev.JoinURL == "" && e.ConferenceData != nil {
		for _, ep := range e.ConferenceData.EntryPoints {
			if ep.EntryPointType == "video" && ep.Uri != "" {
				ev.JoinURL = ep.Uri
				break
			}
		}
	}
	if e.Start != nil && e.Start.Date != "" {
		ev.AllDay = true
		ev.Start, _ = time.Parse("2006-01-02", e.Start.Date)
		if e.End != nil && e.End.Date != "" {
			ev.End, _ = time.Parse("2006-01-02", e.End.Date)
		}
		if ev.End.IsZero() {
			ev.End = ev.Start.AddDate(0, 0, 1)
		}
		return ev
	}
	if e.Start != nil {
		ev.Start, _ = time.Parse(time.RFC3339, e.Start.DateTime)
		ev.Start = ev.Start.UTC()
	}
	if e.End != nil {
		ev.End, _ = time.Parse(time.RFC3339, e.End.DateTime)
		ev.End = ev.End.UTC()
	}
	return ev
}

func eventTo(e connect.Event) *gcal.Event {
	out := &gcal.Event{Summary: e.Title, Description: e.Notes, Location: e.Place,
		ForceSendFields: []string{"Summary", "Description", "Location"}}
	if e.AllDay {
		out.Start = &gcal.EventDateTime{Date: e.Start.UTC().Format("2006-01-02")}
		out.End = &gcal.EventDateTime{Date: e.End.UTC().Format("2006-01-02")}
	} else {
		out.Start = &gcal.EventDateTime{DateTime: e.Start.UTC().Format(time.RFC3339)}
		out.End = &gcal.EventDateTime{DateTime: e.End.UTC().Format(time.RFC3339)}
	}
	return out
}

func (x *session) Events(ctx context.Context, calendarID string, from, to time.Time) ([]connect.Event, error) {
	var out []connect.Event
	call := x.cal.Events.List(calID(calendarID)).TimeMin(from.UTC().Format(time.RFC3339)).TimeMax(to.UTC().Format(time.RFC3339)).
		SingleEvents(true).ShowDeleted(false).MaxResults(250).OrderBy("startTime")
	err := call.Context(ctx).Pages(ctx, func(r *gcal.Events) error {
		for _, e := range r.Items {
			if e.Status == "cancelled" {
				continue
			}
			out = append(out, eventFrom(e))
		}
		return nil
	})
	return out, x.fail(ctx, err)
}

func (x *session) Create(ctx context.Context, calendarID string, e connect.Event, meeting bool) (connect.Event, error) {
	ev := eventTo(e)
	call := x.cal.Events.Insert(calID(calendarID), ev)
	if meeting && !e.AllDay {
		ev.ConferenceData = &gcal.ConferenceData{CreateRequest: &gcal.CreateConferenceRequest{
			RequestId:             uuid.NewString(),
			ConferenceSolutionKey: &gcal.ConferenceSolutionKey{Type: "hangoutsMeet"},
		}}
		call = call.ConferenceDataVersion(1)
	}
	res, err := call.Context(ctx).Do()
	if err != nil {
		return connect.Event{}, x.fail(ctx, err)
	}
	return eventFrom(res), nil
}

func (x *session) Update(ctx context.Context, calendarID string, e connect.Event) (connect.Event, error) {
	res, err := x.cal.Events.Patch(calID(calendarID), e.ID, eventTo(e)).Context(ctx).Do()
	if err != nil {
		return connect.Event{}, x.fail(ctx, err)
	}
	return eventFrom(res), nil
}

func (x *session) Delete(ctx context.Context, calendarID, id string) error {
	err := x.cal.Events.Delete(calID(calendarID), id).Context(ctx).Do()
	if err != nil && errors.Is(x.fail(ctx, err), connect.ErrNotFound) {
		return nil
	}
	return x.fail(ctx, err)
}

// -----------------------------------------------------------------------------
// Contacts
// -----------------------------------------------------------------------------

func (a contactsAdapter) Contacts(ctx context.Context) ([]connect.Contact, error) {
	x := a.x
	var out []connect.Contact
	err := x.ppl.People.Connections.List("people/me").PersonFields("names,emailAddresses,phoneNumbers,organizations").
		PageSize(500).Pages(ctx, func(r *people.ListConnectionsResponse) error {
		for _, p := range r.Connections {
			c := connect.Contact{ID: p.ResourceName, Version: p.Etag}
			if len(p.Names) > 0 {
				c.Name = p.Names[0].DisplayName
			}
			if len(p.EmailAddresses) > 0 {
				c.Email = p.EmailAddresses[0].Value
			}
			if len(p.PhoneNumbers) > 0 {
				c.Phone = p.PhoneNumbers[0].Value
			}
			if len(p.Organizations) > 0 {
				c.Company, c.Title = p.Organizations[0].Name, p.Organizations[0].Title
			}
			out = append(out, c)
		}
		return nil
	})
	return out, x.fail(ctx, err)
}

const personFields = "names,emailAddresses,phoneNumbers,organizations"

func personTo(c connect.Contact) *people.Person {
	p := &people.Person{}
	given, family := splitName(c.Name)
	p.Names = []*people.Name{{GivenName: given, FamilyName: family}}
	if c.Email != "" {
		p.EmailAddresses = []*people.EmailAddress{{Value: c.Email}}
	}
	if c.Phone != "" {
		p.PhoneNumbers = []*people.PhoneNumber{{Value: c.Phone}}
	}
	if c.Company != "" || c.Title != "" {
		p.Organizations = []*people.Organization{{Name: c.Company, Title: c.Title}}
	}
	return p
}

// splitName takes "Priya Shah" to a given name and a family name, and a single word to a given name alone.
func splitName(n string) (string, string) {
	n = strings.TrimSpace(n)
	if i := strings.LastIndex(n, " "); i > 0 {
		return strings.TrimSpace(n[:i]), strings.TrimSpace(n[i+1:])
	}
	return n, ""
}

func contactFrom(p *people.Person) connect.Contact {
	c := connect.Contact{ID: p.ResourceName, Version: p.Etag}
	if len(p.Names) > 0 {
		c.Name = p.Names[0].DisplayName
	}
	if len(p.EmailAddresses) > 0 {
		c.Email = p.EmailAddresses[0].Value
	}
	if len(p.PhoneNumbers) > 0 {
		c.Phone = p.PhoneNumbers[0].Value
	}
	if len(p.Organizations) > 0 {
		c.Company, c.Title = p.Organizations[0].Name, p.Organizations[0].Title
	}
	return c
}

func (a contactsAdapter) Create(ctx context.Context, c connect.Contact) (connect.Contact, error) {
	p, err := a.x.ppl.People.CreateContact(personTo(c)).PersonFields(personFields).Context(ctx).Do()
	if err != nil {
		return connect.Contact{}, a.x.fail(ctx, err)
	}
	out := contactFrom(p)
	if out.Name == "" {
		out.Name = c.Name
	}
	return out, nil
}

func (a contactsAdapter) Update(ctx context.Context, c connect.Contact) (connect.Contact, error) {
	p := personTo(c)
	// Google refuses an edit made from a copy that is not the current one, so the person goes back with the etag
	// it was last read with.
	p.Etag = c.Version
	out, err := a.x.ppl.People.UpdateContact(c.ID, p).UpdatePersonFields(personFields).PersonFields(personFields).Context(ctx).Do()
	if err != nil {
		return connect.Contact{}, a.x.fail(ctx, err)
	}
	return contactFrom(out), nil
}

func (a contactsAdapter) Delete(ctx context.Context, id string) error {
	_, err := a.x.ppl.People.DeleteContact(id).Context(ctx).Do()
	if err != nil && errors.Is(a.x.fail(ctx, err), connect.ErrNotFound) {
		return nil
	}
	return a.x.fail(ctx, err)
}

// -----------------------------------------------------------------------------
// Mail, read and write, through Gmail
// -----------------------------------------------------------------------------

func header(p *gmail.MessagePart, name string) string {
	if p == nil {
		return ""
	}
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func splitAddrs(v string) []string {
	var out []string
	if strings.TrimSpace(v) == "" {
		return nil
	}
	if list, err := mail.ParseAddressList(v); err == nil {
		for _, a := range list {
			out = append(out, a.String())
		}
		return out
	}
	for _, a := range strings.Split(v, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// bodyOf finds the readable body of a message: the plain text part when there is one, else the HTML one.
func bodyOf(p *gmail.MessagePart) (text string, html bool) {
	var plain, rich string
	var walk func(*gmail.MessagePart)
	walk = func(q *gmail.MessagePart) {
		if q == nil {
			return
		}
		if q.Body != nil && q.Body.Data != "" && q.Filename == "" {
			if raw, err := base64URLDecode(q.Body.Data); err == nil {
				switch {
				case strings.HasPrefix(q.MimeType, "text/plain") && plain == "":
					plain = string(raw)
				case strings.HasPrefix(q.MimeType, "text/html") && rich == "":
					rich = string(raw)
				}
			}
		}
		for _, c := range q.Parts {
			walk(c)
		}
	}
	walk(p)
	if plain != "" {
		return plain, false
	}
	return rich, rich != ""
}

func attachmentNames(p *gmail.MessagePart) []string {
	var out []string
	var walk func(*gmail.MessagePart)
	walk = func(q *gmail.MessagePart) {
		if q == nil {
			return
		}
		if q.Filename != "" {
			out = append(out, q.Filename)
		}
		for _, c := range q.Parts {
			walk(c)
		}
	}
	walk(p)
	return out
}

func messageFrom(m *gmail.Message, withBody bool) connect.Message {
	out := connect.Message{ID: m.Id, ThreadID: m.ThreadId, Snippet: m.Snippet, Received: time.UnixMilli(m.InternalDate).UTC(),
		WebLink: "https://mail.google.com/mail/u/0/#all/" + m.Id, Labels: m.LabelIds}
	if m.Payload != nil {
		out.Subject = header(m.Payload, "Subject")
		out.From = header(m.Payload, "From")
		out.To = splitAddrs(header(m.Payload, "To"))
		out.Cc = splitAddrs(header(m.Payload, "Cc"))
		out.Attached = attachmentNames(m.Payload)
		if withBody {
			out.Body, out.HTML = bodyOf(m.Payload)
		}
	}
	for _, l := range m.LabelIds {
		switch l {
		case "UNREAD":
			out.Unread = true
		case "INBOX", "SENT", "DRAFT", "TRASH", "SPAM":
			if out.Folder == "" {
				out.Folder = l
			}
		}
	}
	return out
}

func (x *session) getFull(ctx context.Context, id string) (*gmail.Message, error) {
	m, err := x.gm.Users.Messages.Get("me", id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	return m, nil
}

// Messages reads the inbox and sent mail, with bodies, back to a cut-off.
func (x mailAdapter) Messages(ctx context.Context, since time.Time, limit int) ([]connect.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []connect.Message
	for _, label := range []string{"INBOX", "SENT"} {
		got, token := 0, ""
	pages:
		for got < limit {
			call := x.gm.Users.Messages.List("me").LabelIds(label).MaxResults(50)
			if token != "" {
				call = call.PageToken(token)
			}
			list, err := call.Context(ctx).Do()
			if err != nil {
				return out, x.fail(ctx, err)
			}
			for _, m := range list.Messages {
				full, err := x.getFull(ctx, m.Id)
				if err != nil {
					continue // one unreadable message does not lose the rest
				}
				if time.UnixMilli(full.InternalDate).Before(since) {
					break pages
				}
				out = append(out, messageFrom(full, true))
				got++
				if got >= limit {
					break pages
				}
			}
			if list.NextPageToken == "" {
				break
			}
			token = list.NextPageToken
		}
	}
	return out, nil
}

func (x mailAdapter) Inbox(ctx context.Context, q connect.MailQuery) ([]connect.Message, string, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	call := x.gm.Users.Messages.List("me").MaxResults(int64(limit))
	search := strings.TrimSpace(q.Search)
	switch strings.ToLower(q.Folder) {
	case "sent":
		call = call.LabelIds("SENT")
	case "archive":
		search = strings.TrimSpace("-in:inbox -in:sent -in:drafts -in:trash -in:spam " + search)
	case "all":
	default:
		call = call.LabelIds("INBOX")
	}
	if search != "" {
		call = call.Q(search)
	}
	if q.Cursor != "" {
		call = call.PageToken(q.Cursor)
	}
	list, err := call.Context(ctx).Do()
	if err != nil {
		return nil, "", x.fail(ctx, err)
	}
	var out []connect.Message
	for _, m := range list.Messages {
		full, err := x.gm.Users.Messages.Get("me", m.Id).Format("metadata").
			MetadataHeaders("From", "To", "Cc", "Subject").Context(ctx).Do()
		if err != nil {
			continue
		}
		out = append(out, messageFrom(full, false))
	}
	return out, list.NextPageToken, nil
}

func (x mailAdapter) Get(ctx context.Context, id string) (connect.Message, error) {
	m, err := x.getFull(ctx, id)
	if err != nil {
		return connect.Message{}, err
	}
	return messageFrom(m, true), nil
}

// rfc822 builds a message for Gmail's raw send. Header values are encoded, and the body is base64 so any text
// survives the trip.
func rfc822(from string, o connect.Outgoing, extra map[string]string) string {
	var b strings.Builder
	w := func(k, v string) {
		if v != "" {
			b.WriteString(k + ": " + v + "\r\n")
		}
	}
	w("From", from)
	w("To", strings.Join(o.To, ", "))
	w("Cc", strings.Join(o.Cc, ", "))
	w("Bcc", strings.Join(o.Bcc, ", "))
	w("Subject", mimeSubject(o.Subject))
	for k, v := range extra {
		w(k, v)
	}
	ct := "text/plain"
	if o.HTML {
		ct = "text/html"
	}
	w("MIME-Version", "1.0")
	w("Content-Type", ct+"; charset=UTF-8")
	w("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(o.Body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.String()
}

func mimeSubject(s string) string {
	for _, r := range s {
		if r > 126 || r < 32 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

func (x *session) self(ctx context.Context) (string, error) {
	p, err := x.gm.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return "", x.fail(ctx, err)
	}
	return p.EmailAddress, nil
}

func (x *session) send(ctx context.Context, from string, o connect.Outgoing, extra map[string]string, thread string) error {
	raw := base64.URLEncoding.EncodeToString([]byte(rfc822(from, o, extra)))
	_, err := x.gm.Users.Messages.Send("me", &gmail.Message{Raw: raw, ThreadId: thread}).Context(ctx).Do()
	return x.fail(ctx, err)
}

func (x mailAdapter) Send(ctx context.Context, o connect.Outgoing) error {
	from, err := x.self(ctx)
	if err != nil {
		return err
	}
	return x.send(ctx, from, o, nil, "")
}

func prefixed(prefix, subject string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), strings.ToLower(prefix)) {
		return subject
	}
	return prefix + " " + subject
}

func quote(m connect.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nOn %s, %s wrote:\n", m.Received.Format("Mon, 2 Jan 2006 15:04 MST"), m.From)
	for _, line := range strings.Split(strings.TrimRight(m.Body, "\r\n"), "\n") {
		b.WriteString("> " + strings.TrimRight(line, "\r") + "\n")
	}
	return b.String()
}

func (x mailAdapter) Reply(ctx context.Context, id string, o connect.Outgoing, all bool) error {
	orig, err := x.getFull(ctx, id)
	if err != nil {
		return err
	}
	me, err := x.self(ctx)
	if err != nil {
		return err
	}
	m := messageFrom(orig, true)
	to := header(orig.Payload, "Reply-To")
	if to == "" {
		to = m.From
	}
	o.To = splitAddrs(to)
	if all {
		for _, a := range append(append([]string{}, m.To...), m.Cc...) {
			if !strings.Contains(strings.ToLower(a), strings.ToLower(me)) {
				o.Cc = append(o.Cc, a)
			}
		}
	}
	o.Subject = prefixed("Re:", m.Subject)
	if !m.HTML && !o.HTML {
		o.Body += quote(m)
	}
	mid := header(orig.Payload, "Message-ID")
	refs := strings.TrimSpace(header(orig.Payload, "References") + " " + mid)
	return x.send(ctx, me, o, map[string]string{"In-Reply-To": mid, "References": refs}, orig.ThreadId)
}

func (x mailAdapter) Forward(ctx context.Context, id string, o connect.Outgoing) error {
	orig, err := x.getFull(ctx, id)
	if err != nil {
		return err
	}
	me, err := x.self(ctx)
	if err != nil {
		return err
	}
	m := messageFrom(orig, true)
	o.Subject = prefixed("Fwd:", m.Subject)
	if !m.HTML && !o.HTML {
		o.Body += fmt.Sprintf("\n\n---------- Forwarded message ---------\nFrom: %s\nDate: %s\nSubject: %s\nTo: %s\n\n%s",
			m.From, m.Received.Format("Mon, 2 Jan 2006 15:04 MST"), m.Subject, strings.Join(m.To, ", "), m.Body)
	}
	return x.send(ctx, me, o, nil, "")
}

func (x *session) modify(ctx context.Context, id string, add, remove []string) error {
	_, err := x.gm.Users.Messages.Modify("me", id, &gmail.ModifyMessageRequest{AddLabelIds: add, RemoveLabelIds: remove}).Context(ctx).Do()
	return x.fail(ctx, err)
}

func (x mailAdapter) Archive(ctx context.Context, id string) error {
	return x.modify(ctx, id, nil, []string{"INBOX"})
}

func (x mailAdapter) MarkRead(ctx context.Context, id string, read bool) error {
	if read {
		return x.modify(ctx, id, nil, []string{"UNREAD"})
	}
	return x.modify(ctx, id, []string{"UNREAD"}, nil)
}

func (x mailAdapter) Delete(ctx context.Context, id string) error {
	_, err := x.gm.Users.Messages.Trash("me", id).Context(ctx).Do()
	return x.fail(ctx, err)
}

// labelIDs turns names into Gmail label ids, making a label that does not exist yet when it is being added.
func (x *session) labelIDs(ctx context.Context, names []string, create bool) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	list, err := x.gm.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	byName := map[string]string{}
	for _, l := range list.Labels {
		byName[strings.ToLower(l.Name)] = l.Id
	}
	var ids []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if id, ok := byName[strings.ToLower(n)]; ok {
			ids = append(ids, id)
			continue
		}
		if !create {
			continue
		}
		l, err := x.gm.Users.Labels.Create("me", &gmail.Label{Name: n, LabelListVisibility: "labelShow", MessageListVisibility: "show"}).Context(ctx).Do()
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		ids = append(ids, l.Id)
	}
	return ids, nil
}

func (x mailAdapter) Label(ctx context.Context, id string, add, remove []string) error {
	a, err := x.labelIDs(ctx, add, true)
	if err != nil {
		return err
	}
	r, err := x.labelIDs(ctx, remove, false)
	if err != nil {
		return err
	}
	return x.modify(ctx, id, a, r)
}

func (x mailAdapter) Labels(ctx context.Context) ([]string, error) {
	list, err := x.gm.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	var out []string
	for _, l := range list.Labels {
		if l.Type == "user" {
			out = append(out, l.Name)
		}
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Files: Drive
// -----------------------------------------------------------------------------

const fileFields = "id,name,mimeType,webViewLink,size,modifiedTime"

func fileFrom(driveID string, f *drive.File) connect.FileItem {
	it := connect.FileItem{ID: f.Id, DriveID: driveID, Name: f.Name, MIME: f.MimeType, WebURL: f.WebViewLink, Size: f.Size,
		Folder: f.MimeType == "application/vnd.google-apps.folder"}
	if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
		it.Modified = t.UTC()
	}
	return it
}

func (x filesAdapter) Drives(ctx context.Context) ([]connect.Drive, error) {
	out := []connect.Drive{{ID: "root", Name: "My Drive", Kind: "personal"}}
	err := x.dr.Drives.List().PageSize(100).Pages(ctx, func(r *drive.DriveList) error {
		for _, d := range r.Drives {
			out = append(out, connect.Drive{ID: d.Id, Name: d.Name, Kind: "shared"})
		}
		return nil
	})
	if err != nil {
		// Shared drives may be off for the domain. My Drive still works.
		return out[:1], nil
	}
	return out, nil
}

func (x filesAdapter) List(ctx context.Context, driveID, folderID string) ([]connect.FileItem, error) {
	if folderID == "" {
		folderID = driveID
		if folderID == "" {
			folderID = "root"
		}
	}
	call := x.dr.Files.List().Q(fmt.Sprintf("'%s' in parents and trashed = false", strings.ReplaceAll(folderID, "'", `\'`))).
		Fields("nextPageToken, files(" + fileFields + ")").PageSize(200).OrderBy("folder,name").
		SupportsAllDrives(true).IncludeItemsFromAllDrives(true)
	if driveID != "" && driveID != "root" {
		call = call.Corpora("drive").DriveId(driveID)
	}
	var out []connect.FileItem
	err := call.Pages(ctx, func(r *drive.FileList) error {
		for _, f := range r.Files {
			out = append(out, fileFrom(driveID, f))
		}
		if len(out) >= 1000 {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return out, x.fail(ctx, err)
}

var errStop = errors.New("stop")

func (x filesAdapter) Item(ctx context.Context, driveID, itemID string) (connect.FileItem, error) {
	f, err := x.dr.Files.Get(itemID).Fields(fileFields).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return fileFrom(driveID, f), nil
}

// exports maps a Google document to the Office format Hearth can keep.
var exports = map[string][2]string{
	"application/vnd.google-apps.document":     {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"application/vnd.google-apps.spreadsheet":  {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"application/vnd.google-apps.presentation": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
}

func (x filesAdapter) Open(ctx context.Context, driveID, itemID string, maxBytes int64) (connect.FileItem, io.ReadCloser, error) {
	meta, err := x.Item(ctx, driveID, itemID)
	if err != nil {
		return meta, nil, err
	}
	if meta.Folder {
		return meta, nil, errors.New("a folder cannot be downloaded")
	}
	if maxBytes > 0 && meta.Size > maxBytes {
		return meta, nil, fmt.Errorf("that file is %d MB, over the %d MB import limit; link it instead", meta.Size>>20, maxBytes>>20)
	}
	var resp *http.Response
	if ex, ok := exports[meta.MIME]; ok {
		resp, err = x.dr.Files.Export(itemID, ex[0]).Context(ctx).Download()
		meta.MIME = ex[0]
		if !strings.HasSuffix(strings.ToLower(meta.Name), ex[1]) {
			meta.Name += ex[1]
		}
	} else if strings.HasPrefix(meta.MIME, "application/vnd.google-apps.") {
		return meta, nil, errors.New("that kind of Google item cannot be imported; link it instead")
	} else {
		resp, err = x.dr.Files.Get(itemID).SupportsAllDrives(true).Context(ctx).Download()
	}
	if err != nil {
		return meta, nil, x.fail(ctx, err)
	}
	return meta, resp.Body, nil
}

// -----------------------------------------------------------------------------
// Files, write
// -----------------------------------------------------------------------------

func parentOf(driveID, folderID string) string {
	if folderID != "" {
		return folderID
	}
	if driveID == "" {
		return "root"
	}
	return driveID
}

func (x filesAdapter) Upload(ctx context.Context, driveID, folderID, name, mimeType string, size int64, r io.Reader) (connect.FileItem, error) {
	f, err := x.dr.Files.Create(&drive.File{Name: name, Parents: []string{parentOf(driveID, folderID)}}).
		Media(r, googleapi.ContentType(mimeType)).SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return fileFrom(driveID, f), nil
}

func (x filesAdapter) Replace(ctx context.Context, driveID, itemID, mimeType string, size int64, r io.Reader) (connect.FileItem, error) {
	f, err := x.dr.Files.Update(itemID, &drive.File{}).Media(r, googleapi.ContentType(mimeType)).
		SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return fileFrom(driveID, f), nil
}

func (x filesAdapter) Rename(ctx context.Context, driveID, itemID, name string) (connect.FileItem, error) {
	f, err := x.dr.Files.Update(itemID, &drive.File{Name: name}).SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return fileFrom(driveID, f), nil
}

func (x filesAdapter) Move(ctx context.Context, driveID, itemID, folderID string) (connect.FileItem, error) {
	cur, err := x.dr.Files.Get(itemID).Fields("parents").SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	f, err := x.dr.Files.Update(itemID, &drive.File{}).AddParents(parentOf(driveID, folderID)).
		RemoveParents(strings.Join(cur.Parents, ",")).SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return fileFrom(driveID, f), nil
}

func (x filesAdapter) Mkdir(ctx context.Context, driveID, folderID, name string) (connect.FileItem, error) {
	f, err := x.dr.Files.Create(&drive.File{Name: name, MimeType: "application/vnd.google-apps.folder",
		Parents: []string{parentOf(driveID, folderID)}}).SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return fileFrom(driveID, f), nil
}

// Delete moves to Drive's bin, which is what deleting is in Drive, and the person can restore it from there.
func (x filesAdapter) Delete(ctx context.Context, driveID, itemID string) error {
	_, err := x.dr.Files.Update(itemID, &drive.File{Trashed: true, ForceSendFields: []string{"Trashed"}}).
		SupportsAllDrives(true).Fields("id").Context(ctx).Do()
	return x.fail(ctx, err)
}
