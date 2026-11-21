package web

// The Microsoft 365 and Google Workspace connectors, on screen.
//
// One page, Settings > Connectors. Everybody sees their own connections on it: connect, sync now, which calendars,
// a chat webhook, disconnect. An owner or super admin also sees the controls that decide what is allowed at all:
// the tenant or domain, the switch for each connector, and everybody's status.
//
// Every handler that acts on a connection works on the caller's own principal, taken from the session, never from
// the request. The one exception is an administrator disconnecting somebody, which names a person and is checked
// as an administrator first.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/connect"
)

// ConnectorsView is the whole Connectors page.
type ConnectorsView struct {
	Admin     bool
	Notice    string
	Providers []ProviderView
}

// ProviderView is one provider on the page.
type ProviderView struct {
	Key        string
	Label      string
	Configured bool
	Tenant     string
	TenantHelp string
	Connectors []ConnectorView
	// Mine is the signed-in person's own connection, nil when they have none.
	Mine      *MineView
	Calendars []connect.CalendarChoice
	Spaces    []connect.Space
	ChatHook  *connect.ChatHook
	// People is everybody's connection, for an administrator.
	People []PersonConnView
}

// ConnectorView is one switch.
type ConnectorView struct {
	Key         string
	Label       string
	Describes   string
	Enabled     bool
	MeetLinks   bool
	Connections int
	Failing     int
	// Covered is whether the signed-in person's grant already covers it.
	Covered bool
	LastOK  *time.Time
}

// MineView is the signed-in person's connection.
type MineView struct {
	Email     string
	Live      bool
	LastError string
	Since     time.Time
}

// PersonConnView is one person's connection and what each connector last did for them.
type PersonConnView struct {
	PrincipalID string
	Name        string
	Email       string
	Revoked     bool
	LastError   string
	Statuses    []StatusView
}

// StatusView is one connector's last run for one person.
type StatusView struct {
	Label  string
	OK     *time.Time
	Error  string
	Items  int
	Failed bool
}

var connectorText = map[connect.Kind]string{
	connect.Calendar: "Two-way. Events sync both ways for a week back and ninety days ahead. Where both sides changed an event, the provider's version wins.",
	connect.Contacts: "Two-way. Contacts sync between the person's address book and the CRM, and new, edited and deleted ones flow in both directions. What comes in is the importer's own in the CRM until it is shared.",
	connect.Mail:     "The person's whole mailbox, read and write, in Hearth: open any message with its body, send, reply, forward, archive, label and delete. Messages that name a CRM contact also show on the contact.",
	connect.Files:    "Open, upload, replace, rename, move and delete files and folders in OneDrive, SharePoint and Google Drive from Hearth, link or import them into documents, and save a document's attachment out to a drive.",
	connect.Chat:     "Copies a person's Hearth notifications into a Google Chat space or a Teams chat they choose, through a webhook they supply. It asks the provider for no permissions.",
}

var tenantHelp = map[connect.Provider]string{
	connect.Microsoft: "The Microsoft Entra directory (tenant) ID, from the Entra admin center under Overview.",
	connect.Google:    "The Google Workspace primary domain, like example.com.",
}

func (s *Server) connectorsOn() bool { return s.connect != nil }

