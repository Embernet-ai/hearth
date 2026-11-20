package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/timelog"
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

// The Postgres time service cannot see an entry that lives in EmberOps, so time-remove must go through backendStores, the way
// daily-report, time-import and time-approve do. On the live site it answered "time entry not found" for an entry that was there.
func TestTimeRemoveGoesThroughTheBackendStores(t *testing.T) {
	src, err := os.ReadFile("timeremovecmd.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "timelog.New(") || !strings.Contains(string(src), "backendStores(") {
		t.Fatal("time-remove must take its time store from backendStores, not build the Postgres service")
	}
}

// With HEARTH_BACKEND=emberops the removal works on an entry in EmberOps: it reports it, and with apply it marks it removed.
func TestTimeRemoveRemovesAnEntryThatLivesInEmberOps(t *testing.T) {
	f := fake.New(map[string]string{"svc-tok": "svc@example.com"})
	defer f.Close()
	u, _ := url.Parse(f.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc-tok")})
	f.Put("User", map[string]any{"name": "ann@example.com", "hearth_id": annID, "full_name": "Ann"})
	f.Put("Employee", map[string]any{"name": "EMP-ann", "hearth_id": annID, "user_id": "ann@example.com"})
	tl := &stores.TimeLog{Store: removeAudit{}, Base: stores.Base{C: c}, Dir: stores.Directory{C: c}, Authz: superAdmin{}}
	e, err := tl.LogOwn(context.Background(), "org", core.Subjects{{Kind: core.SubjectPrincipal, ID: annID}}, "", 6, time.Now(), "Added on x")
	if err != nil {
		t.Fatal(err)
	}
	actor := core.Subjects{{Kind: core.SubjectPrincipal, ID: annID}}
	lines, err := tl.RemoveMany(context.Background(), "org", actor, []string{e.ID}, "throwaway", false)
	if err != nil || len(lines) != 1 || lines[0].Removed {
		t.Fatalf("dry run: %+v %v", lines, err)
	}
	lines, err = tl.RemoveMany(context.Background(), "org", actor, []string{e.ID}, "throwaway", true)
	if err != nil || len(lines) != 1 || !lines[0].Removed {
		t.Fatalf("apply: %+v %v", lines, err)
	}
	if got := f.All("Timesheet Detail"); len(got) != 1 || got[0]["hearth_deleted_at"] == nil {
		t.Fatalf("EmberOps has %v", got)
	}
}

type removeAudit struct{ noAutoApprove }

func (removeAudit) AuditRemoval(context.Context, string, string, string, string, string, timelog.RemovedEntry) error {
	return nil
}

type superAdmin struct{}

func (superAdmin) Require(context.Context, core.Subjects, core.Ref, core.AccessLevel) error {
	return nil
}
func (superAdmin) HasCapability(context.Context, string, string, string) (bool, error) {
	return true, nil
}
