package web

// The pages that fill the width of the screen (the dashboard, inbox, outbox, settings, unassigned, channels and contacts) are
// checked at several window widths by ui/layout/fill.mjs, against these page values: full ones, with every section and every
// column a person with real work would have, so the check is not run against a page that was nearly empty.

import (
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	cal "github.com/Embernet-ai/hearth/internal/calendar"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/offline"
)

func fillFixtures(end time.Time) map[string]page {
	day := func(i int) time.Time { return time.Date(2026, 11, 15+i, 0, 0, 0, 0, time.UTC) }
	due := day(3)
	task := func(id, title, status string) *docs.Row {
		return &docs.Row{Document: &docs.Document{ID: id, Kind: "task", Title: title, Task: &docs.TaskFields{Status: status, DueAt: &due}}}
	}
	mine := []*docs.Row{
		task("m1", "Replace the guard switch on line 3", "in_progress"),
		task("m2", "Quote the conveyor retrofit for Acme Motors", "todo"),
		task("m3", "Torque check on the press brake", "todo"),
		task("m4", "Order spare encoders", "blocked"),
	}
	var week []dashDay
	for i := 0; i < 7; i++ {
		d := dashDay{Date: day(i), Label: day(i).Format("2 Jan"), Weekday: day(i).Format("Mon"), Today: i == 2, Selected: i == 2,
			Weekend: i == 0 || i == 6, Href: dayLink(day(i))}
		if i%2 == 0 {
			d.Count = 1
			d.Items = mine[i/2 : i/2+1]
		}
		if i == 2 {
			d.Events = []cal.Entry{{ID: "e1", Kind: cal.KindEvent, Title: "Out: Jeff Ryan", AllDay: true, StartsAt: day(i)},
				{ID: "e2", Kind: cal.KindEvent, Title: "Fireball Industries / Spocky: technical integration call", StartsAt: day(i).Add(17 * time.Hour)}}
			d.Count += 2
		}
		week = append(week, d)
	}
	tile := func(label, value, period string) statTile {
		return statTile{Label: label, Value: value, Period: period, Href: "/documents"}
	}
	stats := &statsPanel{
		Person: personRef{ID: "me", Name: "Me"}, Self: true,
		Groups: []statGroup{
			{Key: "hours", Title: "Hours", Tiles: []statTile{
				{Label: "Logged this week", Value: "12h 30m", Detail: "5 entries", Period: "16 to 22 Nov", Href: "/time", Of: "40h", Percent: 31, HasPercent: true,
					Note: "No weekly hours are set for this person, so the standard 40 hour week is used."},
				{Label: "Logged this month", Value: "283h 2m", Period: "1 to 21 Nov", Href: "/time", Of: "136h", Percent: 208, HasPercent: true},
				tile("Waiting for approval", "4h", "1 to 21 Nov"),
				{Label: "Sent back", Value: "1h", Period: "1 to 21 Nov", Href: "/time", Tone: "bad"},
			}},
			{Key: "work", Title: "Work", Tiles: []statTile{
				tile("Open and assigned", "9", "right now"), {Label: "Overdue", Value: "3", Period: "due before 21 Nov", Href: "/documents", Tone: "bad"},
				tile("Due soon", "2", "21 to 27 Nov"), tile("Completed this week", "5", "16 to 22 Nov"),
				tile("Completed this month", "17", "November 2026"), {Label: "Daily reports filed", Value: "9", Of: "15 working days", Percent: 60, HasPercent: true, Period: "1 to 21 Nov", Href: "/reports/daily"},
			}},
			{Key: "deals", Title: "Deals", Tiles: []statTile{
				{Label: "Open pipeline", Value: "$18.07M", Exact: "$18,066,007.28", Detail: "135 open deals", Period: "right now", Href: "/deals", Note: "All owners' deals. Only mine narrows it to yours."},
				tile("Past expected close", "42", "open, close date before 21 Nov"), {Label: "Won this month", Value: "$1.60M", Exact: "$1,603,521.28", Detail: "13 deals", Period: "November 2026", Href: "/deals"}, tile("Lost this month", "45", "November 2026"),
			}},
		},
		WeeksTarget: "40h",
		Stages: []barRow{{Label: "New", Value: "2, $576,000", Percent: 2, Href: "/deals"}, {Label: "Screening", Value: "3, $316,000", Percent: 3, Href: "/deals"},
			{Label: "Meeting", Value: "108, $6.30M", Exact: "108, $6,296,029", Percent: 100, Href: "/deals"}, {Label: "Proposal", Value: "13, $4.37M", Exact: "13, $4,374,401", Percent: 12, Href: "/deals"}, {Label: "Customer", Value: "9, $6.50M", Exact: "9, $6,503,577.28", Percent: 8, Href: "/deals"}},
		Recent: []recentRow{{Kind: "Deal won", Title: "Line upgrade", Detail: "$4,000", Href: "/deals/d1"}, {Kind: "Deal won", Title: "Press retrofit", Detail: "$14,000", Href: "/deals/d2"}},
	}
	for i, h := range []string{"28 Sep", "5 Oct", "12 Oct", "19 Oct", "26 Oct", "2 Nov", "9 Nov", "16 Nov"} {
		stats.Weeks = append(stats.Weeks, barRow{Label: h, Value: fmt.Sprintf("%dh", 30+i), Percent: 60 + i*5, Href: "/time", Current: i == 7})
	}
	dash := &dashboard{ReportDay: "2026-11-21", Mine: mine, Week: week, Stats: stats, WeekTitle: "15 to 21 Nov", PrevWeek: "/?day=2026-11-08", NextWeek: "/?day=2026-11-22", Today: "/?day=2026-11-17",
		Month:    buildMonth(day(2), day(2), nil, mine),
		Watching: []*docs.Row{task("w1", "Shutdown window plan", "in_progress"), task("w2", "Fire watch roster", "todo"), task("w3", "Paint booth filter change", "todo")},
		Upcoming: []cal.Entry{{ID: "u1", Title: "Safety walk", StartsAt: end, Place: "Plant floor"}, {ID: "u2", Title: "Line 3 shutdown", StartsAt: end.AddDate(0, 0, 2), AllDay: true},
			{ID: "u3", Title: "Replace the guard switch on line 3", DocumentID: "m1", StartsAt: end.AddDate(0, 0, 3), AllDay: true}}}

	ago := func(h int) time.Time { return end.Add(-time.Duration(h) * time.Hour) }
	read := ago(30)
	notes := []*notify.Notification{
		{ID: "n1", Reason: "assigned", EntityType: "document", EntityID: "m1", Title: "Replace the guard switch on line 3", Excerpt: "Yara assigned this to you. Due Wednesday, the line is down until it is done.", ActorName: "Yara", CreatedAt: ago(1)},
		{ID: "n2", Reason: "mention", EntityType: "document", EntityID: "m2", Title: "Quote the conveyor retrofit", Excerpt: "Can you check the freight on this before it goes out?", ActorName: "Gabriel", CreatedAt: ago(3)},
		{ID: "n3", Reason: "comment", EntityType: "document", EntityID: "m3", Title: "Torque check on the press brake", ActorName: "Aria", CreatedAt: ago(9)},
		{ID: "n4", Reason: "status", EntityType: "document", EntityID: "m4", Title: "Order spare encoders", Excerpt: "Moved to blocked: waiting on the supplier.", ActorName: "Constanza", CreatedAt: ago(26), ReadAt: &read},
	}
	from, to, digest := 22*60, 6*60, 8*60

	queued := ago(5)
	eid := "m1"
	outbox := []*offline.Record{
		{ID: "o1", Kind: "comment", EntityID: &eid, QueuedAt: queued, ReceivedAt: end, Status: "conflict", Message: "The job changed while you were out."},
		{ID: "o2", Kind: "status", EntityID: &eid, QueuedAt: queued, ReceivedAt: end, Status: "unknown", Body: "{}"},
	}
	recent := []*offline.Record{
		{ID: "r1", Kind: "time", EntityID: &eid, QueuedAt: queued, ReceivedAt: end, Status: "applied"},
		{ID: "r2", Kind: "comment", EntityID: &eid, QueuedAt: ago(7), ReceivedAt: end, Status: "applied"},
		{ID: "r3", Kind: "status", EntityID: &eid, QueuedAt: ago(8), ReceivedAt: end, Status: "discarded"},
	}

	owners := []auth.Person{{PrincipalID: "p1", DisplayName: "Yara", Status: "active"}, {PrincipalID: "p2", DisplayName: "Gabriel", Status: "active"}, {PrincipalID: "p3", DisplayName: "Aria", Status: "active"}}
	unowned := []*crm.Unowned{
		{Ref: core.Ref{Type: core.EntityCompany, ID: "c1"}, Name: "Acme Motors", Subtext: "acme.example.com", CreatedAt: end},
		{Ref: core.Ref{Type: core.EntityCompany, ID: "c2"}, Name: "Riverside Tool and Die", Subtext: "riverside.example.com, 12 contacts", CreatedAt: end},
		{Ref: core.Ref{Type: core.EntityCompany, ID: "c3"}, Name: "Northline Packaging", Subtext: "northline.example.com", CreatedAt: end},
	}

	chans := []*channels.Channel{
		{ID: "ch1", Name: "Line 3 maintenance", Topic: "Faults, parts and who is on the floor", Visibility: "public", Member: true, Members: 14, Attached: 22, Unread: 3},
		{ID: "ch2", Name: "Sales floor", Topic: "Quotes in flight and who owns the follow up", Visibility: "private", Member: true, Members: 6, Attached: 9},
		{ID: "ch3", Name: "Shutdown planning", Topic: "The December window", Visibility: "public", Members: 21, Attached: 31, Unread: 0},
		{ID: "ch4", Name: "Safety", Topic: "Near misses and walk results", Visibility: "public", Member: true, Members: 40, Attached: 12, Unread: 8},
	}
	co := "co1"
	rows := []*crm.ContactRow{
		{Contact: crm.Contact{ID: "k1", Name: "Ada Lovelace", CompanyID: &co, CompanyName: "Acme Motors", Title: "Service manager", Email: "ada@acme.example.com", Phone: "555 0100"}, OwnerName: "Yara"},
		{Contact: crm.Contact{ID: "k2", Name: "Grace Hopper", CompanyID: &co, CompanyName: "Acme Motors", Title: "Plant engineer", Email: "grace@acme.example.com", Phone: "555 0101"}, OwnerName: "Gabriel"},
		{Contact: crm.Contact{ID: "k3", Name: "Alan Turing", Title: "Electrician", Email: "alan@example.com", Phone: "555 0102"}},
	}
	plain := []*crm.Contact{&rows[0].Contact, &rows[1].Contact, &rows[2].Contact}
	links := map[string]string{"name": "/contacts?sort=name", "company": "/contacts?sort=company", "title": "/contacts?sort=title", "email": "/contacts?sort=email", "owner": "/contacts?sort=owner"}

	return map[string]page{
		"dashboard-full": {Title: "Dashboard", Dash: dash, MyPending: 4},
		"inbox-full":     {Title: "Inbox", Notifications: notes, Prefs: notify.Prefs{Email: "digest", QuietFrom: &from, QuietTo: &to, Timezone: "America/New_York", DigestAt: digest}},
		"inbox-empty":    {Title: "Inbox", UnreadOnly: true, Prefs: notify.Prefs{Email: "immediate", Timezone: "America/New_York", DigestAt: digest}},
		"outbox-full":    {Title: "Outbox", Heading: "Outbox", Outbox: outbox, OutboxRecent: recent, OutboxTitles: map[string]string{"m1": "Replace the guard switch on line 3"}},
		"outbox-empty":   {Title: "Outbox", Heading: "Outbox"},
		"unassigned-full": {Title: "Unassigned", UnownedType: "company", Unowned: unowned, Owners: owners,
			UnownedCounts: map[string]int{"company": 3, "contact": 7, "deal": 2}, Pager: &pager{From: 1, To: 3, Total: 3}},
		"channels-full": {Title: "Channels", Channels: chans},
		"contacts-full": {Title: "Contacts", SalesOn: true, ContactRows: rows, Owners: owners, SortLinks: links, ExportURL: "/contacts.csv", Pager: &pager{From: 1, To: 3, Total: 3},
			Companies: []*crm.Company{{ID: "co1", Name: "Acme Motors"}}},
		"contacts-plain":      {Title: "Contacts", Contacts: plain, Companies: []*crm.Company{{ID: "co1", Name: "Acme Motors"}}},
		"settings-everything": {Title: "Settings", IsAdmin: true, Can: authz.Held{authz.People: true, authz.Agents: true, authz.Audit: true, authz.Finance: true}},
	}
}