func (s *Server) connectorsBack(w http.ResponseWriter, r *http.Request, msg string) {
	to := "/settings/connectors"
	if msg != "" {
		to += "?notice=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) getConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the Microsoft 365 and Google Workspace connectors are not set up on this installation")
		return
	}
	ctx := r.Context()
	id := auth.From(ctx)
	admin, err := s.auth.IsAdmin(ctx, id.OrgID, id.PrincipalID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not check who may administer this")
		return
	}
	settings, err := s.connect.Settings(ctx, id.OrgID)
	if err != nil {
		s.log.ErrorContext(ctx, "connector settings", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the connectors")
		return
	}
	statuses, _ := s.connect.Statuses(ctx, id.OrgID)
	byPerson := map[string][]connect.Status{}
	for _, st := range statuses {
		byPerson[string(st.Provider)+"/"+st.PrincipalID] = append(byPerson[string(st.Provider)+"/"+st.PrincipalID], st)
	}

	view := &ConnectorsView{Admin: admin, Notice: r.URL.Query().Get("notice")}
	for _, p := range connect.Providers {
		pv := ProviderView{Key: string(p), Label: p.Label(), TenantHelp: tenantHelp[p]}
		b := s.connect.Backend(p)
		pv.Configured = b != nil
		pv.Tenant, _ = s.connect.TenantFor(ctx, id.OrgID, p)

		var conns []connect.Conn
		var granted []string
		if b != nil {
			conns, _ = b.Connections(ctx, id.OrgID)
			granted, _ = b.Granted(ctx, id.PrincipalID)
		}
		for _, k := range connect.Kinds {
			st := settings[p][k]
			cv := ConnectorView{Key: string(k), Label: k.Label(), Describes: connectorText[k], Enabled: st.Enabled, MeetLinks: st.MeetLinks}
			if b != nil {
				cv.Covered = k == connect.Chat || connect.HasScopes(granted, b.Scopes(k))
			}
			for _, c := range conns {
				if !c.Revoked && (k == connect.Chat || b != nil && connect.HasScopes(scopesOrNil(ctx, b, c), b.Scopes(k))) {
					cv.Connections++
				}
			}
			for _, ss := range statuses {
				if ss.Provider == p && ss.Kind == k {
					if ss.LastError != "" {
						cv.Failing++
					}
					if ss.LastOKAt != nil && (cv.LastOK == nil || ss.LastOKAt.After(*cv.LastOK)) {
						cv.LastOK = ss.LastOKAt
					}
				}
			}
			pv.Connectors = append(pv.Connectors, cv)
		}
		for _, c := range conns {
			if c.PrincipalID == id.PrincipalID {
				pv.Mine = &MineView{Email: c.Email, Live: !c.Revoked, LastError: c.LastError, Since: c.ConnectedAt}
			}
			if admin {
				pc := PersonConnView{PrincipalID: c.PrincipalID, Name: c.Name, Email: c.Email, Revoked: c.Revoked, LastError: c.LastError}
				for _, ss := range byPerson[string(p)+"/"+c.PrincipalID] {
					pc.Statuses = append(pc.Statuses, StatusView{Label: ss.Kind.Label(), OK: ss.LastOKAt, Error: ss.LastError,
						Items: ss.Items, Failed: ss.LastError != ""})
				}
				pv.People = append(pv.People, pc)
			}
		}
		if pv.Mine != nil && pv.Mine.Live && settings[p][connect.Calendar].Enabled {
			if cals, err := s.connect.Calendars(ctx, id.OrgID, id.PrincipalID, p); err == nil {
				pv.Calendars = cals
				pv.Spaces, _ = s.connect.ManagedSpaces(ctx, id.OrgID, id.PrincipalID)
			}
		}
		if settings[p][connect.Chat].Enabled {
			pv.ChatHook, _ = s.connect.ChatHookFor(ctx, id.PrincipalID)
			if pv.ChatHook != nil && pv.ChatHook.Provider != p {
				pv.ChatHook = nil
			}
		}
		view.Providers = append(view.Providers, pv)
	}
	s.render(w, r, "connectors", page{Title: "Connectors", Connectors: view})
}

// scopesOrNil is the scopes of somebody else's connection, for the count of who is covered.
func scopesOrNil(_ context.Context, _ connect.Backend, c connect.Conn) []string { return c.Scopes }

// formProvider reads and checks the provider field.
func (s *Server) formProvider(w http.ResponseWriter, r *http.Request) (connect.Provider, bool) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return "", false
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return "", false
	}
	p, err := connect.ParseProvider(r.FormValue("provider"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "choose Microsoft 365 or Google Workspace")
		return "", false
	}
	return p, true
}

// postConnectorTenant connects a tenant or domain. Administrators only.
func (s *Server) postConnectorTenant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok || !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.connect.ConnectTenant(r.Context(), id.OrgID, id.PrincipalID, p, r.FormValue("tenant")); err != nil {
		s.connectorsBack(w, r, err.Error())
		return
	}
	s.connectorsBack(w, r, p.Label()+" is connected to that "+map[connect.Provider]string{connect.Microsoft: "tenant", connect.Google: "domain"}[p]+".")
}

// postConnectorToggle switches one connector. Administrators only.
func (s *Server) postConnectorToggle(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok || !s.requireAdmin(w, r) {
		return
	}
	k, err := connect.ParseKind(r.FormValue("connector"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "unknown connector")
		return
	}
	id := auth.From(r.Context())
	on := r.FormValue("enabled") == "on"
	if err := s.connect.SetEnabled(r.Context(), id.OrgID, id.PrincipalID, p, k, on, r.FormValue("meet") == "on"); err != nil {
		s.connectorsBack(w, r, err.Error())
		return
	}
	state := "off"
	if on {
		state = "on"
	}
	s.connectorsBack(w, r, p.Label()+" "+strings.ToLower(k.Label())+" is "+state+".")
}

