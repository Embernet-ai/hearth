package core

import "testing"

func TestAccessLevelOrdering(t *testing.T) {
	// The ordering IS the model: effective access is the maximum across grants.
	levels := []AccessLevel{AccessView, AccessComment, AccessEdit, AccessAdmin}
	for i := range levels {
		for j := range levels {
			got := levels[i].AtLeast(levels[j])
			want := i >= j
			if got != want {
				t.Errorf("%s.AtLeast(%s) = %v, want %v", levels[i], levels[j], got, want)
			}
		}
	}
}

func TestZeroValueDenies(t *testing.T) {
	// The zero value of AccessLevel must deny. If it granted view, then every
	// struct field somebody forgot to populate would silently grant read access.
	var zero AccessLevel
	for _, required := range []AccessLevel{AccessView, AccessComment, AccessEdit, AccessAdmin} {
		if zero.AtLeast(required) {
			t.Errorf("zero AccessLevel granted %s", required)
		}
	}
	if AccessNone.AtLeast(AccessView) {
		t.Error("AccessNone granted view")
	}
	if zero.Valid() {
		t.Error("zero AccessLevel reports valid")
	}
}

func TestUnknownLevelDenies(t *testing.T) {
	// A level read from somewhere untrusted, or a typo, must not grant.
	bogus := AccessLevel("superuser")
	if bogus.AtLeast(AccessView) {
		t.Error("an unrecognised level granted view")
	}
	if bogus.Valid() {
		t.Error("an unrecognised level reports valid")
	}
}

func TestMax(t *testing.T) {
	cases := []struct{ a, b, want AccessLevel }{
		{AccessView, AccessEdit, AccessEdit},
		{AccessEdit, AccessView, AccessEdit},
		{AccessNone, AccessView, AccessView},
		{AccessAdmin, AccessEdit, AccessAdmin},
		{AccessNone, AccessNone, AccessNone},
	}
	for _, c := range cases {
		if got := Max(c.a, c.b); got != c.want {
			t.Errorf("Max(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestSubjectKeyRoundTrip(t *testing.T) {
	for _, s := range []Subject{
		{Kind: SubjectPrincipal, ID: "018f-aaa"},
		{Kind: SubjectChannel, ID: "018f-bbb"},
		{Kind: SubjectTeam, ID: "018f-ccc"},
		{Kind: SubjectOrg, ID: "018f-ddd"},
	} {
		parsed, err := ParseSubject(s.Key())
		if err != nil {
			t.Fatalf("ParseSubject(%q): %v", s.Key(), err)
		}
		if parsed != s {
			t.Errorf("round trip: got %+v, want %+v", parsed, s)
		}
	}
}

func TestParseSubjectRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "principal", "principal:", "wizard:018f", ":018f"} {
		if _, err := ParseSubject(bad); err == nil {
			t.Errorf("ParseSubject(%q) should have failed", bad)
		}
	}
}
