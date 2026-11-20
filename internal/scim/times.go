package scim

// Times on the wire.
//
// The protocol wants RFC 3339 and Postgres gives a time.Time, so this is the
// one place the two meet. A separate type rather than a helper on every scan,
// because the alternative is remembering to format it in four places and
// getting it wrong in the fourth.

import "time"

type timeValue struct{ t time.Time }

func (v *timeValue) Scan(src any) error {
	if t, ok := src.(time.Time); ok {
		v.t = t
	}
	return nil
}

func (v timeValue) iso() string { return iso(v.t) }

func iso(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