// postConnectorRemovePerson disconnects somebody else's connection. Administrators only.
func (s *Server) postConnectorRemovePerson(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok || !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	who := r.FormValue("person")
	// The person has to be in this organisation: the list of connections is the organisation's own.
	var found bool
	if b := s.connect.Backend(p); b != nil {
		cs, _ := b.Connections(r.Context(), id.OrgID)
		for _, c := range cs {
			found = found || c.PrincipalID == who
		}
	}
	if !found {
		s.fail(w, r, http.StatusNotFound, "no such connection")
		return
	}
	if err := s.connect.Disconnect(r.Context(), p, who); err != nil {
		s.log.ErrorContext(r.Context(), "disconnect person", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not disconnect")
		return
	}
	s.connectorsBack(w, r, "Disconnected.")
}

// postConnectorDisconnect ends the caller's own connection.
func (s *Server) postConnectorDisconnect(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if err := s.connect.Disconnect(r.Context(), p, id.PrincipalID); err != nil {
		s.log.ErrorContext(r.Context(), "disconnect", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not disconnect")
		return
	}
	s.connectorsBack(w, r, "Disconnected from "+p.Label()+". Hearth forgot the connection; events and contacts it brought in stay.")
}

// postConnectorSync runs the caller's connectors now.
func (s *Server) postConnectorSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res := s.connect.SyncPerson(ctx, id.OrgID, id.PrincipalID, p)
	var failed []string
	for k, err := range res {
		if err != nil {
			failed = append(failed, k.Label()+": "+err.Error())
		}
	}
	sort.Strings(failed)
	if len(failed) > 0 {
		s.connectorsBack(w, r, strings.Join(failed, "; "))
		return
	}
	s.connectorsBack(w, r, "Synced.")
}

// postConnectorCalendar chooses an extra calendar to sync, or stops one.
func (s *Server) postConnectorCalendar(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	cal := r.FormValue("calendar")
	var err error
	if r.FormValue("action") == "stop" {
		err = s.connect.StopSyncingCalendar(r.Context(), id.PrincipalID, p, cal)
	} else {
		err = s.connect.SyncCalendarInto(r.Context(), id.OrgID, id.PrincipalID, p, cal, r.FormValue("name"), r.FormValue("space"))
	}
	if err != nil {
		s.connectorsBack(w, r, strings.TrimPrefix(err.Error(), "connect: "))
		return
	}
	s.connectorsBack(w, r, "Saved. The next sync brings the calendar in.")
}

// postConnectorChat saves or clears the caller's chat webhook.
func (s *Server) postConnectorChat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.formProvider(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	if r.FormValue("action") == "clear" {
		if err := s.connect.ClearChatHook(r.Context(), id.PrincipalID); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "could not remove it")
			return
		}
		s.connectorsBack(w, r, "Chat notifications are off.")
		return
	}
	if err := s.connect.SetChatHook(r.Context(), s.box, id.OrgID, id.PrincipalID, p, r.FormValue("webhook")); err != nil {
		s.connectorsBack(w, r, strings.TrimPrefix(err.Error(), "connect: "))
		return
	}
	s.connectorsBack(w, r, "Saved. Your notifications are copied there from now on.")
}

// -----------------------------------------------------------------------------
// Connecting an account
// -----------------------------------------------------------------------------

const msStateCookie = "hearth_ms_state"

func randomState() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// kindScopes is the scopes for the enabled connectors the person ticked.
func (s *Server) kindScopes(r *http.Request, p connect.Provider, kinds []string) ([]string, error) {
	b := s.connect.Backend(p)
	if b == nil {
		return nil, errors.New("not set up")
	}
	id := auth.From(r.Context())
	var scopes []string
	for _, ks := range kinds {
		k, err := connect.ParseKind(ks)
		if err != nil {
			return nil, err
		}
		on, err := s.connect.Enabled(r.Context(), id.OrgID, p, k)
		if err != nil {
			return nil, err
		}
		if on {
			scopes = append(scopes, b.Scopes(k)...)
		}
	}
	return scopes, nil
}

