// Package web is the HTML interface: server-rendered pages plus HTMX for the
// bits that update in place.
//
// No bundler, no npm, no node_modules. Templates and static assets are embedded
// in the binary, so `go build` produces the entire application and there is
// nothing to serve from a second place or forget to deploy.
//
// The one exception is static/editor.js, the rich-text editor: a third-party
// editor bundled once from editor/ and committed. It
// is still just a static file to `go build`; Node is only needed to change it.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/agents"
	"github.com/Embernet-ai/hearth/internal/api"
	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/authz"
	// Aliased: this package already has a function called calendar, which
	// would shadow the package name at every use site.
	"github.com/Embernet-ai/hearth/internal/auth"
	cal "github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/calls"
	"github.com/Embernet-ai/hearth/internal/canvas"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/connect"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crm/crmviews"
	"github.com/Embernet-ai/hearth/internal/crmforms"
	"github.com/Embernet-ai/hearth/internal/crmmarketing"
	"github.com/Embernet-ai/hearth/internal/crmops"
	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/files"
	"github.com/Embernet-ai/hearth/internal/forms"
	"github.com/Embernet-ai/hearth/internal/github"
	"github.com/Embernet-ai/hearth/internal/google"
	"github.com/Embernet-ai/hearth/internal/importer"
	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/m365"
	"github.com/Embernet-ai/hearth/internal/mailint"
	"github.com/Embernet-ai/hearth/internal/mailsettings"
	"github.com/Embernet-ai/hearth/internal/maint"
	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/offline"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/buildinfo"
	"github.com/Embernet-ai/hearth/internal/playground"
	"github.com/Embernet-ai/hearth/internal/push"
	"github.com/Embernet-ai/hearth/internal/realtime"
	"github.com/Embernet-ai/hearth/internal/rules"
	"github.com/Embernet-ai/hearth/internal/scim"
	"github.com/Embernet-ai/hearth/internal/search"
	"github.com/Embernet-ai/hearth/internal/social"
	"github.com/Embernet-ai/hearth/internal/teams"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
	"github.com/Embernet-ai/hearth/internal/tools"
	"github.com/Embernet-ai/hearth/internal/views"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed static/*
var staticFS embed.FS

// Server renders the UI.
type Server struct {
	docs     docs.Store
	files    *files.Service
	views    views.Store
	channels *channels.Service
	// inbox rather than notify, because the Server already has a notify method
	// for the realtime hub and two things called the same thing is how a
	// refactor introduces a bug nobody can see.
	inbox  *notify.Service
	push   *push.Service
	rules  rules.Store
	engine *rules.Engine
	time   timelog.Store
	// maint is the schedules and meters; pm is the generator that raises the
	// work. Two, because reading a schedule is a page render and raising a job
	// writes a work order, and only one of them belongs behind a GET.
	maint maint.Store
	pm    *maint.Generator
	plant plant.Store
	crm   crm.Store
	// sales is the pipeline, owners and timeline. Nil when the data lives in
	// EmberOps, where those pages are not offered.
	sales crm.Sales
	// crmOps reads EmberOps's quotes and call logs for a record. Nil when the installation does not run on EmberOps.
	crmOps *crmops.Service
	// crmMkt is the contact page's Mautic panel.
	crmMkt *crmmarketing.Service
	// crmForms is the contact page's form responses.
	crmForms *crmforms.Service
	// crmViews are the saved views of the companies, contacts and deals lists. Nil switches the saving off.
	crmViews *crmviews.Service
	mail     *mailint.Service
	// mailset is each org's own outbound relay settings; orgMail sends a test
	// through them. Both nil-safe: a deployment can run with neither.
	mailset *mailsettings.Service
	orgMail *mailsettings.Mailer
	calls   *calls.Service
	agents  *agents.Service
	mcp     *agents.Connector
	canvas  canvas.Store
	api     *api.Service
	// offline is the record of what phones did with no signal. The replay goes
	// back through authed, which is this same site's authenticated mux held
	// here so a queued request reaches the very handler a browser reaches.
	offline *offline.Service
	authed  *http.ServeMux
	// teams, audit, and scim are the governance surface. audit only reads
	// here: every write goes in beside the change it describes, inside that
	// change's own transaction, which is why nothing on this struct writes one.
	teams teams.Store
	audit *audit.Service
	// az answers what somebody may reach, as opposed to what they may open.
	// Held on every render so the rail can gate on it, for the same reason
	// IsOwner is: navigation that depends on thirty handlers remembering to
	// populate a field is navigation that vanishes at random.
	az *authz.Service
	// calendar is events plus the subscribable feed. Held here because the
	// feed handler is registered outside the authenticated mux.
	calendar cal.Store
	// google is per-person, and nil when the integration is unconfigured.
	google *google.Service
	// microsoft and connect are the Microsoft 365 connection and the connectors that run on both providers. Nil
	// when a deployment has not set them up.
	microsoft *m365.Service
	connect   *connect.Service
	// The Marketing, Forms and Social areas call the apps the installation runs for them, and tools is the launcher.
	// Each is nil, or empty, when the installation has not set it up, and its page then says what to set.
	marketing *mautic.Client
	// panel is the dashboard's team parts, shared for a few minutes (teampanel.go).
	panel      panelCache
	github     *github.Service
	forms      *forms.Client
	social     *social.Client
	tools      []tools.Tool
	box        connect.Sealer
	scim       *scim.Service
	search     *search.Service
	playground *playground.Service
	timeoff    timeoff.Store
	people     PersonSync
	deptreq    deptreq.Store
	queue      *jobs.Queue
	relay      *jobs.Relay
	hub        *realtime.Hub
	oidc       *auth.OIDC
	oidcName   string
	// hideLocalLogin leaves the email and password form off the sign-in page.
	hideLocalLogin bool
	auth           *auth.Service
	// directory lists everybody with an account, for the time report's picker. It is s.auth in production and a fake in tests.
	directory interface {
		People(ctx context.Context, orgID string) ([]auth.Person, error)
	}
	log *slog.Logger

	// searchDB reads the index bookkeeping for the operations page. The web
	// pool, because it is a page render and belongs under its statement
	// timeout like every other one.
	searchDB *pgxpool.Pool
	// notifyDB is used only to wake the worker after a page enqueues something.
	// A pool rather than the queue's, because the notification is not part of
	// any transaction and must not wait behind one.
	notifyDB *pgxpool.Pool
	// inboundKey and mailReady are configuration the pages need to answer for.
	inboundKey string
	mailReady  bool
	// baseURL is the public address, for the one thing that leaves the browser:
	// a printed label with a URL on it.
	baseURL string
	// version is the release name, from the image tag the chart pulled.
	version string
	// workerOn records whether this replica runs jobs, so the operator page can
	// say "nothing is running here" rather than showing a queue that appears
	// stuck for no visible reason.
	workerOn bool
}

// Deps is what the site needs. A struct rather than eleven positional
// parameters, because the eighth and ninth were already interchangeable by
// type and a transposed pair compiles fine and fails at runtime.
type Deps struct {
	Docs     docs.Store
	Files    *files.Service
	Auth     *auth.Service
	Views    views.Store
	Channels *channels.Service
	Notify   *notify.Service
	Push     *push.Service
	Rules    rules.Store
	Engine   *rules.Engine
	Time     timelog.Store
	Maint    maint.Store
	PM       *maint.Generator
	Plant    plant.Store
	CRM      crm.Store
	// Sales is the Postgres-only sales side. Leave nil to switch the pages off.
	Sales crm.Sales
	// CRMViews are the saved views of the CRM lists. May be nil.
	CRMViews *crmviews.Service
	Mail     *mailint.Service
	// MailSettings is each org's own outbound relay; OrgMailer sends test mail
	// through it. Both may be nil.
	MailSettings *mailsettings.Service
	OrgMailer    *mailsettings.Mailer
	Calls        *calls.Service
	Agents       *agents.Service
	Connector    *agents.Connector
	Canvas       canvas.Store
	API          *api.Service
	Offline      *offline.Service
	Teams        teams.Store
	Audit        *audit.Service
	Authz        *authz.Service
	Calendar     cal.Store
	Google       *google.Service
	Microsoft    *m365.Service
	Connect      *connect.Service
	Marketing    *mautic.Client
	Forms        *forms.Client
	// Ops is the EmberOps client, nil when the installation does not run on EmberOps. The CRM reads quotes and call logs from it.
	Ops crmops.Lister
	// CRMMarketing shows a contact's Mautic segments and campaigns on their page. Nil when Mautic is not connected.
	CRMMarketing *crmmarketing.Service
	// CRMForms shows a contact's Formbricks responses on their page. Nil when Formbricks is not connected.
	CRMForms *crmforms.Service
	Social   *social.Client
	// GitHub is each person's read-only link to GitHub. Nil leaves the page saying it is not available.
	GitHub *github.Service
	Tools  []tools.Tool
	Box    connect.Sealer
	SCIM   *scim.Service
	Search *search.Service
	// Playground is Settings -> Tutorial's personal practice organisation.
	Playground *playground.Service
	// TimeOff is requesting and deciding days away.
	TimeOff timeoff.Store
	// People, with the EmberOPS back end, makes sure a person exists there (User, Employee, approver link). Nil in the
	// default back end.
	People PersonSync
	// DeptRequests is people asking which departments they belong in.
	DeptRequests deptreq.Store
	Queue        *jobs.Queue
	Relay        *jobs.Relay
	Hub          *realtime.Hub
	OIDC         *auth.OIDC
	// OIDCName is what the sign-in button calls the provider; HideLocalLogin takes the email and
	// password form off the sign-in page (the one button is then the whole door). Zero values keep
	// today's page: the form shown, the button reading "single sign-on".
	OIDCName       string
	HideLocalLogin bool
	NotifyDB       *pgxpool.Pool
	WorkerOn       bool
	// InboundKey authenticates the estate's MTA on the one endpoint a machine
	// talks to. Empty disables inbound mail entirely, which is the right
	// default: a path that files anything it is sent as work is not a path to
	// leave open.
	InboundKey string
	// MailReady reports whether a relay is configured, so the settings page can
	// say that replies will go nowhere rather than letting somebody find out.
	MailReady bool
	// BaseURL is where a phone reaches this deployment. It goes on printed
	// labels, so a label printed from a laptop must not point at production.
	BaseURL string
	Version string
	Log     *slog.Logger
}

func New(d Deps) (*Server, error) {
	s := &Server{
		docs: d.Docs, files: d.Files, auth: d.Auth, views: d.Views,
		channels: d.Channels, inbox: d.Notify, push: d.Push, search: d.Search, playground: d.Playground, timeoff: d.TimeOff, people: d.People, deptreq: d.DeptRequests,
		rules: d.Rules, engine: d.Engine, time: d.Time,
		maint: d.Maint, pm: d.PM, plant: d.Plant, crm: d.CRM, sales: d.Sales, crmViews: d.CRMViews,
		mail: d.Mail, mailset: d.MailSettings, orgMail: d.OrgMailer, calls: d.Calls,
		agents: d.Agents, mcp: d.Connector, canvas: d.Canvas, api: d.API,
		offline:    d.Offline,
		teams:      d.Teams,
		audit:      d.Audit,
		az:         d.Authz,
		calendar:   d.Calendar,
		google:     d.Google,
		microsoft:  d.Microsoft,
		marketing:  d.Marketing,
		crmOps:     newCRMOps(d.Ops),
		crmMkt:     d.CRMMarketing,
		crmForms:   d.CRMForms,
		forms:      d.Forms,
		social:     d.Social,
		github:     d.GitHub,
		tools:      d.Tools,
		connect:    d.Connect,
		box:        d.Box,
		scim:       d.SCIM,
		inboundKey: d.InboundKey, mailReady: d.MailReady, baseURL: d.BaseURL,
		version: d.Version,
		queue:   d.Queue, relay: d.Relay, hub: d.Hub, oidc: d.OIDC,
		oidcName: d.OIDCName, hideLocalLogin: d.HideLocalLogin,
		notifyDB: d.NotifyDB, searchDB: d.NotifyDB, workerOn: d.WorkerOn, log: d.Log,
	}
	if d.Auth != nil {
		s.directory = d.Auth
	}
	return s, nil
}

// page is the data every template receives.
type page struct {
	Title    string
	Identity *auth.Identity
	Build    buildinfo.Info
	// SSO reports whether single sign-on is configured, so the login page can
	// offer it without the template knowing anything about the provider.
	SSO bool
	// SSOName is what the sign-in button calls the provider, and HideLocalLogin is whether the page
	// leaves the email and password form off. Both are for the sign-in page only.
	SSOName        string
	HideLocalLogin bool
	// Next is where to land after signing in, already filtered to a path on
	// this site. It is what makes scanning an asset code work on a phone that
	// signed out last week.
	Next string

	// Dash is the landing page's content, populated only for the dashboard.
	Dash *dashboard

	// SpaceID is the department currently being viewed, so the rail can mark
	// it. Read from the query string rather than the path, because a
	// department is a filter on the list page rather than a page of its own --
	// which is what lets it keep every view, filter and grouping the list
	// already has.
	SpaceID string

	// SpaceChildren are the sub-departments and projects directly inside the
	// department currently being viewed, so a department is somewhere you can
	// look around rather than only a filter that hides everything else.
	SpaceChildren []*plant.Space
	// SpaceName is the department being viewed, for the heading.
	SpaceName string
	// DailyReports are the space's own daily reports, a dedicated section on
	// the list page rather than a filter someone has to know to type.
	DailyReports []*docs.Row

	// Daily report pages (dailyreportpage.go).
	RepDay       string
	RepSpace     string
	RepFrom      string
	RepTo        string
	RepPerson    string
	RepBody      string
	RepComment   string
	RepError     string
	RepMineToday string
	RepPDFHref   template.URL
	RepTree      []*plant.Space
	RepPeople    []docs.People
	RepEntries   []*reportView

	// Service history page (servicehistorypage.go).
	SvcRecords int
	SvcEvents  int
	SvcDays    int
	SvcShown   []*docs.ServiceRecord
	SvcPDF     template.URL
	SvcCSV     template.URL
	// ReportSpaces, ReportSpace and ReportSpaceNames are the same section on
	// All documents, where there is no space to scope it to: every report,
	// narrowed by a department picker, each labelled with where it is filed.
	ReportSpaces     []*plant.Space
	ReportSpace      string
	ReportSpaceNames map[string]string
	// ReportTree is every space, for where a new report from there is filed.
	ReportTree []*plant.Space

	// Crumbs is the path from the root down to the space being viewed, so the
	// reader knows where they are without having to reconstruct it from the
	// rail. Empty when nothing is being viewed inside a space at all.
	Crumbs []crumb

	// Wide lifts the 72rem reading-width cap on <main>. Set by the handler,
	// not guessed from the page name, because "documents" alone covers the
	// table, board, calendar and timeline views and all four want the room --
	// a page that wants to stay narrow just never sets this.
	Wide bool

	// Org is the department tree the navigation renders, on every page.
	//
	// Loaded in render rather than by each handler: navigation that only
	// appears on the pages somebody remembered to populate is navigation that
	// vanishes at random, and that is a worse bug than it sounds because it
	// looks like the page failed rather than like a field was missed.
	//
	// A real tree ([]*orgNode), not the flat depth-sorted list SpaceTree
	// returns -- see orgtree.go. The rail used to range over the flat list
	// and stop at depth 1 by hand, which is why a project three levels down
	// was reachable only by knowing its URL.
	Org []*orgNode

	// Path is the request path, and it exists so the navigation can mark where
	// the reader currently is. Set centrally in render rather than by each
	// handler: a highlight that depends on thirty call sites remembering to
	// pass it is a highlight that is wrong on the pages nobody tested.
	Path string

	Heading   string
	Kind      string
	KindLabel string
	// KindChoice is whether the New form lets somebody pick the kind.
	KindChoice bool
	Documents  []*docs.Document
	Doc        *docs.Document
	Links      []*docs.Document
	Statuses   []string
	Status     int
	Message    string
	// The set-password link pages.
	LinkURL, LinkEmail, LinkName, LinkExpires, Token string
	LinkEmailed, Done                                bool
	// InPlayground is the Tutorial page asked from inside a playground: it
	// offers Back and Reset rather than Enter. The banner reads Identity.
	InPlayground bool
	// CanManageSpaces gates the rail's "+ folder" affordance: a manager of
	// some department, or an admin.
	CanManageSpaces bool
	// SpaceParent pre-selects the parent in the Add-a-space form, from ?parent=.
	SpaceParent string
	// ManageableSpaces is the set of space ids this reader may reshape, for
	// the Spaces page to show edit controls only where they apply.
	ManageableSpaces map[string]bool
	// CanAdminSpaces: may create a top-level department (admins/playground).
	CanAdminSpaces bool
	// SpaceHasChildren marks spaces that have at least one child, for the
	// collapse caret on the Spaces page.
	SpaceHasChildren map[string]bool
	// AccessSpace is the folder whose Access page this is, and Delegations who
	// holds a delegation of it. For a manager sharing one folder.
	AccessSpace *plant.Space
	Delegations []plant.Delegation
	Attachments []docs.Attachment
	// RemovedAttachments are files taken off the document, kept for Restore.
	RemovedAttachments []docs.Attachment

	// Filter is the status currently being filtered on, so the control can show
	// which one is active rather than resetting itself on every render.
	Filter string
	// People is the assignee picker's options.
	People []docs.People
	// Assignable is every document that could be linked to this one.
	Assignable []*docs.Document
	// From records where this document was imported from, if anywhere.
	From *docs.ExternalRef
	// Import is the outcome of the last import, shown once.
	Import *clickup.Result
	// CRMGroup is the heading the CRM list is grouped under, CRMGroupOptions the choices, and CRMRowGroups the heading of each row
	// shown, in order. CRMGroupCounts is how many rows each heading has across the whole list.
	CRMGroup        string
	CRMGroupOptions []crm.Group
	CRMRowGroups    []string
	CRMGroupCounts  map[string]int
	// CRMViews are the saved views of this CRM list, the person's own first, and ListFrom the address a new one would save.
	CRMViews   []*crmviews.View
	CRMViewsOn bool
	ListFrom   string
	// IsFavorite is whether the person has starred the CRM record on this page, FavoriteType what kind of record it is, and
	// FavoriteItems the person's starred records on the favorites page.
	IsFavorite    bool
	FavoriteType  string
	FavoriteItems []FavoriteItem
	// TimeForm is the log-time form as it was typed when it was refused, for the document page to draw again.
	TimeForm *TimeForm
	// CRMImport is the outcome of the last CRM CSV import or check, shown once.
	CRMImport *CRMImportResult
	// HasTask reports whether this kind carries a lifecycle, so the create form
	// only offers status-shaped fields to things that have one.
	HasTask bool
	// Properties are the custom fields that apply to this kind, each carrying
	// whatever is currently set on this entity.
	Properties []docs.PropertyDef

	// --- views -------------------------------------------------------------
	// Query is the compiled question this list is asking. The controls render
	// themselves from it, so what is on screen and what ran are the same thing.
	Query    docs.Query
	ViewKind string
	// Base is the address this page's own links point at, which is the saved
	// view when one is open and the plain list when it is not.
	Base string
	// SaveTarget is the address a saved view would record, always the plain
	// list rather than /views/<id>, so saving a view opened from another view
	// does not chain them together.
	SaveTarget string
	// CurrentURL is this exact view -- base, filters, grouping -- for an
	// action that wants to return to it afterward (a grouped table's inline
	// Add row) rather than resetting to the kind's bare default.
	CurrentURL string
	Total      int
	Shown      int
	// ClosedHidden is how many done and cancelled items the default view leaves out, so the count on the page and the side
	// panel's count (every live document at that level) can be read together.
	ClosedHidden int
	Tabs         []ViewTab
	GroupBy      string
	GroupOpts    []FieldOption
	ColumnOpts   []FieldOption
	Columns      []docs.Field
	// GroupedTable is Table's rows arranged the same way Board's Groups are --
	// Table always groups now (defaulted to status, same as Board), so this
	// replaced the old flat TableRows entirely rather than sitting alongside it.
	GroupedTable []TableGroup
	Groups       []docs.Group
	SavedView    *views.View
	SavedViews   []*views.View
	PropDefs     []docs.PropertyDef

	Month     time.Time
	Weeks     [][]CalDay
	PrevMonth string
	NextMonth string

	Bars         []Bar
	TimelineFrom time.Time
	TimelineTo   time.Time

	// --- accounts ----------------------------------------------------------
	Persons []auth.Person
	// Person is the one person a person page is about; PersonManaged is the
	// departments they manage, with ids, for its remove buttons.
	Person        *auth.Person
	PersonManaged []authz.Department
	// AskDept and AskParts are the "can they see this department?" prompt shown
	// right after somebody is added to one.
	AskDept  *plant.Space
	AskParts []AskPart
	// AccessGroups is the "Can see work in" editor: each department with its
	// parts, ticked where they have access.
	AccessGroups []AccessGroup
	// IsAdmin is the owner or a super admin: whoever may reach the
	// administration pages. Not the same as "is the owner" -- that is a fact
	// about a person ($p.IsOwner on the People pages), not about the viewer.
	IsAdmin bool

	// Can is what this person may reach. Populated on every render, so a
	// template asks {{if .Can.Has "finance"}} rather than each handler passing
	// a bool per section.
	Can authz.Held

	// ToApprove is how many time entries are waiting on this person, for the
	// badge on the rail. Zero for almost everybody, because almost nobody
	// manages a department.
	ToApprove int
	// TimeOffPending is how many time-off requests wait on this person, for
	// the rail; TimeOff, TimeOffToDecide and TimeOffKinds fill /time-off.
	TimeOffPending  int
	TimeOff         []*timeoff.Request
	TimeOffToDecide []*timeoff.Request
	TimeOffKinds    []timeoff.Kind
	// Department requests: the ones waiting on this person (Approvals), the
	// person's own latest, every department to choose from, and their current
	// set. DeptPending is the count for the rail; NeedsDepartment shows the
	// banner to somebody in no department with nothing asked yet.
	DeptToDecide    []*deptreq.Request
	MyDeptRequest   *deptreq.Request
	AllDepartments  []deptreq.Department
	MyDepartments   []deptreq.Department
	DeptPending     int
	NeedsDepartment bool
	// DeptApplyNow is true for somebody who may place people in departments:
	// their own choice is saved at once, with nobody to ask.
	DeptApplyNow bool

	// The People page: what everybody holds, where everybody sits, and the
	// departments to choose from.
	Grants      map[string]authz.Held
	Chart       map[string]*authz.Structure
	Departments []*plant.Space
	// SpaceGrants is which departments each person can actually see work in,
	// keyed by principal id -- separate from Grants above, which is
	// capabilities (Finance, Audit, People, Agents), not content visibility.
	SpaceGrants map[string][]authz.Department
	Grantable   []authz.Capability
	// CanGrant is whether the reader may change what anybody may reach, which
	// is super admin only. Somebody with People administers accounts and must
	// not be able to promote themselves.
	CanGrant bool
	// CanMovePeople is whether the reader may change somebody's departments,
	// approver, managers or department access: a super admin, or People inside
	// a playground (authz.MayMovePeopleIn, the same rule the check uses).
	CanMovePeople bool

	// Projects, grouped by the department each belongs to.
	ProjectGroups []*projectGroup
	ProjectCount  int

	// The calendar. Month, PrevMonth and NextMonth are the ones the
	// maintenance calendar already declares above and are reused rather than
	// duplicated: two fields meaning the same month is how they end up
	// disagreeing.
	//
	// Days is separate from Weeks because the two carry different things: Weeks
	// holds document rows for the maintenance view, and a calendar cell here
	// holds entries that may not be documents at all.
	Entries []cal.Entry
	Days    [][]calendarDay
	HasFeed bool
	// FeedMadeAt is when the current feed token was issued, which is all the
	// page can say about it: only the hash is stored.
	FeedMadeAt time.Time
	// FeedToken is set for exactly one render, immediately after issuing one,
	// and is never read back from anywhere.
	FeedToken string
	BaseURL   string
	// Version is the release name shown in the footer, and Today is today, for
	// a date input that should default to now rather than to nothing.
	Version string
	Today   string

	// GoogleOn is whether the deployment has the integration configured at all.
	// Google is the connection this person has, or nil.
	GoogleOn bool
	Google   *google.Connection

	// The Microsoft 365 and Google Workspace connectors: the Connectors page, the cloud files browser, the cloud
	// files linked to a document, and the messages linked to a contact.
	Connectors *ConnectorsView `json:",omitempty"`
	Cloud      *CloudView      `json:",omitempty"`
	Mailbox    *MailboxView    `json:",omitempty"`
	// The Marketing, Forms and Social areas and the Tools launcher. ToolsOn shows the Tools entry in the rail.
	Marketing  *MarketingView `json:",omitempty"`
	FormsArea  *FormsView     `json:",omitempty"`
	SocialArea *SocialView    `json:",omitempty"`
	GitHubArea *GitHubView    `json:",omitempty"`
	ToolsList  *ToolsView     `json:",omitempty"`
	ToolsOn    bool           `json:",omitempty"`
	CloudOn    bool           `json:",omitempty"`
	// ConnectorsOn is whether the Settings page links to the Connectors page.
	ConnectorsOn bool                 `json:",omitempty"`
	CloudLinks   []connect.FileLink   `json:",omitempty"`
	LinkedMail   []connect.LinkedMail `json:",omitempty"`

	// MyTime is one person's own entries, and the totals beside them.
	// ShowRemoved is whether removed entries are mixed into MyTime rather
	// than hidden -- the "Show removed" toggle, same shape as Show closed.
	// JustRemoved is the one entry the page should offer an inline Undo for
	// -- the id from a Remove redirect's ?justRemoved=, not a persistent flag.
	MyTime      []timelog.Entry
	MyMinutes   int
	MyPending   int
	ShowRemoved bool
	JustRemoved string
	MyTimeBack  string
	// TimeOf is the colleague whose hours the time page is showing, nil for your own;
	// TimeMonth is a month rather than a week.
	TimeOf    *personRef
	TimeMonth bool

	// Approvals is what is waiting on this person to decide.
	Approvals []timelog.Entry
	// DecidedTime is the record already signed off, for visibility rather than
	// a second decision. SeeAllTime is whether that record is the whole company.
	DecidedTime []timelog.Entry
	SeeAllTime  bool
	// ApprovalGroups is Approvals by person, for signing off a person's
	// worth at once.
	ApprovalGroups []ApprovalGroup
	// Approval is Approvals narrowed by department and person.
	Approval *ApprovalView
	// Subtasks are the task's children the reader can see; ParentDoc is the
	// task it sits inside, when the reader can see that.
	Subtasks  []*docs.Document
	ParentDoc *docs.Document
	// Assignee set and who may change it (creator/manager/admin).
	CanAssign       bool
	AssigneeIDs     []string
	AssigneePrimary string
	// Guests are people the task is shared with (view/comment/edit) who are
	// not assignees. CanAssign also gates who may share.
	Guests []docs.Guest
	// Flash is a one-line confirmation after a redirect ("Approved 12 entries").
	Flash string
	// Imported hours: entries with a clear suggestion grouped by who they
	// belong to, the ones somebody has to decide, and the totals.
	ImportedByPerson []ImportedGroup
	ImportedManual   []timelog.ImportedEntry
	ImportedCount    int
	ImportedMinutes  int
	ImportedSettled  int

	// MyOpenWork is the optional task picker on the time page: this person's
	// own open, assigned work. Optional is the point. Attaching an hour to a
	// job is useful when there is one and an obstacle when there is not, and a
	// form that refuses the entry until something is chosen gets something
	// chosen at random.
	MyOpenWork []*docs.Row
	MustChange bool
	// Me is the caller's principal id, so a list of views can mark which are
	// theirs without a second lookup per row.
	Me string

	// --- conversation --------------------------------------------------------
	Channels       []*channels.Channel
	Channel        *channels.Channel
	Messages       []*channels.Message
	Comments       []*channels.Message
	ChannelMembers []channels.Member
	AttachedDocs   []channels.AttachedDoc
	// MentionedIn is every conversation that referenced this entity, which is
	// the question somebody standing on a work order actually asks.
	MentionedIn []*channels.Message
	// CommentRemoved is the one comment the page should offer an inline Undo
	// for -- the id from a delete redirect's ?commentRemoved=, same shape as
	// JustRemoved for a time entry, not a persistent flag.
	CommentRemoved string

	// --- documents -----------------------------------------------------------
	// Panel is true when this page was asked for by a list's task panel
	// (?peek=1), false for a direct visit to /documents/{id}. Same
	// content either way -- this only decides the couple of things that only
	// make sense in one context, like a "close" button that would otherwise
	// sit on a page with nothing to close.
	Panel bool
	// Peek is the quick view inside that panel (?peek=1, see the "peek"
	// block in document.html): the task at a glance plus the few changes
	// people make on the way past, rather than the whole page.
	Peek bool
	// BodyHTML is the rendered body. It is template.HTML, which is the one
	// place typed text reaches a page unescaped, and it is safe only because
	// the renderer escapes everything first and emits no tag it did not write.
	BodyHTML  template.HTML
	History   []*docs.Version
	Diff      []docs.DiffOp
	DiffFrom  *docs.Version
	DiffTo    *docs.Version
	Checklist []docs.ChecklistItem
	Progress  docs.ChecklistProgress
	Templates []docs.Template
	// ChecklistOpen re-opens the Add-a-step popover after a redirect, so
	// adding several steps in a row does not mean re-clicking Add each time.
	// Not a persistent flag -- read once from ?checklist=open, same shape as
	// JustRemoved/CommentRemoved.
	ChecklistOpen bool

	// --- time ------------------------------------------------------------------
	// Timer is whatever this person has running, on every page. A timer you can
	// only see by navigating back to the job is a timer that runs all night.
	Timer       *timelog.Entry
	TimeEntries []timelog.Entry
	Effort      *timelog.Effort
	AssetTime   *timelog.AssetEffort
	Load        []timelog.Load
	WeekFrom    time.Time
	WeekTo      time.Time
	PrevWeek    string
	NextWeek    string

	// Billing's view, not planning's -- gated on Finance rather than TimeAll.
	// Totals is the whole-org roll-up (the default view); Report is one
	// person's entries, reached by clicking through from Totals.
	Totals      []timelog.ReportTotal
	Report      []timelog.ReportRow
	ReportFrom  string
	ReportTo    string
	ReportTotal int
	// ReportPeople is everybody the details export can be made for: every active person, with the time they logged in the
	// range (none is 0). It is not the people who logged time, because a picker that only lists those is empty in a range
	// with no time in it and a person cannot choose anybody. ReportNotice says why an export made nothing.
	ReportPeople     []timelog.ReportTotal
	ReportNotice     string
	ReportPerson     string
	ReportPersonName string
	// The time report's filters: what is chosen now, the choices, and the choices spelled as parameters for links.
	ReportApproval     string
	ReportTeam         string
	ReportSpaceID      string
	ReportTag          string
	ReportFilterQS     string
	ReportTeams        []*teams.Team
	ReportSpaceOptions []*plant.Space
	ReportTagOptions   []timelog.Tag

	// Tags: the org's own taxonomy for a logged hour. CanManageTags gates the
	// create form on the tags page, not reading the list -- picking a tag does
	// not need the capability that defines new ones.
	Tags          []timelog.Tag
	CanManageTags bool

	// --- maintenance -------------------------------------------------------
	Schedules  []*maint.Schedule
	Compliance *maint.Compliance
	Readings   []maint.Reading
	MeterKeys  []string
	PMHistory  []maint.Occurrence

	// --- the plant tree ------------------------------------------------------
	Assets      []*plant.Asset
	Children    []*plant.Asset
	Asset       *plant.Asset
	AssetPath   []*plant.Asset
	AssetFields plant.Fields
	Spaces      []*plant.Space
	Space       *plant.Space
	Downtime    []plant.Downtime
	Uptime      *plant.Availability
	// AssetCode, ScanURL, and QR are the printed label. QR is template.HTML
	// because it is an SVG document written by this application and passed
	// through unescaped, exactly as the rendered markdown body is.
	AssetCode string
	ScanURL   string
	QR        template.HTML

	// --- contacts, companies, and parts --------------------------------------
	Companies    []*crm.Company
	Company      *crm.Company
	Contacts     []*crm.Contact
	Contact      *crm.Contact
	CompanyKinds []string
	Parties      []crm.Party

	// --- the sales side ------------------------------------------------------
	SalesOn bool
	// SalesNav shows the Deals entry in the rail. Set on every render, so any
	// page can carry it.
	SalesNav       bool
	Unowned        []*crm.Unowned
	CompanyTypes   []crm.CompanyType
	TypeSel        string
	TagSel         string
	Labels         *crm.Labels
	TagList        []string
	CanManageTypes bool
	UnownedType    string
	UnownedCounts  map[string]int
	// Desk is the data of the follow-up, forecast and report pages, and of the follow-ups on a record.
	Desk       *deskData
	Deals      []*crm.Deal
	Deal       *crm.Deal
	DealCols   []dealCol
	DealStages []string
	Activities []*crm.Activity
	// EmberOps is the page this record has in EmberOps, when the CRM sync has sent it there.
	EmberOps    []crm.EmberOpsLink
	Details     *crm.Details
	CompanyRows []*crm.CompanyRow
	ContactRows []*crm.ContactRow
	Owners      []auth.Person
	OwnerSel    string
	Sort        string
	SortLinks   map[string]string
	Pager       *pager
	ViewMode    string
	ExportURL   string
	DealStage   string
	Parts       []*crm.Part
	PartsUsed   []crm.Movement
	PartsCents  int

	// --- email and calls -----------------------------------------------------
	InboundAddresses []*mailint.Address
	InboundReady     bool
	MailReady        bool
	MailThread       []mailint.ThreadEntry
	// OutboundMail is this org's own relay (Settings > Email). CanStorePassword
	// is false on a deployment with no encryption key, which blocks a saved
	// relay password and the page says so.
	OutboundMail     *mailsettings.Settings
	CanStorePassword bool
	Calls            []*calls.Call
	CallOutcomes     []string

	// --- agents and canvas ---------------------------------------------------
	Agents      []*agents.Agent
	AgentTokens []*agents.Token
	Suggestions []*agents.Suggestion
	Decided     []*agents.Suggestion
	Scopes      []string
	// NewToken is a freshly minted secret, shown exactly once. There is
	// nowhere to read it back from, which is the point.
	NewToken string
	MCPURL   string
	Canvases []*canvas.Canvas
	Canvas   *canvas.Canvas
	Nodes    []*canvas.Node
	Edges    []*canvas.Edge

	// --- the public API and webhooks -----------------------------------------
	APITokens     []*api.Token
	Hooks         []*api.Webhook
	APIScopes     []string
	ImportSources []string
	APIBase       string

	// --- inbox ---------------------------------------------------------------
	Notifications []*notify.Notification
	Prefs         notify.Prefs
	UnreadOnly    bool
	Unread        int
	Watching      bool

	// --- search ------------------------------------------------------------
	SearchText string
	Results    *search.Results
	Hits       []RenderedHit
	IndexState *search.IndexState
	Engines    map[string]error

	// --- automations ---------------------------------------------------------
	Rules    []*rules.Rule
	RuleRuns []*rules.Run

	// --- the outbox ----------------------------------------------------------
	// Undecided is how many queued actions are waiting on this person, on every
	// render, for the same reason the unread count is: it is a thing somebody
	// needs to notice without going to look for it.
	Undecided    int
	Outbox       []*offline.Record
	OutboxRecent []*offline.Record
	// OutboxTitles names the entities those actions were about, resolved one at
	// a time through the document service so a row somebody has since lost
	// their grant to shows an id rather than a title.
	OutboxTitles map[string]string

	// --- governance ----------------------------------------------------------
	Audit       []audit.Row
	AuditCount  int64
	AuditKinds  []string
	AuditFilter audit.Filter
	// AuditMore is the id to page back from, empty when this is the last page.
	AuditMore   int64
	Teams       []*teams.Team
	Team        *teams.Team
	TeamMembers []teams.Member
	SCIMTokens  []*scim.Token
	SCIMBase    string

	// --- operations --------------------------------------------------------
	Stats    *jobs.Stats
	Dead     []jobs.DeadLetter
	Routes   []jobs.Route
	WorkerOn bool
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	// A browser navigation gets the interface and nothing else: the page is drawn in the browser from the data
	// this same handler returns when the interface asks for it (Accept: application/json, or ?_data=1). The
	// handler, its permission checks and its status all ran to get here, so a refused page is refused with the
	// right status whichever way it is asked for.
	if !wantsJSON(r) {
		serveShell(w, r)
		return
	}
	p.Identity = auth.From(r.Context())
	p.Build = buildinfo.Get()
	p.SSO = s.oidc != nil
	p.SSOName = s.oidcName
	p.HideLocalLogin = s.oidc != nil && s.hideLocalLogin
	p.Path = r.URL.Path
	p.Version = s.version
	p.SalesNav = s.salesNav(r)
	p.ToolsOn = len(s.tools) > 0
	p.Today = time.Now().Format("2006-01-02")
	// The list page drives its space filter entirely from this query param and
	// never sets SpaceID itself, so reading it here is the only place it comes
	// from there. A document page does set SpaceID itself (the space it is
	// actually filed under) and has no ?space= of its own -- only overwrite
	// when the query string actually carries one, or every document page's
	// breadcrumb was wiped the instant this line ran.
	if sp := r.URL.Query().Get("space"); sp != "" {
		p.SpaceID = sp
	}

	// Whether this is the owner decides which navigation appears. Resolved on
	// every render rather than stashed in the session, because a session
	// outlives a role change by up to thirty days. It is a cached lookup, so
	// the cost is one map read.
	// The running timer rides on every render, for the same reason the unread
	// count does: it is the thing somebody needs to notice without going to
	// look for it.
	if p.Identity != nil && s.time != nil && p.Timer == nil {
		if t, err := s.time.Running(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.Timer = t
		}
	}

	// The unread count rides on every render, so the badge in the top bar is
	// never stale by a page load. One count on a partial index.
	if p.Identity != nil && s.inbox != nil {
		if n, err := s.inbox.UnreadFor(r.Context(), p.Identity.Subjects); err == nil {
			p.Unread = n
		}
	}

	// And what their phone is waiting on them for, same shape as the unread
	// count and the running timer: a question nobody goes looking for.
	if p.Identity != nil && s.offline != nil && p.Undecided == 0 {
		if n, err := s.offline.Count(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.Undecided = n
		}
	}

	// What is inside the department being viewed. Loaded here for the same
	// reason the tree is: a handler that has to remember to populate it is a
	// page where it sometimes vanishes.
	if p.Identity != nil && s.plant != nil && p.SpaceID != "" && p.SpaceChildren == nil {
		if kids, err := s.plant.SpaceChildren(r.Context(), p.Identity.OrgID, p.Identity.Subjects, p.SpaceID); err == nil {
			p.SpaceChildren = kids
		}
		if sp, err := s.plant.GetSpace(r.Context(), p.Identity.OrgID, p.Identity.Subjects, p.SpaceID); err == nil {
			p.SpaceName = sp.Name
			// A department's page is named for the department, in the top bar and the tab as well as over the directory:
			// it said Everything above a list of one department.
			if name == "documents" && p.Kind == "" && p.SavedView == nil && p.Path == "/documents" {
				p.Title, p.Heading = sp.Name, sp.Name
			}
		}
	}

	// The department tree, for the rail, and the breadcrumb -- two views of
	// the same fetch. Failure leaves both empty rather than failing the page:
	// a page without its navigation is still readable and still has the top
	// bar, where a 500 is nothing at all.
	if p.Identity != nil && s.plant != nil && p.Org == nil {
		if flat, err := s.plant.SpaceTree(r.Context(), p.Identity.OrgID, p.Identity.Subjects); err == nil {
			p.Org = buildOrgTree(flat, p.SpaceID)
			if p.SpaceID != "" && p.Crumbs == nil {
				p.Crumbs = spaceCrumbs(flat, p.SpaceID)
			}
		}
	}

	// What this person may reach, on every render, for exactly the reason
	// IsOwner is resolved here: a session outlives a role change by up to
	// thirty days, and a rail gated on a field thirty handlers have to remember
	// to populate is a rail that disappears at random.
	if p.Identity != nil && s.az != nil && p.Can == nil {
		if held, err := s.az.Capabilities(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.Can = held
		}
	}
	if p.Identity != nil && s.plant != nil {
		if ok, err := s.plant.MayManageAnySpace(r.Context(), p.Identity.OrgID, p.Identity.Subjects); err == nil {
			p.CanManageSpaces = ok
		}
	}

	// The approvals badge, same shape as the unread count: a question nobody
	// goes looking for. Only asked of somebody who could approve something,
	// which the query itself decides.
	if p.Identity != nil && s.time != nil && p.ToApprove == 0 {
		if n, err := s.time.PendingCount(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.ToApprove = n
		}
	}
	if p.Identity != nil && s.timeoff != nil {
		if n, err := s.timeoff.PendingCount(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.TimeOffPending = n
		}
	}
	// Department requests waiting on this person, and whether this person is
	// in no department and has not asked -- the banner that asks them.
	if p.Identity != nil && s.deptreq != nil {
		if n, err := s.deptreq.PendingCount(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.DeptPending = n
		}
		if p.Path != "/departments/request" && !p.Identity.Contractor {
			if cur, err := s.deptreq.Current(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil && len(cur) == 0 {
				if last, err := s.deptreq.Latest(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil &&
					departmentPromptOwed(p.Identity.Contractor, last) {
					p.NeedsDepartment = true
				}
			}
		}
	}

	// How much of this person's own week is still waiting on somebody, for the
	// line under the time form on the dashboard. Same source as the time page,
	// so the two never disagree about the number.
	if p.Identity != nil && s.time != nil && p.MyPending == 0 {
		week := startOfWeek(time.Now())
		if _, pending, err := s.time.MineTotal(r.Context(), p.Identity.OrgID,
			p.Identity.PrincipalID, week, week.AddDate(0, 0, 7)); err == nil {
			p.MyPending = pending
		}
	}

	if p.Identity != nil && s.auth != nil && !p.IsAdmin {
		if admin, err := s.auth.IsAdmin(r.Context(), p.Identity.OrgID, p.Identity.PrincipalID); err == nil {
			p.IsAdmin = admin
		}
	}

	if err := writePageJSON(w, name, p); err != nil {
		s.log.ErrorContext(r.Context(), "render json failed", "template", name, "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	// The same sentence, in a header, for the one caller that is a machine: the
	// offline replay has to tell a conflict from a refusal and must not do it
	// by reading prose out of a rendered page.
	w.Header().Set("X-Hearth-Reason", headerSafe(msg))
	// The status line goes out before render runs, and headers set after it are ignored. A page
	// that is asking for its data (the rebuilt interface) has to be told it is getting JSON now.
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Accept")
		w.Header().Set("X-Hearth-Page-Data", "1")
	} else {
		shellHeaders(w)
	}
	w.WriteHeader(status)
	s.render(w, r, "error", page{
		Title: http.StatusText(status), Status: status, Message: msg,
	})
}

// Routes returns the full HTTP surface.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fsSub(staticFS, "static")
	// The {v} segment is the build revision. It is not read: its only job is to
	// change the URL when the binary changes, so a cached asset can never
	// outlive the build it came from.
	mux.Handle("GET /static/{v}/", http.StripPrefix("/static/"+assetVersion()+"/",
		immutable(http.FileServer(http.FS(sub)))))

	// The icons, at fixed addresses and outside the session wall. A browser asks for /favicon.ico on its own, a phone
	// asks for /apple-touch-icon.png, and both ask before anybody has signed in; the versioned /static/{v}/ path
	// above cannot be written into a page that is the same file for every build.
	for path, file := range brandIcons {
		mux.Handle("GET "+path, brandIcon(sub, file))
	}

	// The interface (ui/), embedded in the binary: its bundles and the one script the shell loads from
	// the root. Outside the session wall because they are static; every request the interface then makes
	// for data goes through the session and the permission checks of the page it stands for.
	mux.Handle("GET /_astro/", builtFiles())
	mux.Handle("GET /theme-next.js", builtFiles())
	// The first release put the interface under /next/. Those addresses still work.
	mux.HandleFunc("GET /next", movedFromNext)
	mux.HandleFunc("GET /next/", movedFromNext)

	mux.HandleFunc("GET /setup", s.getSetup)
	mux.HandleFunc("POST /setup", s.postSetup)
	mux.HandleFunc("GET /login", s.getLogin)
	mux.HandleFunc("POST /login", s.postLogin)
	mux.HandleFunc("POST /logout", s.postLogout)
	mux.HandleFunc("GET /set-password", s.getSetPassword)
	mux.HandleFunc("POST /set-password", s.postSetPassword)
	// The calendar feed, deliberately outside the authenticated mux.
	//
	// A calendar application fetches this on a timer forever. It holds no
	// cookie, cannot follow a redirect to a sign-in page, and has nobody in
	// front of it to type a password, so putting it behind auth turns every
	// subscription into a silent failure. The token in the path is the
	// credential, the feed is read only, and it covers only what that one
	// person can already see.
	mux.HandleFunc("GET /calendar/feed/{token}", s.getCalendarFeed)

	mux.HandleFunc("GET /login/sso", s.startSSO)
	mux.HandleFunc("GET /auth/callback", s.ssoCallback)
	mux.HandleFunc("GET /empty", func(w http.ResponseWriter, r *http.Request) {})

	// The installable part, outside the session wall. A phone registers the
	// worker and reads the manifest before anybody signs in, and the no-signal
	// page has to render for a browser that cannot reach the server at all, so
	// none of the three can be behind a redirect to a login page. None of them
	// says anything about this installation or reaches any of its data.
	mux.HandleFunc("GET /sw.js", s.serviceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)
	mux.HandleFunc("GET /offline", s.getOfflineShell)
	// The phone app's server address: the page, and the answer that proves an address is a Hearth.
	mux.HandleFunc("GET /connect", s.getConnect)
	mux.HandleFunc("GET /.well-known/hearth", s.getWellKnown)
	// The one endpoint a machine talks to. Outside the session middleware
	// because an MTA has no cookies, and authenticated by a shared secret
	// compared in constant time.
	mux.HandleFunc("POST /inbound/mail", s.postInbound)
	// And the second: an agent using the assistant connector. Same
	// shape, a bearer token instead of a shared secret, and every tool behind
	// it goes through the grants a person's request hits.
	// And the REST API, which is the third. It resolves a token to a person
	// and then calls the same service methods the pages call, so it refuses
	// exactly what the interface refuses for that caller.
	mux.Handle("/api/", http.StripPrefix("/api", s.api.Routes()))

	// Everything below requires a session.
	authed := http.NewServeMux()
	authed.HandleFunc("GET /", s.getDashboard)
	authed.HandleFunc("GET /documents", s.getList)
	authed.HandleFunc("GET /events", s.events)
	authed.HandleFunc("GET /documents/new", s.newDocument)
	authed.HandleFunc("POST /documents", s.createDocument)
	// Each kind gets its own address. They all reach the same handler, because
	// a work order and a task differ in which fields matter, not in how they are
	// stored. A URL people can type and link to is worth more than the query
	// string it replaces.
	authed.HandleFunc("GET /work-orders", s.getList)
	authed.HandleFunc("GET /assets", s.getList)
	authed.HandleFunc("GET /tasks", s.getList)
	authed.HandleFunc("GET /purchase-orders", s.getList)

	// A saved view is an address. That is the whole feature: a filtered list
	// somebody cannot send to a colleague is half a feature.
	authed.HandleFunc("GET /documents/{id}/history", s.getHistory)
	authed.HandleFunc("POST /documents/{id}/restore/{version}", s.restoreVersion)
	authed.HandleFunc("POST /documents/{id}/checklist", s.addChecklistItem)
	authed.HandleFunc("POST /documents/{id}/checklist/{item}", s.setChecklistItem)
	authed.HandleFunc("POST /documents/{id}/checklist/{item}/delete", s.removeChecklistItem)
	authed.HandleFunc("GET /templates", s.listTemplates)
	authed.HandleFunc("POST /templates/{id}/use", s.useTemplate)

	// The plant tree. /a/{code} is short on purpose: it is printed under the
	// symbol so that somebody can type it when the label is too oily to scan.
	authed.HandleFunc("GET /a/{code}", s.scan)
	authed.HandleFunc("GET /plant", s.getPlant)
	authed.HandleFunc("GET /spaces", s.getSpaces)
	authed.HandleFunc("POST /spaces", s.createSpace)
	authed.HandleFunc("POST /spaces/{id}/rename", s.renameSpace)
	authed.HandleFunc("POST /spaces/{id}/move", s.moveSpace)
	authed.HandleFunc("POST /spaces/{id}/delete", s.deleteSpace)
	authed.HandleFunc("GET /spaces/{id}/access", s.getSpaceAccess)
	authed.HandleFunc("POST /spaces/{id}/access", s.postSpaceDelegate)
	authed.HandleFunc("POST /spaces/{id}/access/{person}/remove", s.postSpaceUndelegate)
	authed.HandleFunc("POST /spaces/reorder", s.reorderSpaces)
	authed.HandleFunc("POST /spaces/save-order", s.saveSpaceOrder)
	authed.HandleFunc("POST /documents/{id}/space", s.moveDocumentToSpace)
	authed.HandleFunc("POST /documents/{id}/asset", s.setAssetFields)
	authed.HandleFunc("GET /documents/{id}/label", s.getLabel)
	authed.HandleFunc("POST /documents/{id}/downtime", s.recordDowntime)
	authed.HandleFunc("POST /documents/{id}/downtime/{entry}/delete", s.removeDowntime)

	// Contacts and companies. Ordinary entities, so they get grants,
	// properties, links, and search from the substrate rather than from four
	// new pieces of code written specially for them.
	authed.HandleFunc("POST /crm/quote", s.postQuoteFromDeal)
	authed.HandleFunc("GET /crm/favorites", s.getFavorites)
	authed.HandleFunc("POST /crm/favorites", s.setFavorite)
	authed.HandleFunc("POST /crm/views", s.saveCRMView)
	authed.HandleFunc("POST /crm/views/{id}/delete", s.deleteCRMView)
	authed.HandleFunc("GET /crm/import", s.getCRMImport)
	authed.HandleFunc("POST /crm/import", s.postCRMImport)
	authed.HandleFunc("GET /companies", s.listCompanies)
	authed.HandleFunc("POST /companies", s.createCompany)
	authed.HandleFunc("GET /companies/{id}", s.getCompany)
	authed.HandleFunc("POST /companies/{id}", s.updateCompany)
	authed.HandleFunc("POST /companies/{id}/delete", s.deleteCompany)
	authed.HandleFunc("GET /contacts", s.listContacts)
	authed.HandleFunc("POST /contacts", s.createContact)
	authed.HandleFunc("GET /contacts/{id}", s.getContact)
	authed.HandleFunc("POST /contacts/{id}", s.updateContact)
	authed.HandleFunc("POST /contacts/{id}/delete", s.deleteContact)
	authed.HandleFunc("POST /contacts/{id}/link", s.linkContact)
	s.salesRoutes(authed)
	s.deskRoutes(authed)
	authed.HandleFunc("POST /companies/{id}/link", s.linkCompany)

	authed.HandleFunc("GET /integrations", s.getIntegrations)
	authed.HandleFunc("POST /integrations/tokens", s.mintAPIToken)
	authed.HandleFunc("POST /integrations/tokens/{id}/revoke", s.revokeAPIToken)
	authed.HandleFunc("POST /integrations/webhooks", s.saveWebhook)
	authed.HandleFunc("POST /integrations/webhooks/{id}/delete", s.deleteWebhook)

	authed.HandleFunc("GET /agents", s.getAgents)
	authed.HandleFunc("POST /agents", s.createAgent)
	authed.HandleFunc("POST /agents/{id}/status", s.setAgentStatus)
	authed.HandleFunc("POST /agents/tokens", s.mintToken)
	authed.HandleFunc("POST /agents/tokens/{id}/revoke", s.revokeToken)
	authed.HandleFunc("POST /suggestions/{id}", s.decideSuggestion)

	authed.HandleFunc("GET /canvas", s.listCanvases)
	authed.HandleFunc("POST /canvas", s.createCanvas)
	authed.HandleFunc("GET /canvas/{id}", s.getCanvas)
	authed.HandleFunc("POST /canvas/{id}/delete", s.deleteCanvas)
	authed.HandleFunc("POST /canvas/{id}/nodes", s.addCanvasNode)
	authed.HandleFunc("POST /canvas/{id}/nodes/{node}", s.moveCanvasNode)
	authed.HandleFunc("POST /canvas/{id}/nodes/{node}/delete", s.removeCanvasNode)
	authed.HandleFunc("POST /canvas/{id}/edges", s.addCanvasEdge)
	authed.HandleFunc("POST /canvas/{id}/edges/{edge}/delete", s.removeCanvasEdge)

	authed.HandleFunc("GET /settings/mail", s.getMailSettings)
	authed.HandleFunc("POST /settings/mail", s.saveInboundAddress)
	authed.HandleFunc("POST /settings/mail/outbound", s.saveOutbound)
	authed.HandleFunc("POST /settings/mail/outbound/test", s.testOutbound)
	authed.HandleFunc("POST /settings/mail/{id}/delete", s.deleteInboundAddress)
	authed.HandleFunc("POST /documents/{id}/reply", s.postReply)
	authed.HandleFunc("POST /calls", s.logCall)
	authed.HandleFunc("POST /calls/{id}/delete", s.removeCall)
	authed.HandleFunc("POST /calls/{id}/recording", s.attachRecording)

	authed.HandleFunc("GET /parts", s.listParts)
	authed.HandleFunc("POST /parts", s.savePart)
	authed.HandleFunc("POST /parts/{id}/delete", s.deletePart)
	authed.HandleFunc("POST /parts/{id}/adjust", s.adjustPart)
	authed.HandleFunc("POST /documents/{id}/parts", s.usePart)
	authed.HandleFunc("POST /documents/{id}/parts/{entry}/delete", s.removeMovement)

	authed.HandleFunc("GET /maintenance", s.getMaintenance)
	authed.HandleFunc("POST /maintenance", s.saveSchedule)
	authed.HandleFunc("POST /maintenance/{id}/enabled", s.toggleSchedule)
	authed.HandleFunc("POST /maintenance/{id}/delete", s.deleteSchedule)
	authed.HandleFunc("POST /maintenance/{id}/raise", s.raiseSchedule)
	authed.HandleFunc("POST /documents/{id}/readings", s.recordReading)

	authed.HandleFunc("GET /workload", s.getWorkload)
	authed.HandleFunc("GET /reports/time", s.getTimeReport)
	authed.HandleFunc("GET /reports/daily", s.getDailyReports)
	authed.HandleFunc("GET /reports/daily.pdf", s.getDailyReportsPDF)
	authed.HandleFunc("GET /reports/daily/new", s.getDailyReportNew)
	authed.HandleFunc("POST /reports/daily", s.postDailyReport)
	authed.HandleFunc("GET /reports/service", s.getServiceHistory)
	authed.HandleFunc("GET /reports/service.pdf", s.getServiceHistoryPDF)
	authed.HandleFunc("GET /reports/service.csv", s.getServiceHistoryCSV)
	authed.HandleFunc("GET /documents/{id}/report.pdf", s.getReportPDF)
	// A person's own time. Not gated: everybody records their own, which is
	// the whole point of it existing beside the document-scoped routes above.
	authed.HandleFunc("GET /time", s.getMyTime)
	authed.HandleFunc("POST /time", s.postMyTime)
	authed.HandleFunc("POST /time/start", s.startMyTimer)
	authed.HandleFunc("GET /settings/connectors", s.getConnectors)
	authed.HandleFunc("POST /settings/connectors/tenant", s.postConnectorTenant)
	authed.HandleFunc("POST /settings/connectors/toggle", s.postConnectorToggle)
	authed.HandleFunc("POST /settings/connectors/remove", s.postConnectorRemovePerson)
	authed.HandleFunc("POST /settings/connectors/disconnect", s.postConnectorDisconnect)
	authed.HandleFunc("POST /settings/connectors/sync", s.postConnectorSync)
	authed.HandleFunc("POST /settings/connectors/calendar", s.postConnectorCalendar)
	authed.HandleFunc("POST /settings/connectors/chat", s.postConnectorChat)
	authed.HandleFunc("POST /integrations/microsoft/connect", s.startMicrosoft)
	authed.HandleFunc("GET /integrations/microsoft/callback", s.microsoftCallback)
	authed.HandleFunc("GET /tools", s.getTools)
	authed.HandleFunc("GET /marketing", s.getMarketing)
	authed.HandleFunc("POST /marketing/email", s.postMarketingEmail)
	authed.HandleFunc("POST /marketing/email/{id}/publish", s.postMarketingEmailPublish)
	authed.HandleFunc("POST /marketing/email/{id}/send", s.postMarketingEmailSend)
	authed.HandleFunc("POST /marketing/email/{id}/delete", s.postMarketingEmailDelete)
	authed.HandleFunc("POST /marketing/campaign", s.postMarketingCampaign)
	authed.HandleFunc("POST /marketing/campaign/{id}/publish", s.postMarketingCampaignPublish)
	authed.HandleFunc("POST /marketing/campaign/{id}/delete", s.postMarketingCampaignDelete)
	authed.HandleFunc("POST /marketing/segment", s.postMarketingSegment)
	authed.HandleFunc("POST /marketing/contact", s.postMarketingContact)
	authed.HandleFunc("POST /marketing/contact/{id}/delete", s.postMarketingContactDelete)
	authed.HandleFunc("POST /marketing/contact/{id}/dnc", s.postMarketingContactDNC)
	authed.HandleFunc("POST /marketing/contact/{id}/segment", s.postMarketingContactSegment)
	authed.HandleFunc("POST /marketing/contact/{id}/send", s.postMarketingContactSend)
	authed.HandleFunc("POST /marketing/email/{id}/test", s.postMarketingEmailTest)
	authed.HandleFunc("POST /marketing/segment/{id}/delete", s.postMarketingSegmentDelete)
	authed.HandleFunc("GET /forms", s.getForms)
	authed.HandleFunc("POST /forms", s.postFormsSave)
	authed.HandleFunc("POST /forms/{id}/status", s.postFormsStatus)
	authed.HandleFunc("POST /forms/{id}/schedule", s.postFormsSchedule)
	authed.HandleFunc("POST /forms/{id}/delete", s.postFormsDelete)
	authed.HandleFunc("GET /forms/{id}/answers.csv", s.getFormsCSV)
	authed.HandleFunc("POST /forms/{id}/answers/{rid}/delete", s.postFormsResponseDelete)
	authed.HandleFunc("GET /github", s.getGitHub)
	authed.HandleFunc("POST /github/connect", s.postGitHubConnect)
	authed.HandleFunc("POST /github/repos", s.postGitHubRepos)
	authed.HandleFunc("POST /github/disconnect", s.postGitHubDisconnect)
	authed.HandleFunc("GET /social", s.getSocial)
	authed.HandleFunc("POST /social/post", s.postSocialPost)
	authed.HandleFunc("POST /social/post/{id}/delete", s.postSocialDelete)
	authed.HandleFunc("POST /social/post/{id}/status", s.postSocialStatus)
	authed.HandleFunc("POST /social/media", s.postSocialUpload)
	authed.HandleFunc("GET /mailbox", s.getMailbox)
	authed.HandleFunc("POST /mailbox/send", s.postMailSend)
	authed.HandleFunc("POST /mailbox/reply", s.postMailReply)
	authed.HandleFunc("POST /mailbox/forward", s.postMailForward)
	authed.HandleFunc("POST /mailbox/archive", s.postMailArchive)
	authed.HandleFunc("POST /mailbox/label", s.postMailLabel)
	authed.HandleFunc("POST /mailbox/read", s.postMailRead)
	authed.HandleFunc("POST /mailbox/delete", s.postMailDelete)
	authed.HandleFunc("POST /cloud/upload", s.postCloudUpload)
	authed.HandleFunc("POST /cloud/replace", s.postCloudReplace)
	authed.HandleFunc("POST /cloud/rename", s.postCloudRename)
	authed.HandleFunc("POST /cloud/move", s.postCloudMove)
	authed.HandleFunc("POST /cloud/mkdir", s.postCloudMkdir)
	authed.HandleFunc("POST /cloud/delete", s.postCloudDelete)
	authed.HandleFunc("POST /cloud/save", s.postCloudSave)
	authed.HandleFunc("GET /cloud", s.getCloud)
	authed.HandleFunc("POST /cloud/link", s.postCloudLink)
	authed.HandleFunc("POST /cloud/import", s.postCloudImport)
	authed.HandleFunc("POST /cloud/unlink", s.postCloudUnlink)
	authed.HandleFunc("POST /integrations/google/connect", s.startGoogle)
	authed.HandleFunc("GET /integrations/google/callback", s.googleCallback)
	authed.HandleFunc("POST /integrations/google/disconnect", s.disconnectGoogle)
	authed.HandleFunc("POST /integrations/google/sync", s.syncGoogle)
	authed.HandleFunc("GET /projects", s.getProjects)
	authed.HandleFunc("GET /calendar", s.getCalendar)
	authed.HandleFunc("POST /calendar", s.postCalendarEvent)
	authed.HandleFunc("POST /calendar/{id}/delete", s.deleteCalendarEvent)
	authed.HandleFunc("POST /calendar/feed", s.postCalendarFeed)
	authed.HandleFunc("GET /time/approvals", s.getApprovals)
	authed.HandleFunc("POST /time/approvals/bulk", s.postApprovalsBulk)
	authed.HandleFunc("POST /time/approvals/{entry}", s.postApproval)
	authed.HandleFunc("GET /time/imported", s.getImported)
	authed.HandleFunc("POST /time/imported/apply", s.postImportedApply)
	authed.HandleFunc("POST /time/imported/{entry}", s.postImportedMove)
	authed.HandleFunc("POST /time/entries/{entry}", s.correctMyTime)
	authed.HandleFunc("POST /time/entries/{entry}/delete", s.removeMyTime)
	authed.HandleFunc("POST /time/entries/{entry}/restore", s.restoreMyTime)
	authed.HandleFunc("POST /time/entries/{entry}/tags", s.tagMyTime)
	authed.HandleFunc("POST /time/entries/{entry}/tags/{tag}/delete", s.untagMyTime)
	authed.HandleFunc("POST /time/batch-tag", s.batchTag)
	authed.HandleFunc("GET /tags", s.getTags)
	authed.HandleFunc("POST /tags", s.postTags)
	authed.HandleFunc("POST /tags/{id}", s.postTagUpdate)
	authed.HandleFunc("POST /tags/{id}/delete", s.postTagDelete)
	authed.HandleFunc("POST /people/{id}/capacity", s.setCapacity)
	authed.HandleFunc("POST /documents/{id}/timer", s.startTimer)
	authed.HandleFunc("POST /timer/stop", s.stopTimer)
	authed.HandleFunc("POST /documents/{id}/time", s.logTime)
	authed.HandleFunc("POST /documents/{id}/time/{entry}", s.correctTime)
	authed.HandleFunc("POST /documents/{id}/time/{entry}/delete", s.removeTime)
	authed.HandleFunc("POST /documents/{id}/estimate", s.setEstimate)

	authed.HandleFunc("GET /inbox", s.getInbox)
	authed.HandleFunc("POST /inbox/read", s.markNotificationsRead)
	authed.HandleFunc("POST /inbox/prefs", s.saveNotificationPrefs)
	// Web Push for this person's own devices. JSON, called by the page that turns push on.
	authed.HandleFunc("GET /push/state", s.getPushState)
	authed.HandleFunc("POST /push/subscribe", s.postPushSubscribe)
	authed.HandleFunc("POST /push/unsubscribe", s.postPushUnsubscribe)
	authed.HandleFunc("POST /push/kinds", s.postPushKinds)
	authed.HandleFunc("POST /push/test", s.postPushTest)
	authed.HandleFunc("POST /documents/{id}/watch", s.toggleWatch)

	authed.HandleFunc("GET /search", s.getSearch)

	// Work done at the machine with no signal. The sync endpoint replays a
	// queue through this same mux, which is why authed is kept on the server
	// below rather than being a local variable.
	authed.HandleFunc("GET /outbox", s.getOutbox)
	authed.HandleFunc("POST /offline/sync", s.postOfflineSync)
	authed.HandleFunc("POST /offline/{id}/keep", s.postOutboxKeep)
	authed.HandleFunc("POST /offline/{id}/discard", s.postOutboxDiscard)
	authed.HandleFunc("GET /scan", s.getScan)

	// Governance. Owner only, every one of them, because the audit log names
	// what everybody has done and team membership decides what they can see.
	authed.HandleFunc("GET /settings", s.getSettings)
	authed.HandleFunc("GET /audit", s.getAudit)
	authed.HandleFunc("GET /teams", s.listTeams)
	authed.HandleFunc("POST /teams", s.createTeam)
	authed.HandleFunc("GET /teams/{id}", s.getTeam)
	authed.HandleFunc("POST /teams/{id}/rename", s.renameTeam)
	authed.HandleFunc("POST /teams/{id}/delete", s.deleteTeam)
	authed.HandleFunc("POST /teams/{id}/members", s.addTeamMember)
	authed.HandleFunc("POST /teams/{id}/members/{principal}/delete", s.removeTeamMember)
	authed.HandleFunc("POST /teams/{id}/grant", s.grantTeam)
	authed.HandleFunc("POST /integrations/provisioning", s.mintSCIMToken)
	authed.HandleFunc("POST /integrations/provisioning/{id}/revoke", s.revokeSCIMToken)

	// Conversation. A comment is a message on an entity rather than a second
	// system, so both go through the same service and the same permissions.
	authed.HandleFunc("GET /channels", s.listChannels)
	authed.HandleFunc("POST /channels", s.createChannel)
	authed.HandleFunc("GET /channels/{id}", s.getChannel)
	authed.HandleFunc("POST /channels/{id}/messages", s.postChannelMessage)
	authed.HandleFunc("POST /channels/{id}/join", s.joinChannel)
	authed.HandleFunc("POST /channels/{id}/leave", s.leaveChannel)
	authed.HandleFunc("POST /channels/{id}/invite", s.inviteToChannel)
	authed.HandleFunc("POST /messages/{id}/delete", s.deleteMessage)
	authed.HandleFunc("POST /messages/{id}", s.editMessage)
	authed.HandleFunc("POST /messages/{id}/restore", s.restoreMessage)
	authed.HandleFunc("GET /views", s.listViews)
	authed.HandleFunc("POST /views", s.saveView)
	authed.HandleFunc("GET /views/{id}", s.getSavedView)
	authed.HandleFunc("POST /views/{id}/delete", s.deleteView)

	authed.HandleFunc("GET /people", s.getPeople)
	authed.HandleFunc("GET /people/{id}", s.getPerson)
	authed.HandleFunc("POST /people", s.addPerson)
	authed.HandleFunc("POST /people/{id}/password-link", s.postPasswordLink)
	authed.HandleFunc("POST /people/{id}/status", s.setPersonStatus)
	authed.HandleFunc("POST /people/{id}/contractor", s.postContractor)
	authed.HandleFunc("POST /people/{id}/capability", s.postCapability)
	authed.HandleFunc("POST /people/{id}/departments", s.postDepartments)
	authed.HandleFunc("POST /people/{id}/grant-space", s.postGrantSpace)
	authed.HandleFunc("POST /people/{id}/access", s.postAccess)
	authed.HandleFunc("POST /people/{id}/revoke-space", s.postRevokeSpace)
	authed.HandleFunc("POST /people/{id}/approver", s.postApprover)
	authed.HandleFunc("POST /people/{id}/time-auto-approve", s.postTimeAutoApprove)
	authed.HandleFunc("POST /people/{id}/manages", s.postManages)

	// Operations. Owner only, and the queue is the first page because a queue
	// with no window into it is a queue nobody trusts.
	authed.HandleFunc("GET /rules", s.listRules)
	authed.HandleFunc("POST /rules", s.saveRule)
	authed.HandleFunc("POST /rules/{id}/enabled", s.toggleRule)
	authed.HandleFunc("POST /rules/{id}/delete", s.deleteRule)
	authed.HandleFunc("POST /rules/{id}/dryrun", s.dryRunRule)

	authed.HandleFunc("GET /ops/jobs", s.getOpsJobs)
	authed.HandleFunc("POST /ops/jobs/selftest", s.postOpsSelfTest)
	authed.HandleFunc("POST /ops/search/backfill", s.postOpsBackfill)
	authed.HandleFunc("POST /ops/jobs/dead/{id}/revive", s.postOpsRevive)
	authed.HandleFunc("POST /ops/jobs/dead/{id}/discard", s.postOpsDiscard)
	authed.HandleFunc("GET /account", s.getAccount)
	authed.HandleFunc("POST /account/password", s.changePassword)
	authed.HandleFunc("GET /settings/tutorial", s.getTutorial)
	authed.HandleFunc("GET /departments/request", s.getDeptRequest)
	authed.HandleFunc("POST /departments/request", s.postDeptRequest)
	authed.HandleFunc("POST /departments/request/cancel", s.postDeptRequestCancel)
	authed.HandleFunc("GET /departments/requests/{id}", s.getDeptRequestLink)
	authed.HandleFunc("POST /departments/requests/{id}/decide", s.postDeptDecide)
	authed.HandleFunc("GET /time-off", s.getTimeOff)
	authed.HandleFunc("POST /time-off", s.postTimeOff)
	authed.HandleFunc("POST /time-off/{id}/decide", s.postDecideTimeOff)
	authed.HandleFunc("POST /time-off/{id}/cancel", s.postCancelTimeOff)
	authed.HandleFunc("POST /time-off/{id}/change", s.postChangeTimeOff)
	authed.HandleFunc("POST /time-off/{id}/cancel-for", s.postCancelForTimeOff)
	authed.HandleFunc("POST /tutorial/enter", s.postEnterPlayground)
	authed.HandleFunc("POST /tutorial/leave", s.postLeavePlayground)
	authed.HandleFunc("POST /tutorial/reset", s.postResetPlayground)
	authed.HandleFunc("POST /account/avatar", s.uploadAvatar)
	authed.HandleFunc("POST /account/avatar/remove", s.removeAvatar)
	authed.HandleFunc("GET /people/{id}/avatar", s.getAvatar)

	authed.HandleFunc("GET /import", s.getImport)
	authed.HandleFunc("POST /import/clickup", s.postImportClickUp)

	authed.HandleFunc("GET /documents/{id}", s.getDocument)
	authed.HandleFunc("POST /documents/{id}", s.updateDocument)
	authed.HandleFunc("POST /documents/{id}/delete", s.deleteDocument)
	authed.HandleFunc("POST /documents/{id}/status", s.updateTask)
	authed.HandleFunc("POST /documents/{id}/properties", s.updateProperties)
	authed.HandleFunc("POST /documents/{id}/comments", s.postComment)
	authed.HandleFunc("POST /documents/{id}/channel", s.attachToChannel)
	authed.HandleFunc("POST /documents/{id}/move", s.moveCard)
	authed.HandleFunc("POST /documents/{id}/link", s.linkDocument)
	authed.HandleFunc("POST /documents/{id}/subtasks", s.addSubtask)
	authed.HandleFunc("POST /documents/{id}/assignees", s.setAssignees)
	authed.HandleFunc("POST /documents/{id}/guests", s.shareGuest)
	authed.HandleFunc("POST /documents/{id}/guests/remove", s.unshareGuest)
	authed.HandleFunc("POST /documents/{id}/unlink", s.unlinkDocument)
	authed.HandleFunc("POST /documents/{id}/files", s.uploadFile)
	authed.HandleFunc("POST /preview", s.postPreview)
	authed.HandleFunc("POST /documents/{id}/files/{fid}/remove", s.removeAttachment)
	authed.HandleFunc("POST /documents/{id}/files/{fid}/restore", s.restoreAttachment)
	authed.HandleFunc("GET /documents/{id}/files/{fid}", s.downloadFile)
	// Held, so a queued request can be replayed into it. Everything the offline
	// path dispatches goes through these handlers with the caller's own
	// session, which is what makes the offline write path and the online one
	// the same code rather than two that have to be kept in step.
	s.authed = authed
	mux.Handle("/", auth.Require(s.mustChangeGate(s.contractorGate(s.playgroundGuard(authed)))))

	return mux
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not check setup state")
		return
	}
	// Once anyone exists, this page is gone. An installation cannot be
	// re-claimed by whoever finds it next.
	if !needs {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "setup", page{Title: "Set up"})
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	_, err := s.auth.Setup(r.Context(),
		r.FormValue("org"), r.FormValue("name"), r.FormValue("email"), r.FormValue("password"))
	if err != nil {
		// Show the reason: at this point there is nobody to leak information to.
		s.render(w, r, "setup", page{Title: "Set up", Message: err.Error()})
		return
	}
	// Sign them straight in. Making somebody who just chose a password type it
	// again immediately is friction with no security benefit.
	if err := s.auth.LoginLocal(r.Context(), w, r, r.FormValue("email"), r.FormValue("password")); err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if auth.From(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if needs, err := s.auth.NeedsSetup(r.Context()); err == nil && needs {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", page{Title: "Sign in",
		Next: auth.SafeNext(r.URL.Query().Get("next"))})
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	next := auth.SafeNext(r.FormValue("next"))
	err := s.auth.LoginLocal(r.Context(), w, r, r.FormValue("email"), r.FormValue("password"))
	switch {
	case errors.Is(err, auth.ErrLockedOut):
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, r, "login", page{Title: "Sign in", Message: err.Error(), Next: next})
		return
	case err != nil && !errors.Is(err, auth.ErrBadCredentials):
		// Not a wrong password: the store failed. Say so, so nobody retypes a
		// right password and decides it is wrong.
		s.log.ErrorContext(r.Context(), "login could not be checked", "error", err)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		s.render(w, r, "login", page{Title: "Sign in",
			Message: "Hearth could not check your sign-in just now. Try again in a moment.", Next: next})
		return
	case err != nil:
		// One message for every failure, whether the address exists or not.
		// Distinguishing them turns this form into a directory of who has an
		// account here.
		s.log.InfoContext(r.Context(), "failed login", "email", r.FormValue("email"))
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, "login", page{Title: "Sign in",
			Message: auth.ErrBadCredentials.Error(), Next: next})
		return
	}
	if next != "" {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.auth.Destroy(r.Context(), w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// kindFor resolves the kind from the path first and the query second, so
// /work-orders and /documents?kind=work_order are the same page.
func kindFor(r *http.Request) string {
	switch r.URL.Path {
	case "/work-orders":
		return docs.KindWorkOrder
	case "/assets":
		return docs.KindAsset
	case "/tasks":
		return docs.KindTask
	case "/purchase-orders":
		return docs.KindPurchaseOrder
	}
	return r.URL.Query().Get("kind")
}

// pathFor is the inverse, for building links and form targets.
func pathFor(kind string) string {
	switch kind {
	case docs.KindWorkOrder:
		return "/work-orders"
	case docs.KindAsset:
		return "/assets"
	case docs.KindTask:
		return "/tasks"
	case docs.KindPurchaseOrder:
		return "/purchase-orders"
	case "":
		return "/documents"
	default:
		return "/documents?kind=" + kind
	}
}

// allStatuses is the lifecycle order the pickers offer. It lives in docs
// because a board, a filter chip, and a validation check all have to agree
// about it, and three copies of a list of five strings is three chances to
// disagree.
var allStatuses = docs.Statuses

func (s *Server) newDocument(w http.ResponseWriter, r *http.Request) {
	// No kind means the generic New on a department or All documents page, so
	// the form offers a choice -- Task first, because that is what people make
	// most. It used to fall back to Doc silently, with no way to pick.
	kind := r.URL.Query().Get("kind")
	choose := kind == ""
	if choose {
		kind = docs.KindTask
	}
	var people []docs.People
	if docs.HasTaskFields(kind) {
		var err error
		if people, err = s.assignable(r); err != nil {
			s.log.ErrorContext(r.Context(), "load assignable", "error", err)
		}
	}

	s.render(w, r, "new", page{
		Title: "New",
		Kind:  kind, KindLabel: label(kind), KindChoice: choose,
		// Files what is made into the department New was clicked from.
		SpaceID: r.URL.Query().Get("space"),
		HasTask: docs.HasTaskFields(kind), People: people,
	})
}

func (s *Server) createDocument(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	kind := r.FormValue("kind")
	if kind == "" {
		kind = docs.KindDoc
	}

	p := docs.CreateParams{
		Kind:  kind,
		Title: strings.TrimSpace(r.FormValue("title")),
		Body:  r.FormValue("body"),
	}
	// Filed on creation when the caller already knows where it belongs (the
	// per-space "New daily report" button does), rather than always landing
	// "Not filed" and needing a second trip through the Space popover.
	if v := r.FormValue("space"); v != "" {
		p.SpaceID = &v
	}
	if docs.HasTaskFields(kind) {
		t := &docs.TaskFields{Status: docs.StatusTodo}
		// Overridable when the caller already knows the status -- the
		// per-group "Add" row on a grouped list creates directly into the
		// group it was typed from, rather than everything landing in Todo
		// and needing a second edit to drag it to the right one.
		if v := r.FormValue("status"); docs.IsValidStatus(v) {
			t.Status = v
		}
		var prio int64
		if _, err := fmt.Sscanf(r.FormValue("priority"), "%d", &prio); err == nil {
			t.Priority = int16(prio)
		}
		if v := strings.TrimSpace(r.FormValue("due")); v != "" {
			if due, err := time.Parse("2006-01-02", v); err == nil {
				t.DueAt = &due
			}
		}
		if v := r.FormValue("assignee"); v != "" {
			t.AssigneeID = &v
		}
		p.Task = t
	}

	d, err := s.docs.Create(r.Context(), id.OrgID, id.Subjects, p)
	switch {
	case errors.Is(err, docs.ErrReportExists):
		// One report a day. Open the one already filed rather than failing.
		if existing, e2 := s.docs.ReportOn(r.Context(), id.OrgID, id.PrincipalID, docs.ReportDay(time.Now())); e2 == nil && existing != "" {
			http.Redirect(w, r, "/documents/"+existing, http.StatusSeeOther)
			return
		}
		s.fail(w, r, http.StatusConflict, "you already filed a daily report today")
		return
	case errors.Is(err, docs.ErrKind):
		s.fail(w, r, http.StatusBadRequest, "unknown kind")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "create document", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not create")
		return
	}
	s.notify(id.OrgID, "documents")
	// Opens the new document by default -- what the "New" panel form wants,
	// since making one there is usually the start of filling it in. A
	// grouped list's inline per-group Add row wants the opposite: stay on
	// the list and see the new row appear in its group, not get pulled away
	// after typing one title. It sends its own back field for that.
	http.Redirect(w, r, backTo(r, "/documents/"+d.ID), http.StatusSeeOther)
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	s.showDocument(w, r, r.PathValue("id"), nil)
}

// refused is a form on a document that could not be accepted. The document is drawn again, still the page the person was
// on, with the reason shown and what they typed kept, rather than an error page at an address that is only a form's target.
type refused struct {
	Status  int
	Message string
	Time    *TimeForm
}

// TimeForm is what was typed into the log-time form, and why it was not taken.
type TimeForm struct {
	Error, Minutes, Hours, Mins, On, Note string
	Tags                                  []string
	// Fix is a correction of one entry that was refused, and Estimate an estimate that was: each shown beside its own field.
	Fix      *EntryFix
	Estimate *EstimateFix
}

// EntryFix is a correction to one time entry that was refused, with what was typed.
type EntryFix struct {
	ID, Error, Hours, Mins, On, Note string
}

// EstimateFix is an estimate that was refused, with what was typed.
type EstimateFix struct {
	Error, Hours, Mins string
}

func (s *Server) showDocument(w http.ResponseWriter, r *http.Request, docID string, refusal *refused) {
	id := auth.From(r.Context())
	d, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, docID)
	switch {
	case errors.Is(err, docs.ErrNotFound):
		// Not-found rather than forbidden, all the way to the surface. Telling
		// someone an id is real is itself a disclosure.
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "get document", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load")
		return
	}
	atts, err := s.docs.Attachments(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load attachments", "error", err)
	}
	removedAtts, err := s.docs.RemovedAttachments(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load removed attachments", "error", err)
	}
	// None of these are fatal to showing the page. A failure to load the link
	// list should not turn a readable work order into an error page.
	linked, err := s.docs.Linked(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load links", "error", err)
	}
	// The people already on this document stay in its picker, so what is
	// assigned never drops off its own page for an account whose lists are cut.
	onIt := []string{d.OwnerID}
	if assigned, _, aerr := s.docs.AssignedTo(r.Context(), d.ID); aerr == nil {
		onIt = append(onIt, assigned...)
	}
	people, err := s.assignable(r, onIt...)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load assignable", "error", err)
	}
	from, err := s.docs.ExternalRefFor(r.Context(), id.OrgID, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load external ref", "error", err)
	}

	// What this could be linked to. A work order concerns an asset, so that is
	// what is offered first; an asset offers its work orders back. A task and
	// a doc offer each other -- documentation attached to the work it
	// describes, which used to be impossible: every other kind fell through
	// to offering assets, including a task and a doc, so neither could ever
	// link to the other through this dropdown. wiki/note are unchanged for
	// now (asset, same as everything else not explicitly handled here) --
	// next in line for the same look, not decided yet.
	target := docs.KindAsset
	switch d.Kind {
	case docs.KindAsset:
		target = docs.KindWorkOrder
	case docs.KindTask:
		target = docs.KindDoc
	case docs.KindDoc, docs.KindDailyReport:
		target = docs.KindTask
	}
	candidates, err := s.docs.List(r.Context(), id.OrgID, id.Subjects,
		docs.ListParams{Kind: target, Limit: 100})
	if err != nil {
		s.log.ErrorContext(r.Context(), "load link candidates", "error", err)
	}

	// Seeded lazily so an org created before custom fields existed still gets
	// them. The insert is a no-op after the first time.
	if d.Kind == docs.KindAsset {
		if err := s.docs.EnsureAssetProperties(r.Context(), id.OrgID); err != nil {
			s.log.ErrorContext(r.Context(), "seed asset properties", "error", err)
		}
	}
	props, err := s.docs.PropertiesFor(r.Context(), id.OrgID, d.Kind, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load properties", "error", err)
	}

	// The conversation about this thing, and the conversations that merely
	// pointed at it. None of these are fatal to showing the page: a failure to
	// load comments should not turn a readable work order into an error.
	comments, err := s.channels.Comments(r.Context(), id.OrgID, id.Subjects, d.Ref(), 200)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load comments", "error", err)
	}
	mentioned, err := s.channels.MentionedIn(r.Context(), id.OrgID, id.Subjects, d.Ref(), 25)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load mentions", "error", err)
	}
	myChannels, err := s.channels.List(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load channels", "error", err)
	}
	watching, err := s.inbox.IsWatching(r.Context(), id.PrincipalID, d.Ref())
	if err != nil {
		s.log.ErrorContext(r.Context(), "is watching", "error", err)
	}
	checklist, err := s.docs.Checklist(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load checklist", "error", err)
	}
	// The parts fitted to this job, and what they came to. Labour alone is
	// half the number: a repair that took two hours and a four hundred pound
	// bearing costs four hundred pounds more than the timesheet says.
	partsUsed, err := s.crm.MovementsFor(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load parts used", "error", err)
	}
	partsCents, err := s.crm.PartsCost(r.Context(), id.OrgID, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "parts cost", "error", err)
	}
	catalogue, err := s.crm.Parts(r.Context(), id.OrgID, "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "load parts", "error", err)
	}
	// Who is involved: the vendor who services it, the engineer who was called.
	// One row in entity_links, and until this it could only be read from the
	// other end.
	parties, err := s.crm.PartiesFor(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load parties", "error", err)
	}
	// The two ways work actually arrives, both on the job rather than in
	// somebody's own inbox or nowhere at all.
	thread, err := s.mail.Thread(r.Context(), id.OrgID, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load mail thread", "error", err)
	}
	callLog, err := s.calls.ForDocument(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load calls", "error", err)
	}
	people2, err := s.crm.Contacts(r.Context(), id.OrgID, id.Subjects, "", "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "load contacts", "error", err)
	}
	effort, err := s.time.EffortFor(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load effort", "error", err)
	}
	entries, err := s.time.Entries(r.Context(), id.OrgID, id.Subjects, d.ID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load time entries", "error", err)
	}
	tags, err := s.time.ListTags(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load tags", "error", err)
	}
	// An asset shows what has been spent on everything linked to it, which is
	// the number that turns a history into a replacement decision.
	var assetTime *timelog.AssetEffort
	var asset *plant.Asset
	var assetFields plant.Fields
	var assetPath, children, parentOptions []*plant.Asset
	var downtime []plant.Downtime
	var uptime *plant.Availability
	var schedules []*maint.Schedule
	var readings []maint.Reading
	var meterKeys []string
	var pmHistory []maint.Occurrence
	var pmTemplates []docs.Template
	if d.Kind == docs.KindAsset {
		if assetTime, err = s.time.ForAsset(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load asset effort", "error", err)
		}
		// The maintenance side of an asset: what is scheduled, what the meters
		// say, and what actually happened. None fatal, for the same reason the
		// links are not.
		if schedules, err = s.maint.ForAsset(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load schedules", "error", err)
		}
		if readings, err = s.maint.Readings(r.Context(), id.OrgID, id.Subjects, d.ID, 20); err != nil {
			s.log.ErrorContext(r.Context(), "load readings", "error", err)
		}
		if meterKeys, err = s.maint.MeterKeys(r.Context(), d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load meter keys", "error", err)
		}
		if pmHistory, err = s.maint.HistoryForAsset(r.Context(), id.OrgID, id.Subjects, d.ID, 25); err != nil {
			s.log.ErrorContext(r.Context(), "load maintenance history", "error", err)
		}
		if pmTemplates, err = s.docs.Templates(r.Context(), id.OrgID, id.Subjects); err != nil {
			s.log.ErrorContext(r.Context(), "load templates", "error", err)
		}
		// Where it sits in the plant, what hangs off it, and how much of the
		// last quarter it spent stopped.
		if asset, err = s.plant.Get(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load asset rollup", "error", err)
		}
		if assetFields, err = s.plant.FieldsFor(r.Context(), id.OrgID, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load asset fields", "error", err)
		}
		if assetPath, err = s.plant.Ancestors(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load asset ancestors", "error", err)
		}
		if children, err = s.plant.Children(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load asset children", "error", err)
		}
		if downtime, err = s.plant.DowntimeFor(r.Context(), id.OrgID, id.Subjects, d.ID, 25); err != nil {
			s.log.ErrorContext(r.Context(), "load downtime", "error", err)
		}
		// A quarter, which is the window a reliability argument is made over.
		if uptime, err = s.plant.AvailabilityFor(r.Context(), id.OrgID, id.Subjects, d.ID,
			time.Now().AddDate(0, -3, 0), time.Now()); err != nil {
			s.log.ErrorContext(r.Context(), "load availability", "error", err)
		}
		parentOptions = s.assetOptions(r, d.ID)
	}
	// Every document can be filed in a space, not only an asset.
	spaces, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load spaces", "error", err)
	}
	var spaceID string
	if d.SpaceID != nil {
		spaceID = *d.SpaceID
	}

	// The task as a folder: its subtasks, and what it sits inside. The parent
	// is shown only when the reader can open it.
	var subtasks []*docs.Document
	if docs.HasTaskFields(d.Kind) {
		if subtasks, err = s.docs.Children(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load subtasks", "error", err)
		}
	}
	var parentDoc *docs.Document
	if d.ParentID != nil {
		parentDoc, _ = s.docs.Get(r.Context(), id.OrgID, id.Subjects, *d.ParentID)
	}

	// Who the task is assigned to, and whether this reader may change it.
	var assigneeIDs []string
	var assigneePrimary string
	var canAssign bool
	var pageGuests []docs.Guest
	if docs.HasTaskFields(d.Kind) {
		if assigneeIDs, assigneePrimary, err = s.docs.AssignedTo(r.Context(), d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "load assignees", "error", err)
		}
		if canAssign, err = s.docs.MayAssign(r.Context(), id.OrgID, id.Subjects, d.ID); err != nil {
			s.log.ErrorContext(r.Context(), "may assign", "error", err)
		}
		if guests, gerr := s.docs.Guests(r.Context(), id.OrgID, d.ID); gerr != nil {
			s.log.ErrorContext(r.Context(), "load guests", "error", gerr)
		} else {
			pageGuests = guests
		}
	}

	if refusal != nil {
		// The same headers fail sets: the reason in a header for the offline replay, and the page data's content type for the
		// interface that asked for data.
		w.Header().Set("X-Hearth-Reason", headerSafe(refusal.Message))
		if wantsJSON(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Accept")
			w.Header().Set("X-Hearth-Page-Data", "1")
		} else {
			shellHeaders(w)
		}
		w.WriteHeader(refusal.Status)
	}
	// Cloud files linked to this document, and whether this person can add one: Files is on for a provider and
	// their connection covers it.
	var cloudLinks []connect.FileLink
	cloudOn := false
	if s.connect != nil {
		cloudLinks, _ = s.connect.LinksFor(r.Context(), id.OrgID, id.PrincipalID, d.ID)
		cloudOn = s.connect.CanBrowse(r.Context(), id.OrgID, id.PrincipalID)
	}
	s.render(w, r, "document", page{
		CloudOn: cloudOn, CloudLinks: cloudLinks,
		Title: d.Title, Doc: d, Attachments: atts, RemovedAttachments: removedAtts, Links: linked,
		Subtasks: subtasks, ParentDoc: parentDoc,
		CanAssign: canAssign, AssigneeIDs: assigneeIDs, AssigneePrimary: assigneePrimary, Guests: pageGuests,
		People: people, Assignable: candidates, From: from,
		Statuses: allStatuses, Properties: props,
		Comments: comments, MentionedIn: mentioned, Channels: myChannels,
		Watching: watching, Checklist: checklist, Progress: docs.Progress(checklist),
		Effort: effort, TimeEntries: entries, Tags: tags, AssetTime: assetTime,
		Flash:          closedFlash(r.URL.Query().Get("closed"), r.URL.Query().Get("moved")),
		JustRemoved:    r.URL.Query().Get("justRemoved"),
		CommentRemoved: r.URL.Query().Get("commentRemoved"),
		ChecklistOpen:  r.URL.Query().Get("checklist") == "open",
		Schedules:      schedules, Readings: readings, MeterKeys: meterKeys,
		PMHistory: pmHistory, Templates: pmTemplates,
		Parts: catalogue, PartsUsed: partsUsed, PartsCents: partsCents,
		Parties: parties, MailThread: thread, Calls: callLog,
		Contacts: people2, CallOutcomes: calls.Outcomes,
		Asset: asset, AssetFields: assetFields, AssetPath: assetPath,
		Assets: parentOptions, Children: children,
		Downtime: downtime, Uptime: uptime, Spaces: spaces,
		BodyHTML: s.renderBody(r.Context(), id.OrgID, id.Subjects, d),
		SpaceID:  spaceID,
		Panel:    r.URL.Query().Get("peek") == "1",
		Peek:     r.URL.Query().Get("peek") == "1",
		Message:  refusalMessage(refusal),
		TimeForm: refusalTime(refusal),
	})
}

func refusalMessage(r *refused) string {
	if r == nil {
		return ""
	}
	return r.Message
}

func refusalTime(r *refused) *TimeForm {
	if r == nil {
		return nil
	}
	return r.Time
}

// updateProperties saves the custom fields.
//
// Field ids come from the form as prop:<def id>, so a stale form naming a
// definition that has since been deleted is skipped rather than failing the
// whole save.
func (s *Server) updateProperties(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	values := make(map[string]string)
	for k, v := range r.Form {
		if defID, ok := strings.CutPrefix(k, "prop:"); ok && len(v) > 0 {
			values[defID] = v[0]
		}
	}
	if err := s.docs.SetProperties(r.Context(), id.OrgID, id.Subjects, docID, values); err != nil {
		s.log.ErrorContext(r.Context(), "set properties", "error", err)
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.notify(id.OrgID, "document")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// updateDocument saves an edit to the title and body.
//
// The version the editor was looking at travels with the form, so two people
// editing the same thing get a conflict rather than one of them quietly losing
// their work to whoever pressed save second.
func (s *Server) updateDocument(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	var version int64
	_, _ = fmt.Sscanf(r.FormValue("version"), "%d", &version)

	p := docs.UpdateParams{ExpectedVersion: version}
	if r.Form.Has("title") {
		v := strings.TrimSpace(r.FormValue("title"))
		p.Title = &v
	}
	if r.Form.Has("body") {
		v := r.FormValue("body")
		p.Body = &v
	}

	_, err := s.docs.Update(r.Context(), id.OrgID, id.Subjects, docID, p)
	switch {
	case errors.Is(err, docs.ErrConflict):
		s.fail(w, r, http.StatusConflict,
			"someone else saved while you had this open. Reload and try again.")
		return
	case errors.Is(err, docs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "update document", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// deleteDocument soft-deletes. Requires admin rather than edit, because
// removing something is not the same authority as changing it.
func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")

	// Read the kind first, so the redirect lands back on the list the document
	// came from rather than dumping the user at the root.
	var back string
	if d, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, docID); err == nil {
		back = pathFor(d.Kind)
	}

	err := s.docs.Delete(r.Context(), id.OrgID, id.Subjects, docID)
	switch {
	case errors.Is(err, docs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "delete refused", "doc", docID, "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot delete this")
		return
	}
	if back == "" {
		back = "/documents"
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) linkDocument(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	target := r.FormValue("target")
	if target == "" {
		http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
		return
	}
	err := s.docs.Link(r.Context(), id.OrgID, id.Subjects,
		core.Ref{Type: core.EntityDocument, ID: docID},
		core.Ref{Type: core.EntityDocument, ID: target}, docs.LinkConcerns)
	if err != nil {
		s.log.ErrorContext(r.Context(), "link", "error", err)
		s.fail(w, r, http.StatusForbidden, "could not link those two")
		return
	}
	s.notify(id.OrgID, "document")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) unlinkDocument(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	if err := s.docs.Unlink(r.Context(), id.OrgID, id.Subjects, docID, r.FormValue("target")); err != nil {
		s.log.ErrorContext(r.Context(), "unlink", "error", err)
		s.fail(w, r, http.StatusForbidden, "could not remove that link")
		return
	}
	s.notify(id.OrgID, "document")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) getImport(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "import", page{Title: "Import", ImportSources: importSources()})
}

// postImportClickUp takes an export straight from the browser.
//
// The file is streamed to the parser rather than buffered whole where it can be
// avoided, and the parser bounds what it will read, so a hostile upload cannot
// turn into memory pressure.
func (s *Server) postImportClickUp(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseMultipartForm(maxUploadForm); err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the upload")
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		s.render(w, r, "import", page{Title: "Import",
			ImportSources: importSources(),
			Message:       "Choose an export file first."})
		return
	}
	defer f.Close()

	kind := r.FormValue("kind")
	if kind == "" {
		kind = docs.KindTask
	}

	// ClickUp keeps its own parser, because its JSON export has quirks the
	// generic reader does not need to know about. Everything else goes through
	// one engine, so the fifth importer cannot have a bug the first four were
	// fixed for.
	source := strings.TrimSpace(r.FormValue("source"))
	if source == "" || source == clickup.Source {
		res, err := clickup.Run(r.Context(), importStore{Store: s.docs, plant: s.plant, auth: s.auth, time: s.time,
			chat: s.channels, notify: s.inbox}, id.OrgID, id.Subjects, f,
			clickup.Options{Kind: kind})
		if err != nil {
			s.log.WarnContext(r.Context(), "import failed",
				"source", clickup.Source, "file", header.Filename, "error", err)
			s.render(w, r, "import", page{Title: "Import", Message: err.Error(),
				ImportSources: importSources()})
			return
		}
		s.log.InfoContext(r.Context(), "import",
			"source", clickup.Source, "file", header.Filename,
			"created", res.Created, "updated", res.Updated, "failed", res.Failed)
		s.notify(id.OrgID, "documents")
		s.render(w, r, "import", page{Title: "Import", Import: res,
			ImportSources: importSources()})
		return
	}

	tasks, err := importer.Parse(source, f)
	if err != nil {
		s.log.WarnContext(r.Context(), "import failed",
			"source", source, "file", header.Filename, "error", err)
		s.render(w, r, "import", page{Title: "Import", Message: err.Error(),
			ImportSources: importSources()})
		return
	}
	out, err := importer.Run(r.Context(), s.docs, id.OrgID, id.Subjects, source, tasks,
		importer.Options{Kind: kind})
	if err != nil {
		s.render(w, r, "import", page{Title: "Import", Message: err.Error(),
			ImportSources: importSources()})
		return
	}
	s.log.InfoContext(r.Context(), "import",
		"source", source, "file", header.Filename,
		"created", out.Created, "updated", out.Updated, "failed", out.Failed)
	s.notify(id.OrgID, "documents")
	// One result shape on the page, so the template does not branch on which
	// importer ran.
	s.render(w, r, "import", page{Title: "Import", ImportSources: importSources(),
		Import: &clickup.Result{
			Created: out.Created, Updated: out.Updated, Failed: out.Failed,
			Total: out.Total, Kind: out.Kind, Problems: out.Problems,
		}})
}

// importSources is every source the form offers, ClickUp first because it is
// the one that already existed.
func importSources() []string {
	return append([]string{clickup.Source}, importer.Sources...)
}

// updateTask applies whichever task fields the form actually carried.
//
// Only the fields present are sent. A form that posts just a status must not
// clear the assignee and the due date, which is exactly what sending a whole
// TaskFields used to do.
func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	var version int64
	_, _ = fmt.Sscanf(r.FormValue("version"), "%d", &version)

	patch := &docs.TaskPatch{}
	if r.Form.Has("status") {
		v := r.FormValue("status")
		patch.Status = &v
	}
	if r.Form.Has("priority") {
		var p int64
		if _, err := fmt.Sscanf(r.FormValue("priority"), "%d", &p); err == nil {
			v := int16(p)
			patch.Priority = &v
		}
	}
	if r.Form.Has("assignee") {
		if v := r.FormValue("assignee"); v == "" {
			patch.ClearAssignee = true
		} else {
			patch.AssigneeID = &v
		}
	}
	if r.Form.Has("due") {
		if v := strings.TrimSpace(r.FormValue("due")); v == "" {
			patch.ClearDue = true
		} else if t, err := time.Parse("2006-01-02", v); err == nil {
			patch.DueAt = &t
		}
	}

	ctx, closed := docs.WithClosedSink(r.Context())
	_, err := s.docs.Update(ctx, id.OrgID, id.Subjects, docID, docs.UpdateParams{
		Task:            patch,
		ExpectedVersion: version,
	})
	switch {
	case errors.Is(err, docs.ErrConflict):
		// A real answer, not a failure. Somebody else saved while this form was
		// open, and silently overwriting them is the behaviour being avoided.
		s.fail(w, r, http.StatusConflict,
			"someone else updated this while you had it open. Reload and try again.")
		return
	case errors.Is(err, docs.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "update status", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not update")
		return
	}
	s.notify(id.OrgID, "documents")
	// Closing a task closed what was inside it; say so on the page it lands on.
	to := "/documents/" + docID
	if n := len(closed.IDs); n > 0 {
		to += "?closed=" + strconv.Itoa(n) + "&moved=" + url.QueryEscape(closed.Status)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// closedFlash is the line a task page shows after a status change closed
// subtasks with it. The count comes back in the address, and anything that is
// not a small positive number shows nothing.
func closedFlash(n, status string) string {
	c, err := strconv.Atoi(n)
	if err != nil || c < 1 || c > 100000 {
		return ""
	}
	return CascadeMessage(c, status)
}

// ClosedMessage is how a close that took subtasks with it reads.
func ClosedMessage(n int) string {
	if n == 1 {
		return "Closed with 1 subtask"
	}
	return "Closed with " + strconv.Itoa(n) + " subtasks"
}

// CascadeMessage is how a status change that moved subtasks with it reads. A
// close to done keeps the first version's words; any other status says where
// they went.
func CascadeMessage(n int, status string) string {
	if status == "" || status == docs.StatusDone {
		return ClosedMessage(n)
	}
	word := " subtasks"
	if n == 1 {
		word = " subtask"
	}
	return "Moved " + strconv.Itoa(n) + word + " to " + docs.StatusLabel(status)
}

func heading(kind string) string {
	switch kind {
	case docs.KindWorkOrder:
		return "Work orders"
	case docs.KindAsset:
		return "Assets"
	case docs.KindTask:
		return "Tasks"
	case "":
		return "Everything"
	default:
		return label(kind)
	}
}

func label(kind string) string {
	return strings.ToUpper(kind[:1]) + strings.ReplaceAll(kind[1:], "_", " ")
}

func humanAgo(v any) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return "never"
		}
		t = *x
	default:
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// assetVersion is the cache-busting segment: the build revision, or a
// timestamp when running from a dirty tree so local edits are never cached.
func assetVersion() string {
	b := buildinfo.Get()
	if b.Dirty || b.Revision == "" || b.Revision == "unknown" {
		return fmt.Sprintf("dev-%d", startedAt.Unix())
	}
	return b.Revision[:12]
}

var startedAt = time.Now()

// immutable marks assets as never changing, which is safe precisely because the
// URL carries the revision. A year is the practical maximum browsers honour.
func immutable(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}

// used by core so the import is not unused in some build configurations
var _ = core.AccessView

// maxUploadForm bounds what is buffered in memory while parsing a multipart
// form. Anything larger streams to a temporary file rather than the heap.
const maxUploadForm = 16 << 20

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")

	if err := r.ParseMultipartForm(maxUploadForm); err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the upload")
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "no file was sent")
		return
	}
	defer f.Close()

	// Authorise BEFORE storing anything. Uploading first and checking after
	// would let anyone with the URL write bytes into the object store, whether
	// or not the attachment ever succeeds.
	if err := s.docs.CanAttach(r.Context(), id.Subjects, docID); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	}

	uploaded, err := s.files.Upload(r.Context(), id.OrgID, id.PrincipalID,
		header.Filename, header.Header.Get("Content-Type"), header.Size, f)
	if err != nil {
		s.log.ErrorContext(r.Context(), "upload", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not store the file")
		return
	}
	if err := s.docs.Attach(r.Context(), id.OrgID, id.Subjects, docID, uploaded.ID); err != nil {
		s.log.ErrorContext(r.Context(), "attach", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "stored the file but could not attach it")
		return
	}
	s.notify(id.OrgID, "document")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID, fileID := r.PathValue("id"), r.PathValue("fid")

	// Access is resolved through the PARENT document. A file id on its own
	// grants nothing, so a link cannot outlive the share it came from.
	if err := s.docs.CanReadAttachment(r.Context(), id.OrgID, id.Subjects, docID, fileID); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such file")
		return
	}

	meta, rc, err := s.files.Open(r.Context(), id.OrgID, fileID)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such file")
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", meta.Filename))
	// The digest travels with the bytes so a caller can verify independently.
	w.Header().Set("X-Content-SHA256", meta.Digest())

	// Streamed through a reader that verifies the digest at EOF. If the stored
	// object has been corrupted, the copy fails partway rather than serving
	// bytes that quietly are not what was uploaded.
	if _, err := io.Copy(w, rc); err != nil {
		s.log.ErrorContext(r.Context(), "download failed mid-stream",
			"file", fileID, "error", err)
	}
}

// events is the live stream.
//
// Topics are decided HERE, from the caller's identity, which is where
// authorisation belongs. A subscriber can only ever be handed topics it was
// granted at connect time, so there is no way to ask for someone else's.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	topics := []realtime.Topic{realtime.OrgTopic(id.OrgID)}

	// Every channel this person is in, computed from their subject set rather
	// than from anything the browser sent. The subject set already IS the list
	// of channels they belong to, which is why a subscriber cannot ask for
	// somebody else's conversation: there is no parameter to ask with.
	for _, sub := range id.Subjects {
		if sub.Kind == core.SubjectChannel {
			topics = append(topics, realtime.ChannelTopic(sub.ID))
		}
	}
	// And the document open in this tab, if it said which. Checked before it
	// is trusted, so a made-up id subscribes to nothing.
	if docID := r.URL.Query().Get("doc"); docID != "" {
		if err := s.docs.CanAttach(r.Context(), id.Subjects, docID); err == nil {
			topics = append(topics, realtime.DocTopic(docID))
		} else if _, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, docID); err == nil {
			topics = append(topics, realtime.DocTopic(docID))
		}
	}
	s.hub.Handler(topics)(w, r)
}

// notify tells everyone in the org that something changed. Fire and forget: a
// failed notification must never fail the write that caused it.
func (s *Server) notify(orgID, event string) {
	s.hub.Publish(realtime.OrgTopic(orgID), realtime.Event{Name: event, Data: "changed"})
}

func (s *Server) startSSO(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		s.fail(w, r, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	s.oidc.Start(w, r)
}

func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		s.fail(w, r, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	if err := s.oidc.Callback(r.Context(), w, r); err != nil {
		// Specific in the log, vague on the page. Telling a stranger which
		// domains are permitted, or that their address exists, is a disclosure
		// with no upside.
		s.log.WarnContext(r.Context(), "sso sign-in failed", "error", err)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, "login", page{Title: "Sign in",
			Message: "Single sign-in did not complete. Try again, or use a local account."})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// departmentPromptOwed is whether somebody who is in no department is asked
// which they are in: not while a request of theirs is waiting, and never a
// contractor account, which is in no department on purpose.
func departmentPromptOwed(contractor bool, last *deptreq.Request) bool {
	if contractor {
		return false
	}
	return last == nil || last.Status != "pending"
}
