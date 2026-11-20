package mapping

import (
	"reflect"
	"testing"
	"time"
)

func TestTaskDocFollowsTheAddonMapping(t *testing.T) {
	z := TZ{Loc: time.FixedZone("EST", -5*3600)}
	got := z.TaskDoc(Rec{
		"id": "t1", "title": "Fix belt", "status": "blocked", "priority": float64(9), "space_id": "s1",
		"due_at": "2026-03-04T15:00:00+00:00", "estimate_minutes": float64(90),
		"assignee_emails": []any{"a@x.com"}, "primary_assignee_email": "a@x.com", "parent_id": "p1",
		"checklist": []any{
			map[string]any{"id": "c2", "text": "second", "position": float64(2)},
			map[string]any{"id": "c1", "text": "first", "position": float64(1), "done_at": "2026-03-01T00:00:00Z"},
		},
	})
	want := map[string]any{
		"doctype": "Task", "hearth_id": "t1", "subject": "Fix belt", "status": "Blocked", "priority": "Urgent",
		"hearth_space": "hearth:s1", "parent_task": "hearth:p1", "hearth_body_format": "markdown",
		"exp_end_date": "2026-03-04", "hearth_due_at": "2026-03-04 10:00:00", "expected_time": 1.5,
		"hearth_primary_assignee": "a@x.com", "_assign": []string{"a@x.com"},
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
	rows := got["hearth_checklist"].([]any)
	if len(rows) != 2 || rows[0].(Doc)["item_text"] != "first" || rows[0].(Doc)["done"] != 1 || rows[1].(Doc)["done"] != 0 {
		t.Errorf("checklist = %v", rows)
	}
	if _, ok := got["hearth_started_at"]; ok {
		t.Error("null fields must be dropped so an upsert never blanks them")
	}
}

func TestWorkOrderIsPlannedWhenGeneratedFromAPlan(t *testing.T) {
	z := TZ{}
	pm := z.WorkOrderDoc(Rec{"id": "w1", "title": "PM", "schedule_id": "pm1", "asset_id": "a1", "priority": float64(3)})
	if pm["wo_type"] != "Planned" || pm["maintenance_category"] != "Preventive" || pm["priority"] != "Critical" || pm["asset"] != "hearth:a1" {
		t.Fatalf("%v", pm)
	}
	if z.WorkOrderDoc(Rec{"id": "w2"})["wo_type"] != "Reactive" {
		t.Fatal("unplanned work order should be reactive")
	}
}

func TestTimeEntryRowTargetsAndApproval(t *testing.T) {
	z := TZ{}
	row := z.TimeEntryRow(Rec{"id": "e1", "principal_id": "p", "started_at": "2026-01-01T09:00:00Z", "ended_at": "2026-01-01T10:30:00Z",
		"minutes": float64(90), "target_kind": "asset", "document_id": "a1", "decided_at": "2026-01-02T00:00:00Z", "approved": true}, []string{"Repair", "Travel"}, "Hearth")
	if row["activity_type"] != "Repair" || row["hearth_tags"] != "Repair,Travel" || row["hours"] != 1.5 ||
		row["hearth_approval"] != "approved" || row["hearth_ref_doctype"] != "Asset" || row["hearth_ref_name"] != "hearth:a1" {
		t.Fatalf("%v", row)
	}
	if z.TimeEntryRow(Rec{"id": "e2", "started_at": "2026-01-01T09:00:00Z"}, nil, "Hearth")["hearth_approval"] != "pending" {
		t.Fatal("undecided entry should be pending")
	}
}

func TestCompanyKindPicksCustomerOrSupplier(t *testing.T) {
	z := TZ{}
	if d := z.CompanyDoc(Rec{"id": "c", "name": "Acme", "kind": "customer", "domain": " ACME.com "}); d["doctype"] != "Customer" || d["hearth_domain"] != "acme.com" {
		t.Fatalf("%v", d)
	}
	if d := z.CompanyDoc(Rec{"id": "c", "name": "Bolt", "kind": "vendor"}); d["doctype"] != "Supplier" || d["supplier_name"] != "Bolt" {
		t.Fatalf("%v", d)
	}
}

func TestMovementEntryTypeKeepsTheSign(t *testing.T) {
	cases := []struct {
		reason string
		q      float64
		want   string
	}{{"consumed", -2, "Material Issue"}, {"received", 5, "Material Receipt"}, {"adjusted", -1, "Material Issue"}, {"adjusted", 1, "Material Receipt"}}
	for _, c := range cases {
		if got := MovementEntryType(c.reason, c.q); got != c.want {
			t.Errorf("%s %v = %s", c.reason, c.q, got)
		}
	}
}

func TestEmployeesAskForNoUserPermission(t *testing.T) {
	d := EmployeeDoc(Rec{"id": "p1", "display_name": "Ann", "email": "ann@example.com"})
	if d["create_user_permission"] != 0 {
		t.Fatalf("employee doc = %v", d)
	}
}

func TestASuspendedPersonIsAnInactiveEmployee(t *testing.T) {
	if EmployeeDoc(Rec{"id": "p", "display_name": "A", "email": "a@x.com", "status": "suspended"})["status"] != "Inactive" ||
		EmployeeDoc(Rec{"id": "p", "display_name": "A", "email": "a@x.com", "status": "active"})["status"] != "Active" {
		t.Fatal("employee status does not follow the person's")
	}
}
