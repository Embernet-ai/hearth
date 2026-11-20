// Package stores serves Hearth's domains from EmberOPS.
//
// Each store implements the domain's Store interface (docs.Store, views.Store
// and so on) over the emberops client and the emberops_hearth addon's doctypes.
// A store that does not yet cover every method of its interface embeds the
// Postgres Service for the rest, so a call it does not override still works,
// against Hearth's own database. Which methods those are is listed in
// docs/emberops-backend.md; they are the open edge of this back end.
//
// Identity: Hearth ids (uuids) are kept in the addon's hearth_id field on every
// record, so ids stay stable across both back ends. People are Users and
// Employees; a Hearth principal id is User.hearth_id / Employee.hearth_id and
// is written as "hearth:<id>" wherever a Link to either belongs.
package stores

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// Base is what every store shares.
type Base struct {
	C *emberops.Client
	// Loc is the EmberOPS site's time zone.
	Loc *time.Location
}

func (b Base) loc() *time.Location {
	if b.Loc == nil {
		return time.UTC
	}
	return b.Loc
}

// ts renders an instant for Frappe, or nil.
func (b Base) ts(t *time.Time) any { return emberops.FormatTime(t, b.loc()) }

// tsv renders a non-pointer instant, nil when zero.
func (b Base) tsv(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return emberops.FormatTime(&t, b.loc())
}

// notFound turns EmberOPS's "does not exist" (or "you may not see it", which
// Frappe also reports as not found for reads through permission queries) into
// the domain's own error, and passes everything else through.
func notFound(err, domain error) error {
	if errors.Is(err, emberops.ErrNotFound) || errors.Is(err, emberops.ErrForbidden) {
		return domain
	}
	return err
}

// Directory maps between Hearth principal ids and the Users and Employees that
// represent them in EmberOPS. Both are small lists, read as Reference data (a
// short shared cache) rather than per row, so a page of fifty tasks resolves
// every assignee with two calls and not fifty.
type Directory struct{ C *emberops.Client }

// Person is a Hearth user as EmberOPS knows them.
type Person struct{ Email, ID, Name string }

// People lists every User that carries a Hearth id.
func (d Directory) People(ctx context.Context) ([]Person, error) {
	rows, err := d.C.ListAll(ctx, emberops.ListQuery{
		Doctype: "User", Fields: []string{"name", "hearth_id", "full_name"}, Reference: true,
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}},
	}, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Person, 0, len(rows))
	for _, r := range rows {
		out = append(out, Person{Email: r.S("name"), ID: r.S("hearth_id"), Name: r.S("full_name")})
	}
	return out, nil
}

// UserIDs returns User.name (the email) -> Hearth principal id.
func (d Directory) UserIDs(ctx context.Context) (map[string]string, error) {
	ps, err := d.People(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ps))
	for _, p := range ps {
		out[p.Email] = p.ID
	}
	return out, nil
}

// Emails returns Hearth principal id -> User.name (email), for the assignee
// fields (_assign) that take an email rather than a reference.
func (d Directory) Emails(ctx context.Context) (map[string]string, error) {
	ps, err := d.People(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ps))
	for _, p := range ps {
		out[p.ID] = p.Email
	}
	return out, nil
}

// Names returns Hearth principal id -> display name.
func (d Directory) Names(ctx context.Context) (map[string]string, error) {
	ps, err := d.People(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ps))
	for _, p := range ps {
		out[p.ID] = p.Name
	}
	return out, nil
}

// EmployeeIDs returns Employee.name -> Hearth principal id.
func (d Directory) EmployeeIDs(ctx context.Context) (map[string]string, error) {
	rows, err := d.C.ListAll(ctx, emberops.ListQuery{
		Doctype: "Employee", Fields: []string{"name", "hearth_id"}, Reference: true,
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}},
	}, 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.S("name")] = r.S("hearth_id")
	}
	return out, nil
}

// lower is the case normalisation the addon applies to hearth_id.
func lower(s string) string { return strings.ToLower(s) }

// HearthIDs maps Frappe names of one doctype to their hearth_id, in one call
// for all the distinct names a page references. A name with no hearth_id (a
// record made in EmberOPS itself) maps to itself, so Hearth can still show and
// link it.
func (d Directory) HearthIDs(ctx context.Context, doctype string, names []string) (map[string]string, error) {
	seen := map[string]bool{}
	var want []any
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			want = append(want, n)
		}
	}
	out := make(map[string]string, len(want))
	for len(want) > 0 {
		n := min(len(want), 200)
		rows, err := d.C.List(ctx, emberops.ListQuery{
			Doctype: doctype, Fields: []string{"name", "hearth_id"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: want[:n]}},
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.S("name")] = r.S("hearth_id")
		}
		want = want[n:]
	}
	for n := range seen {
		if out[n] == "" {
			out[n] = n
		}
	}
	return out, nil
}

// isForbidden reports a permission refusal from EmberOPS.
func isForbidden(err error) bool { return errors.Is(err, emberops.ErrForbidden) }
