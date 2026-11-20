package hubspot

import "testing"

func ptr(s string) *string { return &s }

// The rule that keeps a person's edit: a field is only replaced while Hearth still holds what the import last left.
func TestDecideNeverOverwritesAnEdit(t *testing.T) {
	cases := []struct {
		name        string
		want, cur   string
		snap        *string
		mode        trust
		placeholder bool
		verdict     verdict
	}{
		{"hubspot silent never clears", "", "Acme", nil, snapshotted, false, skipField},
		{"already equal", "Acme", "Acme ", nil, fillOnly, false, sameField},
		{"empty in hearth is filled", "Acme", "", nil, fillOnly, false, setField},
		{"placeholder name is replaced", "Acme", "acme.example", nil, fillOnly, true, setField},
		{"unknown provenance, different value: conflict", "Acme Inc", "Acme", nil, fillOnly, false, conflictField},
		{"snapshot matches hearth: hubspot changed it, update", "Acme Inc", "Acme", ptr("Acme"), snapshotted, false, setField},
		{"snapshot differs from hearth: a person edited it", "Acme Inc", "Acme Corp", ptr("Acme"), snapshotted, false, conflictField},
		{"snapshotted but field never snapshotted: conflict", "Acme Inc", "Acme", nil, snapshotted, false, conflictField},
		{"untouched since the first import: hubspot wins", "Acme Inc", "Acme", nil, untouched, false, setField},
	}
	for _, c := range cases {
		if got := decide("name", c.want, c.cur, c.snap, c.mode, c.placeholder); got != c.verdict {
			t.Errorf("%s: verdict %v, want %v", c.name, got, c.verdict)
		}
	}
}

func TestPhonesCompareAsNumbersNotText(t *testing.T) {
	if decide("phone", "(216) 555-0142", "+1 216 555 0142", nil, fillOnly, false) != sameField {
		t.Error("two spellings of one number are the same")
	}
	if decide("phone", "216-555-0199", "+1 216 555 0142", nil, fillOnly, false) != conflictField {
		t.Error("a different number is a conflict")
	}
}

func TestPlanKeepsSnapshotOfConflictsAndAdvancesTheRest(t *testing.T) {
	want := map[string]string{"name": "Acme Inc", "city": "Dayton", "state": "OH"}
	cur := map[string]string{"name": "Acme", "city": "Toledo", "state": ""}
	snap := map[string]string{"name": "Acme", "city": "Dayton"}
	p := plan(want, cur, snap, snapshotted, nil)
	if p.set["name"] != "Acme Inc" || p.set["state"] != "OH" || len(p.set) != 2 {
		t.Errorf("set = %v", p.set)
	}
	if p.conflicts["city"] != "Dayton" {
		t.Errorf("conflicts = %v, want city left alone because Toledo is not what the import left", p.conflicts)
	}
	if p.next["city"] != "Dayton" || p.next["name"] != "Acme Inc" || p.next["state"] != "OH" {
		t.Errorf("next snapshot = %v", p.next)
	}
}

func TestLegacyKeyMatchesTheFirstImportsDerivation(t *testing.T) {
	// The first import derived each id as a version 5 UUID of "hubspot:<object>:<id>" under a fixed namespace. Computed
	// independently with python's uuid.uuid5 over the same namespace bytes.
	if got, want := legacyKey(Companies, "41643296275"), "company:e8af8564-85b2-5a05-86e6-2d90101d4d0d"; got != want {
		t.Fatalf("%s != %s", got, want)
	}
	if uuid5("hubspot:company:1") == uuid5("hubspot:contact:1") {
		t.Fatal("two kinds of record share an id")
	}
	if v := uuid5("x"); v[14] != '5' {
		t.Fatalf("%s is not a version 5 uuid", v)
	}
}

func TestStageMapIsTheFirstImportsAndReportsUnknown(t *testing.T) {
	for label, want := range map[string]string{"Closed - Won": "won", "Waiting on PO": "customer", "Rejected Quote": "lost",
		"No response": "no_response", "Proposal Prepared": "proposal", "Discussion Scheduled": "meeting", "Scope Document Drafted": "screening"} {
		if got, known := stageFor(label); got != want || !known {
			t.Errorf("%s -> %s %v, want %s", label, got, known, want)
		}
	}
	if _, known := stageFor("A Brand New Stage"); known {
		t.Error("a stage nobody mapped must be reported, not guessed")
	}
}

func TestWebsitesCompareAsAddressesNotText(t *testing.T) {
	for _, same := range [][2]string{{"acme.example", "https://www.acme.example/"}, {"HTTP://Acme.example", "acme.example"}} {
		if !fieldsEqual("website", same[0], same[1]) {
			t.Errorf("%q and %q are one address", same[0], same[1])
		}
	}
	if fieldsEqual("website", "acme.example/a", "acme.example/b") || fieldsEqual("website", "acme.example", "other.example") {
		t.Error("different pages or hosts are different addresses")
	}
}
