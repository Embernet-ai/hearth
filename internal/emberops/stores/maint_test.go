package stores_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
	"github.com/Embernet-ai/hearth/internal/maint"
)

// denyAssets refuses view of the listed assets, as Hearth's grants would.
type denyAssets struct{ deny map[string]bool }

func (d denyAssets) Require(_ context.Context, _ core.Subjects, ref core.Ref, _ core.AccessLevel) error {
	if d.deny[ref.ID] {
		return errors.New("denied")
	}
	return nil
}

func (denyAssets) Grant(context.Context, string, core.Ref, core.Subject, core.AccessLevel, string) error {
	return nil
}

func newMaint(t *testing.T) (*env, *stores.Maint, denyAssets) {
	e := newEnv(t)
	e.f.Put("Asset", map[string]any{"name": "AST-1", "hearth_id": assetID, "asset_name": "Press 4"})
	e.f.Put("Asset", map[string]any{"name": "AST-2", "hearth_id": "66666666-6666-6666-6666-666666666666", "asset_name": "Saw"})
	az := denyAssets{deny: map[string]bool{}}
	return e, &stores.Maint{Base: e.base, Dir: e.dir, Authz: az}, az
}

func TestMaintPlansComeFromEmberOPSWithTheWorkOrderCMMSRaised(t *testing.T) {
	e, s, _ := newMaint(t)
	ctx := e.as("ann-tok")
	days, units, key := 30, 500.0, "hours"
	sch, err := s.Save(ctx, org, actor(ann), maint.Schedule{AssetID: assetID, Name: " Grease ", EveryDays: &days, MeterKey: &key, EveryUnits: &units, LeadDays: 5})
	if err != nil {
		t.Fatal(err)
	}
	plans := e.f.All("Maintenance Plan")
	if len(plans) != 1 || plans[0]["hearth_title"] != "Grease" || plans[0]["frequency"] != float64(30) && plans[0]["frequency"] != 30 || plans[0]["status"] != "Active" || plans[0]["asset"] != "AST-1" {
		t.Fatalf("plan = %v", plans)
	}
	// CMMS raises the work order; Hearth never does.
	e.f.Put("Maintenance Work Order", map[string]any{"name": "MWO-9", "hearth_id": woID, "title": "Grease press", "status": "Open",
		"maintenance_plan": plans[0]["name"], "planned_start": "2026-03-10 00:00:00", "asset": "AST-1", "creation": "2026-03-03 06:00:00"})
	e.f.Put("Maintenance Work Order", map[string]any{"name": "MWO-8", "hearth_id": "88888888-8888-8888-8888-888888888888", "title": "Grease press (Feb)", "status": "Completed",
		"maintenance_plan": plans[0]["name"], "planned_start": "2026-02-10 12:00:00", "actual_end": "2026-02-10 12:30:00", "asset": "AST-1", "creation": "2026-02-03 06:00:00"})

	before := len(e.f.Calls)
	list, err := s.List(ctx, org, actor(ann))
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v %v", list, err)
	}
	got := list[0]
	if got.ID != sch.ID || got.AssetID != assetID || got.AssetTitle != "Press 4" || got.Name != "Grease" || got.LeadDays != 5 ||
		got.EveryDays == nil || *got.EveryDays != 30 || got.MeterKey == nil || *got.MeterKey != "hours" || !got.Enabled {
		t.Fatalf("schedule = %+v", got)
	}
	if got.Open == nil || got.Open.WorkOrder != "Grease press" || got.Open.WorkOrderID == nil || *got.Open.WorkOrderID != woID || got.Open.DueAt.Day() != 10 {
		t.Fatalf("open occurrence = %+v", got.Open)
	}
	if n := len(e.f.Calls) - before; n > 7 {
		t.Fatalf("%d calls to list plans: %v", n, e.f.Calls[before:])
	}

	hist, err := s.HistoryForAsset(ctx, org, actor(ann), assetID, 10)
	if err != nil || len(hist) != 2 || hist[0].Name != "Grease" || hist[1].CompletedAt == nil {
		t.Fatalf("history = %+v %v", hist, err)
	}
	rep, err := s.Report(ctx, org, actor(ann), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || rep.Due != 2 || rep.OnTime != 1 || rep.Missed != 1 {
		t.Fatalf("report = %+v %v", rep, err)
	}

	// Pause and resume, then remove.
	if err := s.SetEnabled(ctx, org, actor(ann), sch.ID, false); err != nil {
		t.Fatal(err)
	}
	if e.f.All("Maintenance Plan")[0]["status"] != "Paused" {
		t.Fatal("not paused")
	}
	if g, _ := s.Get(ctx, org, actor(ann), sch.ID); g.Enabled {
		t.Fatal("still enabled")
	}
	if err := s.Delete(ctx, org, actor(ann), sch.ID); err != nil || len(e.f.All("Maintenance Plan")) != 0 {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, org, actor(ann), sch.ID); !errors.Is(err, maint.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestMaintPlansInheritTheirAssetsVisibility(t *testing.T) {
	e, s, az := newMaint(t)
	ctx := e.as("ann-tok")
	d := 7
	a, _ := s.Save(ctx, org, actor(ann), maint.Schedule{AssetID: assetID, Name: "A", EveryDays: &d})
	b, _ := s.Save(ctx, org, actor(ann), maint.Schedule{AssetID: "66666666-6666-6666-6666-666666666666", Name: "B", EveryDays: &d})
	az.deny["66666666-6666-6666-6666-666666666666"] = true
	list, _ := s.List(ctx, org, actor(ann))
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("list = %+v", list)
	}
	if _, err := s.Get(ctx, org, actor(ann), b.ID); !errors.Is(err, maint.ErrNotFound) {
		t.Fatalf("hidden plan: %v", err)
	}
	if _, err := s.Save(ctx, org, actor(ann), maint.Schedule{AssetID: assetID, Name: "x"}); !errors.Is(err, maint.ErrInvalid) {
		t.Fatalf("a plan with no clock: %v", err)
	}
}

func TestMaintMetersAndTheirProjection(t *testing.T) {
	e, s, _ := newMaint(t)
	ctx := e.as("ann-tok")
	units, key := 100.0, "hours"
	if _, err := s.Save(ctx, org, actor(ann), maint.Schedule{AssetID: assetID, Name: "Service", MeterKey: &key, EveryUnits: &units}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordReading(ctx, org, actor(ann), assetID, "hours", -1, ""); !errors.Is(err, maint.ErrInvalid) {
		t.Fatalf("negative reading: %v", err)
	}
	// Ten days ago 0 hours, now 50: five a day, so the next 100 is another ten days out.
	e.f.Put("Hearth Meter Reading", map[string]any{"name": "R1", "hearth_id": "r1", "asset": "AST-1", "meter_key": "hours", "value": 0.0,
		"read_at": time.Now().UTC().Add(-10 * 24 * time.Hour).Format("2006-01-02 15:04:05")})
	r, err := s.RecordReading(ctx, org, actor(ann), assetID, "hours", 50, " ok ")
	if err != nil || r.Note != "ok" {
		t.Fatalf("reading = %+v %v", r, err)
	}
	if got := e.f.All("Hearth Meter Reading"); len(got) != 2 {
		t.Fatalf("readings = %v", got)
	}
	latest, _ := s.LatestReading(ctx, assetID, "hours")
	if latest == nil || latest.Value != 50 {
		t.Fatalf("latest = %+v", latest)
	}
	rs, err := s.Readings(ctx, org, actor(ann), assetID, 10)
	if err != nil || len(rs) != 2 || rs[0].Value != 50 || rs[0].ReadBy != ann || rs[0].ReadName != "Ann" {
		t.Fatalf("readings = %+v %v", rs, err)
	}
	if keys, _ := s.MeterKeys(ctx, assetID); len(keys) != 1 || keys[0] != "hours" {
		t.Fatalf("keys = %v", keys)
	}
	ps, _ := s.ForAsset(ctx, org, actor(ann), assetID)
	if len(ps) != 1 || ps[0].ProjectedMeterDue == nil {
		t.Fatalf("plans = %+v", ps)
	}
	days := time.Until(*ps[0].ProjectedMeterDue).Hours() / 24
	if days < 9 || days > 11 {
		t.Fatalf("projected %.1f days out, want about 10", days)
	}
}
