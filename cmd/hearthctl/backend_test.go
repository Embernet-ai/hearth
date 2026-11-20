package main

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

type personFinder struct{ id string }

func (p personFinder) SubjectsFor(context.Context, string, string) (core.Subjects, error) {
	return core.Subjects{{Kind: core.SubjectPrincipal, ID: p.id}}, nil
}

const annID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// In the EmberOps back end a person's imported time lands in EmberOps, where Hearth reads it, and the "already there" check
// reads it from there too. Before this the importer built the Postgres services directly.
func TestTimeImportInEmberOpsModeWritesAndChecksEmberOps(t *testing.T) {
	f := fake.New(map[string]string{"svc-tok": "svc@example.com"})
	defer f.Close()
	u, _ := url.Parse(f.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc-tok")})
	f.Put("User", map[string]any{"name": "ann@example.com", "hearth_id": annID, "full_name": "Ann"})
	f.Put("Employee", map[string]any{"name": "EMP-ann", "hearth_id": annID, "user_id": "ann@example.com"})
	tl := &stores.TimeLog{Store: noAutoApprove{}, Base: stores.Base{C: c}, Dir: stores.Directory{C: c}}
	imp := &timeImporter{az: personFinder{annID}, svc: tl, org: "org", who: annID}
	rows := []timeImportRow{{Start: time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC), Minutes: 90, Note: "Reconstructed from GitHub activity: x"}}

	res, err := imp.run(context.Background(), rows, "sum", "", true, false, io.Discard)
	if err != nil || res.Written != 1 {
		t.Fatalf("run: %+v %v", res, err)
	}
	got := f.All("Timesheet Detail")
	if len(got) != 1 || !strings.Contains(got[0]["description"].(string), "Reconstructed from GitHub activity") {
		t.Fatalf("EmberOps has %v", got)
	}
	// run again: the check reads EmberOps, finds the entry, and refuses to write it twice
	if _, err := imp.run(context.Background(), rows, "sum", "", true, false, io.Discard); err == nil || !strings.Contains(err.Error(), "already exist") {
		t.Fatalf("second run = %v", err)
	}
	if len(f.All("Timesheet Detail")) != 1 {
		t.Fatal("the entry was written twice")
	}
}

// noAutoApprove is the Postgres half of time for a test that has no Postgres: nobody's time approves itself.
type noAutoApprove struct{ timelog.Store }

func (noAutoApprove) AutoApproveSince(context.Context, string, string) (*time.Time, error) {
	return nil, nil
}

type spyTime struct {
	timelog.Store
	logged int
	have   []timelog.Entry
}

func (s *spyTime) Mine(context.Context, string, string, time.Time, time.Time, bool) ([]timelog.Entry, error) {
	return s.have, nil
}
func (s *spyTime) LogOwn(_ context.Context, _ string, _ core.Subjects, _ string, m int, on time.Time, n string) (*timelog.Entry, error) {
	s.logged++
	return &timelog.Entry{ID: "e", Minutes: &m, StartedAt: on, Note: n}, nil
}

// In the Postgres back end the same importer goes through the Store it is given, and does no SQL of its own for the check.
func TestTimeImportInPostgresModeGoesThroughTheStore(t *testing.T) {
	spy := &spyTime{}
	imp := &timeImporter{az: personFinder{annID}, svc: spy, org: "org", who: annID}
	rows := []timeImportRow{{Start: time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC), Minutes: 30, Note: "Reconstructed from x"}}
	if res, err := imp.run(context.Background(), rows, "s", "", true, false, io.Discard); err != nil || res.Written != 1 || spy.logged != 1 {
		t.Fatalf("run: %+v %v logged=%d", res, err, spy.logged)
	}
	spy.have = []timelog.Entry{{StartedAt: rows[0].Start}}
	if _, err := imp.run(context.Background(), rows, "s", "", true, false, io.Discard); err == nil {
		t.Fatal("an entry already there was written again")
	}
}

type spyDocs struct {
	docs.Store
	created []docs.CreateParams
}

func (s *spyDocs) ReportOn(context.Context, string, string, string) (string, error) { return "", nil }
func (s *spyDocs) DefaultReportSpace(context.Context, string, string) (string, error) {
	return "space-1", nil
}
func (s *spyDocs) Create(_ context.Context, _ string, _ core.Subjects, p docs.CreateParams) (*docs.Document, error) {
	s.created = append(s.created, p)
	return &docs.Document{ID: "d1"}, nil
}

// The daily report filer files through the docs Store it is given, whichever back end that is.
func TestDailyReportFilesThroughTheStoreInEitherMode(t *testing.T) {
	spy := &spyDocs{}
	f := &dailyReportFiler{az: personFinder{annID}, svc: spy, org: "org", who: annID, now: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }}
	id, space, err := f.file(context.Background(), reportJob{Day: "2026-11-19", Body: "did things"}, true)
	if err != nil || id != "d1" || space != "space-1" || len(spy.created) != 1 || spy.created[0].Kind != docs.KindDailyReport {
		t.Fatalf("file: %q %q %v %+v", id, space, err, spy.created)
	}
}

// With no back end set, the stores a command gets are the Postgres services.
func TestBackendStoresIsPostgresUnlessTheBackendSaysEmberOps(t *testing.T) {
	t.Setenv("HEARTH_BACKEND", "")
	tl, ds, err := backendStores(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tl.(*timelog.Service); !ok {
		t.Fatalf("time store is %T", tl)
	}
	if _, ok := ds.(*docs.Service); !ok {
		t.Fatalf("docs store is %T", ds)
	}
}
