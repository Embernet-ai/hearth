// Package csvimport reads a CSV a person has exported from any other CRM or
// spreadsheet into rows Hearth can create: companies, contacts or deals.
//
// Columns are matched by header name, not position, so a file keeps working when
// its columns are in another order. A header that means nothing here is listed
// back to the person and ignored, never guessed at. A value that cannot be read
// (a stage that does not exist, an amount that is not a number) fails its own
// row and says why, and the rest of the file still goes in.
package csvimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/crm"
)

// Kind is what a file holds.
type Kind string

const (
	Companies Kind = "companies"
	Contacts  Kind = "contacts"
	Deals     Kind = "deals"
)

// MaxRows bounds one upload, so a whole database dump cannot be run as a single
// request. Split a bigger file and run it in parts; running the same part again
// skips what is already there.
const MaxRows = 5000

// Valid reports whether k is something a file can hold.
func (k Kind) Valid() bool { return k == Companies || k == Contacts || k == Deals }

// aliases are, per kind, the canonical field and the headers that mean it. A
// header is compared lower case with everything but letters and digits removed.
var aliases = map[Kind]map[string][]string{
	Companies: {
		"name":   {"name", "company", "companyname", "organization", "organisation", "account", "accountname"},
		"domain": {"domain", "domainname", "website", "web", "url", "site"},
		"kind":   {"kind", "type", "companytype", "category"},
		"notes":  {"notes", "note", "description", "comments"},
	},
	Contacts: {
		"name":    {"name", "fullname", "contact", "contactname", "person"},
		"first":   {"firstname", "first", "givenname"},
		"last":    {"lastname", "last", "surname", "familyname"},
		"email":   {"email", "emailaddress", "mail", "workemail"},
		"phone":   {"phone", "phonenumber", "mobile", "telephone", "tel", "workphone"},
		"title":   {"title", "jobtitle", "role", "position"},
		"company": {"company", "companyname", "organization", "organisation", "account", "accountname"},
		"notes":   {"notes", "note", "description", "comments"},
	},
	Deals: {
		"name":          {"name", "deal", "dealname", "opportunity", "opportunityname", "title"},
		"stage":         {"stage", "dealstage", "status", "pipelinestage"},
		"amount":        {"amount", "value", "dealvalue", "dealamount", "revenue"},
		"currency":      {"currency", "currencycode"},
		"close_date":    {"closedate", "expectedclose", "expectedclosedate", "closingdate", "closeby"},
		"company":       {"company", "companyname", "organization", "organisation", "account", "accountname"},
		"contact_email": {"contactemail", "email", "contact"},
		"owner_email":   {"owneremail", "owner", "accountowner", "dealowner", "assignedto"},
		"notes":         {"notes", "note", "description", "comments"},
	},
}

// Row is one record of the file: canonical field to its trimmed value.
type Row struct {
	Line   int
	Fields map[string]string
	// Err is why the row cannot be read at all, before anything is created.
	Err string
}

// Mapping is one recognised column.
type Mapping struct{ Field, Header string }

// Sheet is a parsed file.
type Sheet struct {
	Kind Kind
	Rows []Row
	// Used lists each field Hearth reads and the header it came from. Ignored
	// are headers that matched nothing.
	Used    []Mapping
	Ignored []string
	// Truncated is set when the file had more than MaxRows records.
	Truncated bool
}

// ErrNoHeader means the file had no header row to read.
var ErrNoHeader = errors.New("the file is empty or has no header row")

// ErrNoColumns means the header named nothing Hearth reads for this kind.
var ErrNoColumns = errors.New("none of the column headers are ones Hearth reads for this")

