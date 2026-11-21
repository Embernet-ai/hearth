// Package connect links a self-hosted Hearth to a Microsoft 365 business tenant or a Google Workspace domain.
//
// The provider packages (internal/m365 and internal/google) hold the credentials and speak each vendor's API
// through its official Go client. This package is the part that is the same for both: what an administrator has
// switched on, what each person's last run did, and the sync that decides what to create, change or leave alone.
// Written once against the small interfaces below, it is tested with fakes and with recorded API responses, and a
// provider only has to answer the interfaces.
//
// Every connector is off until an administrator enables it, every credential is one person's own, and a provider
// is only ever asked for the scopes of the connectors that are on.
package connect

import (
	"context"
	"errors"
	"io"
	"time"
)

// Provider is a vendor Hearth can connect to.
type Provider string

const (
	Google    Provider = "google"
	Microsoft Provider = "microsoft"
)

// Providers lists them in the order a page shows them.
var Providers = []Provider{Microsoft, Google}

// Label is the vendor's own name for the service, which is what an administrator is looking for.
func (p Provider) Label() string {
	if p == Microsoft {
		return "Microsoft 365"
	}
	return "Google Workspace"
}

// Kind is one thing a provider can be connected for.
type Kind string

const (
	Calendar Kind = "calendar"
	Contacts Kind = "contacts"
	Mail     Kind = "mail"
	Files    Kind = "files"
	Chat     Kind = "chat"
)

// Kinds lists them in the order a page shows them.
var Kinds = []Kind{Calendar, Contacts, Mail, Files, Chat}

// Label is the plain name of the connector.
func (k Kind) Label() string {
	switch k {
	case Calendar:
		return "Calendar"
	case Contacts:
		return "Contacts"
	case Mail:
		return "Mail"
	case Files:
		return "Files"
	case Chat:
		return "Chat notifications"
	}
	return string(k)
}

// Synced reports whether a scheduled run does anything for it. Files are browsed and imported on request, and chat
// is a webhook that notifications are copied to, so neither is a pull.
func (k Kind) Synced() bool { return k == Calendar || k == Contacts || k == Mail }

// ParseProvider and ParseKind refuse anything else rather than guess, so a mistyped form value is an error and not
// a quiet no-op.
func ParseProvider(s string) (Provider, error) {
	switch Provider(s) {
	case Google, Microsoft:
		return Provider(s), nil
	}
	return "", errors.New("connect: unknown provider " + s)
}

func ParseKind(s string) (Kind, error) {
	for _, k := range Kinds {
		if string(k) == s {
			return k, nil
		}
	}
	return "", errors.New("connect: unknown connector " + s)
}

var (
	// ErrNotConnected means the person has not connected this provider.
	ErrNotConnected = errors.New("connect: this account is not connected")
	// ErrReconnect means the provider refused the stored grant: the person or an administrator revoked it, and only
	// connecting again restores it.
	ErrReconnect = errors.New("connect: the connection was revoked and must be made again")
	// ErrDisabled means an administrator has not turned this connector on.
	ErrDisabled = errors.New("connect: this connector is off")
	// ErrScope means the person's grant does not cover what the connector needs, so they have to connect again and
	// tick it.
	ErrScope = errors.New("connect: the connection does not include permission for this")
	// ErrNotFound covers an absent item and one the person may not use.
	ErrNotFound = errors.New("connect: not found")
	// ErrWrongTenant means the account belongs to a tenant or domain the administrator did not connect.
	ErrWrongTenant = errors.New("connect: that account is not in the connected tenant or domain")
)

// Event is a calendar event as a provider holds it. An all-day event has Start at midnight UTC of its first day
// and End at midnight UTC of the day after its last, which is how both Google and Microsoft say it.
type Event struct {
	ID      string
	Version string // etag, or Graph's changeKey
	Title   string
	Notes   string
	Place   string
	JoinURL string // a Meet or Teams link, when the event has one
	Start   time.Time
	End     time.Time
	AllDay  bool
}

// CalendarInfo is one calendar a person can read.
type CalendarInfo struct {
	ID       string
	Name     string
	Primary  bool
	Writable bool
}

// CalendarAPI is one person's calendars at a provider.
type CalendarAPI interface {
	Calendars(ctx context.Context) ([]CalendarInfo, error)
	// Events lists a window of one calendar, recurring events expanded to their instances, cancelled ones left out.
	// calendarID "" or "primary" is the person's own.
	Events(ctx context.Context, calendarID string, from, to time.Time) ([]Event, error)
	Create(ctx context.Context, calendarID string, e Event, meeting bool) (Event, error)
	Update(ctx context.Context, calendarID string, e Event) (Event, error)
	Delete(ctx context.Context, calendarID, eventID string) error
}

// Contact is an address-book entry as a provider holds it.
type Contact struct {
	ID      string
	Version string
	Name    string
	Email   string
	Phone   string
	Title   string
	Company string
}

type ContactsAPI interface {
	Contacts(ctx context.Context) ([]Contact, error)
}

// Message is a mail message's headers and a short preview. Never a body, never an attachment.
type Message struct {
	ID       string
	Subject  string
	From     string
	To       []string
	Snippet  string
	Received time.Time
	WebLink  string
}

type MailAPI interface {
	// Messages lists messages received since a time, newest first, up to limit.
	Messages(ctx context.Context, since time.Time, limit int) ([]Message, error)
}

// Drive is a place files live: a person's OneDrive, a SharePoint document library, My Drive or a shared drive.
type Drive struct {
	ID   string
	Name string
	Kind string // "personal", "site" or "shared"
}

// FileItem is a file or a folder.
type FileItem struct {
	ID       string
	DriveID  string
	Name     string
	MIME     string
	WebURL   string
	Size     int64
	Folder   bool
	Modified time.Time
}

type FilesAPI interface {
	Drives(ctx context.Context) ([]Drive, error)
	// List returns a folder's children. folderID "" is the drive's root.
	List(ctx context.Context, driveID, folderID string) ([]FileItem, error)
	Item(ctx context.Context, driveID, itemID string) (FileItem, error)
	// Open streams a file. A Google document is exported to a format Hearth can keep, so the name and type it
	// reports are the ones of what is returned.
	Open(ctx context.Context, driveID, itemID string, maxBytes int64) (FileItem, io.ReadCloser, error)
}

// Session is one person's connection to one provider, ready to call.
type Session interface {
	Calendar() CalendarAPI
	Contacts() ContactsAPI
	Mail() MailAPI
	Files() FilesAPI
}

// Conn is a connection as an administrator sees it. It never carries a token.
type Conn struct {
	OrgID       string
	PrincipalID string
	Name        string
	Email       string
	Tenant      string
	Scopes      []string
	ConnectedAt time.Time
	Revoked     bool
	LastError   string
}

// Backend is what a provider package gives this one.
type Backend interface {
	Provider() Provider
	// Configured reports whether the deployment has this provider's client set up.
	Configured() bool
	// Scopes is what a connector needs, delegated and as small as the connector allows.
	Scopes(k Kind) []string
	Session(ctx context.Context, principalID string) (Session, error)
	// Granted is what the person's connection actually covers, as the provider reported it.
	Granted(ctx context.Context, principalID string) ([]string, error)
	Connections(ctx context.Context, orgID string) ([]Conn, error)
	Disconnect(ctx context.Context, principalID string) error
}

// HasScopes reports whether a granted set covers everything a connector needs.
func HasScopes(granted, need []string) bool {
	have := map[string]bool{}
	for _, g := range granted {
		have[g] = true
	}
	for _, n := range need {
		if !have[n] {
			return false
		}
	}
	return true
}
