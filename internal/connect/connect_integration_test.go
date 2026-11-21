package connect

// The connectors against a fake provider and a real Postgres.
//
// The provider side is a fake so the cases that cannot be produced on demand at Microsoft or Google can be driven:
// both sides changing the same event, a delete on either side, a grant that lacks a scope. What a fake cannot prove
// is that the real services answer as the adapters expect. That is checked at the adapters, against recorded
// responses and published shapes (internal/m365, internal/google), and last by connecting for real.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/files"
	"github.com/Embernet-ai/hearth/internal/platform/blob"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

// ---- the fake provider ----------------------------------------------------

type fakeProvider struct {
	mu       sync.Mutex
	name     Provider
	scopes   []string
	events   map[string]map[string]*Event // calendar -> id -> event
	calendar []CalendarInfo
	contacts []Contact
	messages []Message
	drives   []Drive
	items    map[string][]FileItem
	body     map[string]string
	next     int
	updates  int
	creates  int
	meeting  bool
	people   []Conn
	revoked  bool
}

func newFake(p Provider) *fakeProvider {
	return &fakeProvider{
		name: p, events: map[string]map[string]*Event{"primary": {}},
		calendar: []CalendarInfo{{ID: "primary", Name: "Calendar", Primary: true, Writable: true}},
		items:    map[string][]FileItem{}, body: map[string]string{},
		scopes: []string{"cal", "contacts", "mail", "files"},
	}
}

