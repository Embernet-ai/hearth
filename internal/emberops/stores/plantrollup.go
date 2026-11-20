package stores

import (
	"context"
	"math"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// The asset tree is Hearth's (the spine decides what is visible and where it sits), but its numbers are not: open work,
// logged time, parts cost and downtime per asset and per subtree are computed from tables that, in the EmberOps back end,
// stop receiving time, status changes and downtime, so every column read zero for new activity. Tree and Get take the
// structure from Postgres and recompute the numbers from EmberOps, the way the SQL does: work is what links to the asset
// with kind "concerns", time and parts are on that work, downtime is the asset's unplanned outages.
//
// Labour cost is not here: a person's rate is an Activity Cost in EmberOps, which the migration does not write (docs, "Things
// the migration does not carry"), and Hearth has no capacity rows at Fireball, so the figure is zero on both sides.

type ownNumbers struct{ open, minutes, cost, downtime int }

func (s *Plant) centValue() float64 {
	if s.CurrencyPerCent > 0 {
		return s.CurrencyPerCent
	}
	return 0.01
}

func inVals(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, v := range ss {
		out = append(out, v)
	}
	return out
}

func chunks(ss []string, n int) [][]string {
	var out [][]string
	for len(ss) > n {
		out, ss = append(out, ss[:n]), ss[n:]
	}
	if len(ss) > 0 {
		out = append(out, ss)
	}
	return out
}

// own reads what sits on each asset alone, and which assets have an open outage.
func (s *Plant) own(ctx context.Context, ids []string) (map[string]*ownNumbers, map[string]bool, error) {
	out := map[string]*ownNumbers{}
	down := map[string]bool{}
	for _, id := range ids {
		out[id] = &ownNumbers{}
	}
	nameOf := map[string]string{} // Frappe name -> hearth id
	for _, part := range chunks(ids, 100) {
		rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Asset", Fields: []string{"name", "hearth_id"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: inVals(part)}}})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			nameOf[r.S("name")] = idOf(r)
		}
	}
	if len(nameOf) == 0 {
		return out, down, nil
	}
	var names []string
	for n := range nameOf {
		names = append(names, n)
	}

	// Downtime: unplanned minutes, and whether one is open now.
	for _, part := range chunks(names, 100) {
		rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: failureDoctype,
			Fields:  []string{"asset", "downtime_minutes", "downtime_end", "hearth_planned"},
			Filters: []emberops.Filter{{Field: "asset", Op: "in", Value: inVals(part)}}}, 0)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			o := out[nameOf[r.S("asset")]]
			if o == nil {
				continue
			}
			if !r.B("hearth_planned") {
				o.downtime += int(r.I("downtime_minutes"))
			}
			if r.TP("downtime_end", s.loc()) == nil {
				down[nameOf[r.S("asset")]] = true
			}
		}
	}

	// Work that concerns an asset: Task and Maintenance Work Order by the entity links the migration and Hearth keep.
	jobAsset := map[string][2]string{} // "Doctype\x00name" -> asset id, doctype
	byDoctype := map[string][]string{}
	for _, part := range chunks(names, 100) {
		links, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Hearth Entity Link",
			Fields: []string{"from_doctype", "from_name", "to_name"},
			Filters: []emberops.Filter{{Field: "to_doctype", Value: "Asset"}, {Field: "link_kind", Value: "concerns"},
				{Field: "to_name", Op: "in", Value: inVals(part)}}}, 0)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range links {
			dt := l.S("from_doctype")
			if dt != "Task" && dt != "Maintenance Work Order" {
				continue
			}
			jobAsset[dt+"\x00"+l.S("from_name")] = [2]string{nameOf[l.S("to_name")], dt}
			byDoctype[dt] = append(byDoctype[dt], l.S("from_name"))
		}
	}
	live := map[string]bool{}
	for dt, jobs := range byDoctype {
		for _, part := range chunks(jobs, 100) {
			rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: dt, Fields: []string{"name", "status", "hearth_deleted_at"},
				Filters: []emberops.Filter{{Field: "name", Op: "in", Value: inVals(part)}}}, 0)
			if err != nil {
				return nil, nil, err
			}
			for _, r := range rows {
				if r.Has("hearth_deleted_at") {
					continue
				}
				live[dt+"\x00"+r.S("name")] = true
				if st := r.S("status"); st != "Completed" && st != "Cancelled" {
					if a := jobAsset[dt+"\x00"+r.S("name")][0]; out[a] != nil {
						out[a].open++
					}
				}
			}
		}
	}

	// Time and parts on that work.
	for dt, field := range map[string]string{"Task": "task", "Maintenance Work Order": "maintenance_work_order"} {
		for _, part := range chunks(byDoctype[dt], 100) {
			rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Timesheet Detail", Parent: "Timesheet",
				Fields:  []string{field, "hours", "hearth_deleted_at"},
				Filters: []emberops.Filter{{Field: field, Op: "in", Value: inVals(part)}}}, 0)
			if err != nil {
				return nil, nil, err
			}
			for _, r := range rows {
				if r.Has("hearth_deleted_at") || !live[dt+"\x00"+r.S(field)] {
					continue
				}
				if a := jobAsset[dt+"\x00"+r.S(field)][0]; out[a] != nil {
					out[a].minutes += int(math.Round(r.F("hours") * 60))
				}
			}
		}
	}
	for _, part := range chunks(byDoctype["Maintenance Work Order"], 100) {
		rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Stock Entry",
			Fields:  []string{"hearth_maintenance_work_order", "stock_entry_type", "total_outgoing_value", "total_incoming_value"},
			Filters: []emberops.Filter{{Field: "hearth_maintenance_work_order", Op: "in", Value: inVals(part)}, {Field: "docstatus", Op: "!=", Value: 2}}}, 0)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			if !live["Maintenance Work Order\x00"+r.S("hearth_maintenance_work_order")] {
				continue
			}
			a := jobAsset["Maintenance Work Order\x00"+r.S("hearth_maintenance_work_order")][0]
			if out[a] == nil {
				continue
			}
			// consumed adds the cost of what left stock, returned takes it back
			v := r.F("total_outgoing_value") - r.F("total_incoming_value")
			out[a].cost += int(math.Round(v / s.centValue()))
		}
	}
	return out, down, nil
}

