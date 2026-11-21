package m365

// Microsoft Graph, through Microsoft's own Go SDK (github.com/microsoftgraph/msgraph-sdk-go).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	abs "github.com/microsoft/kiota-abstractions-go"
	absauth "github.com/microsoft/kiota-abstractions-go/authentication"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/drives"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"

	"github.com/Embernet-ai/hearth/internal/connect"
)

// tokenProvider gives the SDK this person's current access token and nothing else. The SDK only sends it to the
// hosts it lists, so a redirect or a next-link to somewhere else never carries it.
type tokenProvider struct {
	s         *Service
	principal string
	hosts     absauth.AllowedHostsValidator
}

func (t *tokenProvider) GetAuthorizationToken(ctx context.Context, _ *url.URL, _ map[string]interface{}) (string, error) {
	return t.s.token(ctx, t.principal)
}

func (t *tokenProvider) GetAllowedHostsValidator() *absauth.AllowedHostsValidator { return &t.hosts }

// Session opens one person's Graph client.
func (s *Service) Session(ctx context.Context, principalID string) (connect.Session, error) {
	// Fail now, with the right error, rather than on the first call.
	if _, err := s.Granted(ctx, principalID); err != nil {
		return nil, err
	}
	u, err := url.Parse(s.graphBase)
	if err != nil {
		return nil, err
	}
	tp := &tokenProvider{s: s, principal: principalID, hosts: absauth.NewAllowedHostsValidator([]string{u.Hostname()})}
	// The SDK's own default client: it retries a throttled or briefly unavailable Graph and follows redirects,
	// which a bare http.Client does not.
	adapter, err := msgraphsdk.NewGraphRequestAdapterWithParseNodeFactoryAndSerializationWriterFactoryAndHttpClient(
		absauth.NewBaseBearerTokenAuthenticationProvider(tp), nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("m365: graph client: %w", err)
	}
	adapter.SetBaseUrl(s.graphBase)
	return &session{s: s, principal: principalID, client: msgraphsdk.NewGraphServiceClient(adapter)}, nil
}

type session struct {
	s         *Service
	principal string
	client    *msgraphsdk.GraphServiceClient
}

func (x *session) Calendar() connect.CalendarAPI { return x }
func (x *session) Contacts() connect.ContactsAPI { return contactsAdapter{x} }
func (x *session) Mail() connect.MailAPI         { return x }
func (x *session) Files() connect.FilesAPI       { return x }

// fail turns an SDK error into what the rest of Hearth understands.
func (x *session) fail(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var api abs.ApiErrorable
	if errors.As(err, &api) {
		switch code := api.GetStatusCode(); {
		case code == http.StatusUnauthorized:
			x.s.markRevoked(ctx, x.principal)
			return connect.ErrReconnect
		case code == http.StatusForbidden:
			return fmt.Errorf("microsoft refused that: %w", connect.ErrScope)
		case code == http.StatusNotFound:
			return connect.ErrNotFound
		}
	}
	return fmt.Errorf("microsoft graph: %w", err)
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ptr[T any](v T) *T { return &v }

// -----------------------------------------------------------------------------
// Calendar
// -----------------------------------------------------------------------------

func utcHeaders() *abs.RequestHeaders {
	h := abs.NewRequestHeaders()
	h.Add("Prefer", `outlook.timezone="UTC"`)
	h.Add("Prefer", `outlook.body-content-type="text"`)
	return h
}

func (x *session) Calendars(ctx context.Context) ([]connect.CalendarInfo, error) {
	var out []connect.CalendarInfo
	resp, err := x.client.Me().Calendars().Get(ctx, nil)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		for _, c := range resp.GetValue() {
			out = append(out, connect.CalendarInfo{
				ID: s(c.GetId()), Name: s(c.GetName()),
				Primary:  c.GetIsDefaultCalendar() != nil && *c.GetIsDefaultCalendar(),
				Writable: c.GetCanEdit() != nil && *c.GetCanEdit(),
			})
		}
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" {
			return out, nil
		}
		resp, err = x.client.Me().Calendars().WithUrl(*next).Get(ctx, nil)
	}
}

