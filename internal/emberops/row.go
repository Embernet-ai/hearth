package emberops

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Typed accessors for Row. Frappe returns numbers as float64 and nulls as nil,
// and the same field can arrive as a string or a number depending on the
// doctype, so these coerce rather than assert.

// S returns a string field ("" for null).
func (r Row) S(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprint(v)
	}
}

// F returns a numeric field (0 for null).
func (r Row) F(k string) float64 {
	switch v := r[k].(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// I returns an integer field.
func (r Row) I(k string) int64 { return int64(r.F(k)) }

// B returns a check field: 1, true and "1" are true.
func (r Row) B(k string) bool { return r.F(k) != 0 }

// Has reports whether the field is present and not null or "".
func (r Row) Has(k string) bool { return r.S(k) != "" }

// Rows returns a child table.
func (r Row) Rows(k string) []Row {
	list, _ := r[k].([]any)
	out := make([]Row, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, Row(m))
		}
	}
	return out
}

// Layouts Frappe emits for Datetime and Date fields.
var frappeLayouts = []string{"2006-01-02 15:04:05.999999", "2006-01-02T15:04:05", time.RFC3339, "2006-01-02"}

// ParseTime reads a Frappe datetime in the site's time zone loc.
func ParseTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, l := range frappeLayouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// T returns a time field in UTC, zero for null.
func (r Row) T(k string, loc *time.Location) time.Time {
	t, _ := ParseTime(r.S(k), loc)
	return t
}

// TP is T as a pointer, nil for null.
func (r Row) TP(k string, loc *time.Location) *time.Time {
	if t, ok := ParseTime(r.S(k), loc); ok {
		return &t
	}
	return nil
}

// FormatTime renders a timestamp as the naive "YYYY-MM-DD HH:MM:SS" Frappe
// stores, in the site's zone loc. Nil renders nil so a payload can clear a
// field.
func FormatTime(t *time.Time, loc *time.Location) any {
	if t == nil || t.IsZero() {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

// FormatDate renders a date-only value.
func FormatDate(t *time.Time, loc *time.Location) any {
	if t == nil || t.IsZero() {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

// Ref writes a reference to a Hearth record: "hearth:<id>".
func Ref(id string) any {
	if id == "" {
		return nil
	}
	return "hearth:" + id
}

// RefID reads one back, passing a plain Frappe name through.
func RefID(s string) string { return strings.TrimPrefix(s, "hearth:") }