// Parse reads the CSV. It strips a byte order mark and works out whether the
// separator is a comma, semicolon or tab from the header row.
func Parse(kind Kind, in io.Reader) (*Sheet, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("csvimport: %q is not a kind of record", kind)
	}
	raw, err := io.ReadAll(io.LimitReader(in, 64<<20))
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(raw))
	r.Comma = separator(raw)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	header, err := r.Read()
	if err != nil {
		return nil, ErrNoHeader
	}
	index := map[string]int{} // field -> column
	sheet := &Sheet{Kind: kind}
	claimed := map[int]bool{}
	for _, field := range fieldOrder(kind) {
		for col, h := range header {
			if claimed[col] || !contains(aliases[kind][field], squash(h)) {
				continue
			}
			if _, taken := index[field]; taken {
				continue
			}
			index[field] = col
			claimed[col] = true
			sheet.Used = append(sheet.Used, Mapping{Field: field, Header: strings.TrimSpace(h)})
		}
	}
	for col, h := range header {
		if !claimed[col] && strings.TrimSpace(h) != "" {
			sheet.Ignored = append(sheet.Ignored, strings.TrimSpace(h))
		}
	}
	if len(index) == 0 {
		return nil, ErrNoColumns
	}

	line := 1
	for {
		rec, err := r.Read()
		line++
		if err == io.EOF {
			break
		}
		if err != nil {
			sheet.Rows = append(sheet.Rows, Row{Line: line, Err: "this line could not be read as CSV"})
			continue
		}
		if len(sheet.Rows) >= MaxRows {
			sheet.Truncated = true
			break
		}
		row := Row{Line: line, Fields: map[string]string{}}
		blank := true
		for field, col := range index {
			if col < len(rec) {
				if v := strings.TrimSpace(rec[col]); v != "" {
					row.Fields[field] = v
					blank = false
				}
			}
		}
		if blank {
			continue // a blank line is not a record
		}
		finish(kind, &row)
		sheet.Rows = append(sheet.Rows, row)
	}
	return sheet, nil
}

// finish joins first and last names and checks what a row cannot do without.
func finish(kind Kind, row *Row) {
	f := row.Fields
	if kind == Contacts {
		if f["name"] == "" {
			f["name"] = strings.TrimSpace(f["first"] + " " + f["last"])
			if f["name"] == "" {
				delete(f, "name")
			}
		}
		delete(f, "first")
		delete(f, "last")
		if f["name"] == "" && f["email"] == "" {
			row.Err = "needs a name or an email"
		}
		return
	}
	if f["name"] == "" {
		row.Err = "needs a name"
	}
}

func fieldOrder(kind Kind) []string {
	switch kind {
	case Companies:
		return []string{"name", "domain", "kind", "notes"}
	case Contacts:
		return []string{"name", "first", "last", "email", "phone", "title", "company", "notes"}
	default:
		return []string{"name", "stage", "amount", "currency", "close_date", "company", "contact_email", "owner_email", "notes"}
	}
}

func separator(raw []byte) rune {
	line := raw
	if i := bytes.IndexByte(raw, '\n'); i >= 0 {
		line = raw[:i]
	}
	best, count := ',', bytes.Count(line, []byte(","))
	for _, c := range []rune{';', '\t'} {
		if n := bytes.Count(line, []byte(string(c))); n > count {
			best, count = c, n
		}
	}
	return best
}

func squash(h string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(h) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Stage reads a stage as a person writes it ("Proposal", "no response", or the
// stored name) and returns the stored name. Empty is a new deal.
func Stage(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return crm.StageNew, nil
	}
	key := strings.NewReplacer(" ", "_", "-", "_").Replace(v)
	for _, s := range crm.Stages {
		if key == s || v == strings.ToLower(crm.StageLabel(s)) {
			return s, nil
		}
	}
	var labels []string
	for _, s := range crm.Stages {
		labels = append(labels, crm.StageLabel(s))
	}
	return "", fmt.Errorf("%q is not one of the stages (%s)", v, strings.Join(labels, ", "))
}

// CompanyKind reads a company kind and falls back to other for one Hearth does
// not have, so an export's own categories do not fail a company.
func CompanyKind(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, k := range crm.Kinds {
		if v == k {
			return k
		}
	}
	return crm.KindOther
}

// Cents reads an amount: 1500, 1,500.50, $1,500 and 1.500,50 are all understood.
func Cents(v string) (int64, error) {
	orig := strings.TrimSpace(v)
	v = strings.NewReplacer("$", "", "€", "", "£", "", " ", "", " ", "").Replace(orig)
	if v == "" {
		return 0, nil
	}
	// a comma followed by exactly two digits at the end, with no point after it,
	// is a decimal comma
	if i := strings.LastIndex(v, ","); i >= 0 && len(v)-i-1 == 2 && !strings.Contains(v[i:], ".") {
		v = strings.ReplaceAll(strings.ReplaceAll(v, ".", ""), ",", ".")
	} else {
		v = strings.ReplaceAll(v, ",", "")
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%q is not an amount", orig)
	}
	return int64(f*100 + 0.5), nil
}

// Date reads a day written as 2026-11-17, 11/17/2026, 17 Nov 2026, Nov 17, 2026
// or an RFC 3339 time. Empty is no date.
func Date(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02", "1/2/2006", "01/02/2006", "2 Jan 2006", "2 January 2006",
		"Jan 2, 2006", "January 2, 2006", time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			return &d, nil
		}
	}
	return nil, fmt.Errorf("%q is not a date", v)
}
