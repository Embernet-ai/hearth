package main

import (
	"reflect"
	"testing"
)

func TestSplitIDsKeepsOrderAndDropsBlanksAndRepeats(t *testing.T) {
	got := splitIDs(" a, b ,,a,c , b ")
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(splitIDs("")) != 0 || len(splitIDs(" , ")) != 0 {
		t.Error("an empty list should give no ids")
	}
}
