package crm_test

// Contacts, companies, and parts, against a real Postgres.
//
// The two claims worth checking are that a contact behaves like every other
// entity, which means grants and search rather than a bespoke list, and that
// parts consumed change what a work order is on file as having cost.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/search"
)

type fx struct {
	svc    *crm.Service
	docs   *docs.Service
	az     *authz.Service
	search *search.Service
	pools  *pg.Pools
	org    string
	alice  string
	bob    string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	az := authz.New(pools.Web)
	f := &fx{
		svc: crm.New(pools.Web, az), docs: docs.New(pools.Web, az), az: az,
		// Postgres only, deliberately. The floor has to find a contact on its
		// own, or a cluster outage takes the phone book with it.
		search: search.New(nil, search.NewPostgres(pools.Web)),
		pools:  pools,
		org:    uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'CRM Test')`, f.org, "ct-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM part_movements WHERE org_id=$1`,
			`DELETE FROM parts WHERE org_id=$1`,
			`DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

// -----------------------------------------------------------------------------

// A contact is searchable and grantable like a document. Not "has its own
// search" and not "has its own sharing": the same two, because it is an
// ordinary entity keyed the same way.
func TestAContactIsSearchableAndGrantableLikeADocument(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	co, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{
		Name: "Baldor Electric", Domain: "https://www.baldor.com/", Kind: crm.KindManufacturer,
	})
	if err != nil {
		t.Fatalf("create company: %v", err)
	}
	// Whatever somebody pastes, one shape is stored, or the inbound mail
	// matcher in the next phase finds none of them.
	if co.Domain != "baldor.com" {
		t.Errorf("domain stored as %q, want baldor.com", co.Domain)
	}

	person, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{
		CompanyID: &co.ID, Name: "Dana Whitfield", Email: "Dana@Baldor.com",
		Phone: "01827 555019", Title: "Rewind supervisor",
	})
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	if person.Email != "dana@baldor.com" {
		t.Errorf("email stored as %q, want it lowercased", person.Email)
	}

	// Findable by name, by what they do, and by their address, because those
	// are the three things somebody actually remembers.
	for _, term := range []string{"Whitfield", "rewind", "baldor.com"} {
		res, err := f.search.Search(ctx, f.org, alice, search.Query{Text: term})
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		found := false
		for _, h := range res.Hits {
			if h.EntityID == person.ID || h.EntityID == co.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("searching %q found neither the contact nor the company.\n"+
				"Somebody looking for the chap at Baldor who does the rewinds "+
				"types one of those three.", term)
		}
	}

	// And grantable: Bob sees nothing until he is granted, then sees it.
	if res, err := f.search.Search(ctx, f.org, bob, search.Query{Text: "Whitfield"}); err != nil {
		t.Fatalf("search as bob: %v", err)
	} else if len(res.Hits) != 0 {
		t.Errorf("Bob has no grant but got %d hits", len(res.Hits))
	}
	if _, err := f.svc.GetContact(ctx, f.org, bob, person.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Errorf("GetContact as Bob returned %v, want not-found", err)
	}
	if list, err := f.svc.Contacts(ctx, f.org, bob, "", ""); err != nil {
		t.Fatalf("list as bob: %v", err)
	} else if len(list) != 0 {
		t.Errorf("Bob sees %d contacts he was not granted", len(list))
	}

	if err := f.az.Grant(ctx, f.org, person.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := f.svc.GetContact(ctx, f.org, bob, person.ID); err != nil {
		t.Errorf("Bob was granted view and still cannot read the contact: %v", err)
	}
	res, err := f.search.Search(ctx, f.org, bob, search.Query{Text: "Whitfield"})
	if err != nil {
		t.Fatalf("search as bob: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Error("Bob was granted the contact and still cannot find it.\n" +
			"The permission filter is the same one documents use, so if this " +
			"fails the two have drifted apart.")
	}
}

// A person added to a firm everybody can see must not be a row only the author
// can find.
func TestAContactInheritsWhoCanSeeItsCompany(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	co, err := f.svc.CreateCompany(ctx, f.org, alice, crm.Company{Name: "Shared Vendor"})
	if err != nil {
		t.Fatalf("create company: %v", err)
	}
	if err := f.az.Grant(ctx, f.org, co.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessEdit, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	person, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{
		CompanyID: &co.ID, Name: "Sam Okafor",
	})
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	if _, err := f.svc.GetContact(ctx, f.org, bob, person.ID); err != nil {
		t.Errorf("Bob can edit the company but cannot see somebody at it: %v", err)
	}
}

// Two contacts cannot share an address, because the inbound mail path resolves
// on it and picking whichever duplicate sorted first is not resolving.
func TestOneAddressBelongsToOnePerson(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if _, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{
		Name: "First", Email: "shared@example.com",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{
		Name: "Second", Email: "Shared@Example.com",
	}); !errors.Is(err, crm.ErrDuplicate) {
		t.Errorf("a second contact took the same address: %v", err)
	}

	got, err := f.svc.ByEmail(ctx, f.org, "SHARED@example.com")
	if err != nil {
		t.Fatalf("by email: %v", err)
	}
	if got.Name != "First" {
		t.Errorf("resolved to %q", got.Name)
	}

	// Contacts with no address at all are fine, and there can be many of them.
	for i := 0; i < 3; i++ {
		if _, err := f.svc.CreateContact(ctx, f.org, alice, crm.Contact{Name: "Nameless"}); err != nil {
			t.Fatalf("a contact with no address was refused: %v", err)
		}
	}
}

// -----------------------------------------------------------------------------
// Parts
// -----------------------------------------------------------------------------

func (f *fx) job(t *testing.T, title string) *docs.Document {
	t.Helper()
	d, err := f.docs.Create(context.Background(), f.org, f.subj(t, f.alice), docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: title,
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return d
}

// The criterion: parts consumed appear in the work order cost.
func TestPartsConsumedAppearInTheWorkOrderCost(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	bearing, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{
		Name: "Bearing, SKF 6205", SKU: "6205-2rs", UnitCostCents: 1250, ReorderAt: 2,
	})
	if err != nil {
		t.Fatalf("save part: %v", err)
	}
	if bearing.SKU != "6205-2RS" {
		t.Errorf("part number stored as %q, want it uppercased", bearing.SKU)
	}
	seal, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{
		Name: "Shaft seal", UnitCostCents: 800,
	})
	if err != nil {
		t.Fatalf("save part: %v", err)
	}

	// Stock arrives.
	po := f.job(t, "PO 1001")
	if _, err := f.svc.Receive(ctx, f.org, alice, po.ID, bearing.ID, 10, 1300, "delivered"); err != nil {
		t.Fatalf("receive: %v", err)
	}

	job := f.job(t, "Replace the drive end bearing")
	if _, err := f.svc.Consume(ctx, f.org, alice, job.ID, bearing.ID, 2, ""); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, err := f.svc.Consume(ctx, f.org, alice, job.ID, seal.ID, 1, ""); err != nil {
		t.Fatalf("consume: %v", err)
	}

	// Two bearings at the delivered price plus one seal: 2600 + 800.
	cost, err := f.svc.PartsCost(ctx, f.org, job.ID)
	if err != nil {
		t.Fatalf("parts cost: %v", err)
	}
	if cost != 3400 {
		t.Errorf("the job cost %d in parts, want 3400.\n"+
			"Labour alone is half the number, and the asset roll-up a "+
			"replacement decision is made from is wrong by exactly this much.",
			cost)
	}

	// A return comes off it.
	if _, err := f.svc.Return(ctx, f.org, alice, job.ID, bearing.ID, 1, "not needed"); err != nil {
		t.Fatalf("return: %v", err)
	}
	if cost, err = f.svc.PartsCost(ctx, f.org, job.ID); err != nil {
		t.Fatalf("parts cost: %v", err)
	}
	if cost != 2100 {
		t.Errorf("after returning one bearing the job cost %d, want 2100", cost)
	}

	// And the shelf reflects all of it: ten in, two out, one back.
	got, err := f.svc.GetPart(ctx, f.org, bearing.ID)
	if err != nil {
		t.Fatalf("get part: %v", err)
	}
	if got.OnHand != 9 {
		t.Errorf("%v on hand, want 9 (ten received, two fitted, one returned)", got.OnHand)
	}
	if got.Used != 2 {
		t.Errorf("%v used, want 2", got.Used)
	}
	// The delivery told us the real price, so the catalogue moved.
	if got.UnitCostCents != 1300 {
		t.Errorf("unit cost is %d, want 1300 after the delivery", got.UnitCostCents)
	}
}

// A price rise must not rewrite history. This is the reason the cost is copied
// onto the movement rather than joined from the catalogue.
func TestAPriceRiseDoesNotChangeWhatLastYearsRepairCost(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	part, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{Name: "Belt", UnitCostCents: 1000})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	job := f.job(t, "Belt change")
	if _, err := f.svc.Consume(ctx, f.org, alice, job.ID, part.ID, 1, ""); err != nil {
		t.Fatalf("consume: %v", err)
	}

	// The supplier doubles the price.
	if _, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{
		ID: part.ID, Name: "Belt", UnitCostCents: 2000,
	}); err != nil {
		t.Fatalf("reprice: %v", err)
	}

	cost, err := f.svc.PartsCost(ctx, f.org, job.ID)
	if err != nil {
		t.Fatalf("cost: %v", err)
	}
	if cost != 1000 {
		t.Errorf("the old repair now costs %d, want 1000.\n"+
			"A maintenance history whose numbers move under you is not "+
			"evidence of anything.", cost)
	}
}

// Stock is a sum over movements, so a correction is a row with a reason on it.
func TestStockIsASumWithAnExplanationBehindIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	part, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{Name: "Filter", UnitCostCents: 500})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := f.svc.Receive(ctx, f.org, alice, "", part.ID, 4, 0, ""); err != nil {
		t.Fatalf("receive: %v", err)
	}
	// A stocktake finds one missing.
	if _, err := f.svc.Adjust(ctx, f.org, alice, part.ID, -1, "stocktake, one unaccounted"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	// And an unexplained correction is refused, which is the whole point of
	// keeping movements instead of a counter.
	if _, err := f.svc.Adjust(ctx, f.org, alice, part.ID, -1, ""); !errors.Is(err, crm.ErrInvalid) {
		t.Errorf("an unexplained adjustment was accepted: %v", err)
	}

	got, err := f.svc.GetPart(ctx, f.org, part.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.OnHand != 3 {
		t.Errorf("%v on hand, want 3", got.OnHand)
	}
	// No reorder point means nobody has said, which has to read as unknown
	// rather than as "never reorder" and rather than as "order more now".
	if got.Low() {
		t.Error("a part with no reorder point set reads as low on stock")
	}
	if _, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{
		ID: part.ID, Name: "Filter", UnitCostCents: 500, ReorderAt: 3,
	}); err != nil {
		t.Fatalf("set a reorder point: %v", err)
	}
	if got, err = f.svc.GetPart(ctx, f.org, part.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Low() {
		t.Errorf("three on hand against a reorder point of three does not read "+
			"as low, so nobody is told to order more: %+v", got)
	}

	history, err := f.svc.HistoryFor(ctx, f.org, part.ID, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("%d movements on file, want 2", len(history))
	}
	if history[0].Reason != crm.ReasonAdjusted || history[0].Note == "" {
		t.Error("the correction is on file with no explanation attached, which " +
			"is the thing this table exists to prevent")
	}
}

// Recording a part is comment access, like logging time. Deleting the record
// changes what the repair is on file as having cost, so that needs edit.
func TestFittingAPartIsCommentAccessAndRemovingTheRecordIsNot(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	part, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{Name: "Gasket", UnitCostCents: 300})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	job := f.job(t, "Head gasket")

	if _, err := f.svc.Consume(ctx, f.org, bob, job.ID, part.ID, 1, ""); err == nil {
		t.Error("somebody with no access fitted a part to a job")
	}
	if err := f.az.Grant(ctx, f.org, job.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	m, err := f.svc.Consume(ctx, f.org, bob, job.ID, part.ID, 1, "fitted")
	if err != nil {
		t.Fatalf("comment access was not enough to fit a part: %v", err)
	}
	if err := f.svc.RemoveMovement(ctx, f.org, bob, m.ID); err == nil {
		t.Error("a commenter erased what the repair cost")
	}
	if err := f.svc.RemoveMovement(ctx, f.org, alice, m.ID); err != nil {
		t.Errorf("an editor could not remove a mistaken record: %v", err)
	}
}

// A part that is scrapped from the catalogue must not make old repairs cheaper.
func TestDeletingAPartDoesNotRewriteWhatARepairCost(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	part, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{Name: "Obsolete relay", UnitCostCents: 4500})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	job := f.job(t, "Relay swap")
	if _, err := f.svc.Consume(ctx, f.org, alice, job.ID, part.ID, 1, ""); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := f.svc.DeletePart(ctx, f.org, alice, part.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	cost, err := f.svc.PartsCost(ctx, f.org, job.ID)
	if err != nil {
		t.Fatalf("cost: %v", err)
	}
	if cost != 4500 {
		t.Errorf("the repair costs %d after the part was removed from the "+
			"catalogue, want 4500", cost)
	}
	if list, err := f.svc.Parts(ctx, f.org, ""); err != nil {
		t.Fatalf("list: %v", err)
	} else if len(list) != 0 {
		t.Errorf("the deleted part is still in the catalogue")
	}
	// And the movement is still readable on the job, so the line item does not
	// become an unexplained amount.
	used, err := f.svc.MovementsFor(ctx, f.org, alice, job.ID)
	if err != nil {
		t.Fatalf("movements: %v", err)
	}
	if len(used) != 1 || used[0].PartName != "Obsolete relay" {
		t.Errorf("the line item lost its part name: %+v", used)
	}
}

// Parts roll up to the asset through the same links the labour does.
func TestPartsRollUpToTheAsset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	asset, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindAsset, Title: "Compressor 7",
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	part, err := f.svc.SavePart(ctx, f.org, alice, crm.Part{Name: "Valve plate", UnitCostCents: 9000})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	for i := 0; i < 2; i++ {
		job := f.job(t, "Valve plate change")
		if err := f.docs.Link(ctx, f.org, alice, job.Ref(), asset.Ref(), "concerns"); err != nil {
			t.Fatalf("link: %v", err)
		}
		if _, err := f.svc.Consume(ctx, f.org, alice, job.ID, part.ID, 1, ""); err != nil {
			t.Fatalf("consume: %v", err)
		}
	}
	// One job on nothing, which must not count.
	loose := f.job(t, "Unrelated")
	if _, err := f.svc.Consume(ctx, f.org, alice, loose.ID, part.ID, 5, ""); err != nil {
		t.Fatalf("consume: %v", err)
	}

	cents, err := f.svc.PartsCostForAsset(ctx, f.org, asset.ID)
	if err != nil {
		t.Fatalf("asset parts cost: %v", err)
	}
	if cents != 18000 {
		t.Errorf("the compressor has cost %d in parts, want 18000.\n"+
			"Two valve plates on it, and five on a job that concerns nothing.", cents)
	}
	_ = time.Now
}
