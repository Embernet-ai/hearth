package main

import (
	"strings"
	"testing"
)

func TestReadEntryIDsTakesAColumnOrALineAndRefusesAnythingElse(t *testing.T) {
	a, b := "0b7e6b5e-1a2b-4c3d-8e9f-0a1b2c3d4e5f", "11111111-2222-4333-8444-555555555555"
	got, err := readEntryIDs(strings.NewReader("id,person\n" + a + ",alice\n\n" + strings.ToUpper(b) + "\n"))
	if err != nil || len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"", "id\n", a + "\nnot-an-id\n"} {
		if _, err := readEntryIDs(strings.NewReader(bad)); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}
