// Package zohocrm brings a Zoho CRM "Contacts" export (the .xlsx the Contacts
// module downloads) into Hearth's CRM through Hearth's own HTTP API.
//
// # Why the API and not the database
//
// The API is the supported way in. It applies the same rules as the pages
// (an email is lowercased and unique, a company needs a name, an unknown field is
// refused), it records who made what, and it works against a deployment this
// code has no database credentials for, such as a customer's. Nothing here
// writes SQL.
//
// # What moves
//
// One company per distinct Account Name (kind customer, matched on the Zoho
// account id), one contact per row (matched on the Zoho record id). A contact is
// put on its company and owned by the person the caller names. Hearth has no
// separate field for most of what Zoho keeps, so every non-empty column that
// has no field of its own goes into the contact's Notes as a "Label: value"
// line. Nothing is dropped except Contact Name, which is First Name and Last Name
// put together, and is counted when it disagrees.
//
// # Safe to run twice
//
// Every create carries an external id, so a second run answers with the record
// that exists and changes nothing. Before it writes, the importer reads what the
// instance already holds: a company with the same name (ignoring case) or a
// contact with the same email is used as it is and counted as skipped, never
// created again and never edited.
package zohocrm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Source is the external-id source every record carries.
const Source = "zoho-crm"

// Row is one data row of the export, by column header. Values are trimmed.
type Row map[string]string

// Get returns a column's value, empty when the column is absent.
func (r Row) Get(col string) string { return strings.TrimSpace(r[col]) }

// ReadXLSX reads the first sheet of a Zoho export. A row with nothing in it is
// skipped. The header row must carry "Record Id", "Last Name" and "Email".
func ReadXLSX(path string) ([]Row, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("zohocrm: open %s: %w", path, err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("zohocrm: %s has no sheets", path)
	}
	it, err := f.Rows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("zohocrm: read sheet %q: %w", sheets[0], err)
	}
	defer it.Close()
	var header []string
	var out []Row
	for it.Next() {
		cols, err := it.Columns()
		if err != nil {
			return nil, fmt.Errorf("zohocrm: read a row: %w", err)
		}
		if header == nil {
			header = make([]string, len(cols))
			for i, c := range cols {
				header[i] = strings.TrimSpace(c)
			}
			for _, need := range []string{"Record Id", "Last Name", "Email"} {
				if !contains(header, need) {
					return nil, fmt.Errorf("zohocrm: %s is not a Zoho Contacts export: no %q column", path, need)
				}
			}
			continue
		}
		row := Row{}
		any := false
		for i, c := range cols {
			if i >= len(header) || header[i] == "" {
				continue
			}
			v := strings.TrimSpace(c)
			if v != "" {
				any = true
			}
			row[header[i]] = v
		}
		if any {
			out = append(out, row)
		}
	}
	return out, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ContactName is First Name and Last Name joined, or the one that is there.
func ContactName(r Row) string {
	return strings.TrimSpace(r.Get("First Name") + " " + r.Get("Last Name"))
}

// noteLines are the columns that go into Notes, in the order they are written,
// with the label each one gets. Columns that have a field of their own (name,
// email, title, phone, the account) are not here.
var noteLines = []struct{ col, label string }{
	{"Record Id", "Record Id"},
	{"Contact Owner", "Zoho owner"},
	{"Contact Owner.id", "Zoho owner id"},
	{"Lead Source", "Lead source"},
	{"Salutation", "Salutation"},
	{"Department", "Department"},
	{"Industry", "Industry"},
	{"Fax", "Fax"},
	{"Asst Phone", "Assistant phone"},
	{"Other Phone", "Other phone"},
	{"Assistant", "Assistant"},
	{"Date of Birth", "Date of birth"},
	{"Skype ID", "Skype"},
	{"Twitter", "Twitter"},
	{"Secondary Email", "Secondary email"},
	{"Reporting To", "Reporting to"},
	{"Tag", "Tag"},
	{"Description", "Description"},
	{"Email Opt Out", "Email opt out"},
	{"Unsubscribed Mode", "Unsubscribed mode"},
	{"Unsubscribed Time", "Unsubscribed"},
	{"Locked", "Locked"},
	{"Created Time", "Created"},
	{"Modified Time", "Modified"},
	{"Last Activity Time", "Last activity"},
	{"Change Log Time", "Change log time"},
}

