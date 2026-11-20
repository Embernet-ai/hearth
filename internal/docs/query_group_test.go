package docs

import (
	"net/url"
	"testing"
)

// "Group by: Nothing" submits group= with nothing in it. That has to survive being
// read and written back into a link, or every later click re-groups the list.
func TestChoosingNoGroupingIsRemembered(t *testing.T) {
	chosen := ParseQuery(url.Values{"group": {""}})
	if !chosen.Ungrouped || !chosen.GroupBy.Zero() {
		t.Fatalf("group= should read as chosen-nothing, got %+v", chosen)
	}
	if got := chosen.Encode(); !got.Has("group") || got.Get("group") != "" {
		t.Fatalf("chosen-nothing must be written back as group=, got %v", got)
	}

	silent := ParseQuery(url.Values{})
	if silent.Ungrouped {
		t.Fatal("no group parameter at all is not a choice")
	}
	if silent.Encode().Has("group") {
		t.Fatal("an untouched query must not grow a group parameter")
	}

	set := ParseQuery(url.Values{"group": {"priority"}})
	if set.Ungrouped || set.Encode().Get("group") != "priority" {
		t.Fatalf("a chosen grouping is kept as it was, got %+v", set)
	}
}
