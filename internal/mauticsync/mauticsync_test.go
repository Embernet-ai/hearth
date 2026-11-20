package mauticsync

import (
	"strings"
	"testing"
)

func TestSegmentsFollowTheCompanyTypesAndTags(t *testing.T) {
	segs := segmentsFor([][2]string{{"client", "Client"}}, []string{"Expo 2026", "expo 2026", ""})
	var got []string
	for _, g := range segs {
		got = append(got, g.Key)
	}
	if strings.Join(got, "|") != "all|type:client|tag:expo 2026" {
		t.Fatalf("segments = %v", got)
	}
	tags := tagsFor(segs)
	if strings.Join(tags, "|") != "hearth|hearth-tag-expo-2026|hearth-type-client" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestAliasOfIsTheAliasTheSegmentWasMadeWith(t *testing.T) {
	for _, g := range segmentsFor([][2]string{{"vendor", "Vendor"}, {"partner", "Partner"}}, []string{"Ünï tag", "a b", "a-b"}) {
		if aliasOf(g.Key) != g.Alias {
			t.Errorf("%s: aliasOf = %q, made as %q", g.Key, aliasOf(g.Key), g.Alias)
		}
	}
}

func TestTwoTagsThatSlugAlikeKeepTheirOwnSegments(t *testing.T) {
	segs := segmentsFor(nil, []string{"a b", "a-b"})
	if len(segs) != 3 || segs[1].Alias == segs[2].Alias {
		t.Fatalf("segments collided: %+v", segs)
	}
}

func TestNamesSplitAtTheFirstSpace(t *testing.T) {
	for in, want := range map[string][2]string{
		"Ada Lovelace":      {"Ada", "Lovelace"},
		"Ada King Lovelace": {"Ada", "King Lovelace"},
		"Ada":               {"Ada", ""},
		"  ":                {"", ""},
	} {
		f, l := splitName(in)
		if f != want[0] || l != want[1] {
			t.Errorf("%q = %q %q", in, f, l)
		}
	}
}

func TestOnlyUsableAddressesAreSent(t *testing.T) {
	for in, ok := range map[string]bool{"a@b.co": true, "a@b": false, "@b.co": false, "a b@c.co": false, "a@b.co,c@d.co": false, "": false} {
		if validEmail(in) != ok {
			t.Errorf("%q = %v", in, !ok)
		}
	}
}

func TestTheHashChangesWithWhatIsSent(t *testing.T) {
	a := hashOf(map[string]any{"x": 1, "optout": false})
	if a != hashOf(map[string]any{"optout": false, "x": 1}) {
		t.Fatal("the same content hashed differently")
	}
	if a == hashOf(map[string]any{"x": 1, "optout": true}) {
		t.Fatal("an opt-out did not change the hash")
	}
}
