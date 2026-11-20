package stores_test

import (
	"errors"
	"testing"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

func newCRM(t *testing.T) (*env, *stores.CRM) {
	e := newEnv(t)
	e.f.Put("Maintenance Work Order", map[string]any{"name": "MWO-1", "hearth_id": woID, "title": "PM press", "asset": "AST-1"})
	e.f.Put("Asset", map[string]any{"name": "AST-1", "hearth_id": assetID, "asset_name": "Press 4"})
	s := &stores.CRM{Base: e.base, Dir: e.dir, Authz: denyAssets{deny: map[string]bool{}}, CurrencyPerCent: 0.01,
		Docs: docLookup{byID: map[string]*docs.Document{
			woID:    {ID: woID, Kind: docs.KindWorkOrder, Title: "PM press"},
			assetID: {ID: assetID, Kind: docs.KindAsset, Title: "Press 4"},
		}}}
	return e, s
}

func TestCRMCompaniesAndContacts(t *testing.T) {
	e, s := newCRM(t)
	ctx := e.as("ann-tok")
	acme, err := s.CreateCompany(ctx, org, actor(ann), crm.Company{Name: " Acme ", Kind: "customer", Domain: "https://www.Acme.com/about"})
	if err != nil {
		t.Fatal(err)
	}
	bolt, err := s.CreateCompany(ctx, org, actor(ann), crm.Company{Name: "Bolt Supply", Kind: "vendor"})
	if err != nil {
		t.Fatal(err)
	}
	if c := e.f.All("Customer"); len(c) != 1 || c[0]["customer_name"] != "Acme" || c[0]["hearth_domain"] != "acme.com" {
		t.Fatalf("customers = %v", c)
	}
	if c := e.f.All("Supplier"); len(c) != 1 || c[0]["supplier_name"] != "Bolt Supply" {
		t.Fatalf("suppliers = %v", c)
	}
	if _, err := s.CreateCompany(ctx, org, actor(ann), crm.Company{Name: "Acme Two", Kind: "customer", Domain: "acme.com"}); !errors.Is(err, crm.ErrDuplicate) {
		t.Fatalf("duplicate domain: %v", err)
	}
	if _, err := s.CreateCompany(ctx, org, actor(ann), crm.Company{Name: " "}); !errors.Is(err, crm.ErrInvalid) {
		t.Fatalf("nameless: %v", err)
	}
	if err := s.UpdateCompany(ctx, org, actor(ann), crm.Company{ID: acme.ID, Name: "Acme", Kind: "vendor"}); !errors.Is(err, crm.ErrInvalid) {
		t.Fatalf("a customer cannot become a vendor: %v", err)
	}

	ct, err := s.CreateContact(ctx, org, actor(ann), crm.Contact{Name: "Dana Reyes", Email: "dana@acme.com", Phone: "555", Title: "Buyer", CompanyID: &acme.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, org, actor(ann), crm.Contact{Name: "Lee", CompanyID: strp("nope")}); !errors.Is(err, crm.ErrInvalid) {
		t.Fatalf("contact at a missing company: %v", err)
	}

	before := len(e.f.Calls)
	cs, err := s.Contacts(ctx, org, actor(ann), "", "")
	if err != nil || len(cs) != 1 || cs[0].ID != ct.ID || cs[0].Name != "Dana Reyes" || cs[0].CompanyName != "Acme" || cs[0].Title != "Buyer" || cs[0].Email != "dana@acme.com" {
		t.Fatalf("contacts = %+v %v", cs, err)
	}
	if n := len(e.f.Calls) - before; n > 5 {
		t.Fatalf("%d calls to list contacts: %v", n, e.f.Calls[before:])
	}
	if cs, _ := s.Contacts(ctx, org, actor(ann), bolt.ID, ""); len(cs) != 0 {
		t.Fatalf("contacts at the other company: %v", cs)
	}
	if got, err := s.ByEmail(ctx, org, "dana@acme.com"); err != nil || got == nil || got.ID != ct.ID {
		t.Fatalf("by email: %+v %v", got, err)
	}
	comps, err := s.Companies(ctx, org, actor(ann), "", "")
	if err != nil || len(comps) != 2 || comps[0].Name != "Acme" || comps[0].Contacts != 1 || comps[1].Contacts != 0 {
		t.Fatalf("companies = %+v %v", comps, err)
	}
	if comps, _ := s.Companies(ctx, org, actor(ann), "vendor", "bolt"); len(comps) != 1 {
		t.Fatalf("filtered companies: %v", comps)
	}

	if err := s.DeleteContact(ctx, org, actor(ann), ct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContact(ctx, org, actor(ann), ct.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("deleted contact: %v", err)
	}
	if err := s.DeleteCompany(ctx, org, actor(ann), bolt.ID); err != nil {
		t.Fatal(err)
	}
	if comps, _ := s.Companies(ctx, org, actor(ann), "", ""); len(comps) != 1 {
		t.Fatalf("a deleted company is listed: %v", comps)
	}
}

func TestCRMPartsAndMovements(t *testing.T) {
	e, s := newCRM(t)
	ctx := e.as("ann-tok")
	sup, _ := s.CreateCompany(ctx, org, actor(ann), crm.Company{Name: "Bolt Supply", Kind: "vendor"})
	p, err := s.SavePart(ctx, org, actor(ann), crm.Part{SKU: " BRG-1 ", Name: "Bearing", UnitCostCents: 2500, ReorderAt: 3, SupplierID: &sup.ID})
	if err != nil {
		t.Fatal(err)
	}
	items := e.f.All("Item")
	if len(items) != 1 || items[0]["item_code"] != "BRG-1" || items[0]["valuation_rate"] != 25.0 || items[0]["hearth_reorder_at"] != 3.0 && items[0]["hearth_reorder_at"] != 3 {
		t.Fatalf("items = %v", items)
	}
	if p.SupplierName != "Bolt Supply" || p.UnitCostCents != 2500 {
		t.Fatalf("part = %+v", p)
	}

	if _, err := s.Consume(ctx, org, actor(ann), "", p.ID, 1, ""); !errors.Is(err, crm.ErrInvalid) {
		t.Fatalf("consume against nothing: %v", err)
	}
	if _, err := s.Receive(ctx, org, actor(ann), "", p.ID, 10, 0, "delivery"); err != nil {
		t.Fatal(err)
	}
	cons, err := s.Consume(ctx, org, actor(ann), woID, p.ID, 3, "")
	if err != nil || cons.Quantity != -3 || cons.UnitCostCents != 2500 {
		t.Fatalf("consume = %+v %v", cons, err)
	}
	ret, err := s.Return(ctx, org, actor(ann), woID, p.ID, 1, "too many")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adjust(ctx, org, actor(ann), p.ID, -1, ""); !errors.Is(err, crm.ErrInvalid) {
		t.Fatalf("unexplained adjustment: %v", err)
	}
	if _, err := s.Adjust(ctx, org, actor(ann), p.ID, -1, "dropped one"); err != nil {
		t.Fatal(err)
	}

	entries := e.f.All("Stock Entry")
	if len(entries) != 4 {
		t.Fatalf("stock entries = %d", len(entries))
	}
	types := map[string]int{}
	for _, en := range entries {
		types[en["stock_entry_type"].(string)]++
		if en["docstatus"] != 1.0 {
			t.Fatalf("a stock entry was left unposted: %v", en)
		}
	}
	if types["Material Issue"] != 2 || types["Material Receipt"] != 2 {
		t.Fatalf("entry types = %v", types)
	}

	// The shelf, as ERPNext's Bin sees it: 10 - 3 + 1 - 1.
	e.f.Put("Bin", map[string]any{"item_code": "BRG-1", "warehouse": "Stores", "actual_qty": 7.0})
	got, err := s.GetPart(ctx, org, p.ID)
	if err != nil || got.OnHand != 7 || got.Used != 4 || got.Low() {
		t.Fatalf("part = %+v %v", got, err)
	}

	before := len(e.f.Calls)
	ms, err := s.MovementsFor(ctx, org, actor(ann), woID)
	if err != nil || len(ms) != 2 {
		t.Fatalf("movements for the job = %+v %v", ms, err)
	}
	if n := len(e.f.Calls) - before; n > 8 {
		t.Fatalf("%d calls for a job's parts: %v", n, e.f.Calls[before:])
	}
	reasons := map[string]float64{}
	for _, m := range ms {
		reasons[m.Reason] = m.Quantity
		if m.DocumentID == nil || *m.DocumentID != woID || m.PartID != p.ID || m.DocumentTitle != "PM press" {
			t.Fatalf("movement = %+v", m)
		}
	}
	if reasons["consumed"] != -3 || reasons["returned"] != 1 {
		t.Fatalf("reasons = %v", reasons)
	}
	// Two bearings net of the one put back, at 25.00 each.
	if c, err := s.PartsCost(ctx, org, woID); err != nil || c != 5000 {
		t.Fatalf("parts cost = %d %v", c, err)
	}
	if c, err := s.PartsCostForAsset(ctx, org, assetID); err != nil || c != 5000 {
		t.Fatalf("parts cost for asset = %d %v", c, err)
	}
	hist, err := s.HistoryFor(ctx, org, p.ID, 10)
	if err != nil || len(hist) != 4 {
		t.Fatalf("history = %d %v", len(hist), err)
	}
	var adj *crm.Movement
	for i := range hist {
		if hist[i].Reason == "adjusted" {
			adj = &hist[i]
		}
	}
	if adj == nil || adj.Note != "dropped one" || adj.Quantity != -1 {
		t.Fatalf("adjustment = %+v", adj)
	}

	if err := s.RemoveMovement(ctx, org, actor(ann), ret.ID); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.MovementsFor(ctx, org, actor(ann), woID); len(ms) != 1 {
		t.Fatalf("a removed movement is still listed: %v", ms)
	}
	if err := s.DeletePart(ctx, org, actor(ann), p.ID); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.Parts(ctx, org, ""); len(ps) != 0 {
		t.Fatalf("a deleted part is listed: %v", ps)
	}
	if _, err := s.Consume(ctx, org, actor(ann), woID, p.ID, 1, ""); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("consume a deleted part: %v", err)
	}
	_ = core.AccessView
}

func TestCRMPartiesComeFromTheLinksCopiedIntoEmberOPS(t *testing.T) {
	e, s := newCRM(t)
	ctx := e.as("ann-tok")
	acme, _ := s.CreateCompany(ctx, org, actor(ann), crm.Company{Name: "Acme", Kind: "customer"})
	ct, _ := s.CreateContact(ctx, org, actor(ann), crm.Contact{Name: "Dana", Title: "Buyer"})
	// Links as the document store copies them.
	e.f.Put("Hearth Entity Link", map[string]any{"name": "L1", "hearth_id": "l1", "from_doctype": "Maintenance Work Order", "from_name": "MWO-1", "to_doctype": "Contact", "to_name": e.f.All("Contact")[0]["name"]})
	e.f.Put("Hearth Entity Link", map[string]any{"name": "L2", "hearth_id": "l2", "from_doctype": "Customer", "from_name": e.f.All("Customer")[0]["name"], "to_doctype": "Maintenance Work Order", "to_name": "MWO-1"})
	parties, err := s.PartiesFor(ctx, org, actor(ann), woID)
	if err != nil || len(parties) != 2 {
		t.Fatalf("parties = %+v %v", parties, err)
	}
	if parties[0].Type != core.EntityCompany || parties[0].Name != "Acme" || parties[0].ID != acme.ID ||
		parties[1].Type != core.EntityContact || parties[1].Name != "Dana" || parties[1].ID != ct.ID || parties[1].Subtext != "Buyer" {
		t.Fatalf("parties = %+v", parties)
	}
}