const graphTime = "2006-01-02T15:04:05.9999999"

func parseGraphTime(dt models.DateTimeTimeZoneable) time.Time {
	if dt == nil || dt.GetDateTime() == nil {
		return time.Time{}
	}
	v := *dt.GetDateTime()
	loc := time.UTC
	if tz := s(dt.GetTimeZone()); tz != "" && !strings.EqualFold(tz, "UTC") {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	if t, err := time.ParseInLocation(graphTime, v, loc); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func eventFrom(e models.Eventable) connect.Event {
	ev := connect.Event{
		ID: s(e.GetId()), Version: s(e.GetChangeKey()), Title: s(e.GetSubject()),
		Start: parseGraphTime(e.GetStart()), End: parseGraphTime(e.GetEnd()),
		AllDay: e.GetIsAllDay() != nil && *e.GetIsAllDay(),
	}
	if b := e.GetBody(); b != nil {
		ev.Notes = strings.TrimSpace(s(b.GetContent()))
	}
	if l := e.GetLocation(); l != nil {
		ev.Place = s(l.GetDisplayName())
	}
	if om := e.GetOnlineMeeting(); om != nil {
		ev.JoinURL = s(om.GetJoinUrl())
	}
	return ev
}

func eventBody(e connect.Event, meeting bool) models.Eventable {
	out := models.NewEvent()
	out.SetSubject(ptr(e.Title))
	body := models.NewItemBody()
	body.SetContentType(ptr(models.TEXT_BODYTYPE))
	body.SetContent(ptr(e.Notes))
	out.SetBody(body)
	loc := models.NewLocation()
	loc.SetDisplayName(ptr(e.Place))
	out.SetLocation(loc)
	mk := func(t time.Time) models.DateTimeTimeZoneable {
		d := models.NewDateTimeTimeZone()
		d.SetDateTime(ptr(t.UTC().Format("2006-01-02T15:04:05.0000000")))
		d.SetTimeZone(ptr("UTC"))
		return d
	}
	out.SetStart(mk(e.Start))
	out.SetEnd(mk(e.End))
	out.SetIsAllDay(ptr(e.AllDay))
	if meeting && !e.AllDay {
		out.SetIsOnlineMeeting(ptr(true))
		out.SetOnlineMeetingProvider(ptr(models.TEAMSFORBUSINESS_ONLINEMEETINGPROVIDERTYPE))
	}
	return out
}

func (x *session) Events(ctx context.Context, calendarID string, from, to time.Time) ([]connect.Event, error) {
	cfg := &users.ItemCalendarViewRequestBuilderGetRequestConfiguration{
		Headers: utcHeaders(),
		QueryParameters: &users.ItemCalendarViewRequestBuilderGetQueryParameters{
			StartDateTime: ptr(from.UTC().Format(time.RFC3339)), EndDateTime: ptr(to.UTC().Format(time.RFC3339)),
			Top: ptr(int32(100)), Orderby: []string{"start/dateTime"},
		},
	}
	var out []connect.Event
	collect := func(vs []models.Eventable) {
		for _, e := range vs {
			if e.GetIsCancelled() != nil && *e.GetIsCancelled() {
				continue
			}
			out = append(out, eventFrom(e))
		}
	}
	if calendarID == "" || calendarID == "primary" {
		resp, err := x.client.Me().CalendarView().Get(ctx, cfg)
		for {
			if err != nil {
				return nil, x.fail(ctx, err)
			}
			collect(resp.GetValue())
			next := resp.GetOdataNextLink()
			if next == nil || *next == "" {
				return out, nil
			}
			resp, err = x.client.Me().CalendarView().WithUrl(*next).Get(ctx, cfg)
		}
	}
	ccfg := &users.ItemCalendarsItemCalendarViewRequestBuilderGetRequestConfiguration{
		Headers: cfg.Headers,
		QueryParameters: &users.ItemCalendarsItemCalendarViewRequestBuilderGetQueryParameters{
			StartDateTime: cfg.QueryParameters.StartDateTime, EndDateTime: cfg.QueryParameters.EndDateTime,
			Top: cfg.QueryParameters.Top, Orderby: cfg.QueryParameters.Orderby,
		},
	}
	b := x.client.Me().Calendars().ByCalendarId(calendarID).CalendarView()
	resp, err := b.Get(ctx, ccfg)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		collect(resp.GetValue())
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" {
			return out, nil
		}
		resp, err = b.WithUrl(*next).Get(ctx, ccfg)
	}
}

