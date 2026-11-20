package crmviews

import "testing"

func TestCleanKeepsTheQuestionAndDropsThePage(t *testing.T) {
	list, q, err := Clean("/companies?kind=vendor&group=kind&page=3&sort=name")
	if err != nil || list != "companies" {
		t.Fatalf("%q %v", list, err)
	}
	if q != "group=kind&kind=vendor&sort=name" {
		t.Fatalf("the page is dropped and the rest kept: %q", q)
	}
	if _, _, err := Clean("/documents?kind=task"); err != ErrList {
		t.Fatalf("only the CRM lists can be saved: %v", err)
	}
	if _, _, err := Clean("%zz"); err == nil {
		t.Fatal("an unreadable address is refused")
	}
}

func TestAViewOpensTheListItWasSavedOn(t *testing.T) {
	v := &View{List: "deals", Query: "stage=proposal&group=owner"}
	if v.Path() != "/deals?stage=proposal&group=owner" {
		t.Fatal(v.Path())
	}
	if (&View{List: "contacts"}).Path() != "/contacts" {
		t.Fatal("no query is the bare list")
	}
	if !v.Own("") || v.Own("someone") {
		t.Fatal("ownership")
	}
}
