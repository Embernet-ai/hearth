package connect

import (
	"context"
	"strings"
	"testing"
)

// A connector the installation cannot run is refused when somebody tries to turn it on, before any database is asked.
func TestUnavailableConnectorsCannotBeSwitchedOn(t *testing.T) {
	s := New(Deps{Unavailable: map[Kind]string{Files: "documents are kept in EmberOps"}})
	if got := s.UnavailableReason(Files); got == "" {
		t.Fatal("files should be unavailable")
	}
	if got := s.UnavailableReason(Calendar); got != "" {
		t.Fatalf("calendar reason = %q", got)
	}
	err := s.SetEnabled(context.Background(), "org", "actor", Google, Files, true, false)
	if err == nil || !strings.Contains(err.Error(), "documents are kept in EmberOps") {
		t.Fatalf("SetEnabled = %v", err)
	}
}
