package web

// The time report's columns through the server as the live one is built.
//
// 2.33.0 passed every test and showed Customer/Project and Service/Project Phase empty on the live report. The tests held a
// stand-in for the plant store; the live server holds the EmberOps store set, which wraps the Postgres plant store and hides
// anything the interface does not name. This builds the store set the way cmd/hearth/main.go does for an install with an
// EmberOps back end (stores.EmberOPS over the real Postgres services, with the repo's own EmberOps double behind it), seeds
// the real shape (White Team Service > Expert Crane > On-Site Testing at Mt. Clemens, and an entry like Owen's 4 Nov row),
// and asks the real router for the files.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/teams"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

func TestTimeReportColumnsOnTheLiveStoreSet(t *testing.T) {
	dsn := pgtest.Database(t, "hearth_test_webtimelevels")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatal(err)
	}

	azSvc := authz.New(pools.Web)
	authSvc := auth.New(pools.Web, azSvc, false)
	owen, err := authSvc.Setup(ctx, "Fireball", "Owen Ellis", "owen@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	var org string
	if err := pools.Web.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pools.Web.Exec(context.Background(), `SELECT audit_purge_org($1)`, org) })
	subj, err := azSvc.SubjectsFor(ctx, org, owen)
	if err != nil {
		t.Fatal(err)
	}

	// The EmberOps double the repo's own store tests use (internal/emberops/fake), with the person already there as a User and
	// an Employee, which is what emberops-migrate and the sign-in hook make on a live install.
	ops := fake.New(map[string]string{"svc": "svc@example.com"})
	t.Cleanup(ops.Close)
	opsURL, _ := url.Parse(ops.URL)
	client := emberops.New(emberops.Config{BaseURL: opsURL, Service: emberops.StaticToken("svc")})
	ops.Put("User", map[string]any{"name": "owen@example.com", "hearth_id": owen, "full_name": "Owen Ellis"})
	ops.Put("Employee", map[string]any{"name": "EMP-owen", "hearth_id": owen, "user_id": "owen@example.com"})

	ny, _ := time.LoadLocation("America/New_York")

	// The store set, as main.go builds it when HEARTH_BACKEND is emberops.
	docsSvc, plantSvc, timeSvc, teamsSvc := docs.New(pools.Web, azSvc), plant.New(pools.Web, azSvc), timelog.New(pools.Web, azSvc), teams.New(pools.Web)
	spaceSync := &stores.SpaceSync{C: client, Source: plantSvc, Dir: stores.Directory{C: client}, Loc: ny, Log: log,
		Real: func(ctx context.Context, orgID string) (bool, error) {
			var real bool
			err := pools.Web.QueryRow(ctx, `SELECT playground_of IS NULL FROM orgs WHERE id = $1`, orgID).Scan(&real)
			return real, err
		}}
	set := stores.EmberOPS(client, stores.PG{Docs: docsSvc, Teams: teamsSvc, Time: timeSvc, Plant: plantSvc}, stores.Options{
		Loc: ny, Authz: azSvc, Spaces: spaceSync,
	})

	// The tree the live report reads.
	mk := func(name, kind string, parent *string) string {
		sp, err := set.Plant.CreateSpace(ctx, org, subj, name, kind, parent)
		if err != nil {
			t.Fatalf("space %s: %v", name, err)
		}
		return sp.ID
	}
	eng := mk("Engineering", "area", nil)
	wts := mk("White Team Service", "folder", &eng)
	crane := mk("Expert Crane", "folder", &wts)
	task, err := set.Docs.Create(ctx, org, subj, docs.CreateParams{Kind: docs.KindTask, Title: "On-Site Testing at Mt. Clemens", SpaceID: &crane, Task: &docs.TaskFields{}})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if _, err := set.Time.Log(ctx, org, subj, task.ID, 195, time.Date(2026, 11, 4, 9, 0, 0, 0, ny), "Travel to Mt. Clemens"); err != nil {
		t.Fatalf("log: %v", err)
	}
	// The three-level case from Carlos Torrijos's November rows: White Team Service > MCSI > MCS1002 Beachwood Ohio, a task
	// "Panel Integration" in it, and an entry with a long note.
	mcsi := mk("MCSI", "folder", &wts)
	beachwood := mk("MCS1002 Beachwood Ohio", "folder", &mcsi)
	panel, err := set.Docs.Create(ctx, org, subj, docs.CreateParams{Kind: docs.KindTask, Title: "Panel Integration", SpaceID: &beachwood, Task: &docs.TaskFields{}})
	if err != nil {
		t.Fatalf("panel task: %v", err)
	}
	longNote := "Electrical Assembly: assembly of electrical power and control cabinet including installation of components, drawings, client requirements review, equipment layout, cab mounting."
	if _, err := set.Time.Log(ctx, org, subj, panel.ID, 480, time.Date(2026, 11, 9, 9, 0, 0, 0, ny), longNote); err != nil {
		t.Fatalf("log panel: %v", err)
	}

	// The real router, with the session wall the live server puts around it.
	site, err := New(Deps{Docs: set.Docs, Auth: authSvc, Time: set.Time, Plant: set.Plant, Authz: azSvc, Teams: set.Teams, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	handler := authSvc.Middleware(site.Routes())
	rec := httptest.NewRecorder()
	if err := authSvc.Create(ctx, rec, httptest.NewRequest(http.MethodGet, "/", nil), org, owen); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	const q = "/reports/time?detail=1&all=1&from=2026-11-01&to=2026-11-30&format="
	want := []string{"White Team Service", "Expert Crane", "", "On-Site Testing at Mt. Clemens"}
	want3 := []string{"White Team Service", "MCSI", "MCS1002 Beachwood Ohio", "Panel Integration"}

	w := get(q + "csv")
	if w.Code != http.StatusOK {
		t.Fatalf("details csv: %d %s", w.Code, w.Body.String())
	}
	recs, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || len(recs) != 3 {
		t.Fatalf("details csv: %v %v", recs, err)
	}
	if got := strings.Join(recs[0][6:10], ","); got != "space,customer_project,service_project_phase,task" {
		t.Fatalf("details csv header %q", got)
	}
	if got := recs[1][6:10]; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("details csv row reads %q, want %q", got, want)
	}
	if got := recs[2][6:10]; strings.Join(got, "|") != strings.Join(want3, "|") {
		t.Errorf("details csv three-level row reads %q, want %q", got, want3)
	}

	w = get(q + "xlsx")
	if w.Code != http.StatusOK {
		t.Fatalf("details xlsx: %d %s", w.Code, w.Body.String())
	}
	f, err := excelize.OpenReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := f.GetRows("Time details")
	if err != nil || len(rows) != 3 {
		t.Fatalf("details xlsx rows: %v %v", rows, err)
	}
	for i, c := range want3 {
		if rows[2][6+i] != c {
			t.Errorf("details xlsx three-level column %d reads %q, want %q", 6+i, rows[2][6+i], c)
		}
	}
	if strings.Join(rows[0][6:10], ",") != "space,customer_project,service_project_phase,task" {
		t.Fatalf("details xlsx header %v", rows[0])
	}
	for i, c := range want {
		if rows[1][6+i] != c {
			t.Errorf("details xlsx column %d reads %q, want %q", 6+i, rows[1][6+i], c)
		}
	}

	// The per-person files and the page itself, the same four columns for both shapes.
	person := "/reports/time?from=2026-11-01&to=2026-11-30&person=" + owen
	w = get(person + "&format=csv")
	cr := csv.NewReader(w.Body)
	cr.FieldsPerRecord = -1
	recs, err = cr.ReadAll()
	if err != nil || len(recs) < 4 || strings.Join(recs[1][2:6], "|") != strings.Join(want, "|") || strings.Join(recs[2][2:6], "|") != strings.Join(want3, "|") {
		t.Errorf("per-person csv: %v %v", recs, err)
	}
	w = get(person + "&format=xlsx")
	pf, err := excelize.OpenReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	prow, err := pf.GetRows("Time report")
	if err != nil || len(prow) < 3 || strings.Join(prow[0][2:6], ",") != "Space,Customer/Project,Service/Project Phase,Task" || strings.Join(prow[2][2:6], "|") != strings.Join(want3, "|") {
		t.Errorf("per-person xlsx: %v %v", prow, err)
	}
	// The page the interface draws, as the data it fetches.
	r := httptest.NewRequest(http.MethodGet, person, nil)
	r.Header.Set("Accept", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	pw := httptest.NewRecorder()
	handler.ServeHTTP(pw, r)
	var env struct {
		Page struct {
			Report []struct {
				LevelSpace, LevelCustomer, LevelPhase, DocTitle, Note string
			}
		}
	}
	if err := json.Unmarshal(pw.Body.Bytes(), &env); err != nil || len(env.Page.Report) != 2 {
		t.Fatalf("person page data: %v %s", err, pw.Body.String())
	}
	if g := env.Page.Report[1]; g.LevelSpace != want3[0] || g.LevelCustomer != want3[1] || g.LevelPhase != want3[2] || g.DocTitle != want3[3] || g.Note != longNote {
		t.Errorf("person page three-level row = %+v", g)
	}
}