// usedColumns are the columns the importer reads, directly or through
// Notes, so the report can name any column that carried data and went nowhere.
var usedColumns = func() map[string]bool {
	m := map[string]bool{
		"First Name": true, "Last Name": true, "Email": true, "Title": true, "Phone": true,
		"Account Name": true, "Account Name.id": true, "Home Phone": true, "Mobile": true,
		"Created By": true, "Created By.id": true, "Modified By": true, "Modified By.id": true,
		"Mailing Street": true, "Mailing City": true, "Mailing State": true, "Mailing Zip": true, "Mailing Country": true,
		"Other Street": true, "Other City": true, "Other State": true, "Other Zip": true, "Other Country": true,
		// Contact Name is First and Last put together and is checked, not kept.
		"Contact Name": true,
	}
	for _, n := range noteLines {
		m[n.col] = true
	}
	return m
}()

// UnusedColumns lists the columns that carried data in some row and that the
// importer does not move. A non-empty answer means data would be dropped.
func UnusedColumns(rows []Row) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		for c, v := range r {
			if v != "" && !usedColumns[c] {
				seen[c] = true
			}
		}
	}
	var out []string
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func addressLine(r Row, prefix string) string {
	var parts []string
	for _, f := range []string{"Street", "City", "State", "Zip", "Country"} {
		if v := r.Get(prefix + " " + f); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// Notes is the contact's Notes text: every column without a field of its own,
// as "Label: value" lines.
func Notes(r Row) string {
	var lines []string
	add := func(label, v string) {
		if v != "" {
			lines = append(lines, label+": "+v)
		}
	}
	for _, n := range noteLines {
		add(n.label, r.Get(n.col))
		// The two addresses and the who-did-it lines sit with the dates.
		if n.col == "Locked" {
			add("Mailing address", addressLine(r, "Mailing"))
			add("Other address", addressLine(r, "Other"))
			add("Created by", byLine(r.Get("Created By"), r.Get("Created By.id")))
			add("Modified by", byLine(r.Get("Modified By"), r.Get("Modified By.id")))
		}
	}
	return strings.Join(lines, "\n")
}

func byLine(name, id string) string {
	switch {
	case name != "" && id != "":
		return name + " (" + id + ")"
	case name != "":
		return name
	}
	return id
}

// ContactBody is the POST /v1/contacts body for a row, owned by ownerID and put
// on companyID when it is not empty.
func ContactBody(r Row, ownerID, companyID string) map[string]any {
	b := map[string]any{
		"name":        ContactName(r),
		"email":       strings.ToLower(r.Get("Email")),
		"notes":       Notes(r),
		"owner_id":    ownerID,
		"external_id": r.Get("Record Id"),
		"source":      Source,
	}
	if v := r.Get("Title"); v != "" {
		b["title"] = v
	}
	if v := r.Get("Phone"); v != "" {
		b["phone"] = v
	}
	var extra []string
	for _, c := range []string{"Home Phone", "Mobile"} {
		if v := r.Get(c); v != "" && v != r.Get("Phone") {
			extra = append(extra, v)
		}
	}
	if len(extra) > 0 {
		b["extra_phones"] = extra
	}
	if companyID != "" {
		b["company_id"] = companyID
	}
	return b
}

// CompanyBody is the POST /v1/companies body for an account.
func CompanyBody(name, accountID, ownerID string) map[string]any {
	b := map[string]any{
		"name":     name,
		"kind":     "customer",
		"owner_id": ownerID,
		"source":   Source,
	}
	if accountID != "" {
		b["external_id"] = accountID
		b["notes"] = "Zoho account id: " + accountID
	}
	return b
}

// Account is a distinct Account Name in the export.
type Account struct {
	ID   string
	Name string
}

// accountKey identifies an account: its Zoho id, or its lowercase name when the
// row carries no id.
func accountKey(id, name string) string {
	if id != "" {
		return id
	}
	return "name:" + strings.ToLower(name)
}

// Accounts returns the distinct accounts of the rows in first-seen order. Rows
// with no account name have none.
func Accounts(rows []Row) []Account {
	seen := map[string]bool{}
	var out []Account
	for _, r := range rows {
		name := r.Get("Account Name")
		if name == "" {
			continue
		}
		id := r.Get("Account Name.id")
		key := accountKey(id, name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Account{ID: id, Name: name})
	}
	return out
}

// AccountKey is the key Accounts uses for a row, empty when the row has none.
func AccountKey(r Row) string {
	name := r.Get("Account Name")
	if name == "" {
		return ""
	}
	return accountKey(r.Get("Account Name.id"), name)
}
