package zohocrm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xuri/excelize/v2"
)

func writeXLSX(t *testing.T, header []string, rows [][]string) string {
	t.Helper()
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	put := func(r int, vals []string) {
		for c, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(c+1, r)
			if err := f.SetCellStr(sheet, cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	put(1, header)
	for i, r := range rows {
		put(i+2, r)
	}
	path := filepath.Join(t.TempDir(), "export.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

var testHeader = []string{"Record Id", "Contact Owner", "Lead Source", "First Name", "Last Name", "Account Name.id", "Account Name",
	"Email", "Title", "Phone", "Home Phone", "Mobile", "Fax", "Mailing Street", "Mailing City", "Mailing State",
	"Mailing Zip", "Mailing Country", "Email Opt Out", "Created By", "Created By.id", "Contact Name", "Twitter", "Vendor Name"}

func testRows() [][]string {
	return [][]string{
		{"100", "Zed Owner", "Web", "Ada", "Lovelace", "A1", "Analytical Engines", "Ada@Example.com", "Chief", "555-1", "555-2", "555-3", "555-4",
			"1 Main St", "Town", "KY", "41000", "US", "false", "Zed", "z1", "Ada Lovelace", "", ""},
		{"101", "Zed Owner", "", "", "Babbage", "A1", "Analytical Engines", "charles@example.com", "", "", "", "", "",
			"", "", "", "", "", "true", "", "", "Babbage", "@cb", ""},
		{"102", "Zed Owner", "", "Grace", "Hopper", "", "", "grace@example.com", "", "555-1", "555-1", "", "",
			"", "", "", "", "", "false", "", "", "Someone Else", "", "Acme"},
	}
}

func TestReadXLSXAndMapping(t *testing.T) {
	rows, err := ReadXLSX(writeXLSX(t, testHeader, testRows()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	b := ContactBody(rows[0], "owner-1", "co-1")
	if b["name"] != "Ada Lovelace" || b["email"] != "ada@example.com" || b["owner_id"] != "owner-1" ||
		b["company_id"] != "co-1" || b["external_id"] != "100" || b["source"] != Source || b["title"] != "Chief" || b["phone"] != "555-1" {
		t.Fatalf("contact body wrong: %v", b)
	}
	if got := b["extra_phones"].([]string); len(got) != 2 || got[0] != "555-2" || got[1] != "555-3" {
		t.Fatalf("extra phones = %v", b["extra_phones"])
	}
	notes := b["notes"].(string)
	for _, want := range []string{"Record Id: 100", "Zoho owner: Zed Owner", "Lead source: Web", "Fax: 555-4",
		"Mailing address: 1 Main St, Town, KY, 41000, US", "Email opt out: false", "Created by: Zed (z1)"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	// A blank first name leaves just the last name, and no company id is sent without one.
	b = ContactBody(rows[1], "owner-1", "")
	if b["name"] != "Babbage" {
		t.Fatalf("name = %v", b["name"])
	}
	if _, has := b["company_id"]; has {
		t.Fatal("company_id sent with no company")
	}
	if !strings.Contains(b["notes"].(string), "Twitter: @cb") || !strings.Contains(b["notes"].(string), "Email opt out: true") {
		t.Fatalf("notes = %q", b["notes"])
	}
	// A phone that repeats the main one is not sent again.
	b = ContactBody(rows[2], "owner-1", "")
	if _, has := b["extra_phones"]; has {
		t.Fatalf("a repeated phone was sent as extra: %v", b["extra_phones"])
	}
	// Vendor Name carried data and goes nowhere: the report must say so.
	if got := UnusedColumns(rows); len(got) != 1 || got[0] != "Vendor Name" {
		t.Fatalf("unused columns = %v", got)
	}
	cb := CompanyBody("Analytical Engines", "A1", "owner-1")
	if cb["kind"] != "customer" || cb["external_id"] != "A1" || cb["owner_id"] != "owner-1" {
		t.Fatalf("company body wrong: %v", cb)
	}
	if acc := Accounts(rows); len(acc) != 1 || acc[0].ID != "A1" {
		t.Fatalf("accounts = %v", acc)
	}
}

func TestReadXLSXRefusesAnotherFile(t *testing.T) {
	_, err := ReadXLSX(writeXLSX(t, []string{"Name", "Mail"}, [][]string{{"a", "b"}}))
	if err == nil || !strings.Contains(err.Error(), "not a Zoho Contacts export") {
		t.Fatalf("err = %v", err)
	}
}

// fakeHearth is the part of Hearth's CRM API the importer uses: list, create with
// external ids that replay, and a unique email that answers 409.
type fakeHearth struct {
	mu        sync.Mutex
	companies map[string]map[string]any // id -> body
	contacts  map[string]map[string]any
	refs      map[string]string // kind:external -> id
	writes    int
	limited   int // answer 429 this many times first
	token     string
}

func newFake() *fakeHearth {
	return &fakeHearth{companies: map[string]map[string]any{}, contacts: map[string]map[string]any{}, refs: map[string]string{}, token: "tok"}
}

func (f *fakeHearth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, `{"error":"no valid token"}`, http.StatusUnauthorized)
		return
	}
	if f.limited > 0 {
		f.limited--
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api")
	out := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && path == "/v1/companies":
		var list []map[string]any
		for id, b := range f.companies {
			list = append(list, map[string]any{"id": id, "name": b["name"]})
		}
		out(200, map[string]any{"companies": list, "total": len(list)})
	case r.Method == http.MethodGet && path == "/v1/contacts":
		var list []map[string]any
		for id, b := range f.contacts {
			list = append(list, map[string]any{"id": id, "email": b["email"]})
		}
		out(200, map[string]any{"contacts": list, "total": len(list)})
	case r.Method == http.MethodPost && (path == "/v1/companies" || path == "/v1/contacts"):
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		kind := strings.TrimPrefix(path, "/v1/")
		key := kind + ":" + b["source"].(string) + ":" + b["external_id"].(string)
		if id, ok := f.refs[key]; ok {
			w.Header().Set("Idempotent-Replay", "true")
			out(200, map[string]any{"id": id})
			return
		}
		store := f.companies
		if kind == "contacts" {
			store = f.contacts
			for id, c := range store {
				if c["email"] == b["email"] {
					out(409, map[string]any{"error": "that email is taken", "existing_id": id})
					return
				}
			}
		}
		id := kind + "-" + string(rune('a'+len(store)))
		store[id] = b
		f.refs[key] = id
		f.writes++
		out(201, map[string]any{"id": id})
	default:
		http.NotFound(w, r)
	}
}

func runAgainst(t *testing.T, f *fakeHearth, rows []Row, dry bool) (*Report, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	var manifest bytes.Buffer
	rep, err := Run(context.Background(), Config{BaseURL: srv.URL, Token: "tok", OwnerID: "owner-1", DryRun: dry,
		PerSecond: 1000, Manifest: &manifest}, rows)
	if err != nil {
		t.Fatal(err)
	}
	return rep, manifest.String()
}

func loadRows(t *testing.T) []Row {
	rows, err := ReadXLSX(writeXLSX(t, testHeader, testRows()))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRunCreatesThenSecondRunChangesNothing(t *testing.T) {
	f := newFake()
	rows := loadRows(t)
	rep, manifest := runAgainst(t, f, rows, false)
	if rep.CompaniesCreated != 1 || rep.ContactsCreated != 3 || rep.ContactsFailed != 0 || rep.NameMismatches != 1 {
		t.Fatalf("first run: %+v", rep)
	}
	if len(f.companies) != 1 || len(f.contacts) != 3 {
		t.Fatalf("hearth holds %d companies and %d contacts", len(f.companies), len(f.contacts))
	}
	// Both contacts of the account are on the one company, and every record has the owner.
	for _, c := range f.contacts {
		if c["owner_id"] != "owner-1" {
			t.Fatalf("a contact has owner %v", c["owner_id"])
		}
	}
	if strings.Contains(manifest, "@") || strings.Contains(manifest, "Lovelace") {
		t.Fatalf("the manifest holds personal data: %s", manifest)
	}
	if n := strings.Count(manifest, `"action":"created"`); n != 4 {
		t.Fatalf("manifest created lines = %d", n)
	}

	writes := f.writes
	rep, _ = runAgainst(t, f, rows, false)
	if rep.CompaniesCreated != 0 || rep.ContactsCreated != 0 || rep.CompaniesExisting != 1 || rep.ContactsExisting != 3 {
		t.Fatalf("second run: %+v", rep)
	}
	if f.writes != writes {
		t.Fatalf("the second run wrote %d records", f.writes-writes)
	}
}

func TestRunNeverDuplicatesWhatIsAlreadyThere(t *testing.T) {
	f := newFake()
	// A company with the same name (other case) and a contact with the same email, made by hand.
	f.companies["co-hand"] = map[string]any{"name": "ANALYTICAL engines"}
	f.contacts["ct-hand"] = map[string]any{"email": "ada@example.com"}
	rep, manifest := runAgainst(t, f, loadRows(t), false)
	if rep.CompaniesCreated != 0 || rep.CompaniesExisting != 1 || rep.ContactsCreated != 2 || rep.ContactsExisting != 1 {
		t.Fatalf("%+v", rep)
	}
	if len(f.companies) != 1 {
		t.Fatalf("a second company was made")
	}
	if !strings.Contains(manifest, "co-hand") || !strings.Contains(manifest, "ct-hand") {
		t.Fatalf("the manifest does not name what was found: %s", manifest)
	}
	// The hand-made contact was not edited.
	if len(f.contacts["ct-hand"]) != 1 {
		t.Fatalf("the existing contact was changed: %v", f.contacts["ct-hand"])
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	f := newFake()
	rep, _ := runAgainst(t, f, loadRows(t), true)
	if f.writes != 0 || rep.ContactsCreated != 3 || rep.CompaniesCreated != 1 {
		t.Fatalf("writes %d, %+v", f.writes, rep)
	}
}

func TestRunWaitsOutARateLimit(t *testing.T) {
	f := newFake()
	f.limited = 2
	rep, _ := runAgainst(t, f, loadRows(t), false)
	if rep.ContactsCreated != 3 {
		t.Fatalf("%+v", rep)
	}
}

func TestRunNeedsItsInputs(t *testing.T) {
	if _, err := Run(context.Background(), Config{BaseURL: "http://x", Token: "t"}, nil); err == nil {
		t.Fatal("ran with no owner")
	}
}

func TestRunStopsOnABadToken(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()
	_, err := Run(context.Background(), Config{BaseURL: srv.URL, Token: "wrong", OwnerID: "o", PerSecond: 1000}, loadRows(t))
	if err == nil || f.writes != 0 {
		t.Fatalf("err = %v, writes = %d", err, f.writes)
	}
}