func (x *session) Create(ctx context.Context, calendarID string, e connect.Event, meeting bool) (connect.Event, error) {
	body := eventBody(e, meeting)
	var res models.Eventable
	var err error
	if calendarID == "" || calendarID == "primary" {
		res, err = x.client.Me().Events().Post(ctx, body, &users.ItemEventsRequestBuilderPostRequestConfiguration{Headers: utcHeaders()})
	} else {
		res, err = x.client.Me().Calendars().ByCalendarId(calendarID).Events().Post(ctx, body,
			&users.ItemCalendarsItemEventsRequestBuilderPostRequestConfiguration{Headers: utcHeaders()})
	}
	if err != nil {
		return connect.Event{}, x.fail(ctx, err)
	}
	return eventFrom(res), nil
}

func (x *session) Update(ctx context.Context, _ string, e connect.Event) (connect.Event, error) {
	// An event id is unique across the person's calendars, so it is addressed directly.
	res, err := x.client.Me().Events().ByEventId(e.ID).Patch(ctx, eventBody(e, false),
		&users.ItemEventsEventItemRequestBuilderPatchRequestConfiguration{Headers: utcHeaders()})
	if err != nil {
		return connect.Event{}, x.fail(ctx, err)
	}
	return eventFrom(res), nil
}

func (x *session) Delete(ctx context.Context, _ string, id string) error {
	if err := x.client.Me().Events().ByEventId(id).Delete(ctx, nil); err != nil {
		if errors.Is(x.fail(ctx, err), connect.ErrNotFound) {
			return nil // already gone, which is what was asked for
		}
		return x.fail(ctx, err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Contacts
// -----------------------------------------------------------------------------

type contactsAdapter struct{ x *session }

func (a contactsAdapter) Contacts(ctx context.Context) ([]connect.Contact, error) {
	x := a.x
	cfg := &users.ItemContactsRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemContactsRequestBuilderGetQueryParameters{
			Top:    ptr(int32(100)),
			Select: []string{"id", "changeKey", "displayName", "givenName", "surname", "emailAddresses", "mobilePhone", "businessPhones", "jobTitle", "companyName"},
		},
	}
	var out []connect.Contact
	resp, err := x.client.Me().Contacts().Get(ctx, cfg)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		for _, c := range resp.GetValue() {
			ct := connect.Contact{ID: s(c.GetId()), Version: s(c.GetChangeKey()), Name: s(c.GetDisplayName()),
				Title: s(c.GetJobTitle()), Company: s(c.GetCompanyName()), Phone: s(c.GetMobilePhone())}
			if ct.Name == "" {
				ct.Name = strings.TrimSpace(s(c.GetGivenName()) + " " + s(c.GetSurname()))
			}
			if ct.Phone == "" && len(c.GetBusinessPhones()) > 0 {
				ct.Phone = c.GetBusinessPhones()[0]
			}
			for _, a := range c.GetEmailAddresses() {
				if v := s(a.GetAddress()); v != "" {
					ct.Email = v
					break
				}
			}
			out = append(out, ct)
		}
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" {
			return out, nil
		}
		resp, err = x.client.Me().Contacts().WithUrl(*next).Get(ctx, cfg)
	}
}

// -----------------------------------------------------------------------------
// Mail
// -----------------------------------------------------------------------------

