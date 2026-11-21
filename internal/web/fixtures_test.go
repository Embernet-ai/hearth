package web

// The pages a person sees are drawn in the browser from page data, and what each role is shown is decided by the views in
// ui/src/views/pages. This file builds the page values that decide it (somebody with no grants, a super admin, an owner, a
// request waiting to be decided) and writes them as the JSON the server sends, to ui/src/views/fixtures. The view specs next
// to the views read those files, so a fixture is the wire format and never a hand-written guess of it.
//
//	go test ./internal/web -run TestViewFixtures -update-fixtures
//
// regenerates them. Without the flag the test fails when a file is out of date, so a change to the page value that the views
// have not been checked against shows up here.

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crm/crmviews"
	"github.com/Embernet-ai/hearth/internal/crm/csvimport"
	"github.com/Embernet-ai/hearth/internal/deptreq"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

var updateFixtures = flag.Bool("update-fixtures", false, "write ui/src/views/fixtures")

func fixtures() map[string]page {
	// A fixed clock, so the files do not change from one run to the next.
	end := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	min := 90
	quarter := 45
	estimate := 195
	waiting := []timelog.Entry{
		{ID: "e1", PrincipalID: "p1", PersonName: "Gabriel", StartedAt: end, EndedAt: &end, Minutes: &min},
		{ID: "e2", PrincipalID: "p2", PersonName: "Aria", StartedAt: end, EndedAt: &end, Minutes: &min},
		{ID: "e3", PrincipalID: "p1", PersonName: "Gabriel", StartedAt: end, EndedAt: &end, Minutes: &min},
		{ID: "e4", PrincipalID: "p3", PersonName: "Constanza", StartedAt: end, EndedAt: &end, Minutes: &min},
	}
	chart := map[string]*authz.Structure{
		"p1": {Departments: []authz.Department{{ID: "white", Name: "White Team"}}},
		"p2": {Departments: []authz.Department{{ID: "eng", Name: "Engineering"}}},
	}
	approvals := func(dept, person string) page {
		v := approvalView(groupByPerson(waiting), chart, dept, person)
		return page{Title: "Approvals", Approvals: waiting, ApprovalGroups: v.People, Approval: v}
	}

	eng := &plant.Space{ID: "d1", Name: "Engineering", Path: "Engineering"}
	fin := &plant.Space{ID: "d2", Name: "Finance", Depth: 0}
	red := &plant.Space{ID: "d3", Name: "Red Team", Depth: 1, Path: "Engineering / Red Team"}
	dave := auth.Person{PrincipalID: "p1", DisplayName: "David", Email: "david@example.com", Status: "active"}
	everything := page{
		Title: "David", Person: &dave,
		Persons:     []auth.Person{dave, {PrincipalID: "p2", DisplayName: "Yara", Status: "active"}},
		Departments: []*plant.Space{eng, fin, red},
		Chart: map[string]*authz.Structure{"p1": {
			Departments: []authz.Department{{ID: "d1", Name: "Engineering"}},
			ApproverID:  "p2", Manages: []string{"Red Team"},
		}},
		PersonManaged: []authz.Department{{ID: "d3", Name: "Red Team"}},
		SpaceGrants:   map[string][]authz.Department{"p1": {{ID: "d2", Name: "Finance"}}},
		Grants:        map[string]authz.Held{"p1": {authz.Finance: true}},
		Grantable:     authz.Grantable, CanGrant: true, CanMovePeople: true, IsAdmin: true,
		AccessGroups: []AccessGroup{
			{Dept: AccessNode{Space: fin, Checked: true}, Open: true},
			{Dept: AccessNode{Space: eng, Checked: true}, Open: true,
				Parts: []AccessNode{{Space: red, Indent: 1, Covered: true}}},
		},
	}
	notAdmin := everything
	notAdmin.IsAdmin = false
	owner := dave
	owner.IsOwner = true
	ofOwner := everything
	ofOwner.Person = &owner

	readOnly := page{
		Title: "David", Person: &dave, Persons: []auth.Person{dave},
		Departments: []*plant.Space{eng}, AskDept: eng,
		Chart: map[string]*authz.Structure{"p1": {
			Departments:  []authz.Department{{ID: "d1", Name: "Engineering"}},
			ApproverName: "Yara",
		}},
		PersonManaged: []authz.Department{{ID: "d1", Name: "Engineering"}},
		AccessGroups:  []AccessGroup{{Dept: AccessNode{Space: eng, Checked: true}, Open: true}},
		Grantable:     authz.Grantable, CanGrant: false,
	}
	asks := page{Title: "David", Person: &dave, Persons: []auth.Person{dave},
		AskDept: eng, AskParts: []AskPart{{Space: red, Indent: 1}}, CanGrant: true, CanMovePeople: true}

	white := deptreq.Department{ID: "w1", Name: "White Team"}
	engd := deptreq.Department{ID: "e1", Name: "Engineering"}
	req := &deptreq.Request{ID: "r1", RequesterName: "Constanza", Status: "pending",
		Asked: []deptreq.Department{white}, Note: "contractor", CreatedAt: end}

	rollup := &docs.ReportEntry{ID: "11111111-1111-1111-1111-111111111111", Day: "2026-10-26",
		Author: "Gabriel", SpacePath: "Engineering / White Team"}

	// Time off: what an approver or an admin sees of somebody else's requests, and what the person sees of their own.
	leave := func(id, status string, half bool) *timeoff.Request {
		return &timeoff.Request{ID: id, RequesterID: "p2", RequesterName: "Aria", Kind: "vacation", Status: status, Days: 3, HalfDay: half,
			StartsOn: time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC), EndsOn: time.Date(2026, 12, 9, 0, 0, 0, 0, time.UTC),
			Comments: "flying out Sunday", DeciderName: "Yessid", CreatedAt: end}
	}
	cancelled := leave("c1", "cancelled", false)
	cancelled.DecisionNote = "Cancelled by Yessid: the trip moved"

	return map[string]page{
		"timeoff-decide": {Title: "Time off", TimeOffKinds: timeoff.Kinds,
			TimeOffToDecide: []*timeoff.Request{leave("t1", "pending", false), leave("t2", "approved", true), cancelled}},
		"timeoff-own": {Title: "Time off", TimeOffKinds: timeoff.Kinds,
			TimeOff: []*timeoff.Request{leave("t2", "approved", true), cancelled}},
		"settings-nobody":  {Title: "Settings"},
		"settings-finance": {Title: "Settings", Can: authz.Held{authz.Finance: true}},
		"settings-owner":   {Title: "Settings", IsAdmin: true},

		"person-everything":   everything,
		"person-not-admin":    notAdmin,
		"person-of-the-owner": ofOwner,
		"person-read-only":    readOnly,
		"person-asks-access":  asks,
		"people": {Title: "People", SearchText: "dav",
			Persons: []auth.Person{{PrincipalID: "p1", DisplayName: "David", Status: "active"}},
			Grants:  map[string]authz.Held{"p1": {authz.Finance: true}}, Grantable: authz.Grantable},

		"approvals-overview": approvals("", ""),
		"approvals-white":    approvals("white", ""),
		"approvals-gabriel":  approvals("white", "p1"),
		"approvals-request": {Title: "Approvals", DeptToDecide: []*deptreq.Request{req},
			AllDepartments: []deptreq.Department{engd, white}, Approval: &ApprovalView{}},
		"deptrequest-pending": {Title: "My departments", AllDepartments: []deptreq.Department{engd, white}, MyDeptRequest: req},

		"imported": {Title: "Imported hours", ImportedCount: 3, ImportedMinutes: 600,
			ImportedByPerson: []ImportedGroup{{PersonID: "p1", PersonName: "Gabriel", Count: 2, Minutes: 480}},
			ImportedManual: []timelog.ImportedEntry{{ID: "e9", DocumentID: "d9", DocTitle: "Shared job",
				PersonName: "Pat", Minutes: 120, StartedAt: end, Assignees: []string{"Aria", "Gabriel"}}},
			People: []docs.People{{ID: "p1", Name: "Gabriel"}}},
		"spaces": {Title: "Spaces",
			Spaces: []*plant.Space{
				{ID: "mine", Name: "White Team", Kind: "folder", Documents: 2},
				{ID: "theirs", Name: "Sales", Kind: "folder", Documents: 1},
			},
			ManageableSpaces: map[string]bool{"mine": true}},

		"spaceaccess": {Title: "Access to Alpha",
			AccessSpace: &plant.Space{ID: "alpha", Name: "Alpha", Path: "Dept / Projects / Alpha"},
			Delegations: []plant.Delegation{
				{PersonID: "p1", Name: "Carol", Email: "carol@example.test", Contractor: true, Level: core.AccessEdit, ExpiresAt: &end},
				{PersonID: "p2", Name: "Dan", Level: core.AccessView, ExpiresAt: &end, Expired: true},
				{PersonID: "p3", Name: "Eve", Level: core.AccessComment},
			}},
		"spaceaccess-empty": {Title: "Access to Alpha",
			AccessSpace: &plant.Space{ID: "alpha", Name: "Alpha", Path: "Alpha"},
			Message:     "Nobody here signs in with that address. Ask an admin to add the account first."},

		"dailyreport-form": {Title: "File today's report", RepDay: "2026-10-26", RepSpace: "sp2",
			RepTree: []*plant.Space{{ID: "sp1", Path: "Engineering"}, {ID: "sp2", Path: "Daily reports"}},
			RepBody: "kept text", RepError: "Write what you did today, or add a photo."},
		"dailyreports-rollup": {Title: "Daily reports", RepFrom: "2026-10-20", RepTo: "2026-10-26",
			RepPDFHref: "/reports/daily.pdf?from=2026-10-20&to=2026-10-26",
			RepEntries: []*reportView{{
				ReportEntry: rollup, BodyHTML: "<p>Done: wiring</p>",
				Photos:   []docs.Attachment{{FileID: "f1", Filename: "panel.jpg"}},
				Comments: []*channels.Message{{AuthorName: "Yara", Body: "thanks", CreatedAt: end}},
			}}},
		"dailyreports-filed":  {RepMineToday: "abc", RepFrom: "a", RepTo: "b"},
		"dashboard-not-filed": {Dash: &dashboard{ReportDay: "2026-10-26"}},
		"dashboard-filed":     {Dash: &dashboard{ReportDay: "2026-10-26", ReportID: "rid"}},
		"servicehistory": {Title: "Service history", RepSpace: "sp1", RepFrom: "2025-01-01", RepTo: "2025-12-31",
			RepTree:    []*plant.Space{{ID: "sp1", Path: "White Team Service / Acme"}},
			SvcRecords: 3, SvcEvents: 9, SvcDays: 4,
			SvcPDF: "/reports/service.pdf?space=sp1&from=2025-01-01", SvcCSV: "/reports/service.csv?space=sp1",
			SvcShown: []*docs.ServiceRecord{{ID: "r1", Title: "Drive fault", Job: "Line 3", Status: "done",
				Events: []docs.ServiceEvent{{Day: "2025-03-03"}}}}},
		"dashboard-numbers": {Dash: &dashboard{ReportDay: "2026-10-26", Stats: &statsPanel{
			Person: personRef{ID: "me", Name: "Me"}, Self: true,
			Groups: []statGroup{
				{Key: "hours", Title: "Hours", Tiles: []statTile{
					{Label: "Logged this week", Value: "12h 30m", Detail: "5 entries", Period: "16 to 22 Nov", Href: "/time?from=2026-11-16",
						Of: "40h", Percent: 31, HasPercent: true, Note: "No weekly hours are set for this person, so the standard 40 hour week is used."},
					{Label: "Sent back", Value: "1h", Period: "1 to 17 Nov", Href: "/time?from=2026-11-01&span=month", Tone: "bad"},
				}},
				{Key: "work", Title: "Work", Tiles: []statTile{
					{Label: "Overdue", Value: "3", Period: "due before 17 Nov", Href: "/documents?f=status%3Aany_of%3Atodo", Tone: "bad"},
				}},
				{Key: "deals", Title: "Deals", Tiles: []statTile{
					{Label: "Open pipeline", Value: "$12,500", Detail: "4 open deals", Period: "right now", Href: "/deals?view=table&owner=me&open=1"},
				}},
			},
			Weeks:       []barRow{{Label: "9 Nov", Value: "30h", Percent: 75, Href: "/time?from=2026-11-09"}, {Label: "16 Nov", Value: "12h 30m", Percent: 31, Href: "/time?from=2026-11-16", Current: true}},
			WeeksTarget: "40h",
			Stages:      []barRow{{Label: "Proposal", Value: "2, $10,000", Percent: 100, Href: "/deals?view=table&owner=me&stage=proposal"}},
			Recent:      []recentRow{{Kind: "Deal won", Title: "Line upgrade", Detail: "$4,000", Href: "/deals/d1"}},
		}}},
		"dashboard-numbers-picker": {Dash: &dashboard{Stats: &statsPanel{
			Person: personRef{ID: "p2", Name: "Aria"}, Self: false,
			People: []personRef{{ID: "me", Name: "Me"}, {ID: "p2", Name: "Aria"}},
			Groups: []statGroup{{Key: "work", Title: "Work", Tiles: []statTile{{Label: "Open and assigned", Value: "7", Period: "right now", Href: "/documents"}}}},
		}}},
		"time-colleague": {Title: "My time", TimeOf: &personRef{ID: "p2", Name: "Aria"}, TimeMonth: true,
			WeekFrom: end, WeekTo: end.AddDate(0, 1, 0), PrevWeek: "2026-10-01", NextWeek: "2026-12-01",
			MyMinutes: 600, MyTimeBack: "/time?from=2026-11-01&span=month&person=p2",
			MyTime: []timelog.Entry{{ID: "t1", PrincipalID: "p2", PersonName: "Aria", StartedAt: end, EndedAt: &end, Minutes: &min, Note: "wiring"}}},
		"time-own": {Title: "My time", WeekFrom: end, WeekTo: end.AddDate(0, 0, 7), PrevWeek: "2026-11-09", NextWeek: "2026-11-23",
			MyMinutes: 90, MyTimeBack: "/time?from=2026-11-16",
			MyTime: []timelog.Entry{{ID: "t1", PrincipalID: "me", PersonName: "Me", StartedAt: end, EndedAt: &end, Minutes: &min, Note: "wiring"}}},
		"companies-grouped": {Title: "Companies", SalesOn: true, CRMGroup: "kind",
			CRMGroupOptions: crm.GroupOptions("companies"), CRMRowGroups: []string{"vendor", "vendor", "customer"},
			CRMGroupCounts: map[string]int{"vendor": 12, "customer": 4}, CRMViewsOn: true, ListFrom: "/companies?group=kind",
			CRMViews: []*crmviews.View{{ID: "v1", OwnerID: "me", OwnerName: "Me", List: "companies", Name: "Vendors by owner", Query: "kind=vendor&group=owner"},
				{ID: "v2", OwnerID: "p2", OwnerName: "Aria", List: "companies", Name: "Open accounts", Query: "group=kind", Shared: true}},
			CompanyRows: []*crm.CompanyRow{
				{Company: crm.Company{ID: "c1", Name: "Baldor Electric", Kind: "vendor"}, OwnerName: "Aria"},
				{Company: crm.Company{ID: "c2", Name: "Acme Motors", Kind: "vendor"}},
				{Company: crm.Company{ID: "c3", Name: "Acme Paint", Kind: "customer"}, OwnerName: "Me"}},
			CompanyKinds: crm.Kinds, Me: "me"},
		"crmfavorites": {Title: "Favorites", SalesOn: true, FavoriteItems: []FavoriteItem{
			{Kind: "Company", Name: "Baldor Electric", Detail: "vendor", Href: "/companies/c1"},
			{Kind: "Contact", Name: "Ada Lovelace", Detail: "Acme Motors", Href: "/contacts/k1"},
			{Kind: "Deal", Name: "Press brake retrofit", Detail: "Proposal $12,500.00", Href: "/deals/d1"}}},
		"company-favorite": {Title: "Baldor Electric", SalesOn: true, IsFavorite: true, FavoriteType: "company",
			Company: &crm.Company{ID: "c1", Name: "Baldor Electric", Kind: "vendor"}, CompanyKinds: crm.Kinds, Details: &crm.Details{}},
		"company-plain": {Title: "Acme Motors", SalesOn: true, FavoriteType: "company",
			Company: &crm.Company{ID: "c2", Name: "Acme Motors", Kind: "vendor"}, CompanyKinds: crm.Kinds, Details: &crm.Details{}},
		"company-in-emberops": {Title: "Baldor Electric", SalesOn: true, FavoriteType: "company",
			Company: &crm.Company{ID: "c1", Name: "Baldor Electric", Kind: "customer"}, CompanyKinds: crm.Kinds, Details: &crm.Details{},
			EmberOps: []crm.EmberOpsLink{{Label: "Customer", URL: "https://ops.example/app/customer/Baldor%20Electric"}}},
		"document-time-refused": {Title: "Replace the guard switch", Doc: &docs.Document{ID: "d1", Kind: "task", Title: "Replace the guard switch",
			Task: &docs.TaskFields{Status: "todo"}},
			Tags:     []timelog.Tag{{ID: "t1", Name: "Onsite"}, {ID: "t2", Name: "Remote"}},
			TimeForm: &TimeForm{Error: "Minutes go from 0 to 59. Put whole hours in the Hours box.", Hours: "1", Mins: "75", On: "2026-11-18", Note: "wired the panel", Tags: []string{"t2"}}},
		"document-time-fix": {Title: "Replace the guard switch", Doc: &docs.Document{ID: "d1", Kind: "task", Title: "Replace the guard switch",
			Task: &docs.TaskFields{Status: "todo"}},
			TimeEntries: []timelog.Entry{
				{ID: "e1", PrincipalID: "me", PersonName: "Me", StartedAt: end, EndedAt: &end, Minutes: &min, Note: "wiring"},
				{ID: "e2", PrincipalID: "me", PersonName: "Me", StartedAt: end, EndedAt: &end, Minutes: &quarter, Note: "testing"}},
			TimeForm: &TimeForm{Fix: &EntryFix{ID: "e2", Error: "Enter how long it took: hours, minutes, or both.", Hours: "", Mins: "", On: "2026-11-16", Note: "testing"}}},
		"document-estimate-set": {Title: "Replace the guard switch", Doc: &docs.Document{ID: "d1", Kind: "task", Title: "Replace the guard switch",
			Task: &docs.TaskFields{Status: "todo"}}, Effort: &timelog.Effort{EstimateMinutes: &estimate}},
		"crmimport": {Title: "Import contacts, companies and deals", SalesOn: true},
		"crmimport-checked": {Title: "Import contacts, companies and deals", SalesOn: true, CRMImport: &CRMImportResult{
			Kind: "deals", Check: true, Total: 6, Created: 3, Skipped: 1, Failed: 2, CompaniesCreated: 1, MaxRows: 5000,
			Used:    []csvimport.Mapping{{Field: "name", Header: "Deal"}, {Field: "stage", Header: "Stage"}, {Field: "amount", Header: "Amount"}},
			Ignored: []string{"Owner region", "Lead source"},
			Problems: []string{"Line 3: \"haggling\" is not one of the stages (New, Screening, Meeting, Proposal, Customer, Won, Lost, No response)",
				"Line 5: no active person has the email nobody@example.com"}}},
		"setup":            {Title: "Set up", Message: "password is too short"},
		"setpassword":      {Title: "Choose a password", LinkEmail: "ana@example.com", Token: "tok"},
		"setpassword-done": {Title: "Choose a password", Done: true},
		"mail":             {Title: "Email", InboundReady: true, ScanURL: "https://hearth.example.com/inbound/mail"},
	}
}

func TestViewFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "ui", "src", "views", "fixtures")
	if _, err := os.Stat(filepath.Join("..", "..", "ui")); err != nil {
		t.Skip("ui/ is not in this checkout")
	}
	for name, p := range fixtures() {
		raw, err := json.MarshalIndent(tree(reflect.ValueOf(p), true), "", " ")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw = append(raw, '\n')
		path := filepath.Join(dir, name+".json")
		if *updateFixtures {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(have, raw) {
			t.Errorf("ui/src/views/fixtures/%s.json is out of date: run go test ./internal/web -run TestViewFixtures -update-fixtures", name)
		}
	}
}
