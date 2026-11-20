package hubspot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func TestModifiedReadsOnlyWhatChangedSinceAndOldestFirst(t *testing.T) {
	f := newFakeHub(t)
	f.add(Companies, "1", t0.Add(-time.Hour), map[string]string{"name": "before the last import"})
	f.add(Companies, "3", t0.Add(3*time.Hour), map[string]string{"name": "third"})
	f.add(Companies, "2", t0.Add(time.Hour), map[string]string{"name": "second"})
	var got []string
	err := f.client().Modified(context.Background(), Companies, props[Companies], t0, func(o Object) error {
		got = append(got, o.ID+":"+o.Props["name"])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[2:second 3:third]" {
		t.Fatalf("read %v, want only the two changed since the last import, oldest first", got)
	}
}

func TestModifiedAsksContactsAboutLastmodifieddate(t *testing.T) {
	f := newFakeHub(t)
	f.add(Contacts, "9", t0.Add(time.Hour), map[string]string{"email": "a@example.com"})
	var n int
	if err := f.client().Modified(context.Background(), Contacts, props[Contacts], t0, func(Object) error { n++; return nil }); err != nil {
		t.Fatalf("a contact search filtered on the wrong property: %v", err)
	}
	if n != 1 {
		t.Fatalf("read %d contacts, want 1", n)
	}
}

// HubSpot pages through at most 10,000 results of one search. The importer has to start a new window from the last change it
// reached, and give a record on the edge of two windows only once.
func TestModifiedPagesPastTheSearchCapWithoutLosingOrRepeating(t *testing.T) {
	old := SearchCap
	SearchCap = 10
	t.Cleanup(func() { SearchCap = old })
	f := newFakeHub(t)
	// 35 records; every third pair shares a modified time, so a window can end between two records with one time.
	for i := 0; i < 35; i++ {
		f.add(Companies, fmt.Sprint(100+i), t0.Add(time.Duration(i/2)*time.Minute), map[string]string{"name": fmt.Sprint(i)})
	}
	c := f.client()
	c.PageSize = 3
	seen := map[string]int{}
	if err := c.Modified(context.Background(), Companies, props[Companies], t0, func(o Object) error { seen[o.ID]++; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 35 {
		t.Fatalf("read %d distinct records, want 35", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("record %s was handed over %d times", id, n)
		}
	}
	if w := len(f.searches(Companies)); w < 3 {
		t.Errorf("only %d searches: the cap was never hit, so this proved nothing", w)
	}
}

func TestModifiedGivesUpWhenOneInstantHoldsMoreThanTheCap(t *testing.T) {
	old := SearchCap
	SearchCap = 6
	t.Cleanup(func() { SearchCap = old })
	f := newFakeHub(t)
	for i := 0; i < 20; i++ {
		f.add(Companies, fmt.Sprint(i), t0.Add(time.Minute), nil)
	}
	c := f.client()
	c.PageSize = 3
	if err := c.Modified(context.Background(), Companies, nil, t0, func(Object) error { return nil }); err == nil {
		t.Fatal("expected an error rather than looping on one modified time")
	}
}

func TestClientRetriesAThrottledCall(t *testing.T) {
	f := newFakeHub(t)
	f.add(Deals, "5", t0.Add(time.Hour), map[string]string{"dealname": "d"})
	f.throttle429 = 2
	var n int
	if err := f.client().Modified(context.Background(), Deals, props[Deals], t0, func(Object) error { n++; return nil }); err != nil {
		t.Fatalf("two 429s should be waited out: %v", err)
	}
	if n != 1 {
		t.Fatalf("read %d, want 1", n)
	}
}

func TestOwnersWithoutTheScopeIsErrScope(t *testing.T) {
	f := newFakeHub(t)
	if _, err := f.client().Owners(context.Background()); !errors.Is(err, ErrScope) {
		t.Fatalf("err = %v, want ErrScope", err)
	}
	f.ownersOK = true
	f.owners = []Owner{{ID: "7", Email: "rep@example.com"}}
	got, err := f.client().Owners(context.Background())
	if err != nil || len(got) != 1 || got[0].Email != "rep@example.com" {
		t.Fatalf("owners = %v, %v", got, err)
	}
}

func TestAssociationsFillsEachObject(t *testing.T) {
	f := newFakeHub(t)
	f.link(Contacts, Companies, "10", "500")
	objs := []Object{{ID: "10", Assoc: map[string][]string{}}, {ID: "11", Assoc: map[string][]string{}}}
	if err := f.client().Associations(context.Background(), Contacts, Companies, objs); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(objs[0].Assoc[Companies]) != "[500]" || len(objs[1].Assoc[Companies]) != 0 {
		t.Fatalf("associations = %v / %v", objs[0].Assoc, objs[1].Assoc)
	}
}

func TestAWrongTokenIsAScopeError(t *testing.T) {
	f := newFakeHub(t)
	c := f.client()
	c.Token = "nope"
	if err := c.Modified(context.Background(), Companies, nil, t0, func(Object) error { return nil }); !errors.Is(err, ErrScope) {
		t.Fatalf("err = %v", err)
	}
}