func (x *session) Messages(ctx context.Context, since time.Time, limit int) ([]connect.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	filter := "receivedDateTime ge " + since.UTC().Format("2006-01-02T15:04:05Z")
	cfg := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
			Filter: &filter, Orderby: []string{"receivedDateTime desc"}, Top: ptr(int32(min(limit, 100))),
			Select: []string{"id", "subject", "from", "toRecipients", "bodyPreview", "receivedDateTime", "webLink"},
		},
	}
	var out []connect.Message
	resp, err := x.client.Me().Messages().Get(ctx, cfg)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		for _, m := range resp.GetValue() {
			msg := connect.Message{ID: s(m.GetId()), Subject: s(m.GetSubject()), Snippet: s(m.GetBodyPreview()), WebLink: s(m.GetWebLink())}
			if t := m.GetReceivedDateTime(); t != nil {
				msg.Received = t.UTC()
			}
			if f := m.GetFrom(); f != nil && f.GetEmailAddress() != nil {
				msg.From = s(f.GetEmailAddress().GetAddress())
			}
			for _, r := range m.GetToRecipients() {
				if r.GetEmailAddress() != nil {
					msg.To = append(msg.To, s(r.GetEmailAddress().GetAddress()))
				}
			}
			out = append(out, msg)
			if len(out) >= limit {
				return out, nil
			}
		}
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" {
			return out, nil
		}
		resp, err = x.client.Me().Messages().WithUrl(*next).Get(ctx, cfg)
	}
}

// -----------------------------------------------------------------------------
// Files: OneDrive and SharePoint
// -----------------------------------------------------------------------------

func itemFrom(driveID string, it models.DriveItemable) connect.FileItem {
	f := connect.FileItem{ID: s(it.GetId()), DriveID: driveID, Name: s(it.GetName()), WebURL: s(it.GetWebUrl()),
		Folder: it.GetFolder() != nil}
	if it.GetSize() != nil {
		f.Size = *it.GetSize()
	}
	if it.GetFile() != nil {
		f.MIME = s(it.GetFile().GetMimeType())
	}
	if t := it.GetLastModifiedDateTime(); t != nil {
		f.Modified = t.UTC()
	}
	return f
}

// Drives lists the person's OneDrive and the document libraries of the SharePoint sites they follow.
func (x *session) Drives(ctx context.Context) ([]connect.Drive, error) {
	var out []connect.Drive
	own, err := x.client.Me().Drive().Get(ctx, nil)
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	out = append(out, connect.Drive{ID: s(own.GetId()), Name: "OneDrive", Kind: "personal"})

	sites, err := x.client.Me().FollowedSites().Get(ctx, nil)
	if err != nil {
		// Followed sites need Sites.Read.All, which a tenant may not have approved. OneDrive still works.
		return out, nil
	}
	for _, st := range sites.GetValue() {
		ds, err := x.client.Sites().BySiteId(s(st.GetId())).Drives().Get(ctx, nil)
		if err != nil {
			continue
		}
		for _, d := range ds.GetValue() {
			out = append(out, connect.Drive{ID: s(d.GetId()), Name: s(st.GetDisplayName()) + " / " + s(d.GetName()), Kind: "site"})
		}
	}
	return out, nil
}

func (x *session) List(ctx context.Context, driveID, folderID string) ([]connect.FileItem, error) {
	if folderID == "" {
		root, err := x.client.Drives().ByDriveId(driveID).Root().Get(ctx, nil)
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		folderID = s(root.GetId())
	}
	cfg := &drives.ItemItemsItemChildrenRequestBuilderGetRequestConfiguration{
		QueryParameters: &drives.ItemItemsItemChildrenRequestBuilderGetQueryParameters{Top: ptr(int32(200))},
	}
	b := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(folderID).Children()
	var out []connect.FileItem
	resp, err := b.Get(ctx, cfg)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		for _, it := range resp.GetValue() {
			out = append(out, itemFrom(driveID, it))
		}
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" || len(out) >= 1000 {
			return out, nil
		}
		resp, err = b.WithUrl(*next).Get(ctx, cfg)
	}
}

func (x *session) Item(ctx context.Context, driveID, itemID string) (connect.FileItem, error) {
	it, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Get(ctx, nil)
	if err != nil {
		return connect.FileItem{}, x.fail(ctx, err)
	}
	return itemFrom(driveID, it), nil
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
	data, err := x.client.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Content().Get(ctx, nil)
	if err != nil {
		return meta, nil, x.fail(ctx, err)
	}
	return meta, io.NopCloser(strings.NewReader(string(data))), nil
}
