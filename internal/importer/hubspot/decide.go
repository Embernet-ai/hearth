package hubspot

import "strings"

// trust is how far an existing Hearth value may be replaced by what HubSpot now says.
type trust int

const (
	// fillOnly: Hearth already holds this record and nothing says the import last wrote it, so a field is only
	// filled when it is empty. A different value is a conflict and stays as it is.
	fillOnly trust = iota
	// untouched: the record came across in the first import and nobody has changed it in Hearth since (its change time is
	// no later than that import), so HubSpot is still the only author and wins where it differs.
	untouched
	// snapshotted: an earlier run of this importer left a snapshot. A field is brought up to date only while Hearth still
	// holds exactly what the snapshot says the import last left there; a field that differs was edited here.
	snapshotted
)

func (t trust) String() string {
	switch t {
	case untouched:
		return "unchanged since the first import"
	case snapshotted:
		return "tracked by an earlier run"
	}
	return "not written by an import"
}

// verdict is what to do with one field.
type verdict int

const (
	skipField     verdict = iota // HubSpot has nothing to say
	sameField                    // already what HubSpot says
	setField                     // write HubSpot's value
	conflictField                // Hearth holds something else and it is not ours to replace
)

// fieldsEqual compares two values of one field the way that field is compared by a person.
func fieldsEqual(field, a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch field {
	case "phone":
		return samePhone(a, b)
	case "website":
		// "acme.example", "https://www.acme.example/" and "HTTP://acme.example" are one web address.
		if da, db := domainOf(a), domainOf(b); da != "" && db != "" {
			return da == db && strings.EqualFold(pathOf(a), pathOf(b))
		}
		return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
	case "domain", "email", "linkedin":
		return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
	}
	return a == b
}

// decide is the whole rule for one field. want is what HubSpot says now (empty means HubSpot has no value, which never
// clears one), cur is what Hearth holds, snap is what the import last left (nil when it never has), and placeholder says
// cur is a stand-in for a value nobody had (a company named by its web address).
func decide(field, want, cur string, snap *string, mode trust, placeholder bool) verdict {
	want = strings.TrimSpace(want)
	if want == "" {
		return skipField
	}
	if fieldsEqual(field, want, cur) {
		return sameField
	}
	if strings.TrimSpace(cur) == "" || placeholder {
		return setField
	}
	switch mode {
	case untouched:
		return setField
	case snapshotted:
		if snap != nil && fieldsEqual(field, *snap, cur) {
			return setField
		}
	}
	return conflictField
}

// fieldPlan is the result for one record.
type fieldPlan struct {
	set       map[string]string // field to the value to write
	conflicts map[string]string // field to what HubSpot says, where Hearth was left alone
	same      int
	// next is the snapshot to keep: for each field HubSpot spoke to, the value Hearth holds once the run is done, but
	// only where that is HubSpot's value. A field left in conflict keeps whatever the snapshot had.
	next map[string]string
}

func (p fieldPlan) changed() bool { return len(p.set) > 0 }

// plan applies decide to every field HubSpot spoke to.
func plan(want, cur, snap map[string]string, mode trust, placeholder map[string]bool) fieldPlan {
	p := fieldPlan{set: map[string]string{}, conflicts: map[string]string{}, next: map[string]string{}}
	for f, v := range snap {
		p.next[f] = v
	}
	for f, w := range want {
		var sp *string
		if v, ok := snap[f]; ok {
			sp = &v
		}
		switch decide(f, w, cur[f], sp, mode, placeholder[f]) {
		case sameField:
			p.same++
			p.next[f] = w
		case setField:
			p.set[f] = w
			p.next[f] = w
		case conflictField:
			p.conflicts[f] = w
		}
	}
	return p
}

// pathOf is what follows the host of a web address, without a trailing slash, so two addresses to the same host that go to
// different pages are not called the same.
func pathOf(v string) string {
	v = urlPrefix.ReplaceAllString(strings.TrimSpace(v), "")
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		return strings.TrimRight(v[i:], "/")
	}
	return ""
}