func (f *fakeProvider) Provider() Provider { return f.name }
func (f *fakeProvider) Configured() bool   { return true }
func (f *fakeProvider) Scopes(k Kind) []string {
	switch k {
	case Calendar:
		return []string{"cal"}
	case Contacts:
		return []string{"contacts"}
	case Mail:
		return []string{"mail"}
	case Files:
		return []string{"files"}
	}
	return nil
}
func (f *fakeProvider) Session(context.Context, string) (Session, error) {
	if f.revoked {
		return nil, ErrReconnect
	}
	return f, nil
}
func (f *fakeProvider) Granted(context.Context, string) ([]string, error) {
	if f.revoked {
		return nil, ErrReconnect
	}
	return f.scopes, nil
}
func (f *fakeProvider) Connections(_ context.Context, org string) ([]Conn, error) {
	var out []Conn
	for _, c := range f.people {
		if c.OrgID == org {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeProvider) Disconnect(_ context.Context, id string) error {
	for i, c := range f.people {
		if c.PrincipalID == id {
			f.people = append(f.people[:i], f.people[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeProvider) Calendar() CalendarAPI { return f }
func (f *fakeProvider) Contacts() ContactsAPI { return fakeContacts{f} }
func (f *fakeProvider) Mail() MailAPI         { return f }
func (f *fakeProvider) Files() FilesAPI       { return f }

type fakeContacts struct{ f *fakeProvider }

func (c fakeContacts) Contacts(context.Context) ([]Contact, error) { return c.f.contacts, nil }

func (f *fakeProvider) Calendars(context.Context) ([]CalendarInfo, error) { return f.calendar, nil }
func (f *fakeProvider) Events(_ context.Context, cal string, from, to time.Time) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Event
	for _, e := range f.events[cal] {
		if !e.Start.Before(from) && e.Start.Before(to) {
			out = append(out, *e)
		}
	}
	return out, nil
}
func (f *fakeProvider) put(cal string, e Event) Event {
	if f.events[cal] == nil {
		f.events[cal] = map[string]*Event{}
	}
	f.next++
	if e.ID == "" {
		e.ID = fmt.Sprintf("ev%d", f.next)
	}
	e.Version = fmt.Sprintf("v%d", f.next)
	f.events[cal][e.ID] = &e
	return e
}
func (f *fakeProvider) Create(_ context.Context, cal string, e Event, meeting bool) (Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.meeting = meeting
	return f.put(cal, e), nil
}
func (f *fakeProvider) Update(_ context.Context, cal string, e Event) (Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	return f.put(cal, e), nil
}
func (f *fakeProvider) Delete(_ context.Context, cal, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.events[cal], id)
	return nil
}
func (f *fakeProvider) Messages(_ context.Context, since time.Time, limit int) ([]Message, error) {
	var out []Message
	for _, m := range f.messages {
		if !m.Received.Before(since) {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeProvider) Drives(context.Context) ([]Drive, error) { return f.drives, nil }
func (f *fakeProvider) List(_ context.Context, drive, folder string) ([]FileItem, error) {
	return f.items[drive+"/"+folder], nil
}
func (f *fakeProvider) Item(_ context.Context, drive, id string) (FileItem, error) {
	for _, list := range f.items {
		for _, it := range list {
			if it.ID == id {
				return it, nil
			}
		}
	}
	return FileItem{}, ErrNotFound
}
func (f *fakeProvider) Open(ctx context.Context, drive, id string, max int64) (FileItem, io.ReadCloser, error) {
	it, err := f.Item(ctx, drive, id)
	if err != nil {
		return it, nil, err
	}
	return it, io.NopCloser(strings.NewReader(f.body[id])), nil
}

// ---- the fixture ----------------------------------------------------------

type fx struct {
	t     *testing.T
	svc   *Service
	db    *pgxpool.Pool
	fake  *fakeProvider
	org   string
	dana  string
	omar  string
	az    *authz.Service
	crm   *crm.Service
	docs  *docs.Service
	clock time.Time
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_connect")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)

	f := &fx{t: t, db: pools.Web, org: uuid.NewString(), dana: uuid.NewString(), omar: uuid.NewString(),
		fake: newFake(Google), clock: time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Connect Test')`, f.org, "c-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`, f.dana, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Omar')`, f.omar, f.org)

	var store *blob.Store
	if ep := os.Getenv("TEST_MINIO_ENDPOINT"); ep != "" {
		store, err = blob.Open(ctx, blob.Config{Endpoint: ep, PublicEndpoint: ep,
			AccessKey: envOr("TEST_MINIO_ACCESS_KEY", "hearth"), SecretKey: envOr("TEST_MINIO_SECRET_KEY", "hearthsecret"),
			Bucket: "hearth-test"})
		if err != nil {
			t.Fatalf("minio: %v", err)
		}
	}
	f.az = authz.New(pools.Web)
	f.crm = crm.New(pools.Web, f.az)
	f.docs = docs.New(pools.Web, f.az)
	f.svc = New(Deps{DB: pools.Web, Authz: f.az, CRM: f.crm, Docs: f.docs, Files: files.New(pools.Web, store),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, f.fake)
	f.svc.now = func() time.Time { return f.clock }

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM connector_mail WHERE org_id=$1`, `DELETE FROM connector_file_links WHERE org_id=$1`,
			`DELETE FROM connector_links WHERE org_id=$1`, `DELETE FROM connector_status WHERE org_id=$1`,
			`DELETE FROM connector_calendars WHERE org_id=$1`, `DELETE FROM connector_chat_hooks WHERE org_id=$1`,
			`DELETE FROM connector_settings WHERE org_id=$1`, `DELETE FROM connector_tenants WHERE org_id=$1`,
			`DELETE FROM calendar_events WHERE org_id=$1`,
			`SELECT audit_purge_org($1)`,
			`DELETE FROM principals WHERE org_id=$1`, `DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// turnOn is what an administrator does: connect the tenant, switch the connectors on.
func (f *fx) turnOn(kinds ...Kind) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.svc.ConnectTenant(ctx, f.org, f.dana, Google, "example.com"); err != nil {
		f.t.Fatalf("tenant: %v", err)
	}
	for _, k := range kinds {
		if err := f.svc.SetEnabled(ctx, f.org, f.dana, Google, k, true, k == Calendar); err != nil {
			f.t.Fatalf("enable %s: %v", k, err)
		}
	}
}

func (f *fx) localEvents(who string) map[string]localEvent {
	f.t.Helper()
	rows, err := f.db.Query(context.Background(),
		`SELECT id::text FROM calendar_events WHERE org_id=$1 AND (principal_id=$2::uuid OR space_id = $2::uuid)`, f.org, who)
	if err != nil {
		f.t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := map[string]localEvent{}
	for _, id := range ids {
		e, _ := f.svc.loadEvent(context.Background(), f.org, id)
		out[e.Title] = *e
	}
	return out
}

func (f *fx) syncCal() int {
	f.t.Helper()
	n, err := f.svc.SyncCalendar(context.Background(), f.org, f.dana, Google)
	if err != nil {
		f.t.Fatalf("sync calendar: %v", err)
	}
	return n
}

func (f *fx) at(days int, hour int) time.Time {
	return time.Date(2026, 12, 1+days, hour, 0, 0, 0, time.UTC)
}

// ---- settings -------------------------------------------------------------

func TestEverythingIsOffUntilAnAdministratorTurnsItOn(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	all, err := f.svc.Settings(ctx, f.org)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Providers {
		for _, k := range Kinds {
			if all[p][k].Enabled {
				t.Errorf("%s %s is on in a fresh installation", p, k)
			}
		}
	}
	if _, err := f.svc.SyncCalendar(ctx, f.org, f.dana, Google); !errors.Is(err, ErrDisabled) {
		t.Fatalf("a calendar sync with the connector off gave %v, want ErrDisabled", err)
	}
	if err := f.svc.SetEnabled(ctx, f.org, f.dana, Google, Calendar, true, false); err == nil {
		t.Fatal("a connector was turned on with no tenant or domain connected")
	}
	f.turnOn(Calendar)
	if on, _ := f.svc.Enabled(ctx, f.org, Google, Calendar); !on {
		t.Fatal("not on after the administrator turned it on")
	}
	if on, _ := f.svc.Enabled(ctx, f.org, Google, Mail); on {
		t.Fatal("turning the calendar on turned mail on")
	}
}

func TestATenantIsCheckedWhenItIsTyped(t *testing.T) {
	for _, c := range []struct {
		p    Provider
		in   string
		good bool
	}{
		{Microsoft, "8F1C2B7E-0000-4A1D-9C3E-5D2F7A6B1C90", true},
		{Microsoft, "contoso.onmicrosoft.com", false},
		{Microsoft, "organizations", false},
		{Google, "Example.COM", true},
		{Google, "someone@example.com", false},
		{Google, "gmail", false},
	} {
		_, err := NormalizeTenant(c.p, c.in)
		if (err == nil) != c.good {
			t.Errorf("%s %q: error %v, want good=%v", c.p, c.in, err, c.good)
		}
	}
}

func TestAConnectorWhoseGrantLacksTheScopeIsRefused(t *testing.T) {
	f := setup(t)
	f.turnOn(Calendar)
	f.fake.scopes = []string{"contacts"}
	if _, err := f.svc.SyncCalendar(context.Background(), f.org, f.dana, Google); !errors.Is(err, ErrScope) {
		t.Fatalf("got %v, want ErrScope", err)
	}
}

// ---- calendar -------------------------------------------------------------

func TestCalendarSyncsBothWaysAndKeepsWhoChangedWhatApart(t *testing.T) {
	f := setup(t)
	f.turnOn(Calendar)
	ctx := context.Background()

	// New at the provider comes in.
	f.fake.put("primary", Event{ID: "A", Title: "Plant review", Notes: "agenda", Place: "Room 4",
		Start: f.at(1, 9), End: f.at(1, 10)})
	f.syncCal()
	a, ok := f.localEvents(f.dana)["Plant review"]
	if !ok || a.Notes != "agenda" || a.Place != "Room 4" || !a.Start.Equal(f.at(1, 9)) {
		t.Fatalf("the provider's event did not arrive intact: %+v ok=%v", a, ok)
	}

	// New in Hearth goes out, once. A second run changes nothing and sends nothing.
	if _, err := f.db.Exec(ctx, `INSERT INTO calendar_events (id, org_id, principal_id, title, starts_at, ends_at, created_by)
		VALUES ($1,$2,$3,'Safety walk',$4,$5,$3)`, uuid.NewString(), f.org, f.dana, f.at(2, 13), f.at(2, 14)); err != nil {
		t.Fatal(err)
	}
	f.syncCal()
	if f.fake.creates != 1 {
		t.Fatalf("a Hearth event was created at the provider %d times, want 1", f.fake.creates)
	}
	creates, updates := f.fake.creates, f.fake.updates
	if n := f.syncCal(); n != 0 || f.fake.creates != creates || f.fake.updates != updates {
		t.Fatalf("an unchanged calendar produced changes: n=%d creates=%d updates=%d", n, f.fake.creates, f.fake.updates)
	}
	if !f.fake.meeting {
		t.Error("the administrator asked for meeting links and the create did not carry the request")
	}

	// Changed at the provider: Hearth follows.
	f.fake.put("primary", Event{ID: "A", Title: "Plant review (moved)", Notes: "agenda", Place: "Room 4",
		Start: f.at(1, 11), End: f.at(1, 12)})
	f.syncCal()
	if got, ok := f.localEvents(f.dana)["Plant review (moved)"]; !ok || !got.Start.Equal(f.at(1, 11)) {
		t.Fatalf("a change at the provider did not reach Hearth: %+v", got)
	}

	// Changed in Hearth: the provider follows.
	id := f.localEvents(f.dana)["Safety walk"].ID
	if _, err := f.db.Exec(ctx, `UPDATE calendar_events SET title='Safety walk, east bay' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	before := f.fake.updates
	f.syncCal()
	if f.fake.updates != before+1 {
		t.Fatalf("a change in Hearth made %d updates at the provider, want 1", f.fake.updates-before)
	}
	found := false
	for _, e := range f.fake.events["primary"] {
		found = found || e.Title == "Safety walk, east bay"
	}
	if !found {
		t.Fatal("the provider does not have the new title")
	}

	// Changed on both sides: the provider's version wins and nothing is sent back over it.
	id = f.localEvents(f.dana)["Plant review (moved)"].ID
	if _, err := f.db.Exec(ctx, `UPDATE calendar_events SET title='Local edit' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	f.fake.put("primary", Event{ID: "A", Title: "Remote edit", Start: f.at(1, 11), End: f.at(1, 12)})
	before = f.fake.updates
	f.syncCal()
	if _, ok := f.localEvents(f.dana)["Remote edit"]; !ok {
		t.Fatal("on a clash the provider's version did not win")
	}
	if f.fake.updates != before {
		t.Fatal("on a clash Hearth overwrote the provider's version")
	}

	// Deleted at the provider: gone here. Deleted here: gone there.
	f.fake.Delete(ctx, "primary", "A")
	f.syncCal()
	if _, ok := f.localEvents(f.dana)["Remote edit"]; ok {
		t.Fatal("an event deleted at the provider is still in Hearth")
	}
	id = f.localEvents(f.dana)["Safety walk, east bay"].ID
	if _, err := f.db.Exec(ctx, `DELETE FROM calendar_events WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	f.syncCal()
	for _, e := range f.fake.events["primary"] {
		if e.Title == "Safety walk, east bay" {
			t.Fatal("an event deleted in Hearth is still at the provider")
		}
	}
}

func TestAnAllDayEventKeepsItsDaysThroughBothConventions(t *testing.T) {
	f := setup(t)
	f.turnOn(Calendar)
	// The provider's all-day end is the day after the last: Dec 3 to Dec 5 is the 3rd and the 4th.
	f.fake.put("primary", Event{ID: "D", Title: "Shutdown", AllDay: true,
		Start: time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC)})
	f.syncCal()
	e := f.localEvents(f.dana)["Shutdown"]
	if !e.AllDay || e.Start.Day() != 3 || e.End.Day() != 4 {
		t.Fatalf("Hearth holds %v to %v, want the 3rd to the 4th inclusive", e.Start, e.End)
	}
	if f.fake.updates != 0 {
		t.Fatal("reading an all-day event back made an update")
	}
	back := e.toRemote()
	if back.End.Day() != 5 || back.Start.Day() != 3 {
		t.Fatalf("sent back as %v to %v, want the 3rd to the 5th exclusive", back.Start, back.End)
	}
}

func TestADepartmentCalendarIsTheDepartmentsAndNeedsAManager(t *testing.T) {
	f := setup(t)
	f.turnOn(Calendar)
	ctx := context.Background()
	space := uuid.NewString()
	if _, err := f.db.Exec(ctx, `INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Plant 3','folder',$3)`,
		space, f.org, f.dana); err != nil {
		t.Fatal(err)
	}
	f.fake.calendar = append(f.fake.calendar, CalendarInfo{ID: "shared1", Name: "Plant 3 shifts"})

	if err := f.svc.SyncCalendarInto(ctx, f.org, f.dana, Google, "shared1", "Plant 3 shifts", space); !errors.Is(err, ErrNotManager) {
		t.Fatalf("a person who does not manage the department got %v", err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, f.org, space, f.dana); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SyncCalendarInto(ctx, f.org, f.dana, Google, "shared1", "Plant 3 shifts", space); err != nil {
		t.Fatal(err)
	}
	f.fake.put("shared1", Event{ID: "S1", Title: "Night shift", Start: f.at(1, 22), End: f.at(2, 6)})
	f.syncCal()

	var owner, sp *string
	if err := f.db.QueryRow(ctx, `SELECT principal_id::text, space_id::text FROM calendar_events WHERE org_id=$1 AND title='Night shift'`, f.org).
		Scan(&owner, &sp); err != nil {
		t.Fatalf("the shared calendar's event is not in Hearth: %v", err)
	}
	if owner != nil || sp == nil || *sp != space {
		t.Fatalf("the event belongs to person=%v space=%v, want the department only", owner, sp)
	}

	// Somebody else who also sees that calendar cannot import it a second time.
	if _, err := f.db.Exec(ctx, `INSERT INTO space_managers (org_id, space_id, principal_id) VALUES ($1,$2,$3)`, f.org, space, f.omar); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SyncCalendarInto(ctx, f.org, f.omar, Google, "shared1", "Plant 3 shifts", space); err == nil {
		t.Fatal("two people synced the same calendar into the same department")
	}

	// And a Hearth event added to the department goes to that calendar.
	if _, err := f.db.Exec(ctx, `INSERT INTO calendar_events (id, org_id, space_id, title, starts_at, ends_at, created_by)
		VALUES ($1,$2,$3,'Shutdown briefing',$4,$5,$6)`, uuid.NewString(), f.org, space, f.at(3, 8), f.at(3, 9), f.omar); err != nil {
		t.Fatal(err)
	}
	f.syncCal()
	got := false
	for _, e := range f.fake.events["shared1"] {
		got = got || e.Title == "Shutdown briefing"
	}
	if !got {
		t.Fatal("a department event did not reach the shared calendar")
	}
}

// ---- contacts -------------------------------------------------------------

func TestContactsImportAdoptAndNeverLoseHearthsOwnEdits(t *testing.T) {
	f := setup(t)
	f.turnOn(Contacts)
	ctx := context.Background()
	actor, _ := f.az.SubjectsFor(ctx, f.org, f.dana)

	existing, err := f.crm.CreateContact(ctx, f.org, actor, crm.Contact{Name: "Priya Shah", Email: "priya@acme.test", Title: "Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	f.fake.contacts = []Contact{
		{ID: "c1", Version: "1", Name: "Priya Shah (work)", Email: "priya@acme.test", Title: "Head of purchasing"},
		{ID: "c2", Version: "1", Name: "Marcus Lee", Email: "marcus@acme.test", Phone: "555 0100", Title: "Engineer"},
		{ID: "c3", Version: "1", Email: "noname@acme.test"},
		{ID: "c4", Version: "1"},
	}
	if _, err := f.svc.SyncContacts(ctx, f.org, f.dana, Google); err != nil {
		t.Fatal(err)
	}

	// An address already in the CRM is linked, not overwritten.
	got, _ := f.crm.GetContact(ctx, f.org, actor, existing.ID)
	if got.Name != "Priya Shah" || got.Title != "Buyer" {
		t.Fatalf("an existing contact was overwritten: %+v", got)
	}
	m, err := f.crm.ByEmail(ctx, f.org, "marcus@acme.test")
	if err != nil || m.Name != "Marcus Lee" || m.Phone != "555 0100" {
		t.Fatalf("a new contact did not arrive intact: %+v %v", m, err)
	}
	if n, err := f.crm.ByEmail(ctx, f.org, "noname@acme.test"); err != nil || n.Name != "noname@acme.test" {
		t.Fatalf("a contact with only an address should be named by it: %+v %v", n, err)
	}

	// A change at the provider reaches a contact nobody has edited in Hearth.
	f.fake.contacts[1] = Contact{ID: "c2", Version: "2", Name: "Marcus Lee", Email: "marcus@acme.test", Phone: "555 0199", Title: "Senior engineer"}
	if _, err := f.svc.SyncContacts(ctx, f.org, f.dana, Google); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.crm.GetContact(ctx, f.org, actor, m.ID); m.Title != "Senior engineer" || m.Phone != "555 0199" {
		t.Fatalf("the provider's change did not arrive: %+v", m)
	}

	// An edit in Hearth is kept when the provider changes it afterwards.
	mine, _ := f.crm.GetContact(ctx, f.org, actor, m.ID)
	mine.Title = "Plant manager"
	if err := f.crm.UpdateContact(ctx, f.org, actor, *mine); err != nil {
		t.Fatal(err)
	}
	f.fake.contacts[1] = Contact{ID: "c2", Version: "3", Name: "Marcus Lee", Email: "marcus@acme.test", Title: "Director"}
	if _, err := f.svc.SyncContacts(ctx, f.org, f.dana, Google); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.crm.GetContact(ctx, f.org, actor, m.ID); m.Title != "Plant manager" {
		t.Fatalf("an edit made in Hearth was overwritten: %q", m.Title)
	}

	// Deleted at the provider: the CRM record stays. Deleted in Hearth: it is not made again.
	f.fake.contacts = f.fake.contacts[:1]
	if _, err := f.svc.SyncContacts(ctx, f.org, f.dana, Google); err != nil {
		t.Fatal(err)
	}
	if _, err := f.crm.ByEmail(ctx, f.org, "marcus@acme.test"); err != nil {
		t.Fatal("a contact deleted at the provider was removed from the CRM")
	}
	if err := f.crm.DeleteContact(ctx, f.org, actor, existing.ID); err != nil {
		t.Fatal(err)
	}
	f.fake.contacts[0].Version = "9"
	if _, err := f.svc.SyncContacts(ctx, f.org, f.dana, Google); err != nil {
		t.Fatal(err)
	}
	if _, err := f.crm.ByEmail(ctx, f.org, "priya@acme.test"); err == nil {
		t.Fatal("a contact deleted in Hearth was made again")
	}
}

func TestAnImportedContactIsPrivateToTheImporter(t *testing.T) {
	f := setup(t)
	f.turnOn(Contacts)
	ctx := context.Background()
	f.fake.contacts = []Contact{{ID: "c1", Version: "1", Name: "Hidden Person", Email: "hidden@acme.test"}}
	if _, err := f.svc.SyncContacts(ctx, f.org, f.dana, Google); err != nil {
		t.Fatal(err)
	}
	other, _ := f.az.SubjectsFor(ctx, f.org, f.omar)
	cs, err := f.crm.Contacts(ctx, f.org, other, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Email == "hidden@acme.test" {
			t.Fatal("a colleague can see a contact imported from somebody else's address book")
		}
	}
}

// ---- mail -----------------------------------------------------------------

func TestMailIsLinkedOnlyToPeopleInTheCRMAndOnlyShownToTheMailboxOwner(t *testing.T) {
	f := setup(t)
	f.turnOn(Mail)
	ctx := context.Background()
	actor, _ := f.az.SubjectsFor(ctx, f.org, f.dana)
	priya, err := f.crm.CreateContact(ctx, f.org, actor, crm.Contact{Name: "Priya Shah", Email: "priya@acme.test"})
	if err != nil {
		t.Fatal(err)
	}
	f.fake.messages = []Message{
		{ID: "m1", Subject: "Quote", From: "Priya Shah <priya@acme.test>", To: []string{"dana@example.com"},
			Snippet: "Please find", Received: f.clock.Add(-time.Hour), WebLink: "https://mail.example/m1"},
		{ID: "m2", Subject: "Re: Quote", From: "dana@example.com", To: []string{"PRIYA@acme.test"}, Received: f.clock.Add(-30 * time.Minute)},
		{ID: "m3", Subject: "Newsletter", From: "list@spam.test", To: []string{"dana@example.com"}, Received: f.clock.Add(-time.Hour)},
		{ID: "old", Subject: "Ancient", From: "priya@acme.test", Received: f.clock.Add(-60 * 24 * time.Hour)},
	}
	n, err := f.svc.SyncMail(ctx, f.org, f.dana, Google)
	if err != nil || n != 2 {
		t.Fatalf("linked %d messages (%v), want 2: the two with Priya, and not the newsletter or the one past the look-back", n, err)
	}
	if n, _ := f.svc.SyncMail(ctx, f.org, f.dana, Google); n != 0 {
		t.Fatalf("a second pass linked %d more", n)
	}
	mine, _ := f.svc.MailFor(ctx, f.org, f.dana, priya.ID, 10)
	if len(mine) != 2 || mine[0].Subject != "Re: Quote" {
		t.Fatalf("the mailbox owner sees %+v", mine)
	}
	if theirs, _ := f.svc.MailFor(ctx, f.org, f.omar, priya.ID, 10); len(theirs) != 0 {
		t.Fatal("a colleague sees mail from somebody else's mailbox")
	}
	var snippet string
	_ = f.db.QueryRow(ctx, `SELECT snippet FROM connector_mail WHERE message_id='m1'`).Scan(&snippet)
	if snippet != "Please find" {
		t.Fatalf("snippet %q", snippet)
	}
}

// ---- files ----------------------------------------------------------------

func TestAFileIsLinkedToADocumentAndOnlyByWhoMayEditIt(t *testing.T) {
	f := setup(t)
	f.turnOn(Files)
	ctx := context.Background()
	actor, _ := f.az.SubjectsFor(ctx, f.org, f.dana)
	doc, err := f.docs.Create(ctx, f.org, actor, docs.CreateParams{Title: "Quote 118"})
	if err != nil {
		t.Fatal(err)
	}
	f.fake.drives = []Drive{{ID: "d1", Name: "OneDrive", Kind: "personal"}}
	f.fake.items["d1/"] = []FileItem{{ID: "f1", DriveID: "d1", Name: "quote.pdf", MIME: "application/pdf", Size: 5, WebURL: "https://files.example/f1"}}

	if _, err := f.svc.Link(ctx, f.org, f.omar, Google, "d1", "f1", doc.ID); err == nil {
		t.Fatal("a person who cannot edit the document linked a file to it")
	}
	l, err := f.svc.Link(ctx, f.org, f.dana, Google, "d1", "f1", doc.ID)
	if err != nil || l.Name != "quote.pdf" || l.Imported {
		t.Fatalf("link: %+v %v", l, err)
	}
	if _, err := f.svc.Link(ctx, f.org, f.dana, Google, "d1", "f1", doc.ID); err != nil {
		t.Fatalf("linking twice should be a no-op: %v", err)
	}
	links, err := f.svc.LinksFor(ctx, f.org, f.dana, doc.ID)
	if err != nil || len(links) != 1 {
		t.Fatalf("links %+v %v", links, err)
	}
	if _, err := f.svc.LinksFor(ctx, f.org, f.omar, doc.ID); err == nil {
		t.Fatal("a person who cannot see the document read its links")
	}
	if err := f.svc.Unlink(ctx, f.org, f.omar, l.ID); err == nil {
		t.Fatal("a person who cannot edit the document removed its link")
	}
	if err := f.svc.Unlink(ctx, f.org, f.dana, l.ID); err != nil {
		t.Fatal(err)
	}
}

func TestImportingAFileAttachesACopy(t *testing.T) {
	if os.Getenv("TEST_MINIO_ENDPOINT") == "" {
		t.Skip("TEST_MINIO_ENDPOINT not set")
	}
	f := setup(t)
	f.turnOn(Files)
	ctx := context.Background()
	actor, _ := f.az.SubjectsFor(ctx, f.org, f.dana)
	doc, err := f.docs.Create(ctx, f.org, actor, docs.CreateParams{Title: "Quote 119"})
	if err != nil {
		t.Fatal(err)
	}
	f.fake.items["d1/"] = []FileItem{{ID: "f2", DriveID: "d1", Name: "notes.txt", MIME: "text/plain", Size: 11, WebURL: "https://files.example/f2"}}
	f.fake.body["f2"] = "hello world"
	l, err := f.svc.Import(ctx, f.org, f.dana, Google, "d1", "f2", doc.ID)
	if err != nil || !l.Imported {
		t.Fatalf("import: %+v %v", l, err)
	}
	atts, err := f.docs.Attachments(ctx, f.org, actor, doc.ID)
	if err != nil || len(atts) != 1 || atts[0].Filename != "notes.txt" {
		t.Fatalf("attachments %+v %v", atts, err)
	}
}

// ---- disconnect and the scheduled run --------------------------------------

func TestDisconnectingForgetsWhatIsThePersonsAlone(t *testing.T) {
	f := setup(t)
	f.turnOn(Calendar, Mail)
	ctx := context.Background()
	f.fake.put("primary", Event{ID: "A", Title: "Kept", Start: f.at(1, 9), End: f.at(1, 10)})
	f.syncCal()
	f.fake.people = []Conn{{OrgID: f.org, PrincipalID: f.dana, Email: "dana@example.com"}}
	if err := f.svc.Disconnect(ctx, Google, f.dana); err != nil {
		t.Fatal(err)
	}
	var links, status int
	_ = f.db.QueryRow(ctx, `SELECT count(*) FROM connector_links WHERE principal_id=$1`, f.dana).Scan(&links)
	_ = f.db.QueryRow(ctx, `SELECT count(*) FROM connector_status WHERE principal_id=$1`, f.dana).Scan(&status)
	if links != 0 || status != 0 {
		t.Fatalf("after disconnecting %d links and %d statuses remain", links, status)
	}
	if _, ok := f.localEvents(f.dana)["Kept"]; !ok {
		t.Fatal("disconnecting removed an event the person now owns")
	}
}

func TestAScheduledPassSyncsEveryoneAndRecordsWhatHappened(t *testing.T) {
	f := setup(t)
	f.turnOn(Calendar)
	ctx := context.Background()
	f.fake.people = []Conn{{OrgID: f.org, PrincipalID: f.dana}, {OrgID: f.org, PrincipalID: f.omar, Revoked: true}}
	f.fake.put("primary", Event{ID: "A", Title: "Standup", Start: f.at(1, 9), End: f.at(1, 10)})
	f.svc.RunAll(ctx)
	if _, ok := f.localEvents(f.dana)["Standup"]; !ok {
		t.Fatal("the scheduled pass did not sync a connected person")
	}
	if len(f.localEvents(f.omar)) != 0 {
		t.Fatal("the scheduled pass synced a revoked connection")
	}
	sts, _ := f.svc.Statuses(ctx, f.org)
	if len(sts) != 1 || sts[0].LastOKAt == nil || sts[0].LastError != "" {
		t.Fatalf("statuses %+v", sts)
	}

	// A revoked grant is recorded as what it is, in words an administrator can act on.
	f.fake.revoked = true
	f.fake.people[1].Revoked = false
	f.svc.RunAll(ctx)
	sts, _ = f.svc.Statuses(ctx, f.org)
	var failed int
	for _, st := range sts {
		if st.LastError != "" {
			failed++
			if !strings.Contains(st.LastError, "revoked") {
				t.Errorf("status error %q does not say the connection was revoked", st.LastError)
			}
		}
	}
	if failed == 0 {
		t.Fatal("a revoked connection left no error for the administrator to read")
	}
}