// roll puts the numbers on the assets and gathers every asset's up through its ancestors.
func (s *Plant) roll(ctx context.Context, tree []*plant.Asset) error {
	if len(tree) == 0 {
		return nil
	}
	ids := make([]string, 0, len(tree))
	byID := map[string]*plant.Asset{}
	for _, a := range tree {
		ids = append(ids, a.ID)
		byID[a.ID] = a
	}
	own, down, err := s.own(ctx, ids)
	if err != nil {
		return err
	}
	for _, a := range tree {
		o := own[a.ID]
		a.OwnOpen, a.OwnMinutes, a.OwnCostCents, a.OwnDowntime, a.Down = o.open, o.minutes, o.cost, o.downtime, down[a.ID]
		a.RolledOpen, a.RolledMinutes, a.RolledCost, a.RolledDowntime = 0, 0, 0, 0
	}
	for _, a := range tree {
		o := own[a.ID]
		seen := map[string]bool{}
		for x := a; x != nil && !seen[x.ID]; {
			seen[x.ID] = true
			x.RolledOpen += o.open
			x.RolledMinutes += o.minutes
			x.RolledCost += o.cost
			x.RolledDowntime += o.downtime
			if x.ParentID == nil {
				break
			}
			x = byID[*x.ParentID]
		}
	}
	return nil
}

// Tree is Hearth's tree with EmberOps's numbers.
func (s *Plant) Tree(ctx context.Context, orgID string, actor core.Subjects) ([]*plant.Asset, error) {
	tree, err := s.Store.Tree(ctx, orgID, actor)
	if err != nil {
		return nil, err
	}
	return tree, s.roll(ctx, tree)
}

// Get is one asset with its rollup, which is what the asset page shows. The subtree is read from the tree, so the rolled
// figures count every descendant the caller can see.
func (s *Plant) Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*plant.Asset, error) {
	a, err := s.Store.Get(ctx, orgID, actor, id)
	if err != nil || a == nil {
		return a, err
	}
	tree, err := s.Tree(ctx, orgID, actor)
	if err != nil {
		return a, err
	}
	for _, t := range tree {
		if t.ID == id {
			a.OwnOpen, a.RolledOpen, a.OwnMinutes, a.RolledMinutes = t.OwnOpen, t.RolledOpen, t.OwnMinutes, t.RolledMinutes
			a.OwnCostCents, a.RolledCost, a.OwnDowntime, a.RolledDowntime, a.Down = t.OwnCostCents, t.RolledCost, t.OwnDowntime, t.RolledDowntime, t.Down
		}
	}
	return a, nil
}
