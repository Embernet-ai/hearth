package web

import (
	"reflect"
	"testing"

	"github.com/Embernet-ai/hearth/internal/docs"
)

func TestKeepReachable(t *testing.T) {
	all := []docs.People{{ID: "me", Name: "Me"}, {ID: "a", Name: "Ann"}, {ID: "b", Name: "Bo"}, {ID: "c", Name: "Cy"}}

	// An ordinary account, a super admin or anybody not restricted: the list
	// comes back exactly as it was, in the same order.
	if got := keepReachable(all, map[string]bool{"me": true}, false); !reflect.DeepEqual(got, all) {
		t.Errorf("an unrestricted account's list changed: %v", got)
	}
	if got := keepReachable(all, nil, false, "a"); !reflect.DeepEqual(got, all) {
		t.Errorf("an unrestricted account's list changed with extras: %v", got)
	}

	// A contractor account: themselves and whoever holds the folder, in order.
	got := keepReachable(all, map[string]bool{"me": true, "b": true}, true)
	if want := []docs.People{{ID: "me", Name: "Me"}, {ID: "b", Name: "Bo"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("restricted list = %v, want %v", got, want)
	}
	// The people already on a document stay in its picker.
	got = keepReachable(all, map[string]bool{"me": true}, true, "c")
	if want := []docs.People{{ID: "me", Name: "Me"}, {ID: "c", Name: "Cy"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("restricted list with the assignee = %v, want %v", got, want)
	}
	// Nobody reachable is an empty list, never the whole one.
	if got := keepReachable(all, nil, true); len(got) != 0 {
		t.Errorf("a restricted account with nobody reachable was given %d people", len(got))
	}
}