// startMicrosoft sends the caller to their tenant's own sign-in.
func (s *Server) startMicrosoft(w http.ResponseWriter, r *http.Request) {
	if !s.connectorsOn() || s.microsoft == nil {
		s.fail(w, r, http.StatusNotFound, "Microsoft 365 is not set up on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	id := auth.From(r.Context())
	tenant, err := s.connect.TenantFor(r.Context(), id.OrgID, connect.Microsoft)
	if err != nil || tenant == "" {
		s.connectorsBack(w, r, "An administrator has to connect the Microsoft 365 tenant first.")
		return
	}
	scopes, err := s.kindScopes(r, connect.Microsoft, r.Form["connector"])
	if err != nil || len(scopes) == 0 {
		s.connectorsBack(w, r, "Choose at least one thing an administrator has turned on.")
		return
	}
	state, err := randomState()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not start that")
		return
	}
	verifier := oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{
		Name: msStateCookie, Value: state + "." + verifier, Path: "/", HttpOnly: true,
		Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(10 * time.Minute),
	})
	if r.FormValue("admin") == "on" {
		// A tenant administrator granting the permissions for everybody. There is no code to exchange on return.
		if ok, _ := s.auth.IsAdmin(r.Context(), id.OrgID, id.PrincipalID); !ok {
			s.fail(w, r, http.StatusForbidden, "only the owner or a super admin can grant for the whole tenant")
			return
		}
		http.Redirect(w, r, s.microsoft.AdminConsentURL(tenant, state, scopes), http.StatusFound)
		return
	}
	http.Redirect(w, r, s.microsoft.AuthURL(tenant, state, verifier, scopes), http.StatusFound)
}

// microsoftCallback finishes the connection.
func (s *Server) microsoftCallback(w http.ResponseWriter, r *http.Request) {
	if !s.connectorsOn() || s.microsoft == nil {
		s.fail(w, r, http.StatusNotFound, "Microsoft 365 is not set up on this installation")
		return
	}
	id := auth.From(r.Context())
	c, err := r.Cookie(msStateCookie)
	if err != nil || c.Value == "" {
		s.fail(w, r, http.StatusBadRequest, "no connection was in progress")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: msStateCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})
	state, verifier, _ := strings.Cut(c.Value, ".")
	q := r.URL.Query()
	if q.Get("state") != state || state == "" {
		s.fail(w, r, http.StatusBadRequest, "that connection did not start here")
		return
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" || e == "consent_required" {
			s.connectorsBack(w, r, "Microsoft did not grant it: "+q.Get("error_description"))
			return
		}
		s.fail(w, r, http.StatusBadRequest, "Microsoft refused that: "+e)
		return
	}
	if q.Get("admin_consent") != "" {
		s.connectorsBack(w, r, "The permissions are granted for the tenant. Everybody can connect now.")
		return
	}
	tenant, err := s.connect.TenantFor(r.Context(), id.OrgID, connect.Microsoft)
	if err != nil || tenant == "" {
		s.connectorsBack(w, r, "An administrator has to connect the Microsoft 365 tenant first.")
		return
	}
	if err := s.microsoft.Connect(r.Context(), id.OrgID, id.PrincipalID, tenant, q.Get("code"), verifier); err != nil {
		if errors.Is(err, connect.ErrWrongTenant) {
			s.connectorsBack(w, r, "That account is not in the connected Microsoft 365 tenant.")
			return
		}
		s.log.WarnContext(r.Context(), "microsoft connect", "error", err)
		s.connectorsBack(w, r, strings.TrimPrefix(err.Error(), "m365: "))
		return
	}
	s.firstSync(r, id.OrgID, id.PrincipalID, connect.Microsoft)
	s.connectorsBack(w, r, "Connected to Microsoft 365.")
}

// firstSync runs a new connection's connectors once, in the background, so the person sees something.
func (s *Server) firstSync(r *http.Request, org, principal string, p connect.Provider) {
	if s.connect == nil {
		return
	}
	bg := context.WithoutCancel(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(bg, 3*time.Minute)
		defer cancel()
		s.connect.SyncPerson(ctx, org, principal, p)
	}()
}

// googleDomain is the Workspace domain an administrator connected, "" when none.
func (s *Server) googleDomain(ctx context.Context, org string) string {
	if s.connect == nil {
		return ""
	}
	d, _ := s.connect.TenantFor(ctx, org, connect.Google)
	return d
}
