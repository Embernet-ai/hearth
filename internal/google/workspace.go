package google

// Google Workspace, through Google's own Go clients (google.golang.org/api): Calendar, People, Gmail and Drive.
//
// This is the connect.Backend for Google. The connection, its sealed tokens and its refresh are the ones already
// in this package; this file adds what the connectors need on top: the scopes each asks for, and a session whose
// calls are made as the person.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
)

// apiEndpoints are overridden by tests, which serve a recorded Google from one address.
type apiEndpoints struct{ calendar, people, gmail, drive string }

var _ connect.Backend = (*Service)(nil)

func (s *Service) Provider() connect.Provider { return connect.Google }

// Configured reports whether the client is set up and there is somewhere safe to keep a token.
func (s *Service) Configured() bool { return s.cfg.Enabled() && s.box != nil }

// Scopes is what each connector needs, and only that. The Gmail one is the metadata scope: headers and a preview,
// with no way to read a body.
func (s *Service) Scopes(k connect.Kind) []string {
	switch k {
	case connect.Calendar:
		return []string{ScopeCalendarWrite, ScopeCalendarList}
	case connect.Contacts:
		return []string{ScopeContacts}
	case connect.Mail:
		return []string{ScopeGmailMeta}
	case connect.Files:
		return []string{ScopeDriveRead}
	}
	return nil
}

// expand adds the narrower scopes a broader grant already covers, so a person who connected before these
// connectors existed, with calendar.readonly or gmail.readonly, is not asked to do it again for something their
// grant already allows.
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
	add(ScopeCalendarRead, ScopeCalendarList)
	add("https://www.googleapis.com/auth/calendar", ScopeCalendarWrite, ScopeCalendarRead, ScopeCalendarList)
	add(ScopeGmailRead, ScopeGmailMeta)
	add("https://www.googleapis.com/auth/drive", ScopeDriveRead)
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

func (x *session) Calendar() connect.CalendarAPI { return x }
func (x *session) Contacts() connect.ContactsAPI { return contactsAdapter{x} }
func (x *session) Mail() connect.MailAPI         { return x }
func (x *session) Files() connect.FilesAPI       { return x }

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
			return fmt.Errorf("google refused that; the API may not be enabled on the Google project, or the scope was not granted: %w", connect.ErrScope)
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

// -----------------------------------------------------------------------------
// Mail
// -----------------------------------------------------------------------------

// Messages reads the inbox and sent mail. The metadata scope cannot search, so it takes the newest of each label
// and stops at the first message older than the cut-off.
func (x *session) Messages(ctx context.Context, since time.Time, limit int) ([]connect.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []connect.Message
	for _, label := range []string{"INBOX", "SENT"} {
		got := 0
		token := ""
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
				full, err := x.gm.Users.Messages.Get("me", m.Id).Format("metadata").
					MetadataHeaders("From", "To", "Subject").Context(ctx).Do()
				if err != nil {
					continue // one unreadable message does not lose the rest
				}
				when := time.UnixMilli(full.InternalDate).UTC()
				if when.Before(since) {
					break pages
				}
				msg := connect.Message{ID: full.Id, Snippet: full.Snippet, Received: when,
					WebLink: "https://mail.google.com/mail/u/0/#all/" + full.Id}
				if full.Payload != nil {
					for _, h := range full.Payload.Headers {
						switch strings.ToLower(h.Name) {
						case "from":
							msg.From = h.Value
						case "to":
							for _, a := range strings.Split(h.Value, ",") {
								msg.To = append(msg.To, strings.TrimSpace(a))
							}
						case "subject":
							msg.Subject = h.Value
						}
					}
				}
				out = append(out, msg)
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

func (x *session) Drives(ctx context.Context) ([]connect.Drive, error) {
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

func (x *session) List(ctx context.Context, driveID, folderID string) ([]connect.FileItem, error) {
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

func (x *session) Item(ctx context.Context, driveID, itemID string) (connect.FileItem, error) {
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

func (x *session) Open(ctx context.Context, driveID, itemID string, maxBytes int64) (connect.FileItem, io.ReadCloser, error) {
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
