package mauticsync_test

// The sync against a real Postgres and a fake Mautic: what goes out, that sending twice is one send, what comes back onto the
// timeline, and that an opt-out stays an opt-out on both sides.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/mauticsync"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// fake is Mautic, in memory.
type fake struct {
	mu        sync.Mutex
	next      int64
	contacts  map[int64]mautic.Fields
	byEmail   map[string]int64
	companies map[int64]mautic.Fields
	segments  map[string]int64
	members   map[int64]map[int64]bool // segment id to contact ids
	dnc       map[int64]int            // contact id to reason
	rows      map[string][]mautic.Row
	calls     int
}

func newFake() *fake {
	return &fake{next: 100, contacts: map[int64]mautic.Fields{}, byEmail: map[string]int64{}, companies: map[int64]mautic.Fields{},
		segments: map[string]int64{}, members: map[int64]map[int64]bool{}, dnc: map[int64]int{}, rows: map[string][]mautic.Row{}}
}

func (f *fake) id() int64 { f.next++; return f.next }

func (f *fake) UpsertContact(_ context.Context, id int64, fl mautic.Fields) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if id == 0 {
		if e, _ := fl["email"].(string); e != "" {
			if got, ok := f.byEmail[strings.ToLower(e)]; ok {
				id = got
			}
		}
	}
	if id == 0 {
		id = f.id()
		f.contacts[id] = mautic.Fields{}
	}
	if _, ok := f.contacts[id]; !ok {
		return 0, &mautic.APIError{Status: 404}
	}
	for k, v := range fl {
		f.contacts[id][k] = v
	}
	if e, _ := fl["email"].(string); e != "" {
		f.byEmail[strings.ToLower(e)] = id
	}
	return id, nil
}
func (f *fake) UpsertCompany(_ context.Context, id int64, fl mautic.Fields) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == 0 {
		id = f.id()
	}
	f.companies[id] = fl
	return id, nil
}
func (f *fake) LinkCompany(context.Context, int64, int64) error   { return nil }
func (f *fake) UnlinkCompany(context.Context, int64, int64) error { return nil }
func (f *fake) AddDoNotContact(_ context.Context, id int64, reason int, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dnc[id] = reason
	return nil
}
func (f *fake) Segments(context.Context) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]int64{}
	for k, v := range f.segments {
		m[k] = v
	}
	return m, nil
}
func (f *fake) CreateSegment(_ context.Context, _, alias, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id()
	f.segments[alias] = id
	f.members[id] = map[int64]bool{}
	return id, nil
}
func (f *fake) SegmentAdd(_ context.Context, s, c int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[s][c] = true
	return nil
}
func (f *fake) SegmentRemove(_ context.Context, s, c int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.members[s], c)
	return nil
}
func (f *fake) EnsureField(context.Context, string, string, string) error { return nil }
func (f *fake) FieldDefs(context.Context, string) (map[string]mautic.FieldDef, error) {
	return nil, nil
}
func (f *fake) FindCompany(_ context.Context, name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, c := range f.companies {
		if n, _ := c["companyname"].(string); strings.EqualFold(n, name) {
			return id, nil
		}
	}
	return 0, nil
}
func (f *fake) Users(context.Context) (map[string]int64, error) {
	return map[string]int64{"owner@example.test": 7}, nil
}
func (f *fake) Stats(_ context.Context, table, _ string, limit int, w ...mautic.Where) ([]mautic.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if table == "lead_donotcontact" {
		var out []mautic.Row
		for c, r := range f.dnc {
			out = append(out, mautic.Row{"id": strconv.FormatInt(c, 10), "lead_id": strconv.FormatInt(c, 10), "channel": "email",
				"reason": strconv.Itoa(r), "date_added": "2026-11-20 02:00:00", "comments": f.rowsComment(c)})
		}
		return filter(out, w), nil
	}
	return filter(f.rows[table], w), nil
}
func (f *fake) rowsComment(int64) string { return "" }
func filter(rows []mautic.Row, w []mautic.Where) []mautic.Row {
	var out []mautic.Row
	for _, r := range rows {
		ok := true
		for _, c := range w {
			switch {
			case c.Col == "id" && c.Expr == "gt":
				a, _ := strconv.ParseInt(r["id"], 10, 64)
				b, _ := strconv.ParseInt(c.Val, 10, 64)
				ok = ok && a > b
			case c.Col == "is_read":
				ok = ok && r["is_read"] == c.Val
			case c.Col == "date_read" && c.Expr == "gte":
				ok = ok && r["date_read"] >= c.Val
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}
func (f *fake) Email(context.Context, int64) (mautic.Names, error) {
	return mautic.Names{Name: "Spring", Subject: "Spring news"}, nil
}
func (f *fake) Form(context.Context, int64) (string, error) { return "Contact us", nil }

func TestTheSyncSendsContactsOnceBringsActivityBackAndNeverResubscribes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	org, who := uuid.NewString(), uuid.NewString()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, q, a...); err != nil {
			t.Fatalf("setup: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mautic Test')`, org, "ms-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Owner')`, who, org)
	exec(`INSERT INTO user_identities (principal_id, issuer, subject, email) VALUES ($1,'t',$2,'owner@example.test')`, who, who)
	t.Cleanup(func() {
		for _, q := range []string{`SELECT audit_purge_org($1)`, `DELETE FROM crm_activities WHERE org_id=$1`, `DELETE FROM external_refs WHERE org_id=$1`,
			`DELETE FROM mautic_sync_state WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`, `DELETE FROM companies WHERE org_id=$1`,
			`DELETE FROM company_types WHERE org_id=$1`, `DELETE FROM outbox WHERE org_id=$1`, `DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	exec(`INSERT INTO company_types (id, org_id, key, label, erp_role, position) VALUES (gen_random_uuid(), $1, 'client', 'Client', 'customer', 1)`, org)
	co, c1, c2, c3 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO companies (id, org_id, name, domain, owner_id, created_by) VALUES ($1,$2,'Acme','acme.test',$3,$3)`, co, org, who)
	exec(`INSERT INTO company_type_links (company_id, type_id) SELECT $1, id FROM company_types WHERE org_id=$2 AND key='client'`, co, org)
	exec(`INSERT INTO company_tags (company_id, tag) VALUES ($1,'Expo')`, co)
	mkc := func(id, name, email, company string) {
		var cid any
		if company != "" {
			cid = company
		}
		exec(`INSERT INTO contacts (id, org_id, company_id, name, email, created_by) VALUES ($1,$2,$3,$4,$5,$6)`, id, org, cid, name, email, who)
	}
	mkc(c1, "Ada Lovelace", "ada@example.test", co)
	mkc(c2, "Grace Hopper", "grace@example.test", "")
	mkc(c3, "No Address", "not an address", "")

	f := newFake()
	s := mauticsync.New(pools.Web, f, org)

	// A dry run writes nothing anywhere.
	rep, err := s.Run(ctx, mauticsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.contacts) != 0 || len(f.segments) != 0 {
		t.Fatalf("dry run wrote to Mautic: %d contacts %d segments", len(f.contacts), len(f.segments))
	}
	var n int
	_ = pools.Web.QueryRow(ctx, `SELECT count(*) FROM mautic_links WHERE org_id=$1`, org).Scan(&n)
	if n != 0 || rep.Count("contact", "create") != 2 {
		t.Fatalf("dry run: links=%d, planned creates=%d", n, rep.Count("contact", "create"))
	}

	// The real run sends two and skips the one with no usable address.
	rep, err = s.Run(ctx, mauticsync.Options{Write: true})
	if err != nil || rep.Failed() {
		t.Fatalf("run: %v failed=%v", err, rep.Failed())
	}
	if len(f.contacts) != 2 || rep.Count("contact", "skip") != 1 {
		t.Fatalf("contacts in Mautic = %d, skipped = %d", len(f.contacts), rep.Count("contact", "skip"))
	}
	ada := f.byEmail["ada@example.test"]
	if f.contacts[ada]["owner"] != int64(7) || f.contacts[ada]["firstname"] != "Ada" {
		t.Fatalf("ada = %v", f.contacts[ada])
	}
	for _, a := range []string{"hearth-all", "hearth-type-client"} {
		if !f.members[f.segments[a]][ada] {
			t.Errorf("ada is not in %s", a)
		}
	}
	if f.members[f.segments["hearth-type-client"]][f.byEmail["grace@example.test"]] {
		t.Error("grace has no company and is in the client segment")
	}

	// Sending again is nothing.
	calls := f.calls
	rep, err = s.Run(ctx, mauticsync.Options{Write: true})
	if err != nil || f.calls != calls || rep.Count("contact", "same") != 2 {
		t.Fatalf("second run: err=%v calls %d to %d, same=%d", err, calls, f.calls, rep.Count("contact", "same"))
	}

	// Taking the type away takes ada out of its segment.
	exec(`DELETE FROM company_type_links WHERE company_id=$1`, co)
	if _, err = s.Run(ctx, mauticsync.Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if f.members[f.segments["hearth-type-client"]][ada] || !f.members[f.segments["hearth-all"]][ada] {
		t.Fatal("ada's segments did not follow the company's types")
	}

	// Activity comes back once.
	aid := strconv.FormatInt(ada, 10)
	f.rows["email_stats"] = []mautic.Row{{"id": "1", "lead_id": aid, "email_id": "5", "email_address": "ada@example.test", "is_read": "1",
		"date_sent": "2026-11-20 03:00:00", "date_read": "2026-11-20 03:05:00"}}
	f.rows["page_hits"] = []mautic.Row{{"id": "1", "lead_id": aid, "email_id": "5", "url": "https://example.test/x", "date_hit": "2026-11-20 03:06:00"}}
	f.rows["form_submissions"] = []mautic.Row{{"id": "1", "lead_id": aid, "form_id": "2", "date_submitted": "2026-11-20 03:07:00"}}
	for i := 0; i < 2; i++ {
		if _, err = s.Run(ctx, mauticsync.Options{Write: true}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := pools.Web.Query(ctx, `SELECT a.title FROM crm_activities a JOIN crm_activity_targets t ON t.activity_id=a.id WHERE t.target_id=$1 AND t.target_type='contact' ORDER BY a.occurred_at`, c1)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		titles = append(titles, s)
	}
	rows.Close()
	want := "Email sent: Spring news|Email opened: Spring news|Clicked a link in: Spring news|Submitted a form: Contact us"
	if strings.Join(titles, "|") != want {
		t.Fatalf("timeline = %v", titles)
	}
	var onCompany int
	_ = pools.Web.QueryRow(ctx, `SELECT count(*) FROM crm_activity_targets WHERE target_type='company' AND target_id=$1`, co).Scan(&onCompany)
	if onCompany != 4 {
		t.Fatalf("company timeline has %d of the 4 notes", onCompany)
	}

	// An unsubscribe in Mautic is an opt-out in Hearth, once on the timeline.
	f.mu.Lock()
	f.dnc[ada] = mautic.DNCUnsubscribed
	f.mu.Unlock()
	for i := 0; i < 2; i++ {
		if _, err = s.Run(ctx, mauticsync.Options{Write: true}); err != nil {
			t.Fatal(err)
		}
	}
	var reason string
	var at any
	_ = pools.Web.QueryRow(ctx, `SELECT marketing_opt_out_reason, marketing_opt_out_at FROM contacts WHERE id=$1`, c1).Scan(&reason, &at)
	if reason != "unsubscribed" || at == nil {
		t.Fatalf("opt-out = %q %v", reason, at)
	}
	var unsub int
	_ = pools.Web.QueryRow(ctx, `SELECT count(*) FROM crm_activities WHERE org_id=$1 AND title='Unsubscribed from email'`, org).Scan(&unsub)
	if unsub != 1 {
		t.Fatalf("%d unsubscribe notes", unsub)
	}

	// Mautic loses the do-not-contact: Hearth puts it back, and the opt-out stays.
	f.mu.Lock()
	delete(f.dnc, ada)
	f.mu.Unlock()
	rep, err = s.Run(ctx, mauticsync.Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.dnc[ada] == 0 {
		t.Fatal("the do-not-contact was not put back")
	}
	_ = pools.Web.QueryRow(ctx, `SELECT marketing_opt_out_reason FROM contacts WHERE id=$1`, c1).Scan(&reason)
	if reason != "unsubscribed" {
		t.Fatalf("the opt-out was cleared: %q", reason)
	}

	// An opt-out made in Hearth reaches Mautic.
	exec(`UPDATE contacts SET marketing_opt_out_at=now(), marketing_opt_out_reason='manual' WHERE id=$1`, c2)
	if _, err = s.Run(ctx, mauticsync.Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if f.dnc[f.byEmail["grace@example.test"]] != mautic.DNCManual {
		t.Fatalf("grace's opt-out did not reach Mautic: %v", f.dnc)
	}

	// A deleted contact leaves its segments.
	exec(`UPDATE contacts SET deleted_at=now() WHERE id=$1`, c2)
	if _, err = s.Run(ctx, mauticsync.Options{Write: true}); err != nil {
		t.Fatal(err)
	}
	if f.members[f.segments["hearth-all"]][f.byEmail["grace@example.test"]] {
		t.Fatal("a deleted contact is still in a segment")
	}
	_ = fmt.Sprint()
}
