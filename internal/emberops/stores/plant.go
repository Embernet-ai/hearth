package stores

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
	"github.com/Embernet-ai/hearth/internal/plant"
)

// SpaceSource reads Hearth's own space structure and access. *plant.Service
// provides it.
type SpaceSource interface {
	SpaceAccessAll(ctx context.Context, orgID string) ([]*plant.SpaceAccess, error)
}

var _ SpaceSource = (*plant.Service)(nil)

// SpaceSync copies Hearth's space tree, its department members and managers,
// and its space grants into EmberOPS as Hearth Spaces, so the permission hooks
// the addon runs inside EmberOPS (permission_query_conditions on every doctype
// with a space) compute the same access Hearth shows.
//
// Hearth's own tables stay the source of truth for who may reach a space: its
// access control is a query over them, on every page, and there is no safe way
// to ask EmberOPS those questions per row. EmberOPS therefore holds a copy,
// and the copy is brought up to date after every change Hearth makes to the
// structure: Sync runs inline after a space edit, Kick (debounced, in the
// background) after any other write the web layer sees, and on a timer.
// A copy that is already current is detected by digest and costs no call.
type SpaceSync struct {
	C      *emberops.Client
	Source SpaceSource
	Dir    Directory
	Loc    *time.Location
	Log    *slog.Logger
	// Real says whether an org is a company rather than somebody's playground. EmberOPS has one org per site, so a
	// playground has no counterpart there, and its sync would copy sandbox spaces over the real ones and then delete
	// every space it does not know, which is all of the real org's. Nil treats every org as real.
	Real func(ctx context.Context, orgID string) (bool, error)

	mu     sync.Mutex
	digest map[string]string // org -> digest of what was last sent
	timer  *time.Timer
	orgs   map[string]bool
}

func (s *SpaceSync) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Sync brings EmberOPS's spaces in line with Hearth's for one org.
func (s *SpaceSync) Sync(ctx context.Context, orgID string) error {
	if s.Real != nil {
		real, err := s.Real(ctx, orgID)
		if err != nil {
			return err
		}
		if !real {
			return nil
		}
	}
	spaces, err := s.Source.SpaceAccessAll(ctx, orgID)
	if err != nil {
		return err
	}
	emails, err := s.Dir.Emails(ctx)
	if err != nil {
		return err
	}
	z := mapping.TZ{Loc: s.Loc}
	mails := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			if e := emails[id]; e != "" {
				out = append(out, e)
			}
		}
		sort.Strings(out)
		return out
	}
	items := make([]emberops.BulkItem, 0, len(spaces))
	want := map[string]bool{}
	for _, sp := range spaces {
		rec := mapping.Rec{"id": sp.ID, "name": sp.Name, "kind": sp.Kind, "position": float64(sp.Position)}
		if sp.ParentID != nil {
			rec["parent_id"] = *sp.ParentID
		}
		var grants []mapping.Rec
		for _, g := range sp.Grants {
			gr := mapping.Rec{"kind": string(g.Kind), "level": string(g.Level), "team_id": g.SubjectID}
			if g.Kind == core.SubjectPrincipal {
				gr["email"] = emails[g.SubjectID]
			}
			if g.ExpiresAt != nil {
				gr["expires_at"] = g.ExpiresAt.UTC().Format(time.RFC3339)
			}
			grants = append(grants, gr)
		}
		doc := z.SpaceAccessDoc(rec, mails(sp.Managers), mails(sp.Members), grants, sp.ParentID == nil)
		delete(doc, "doctype")
		delete(doc, "hearth_id")
		items = append(items, emberops.BulkItem{Doctype: "Hearth Space", HearthID: sp.ID, Values: doc})
		want[strings.ToLower(sp.ID)] = true
	}
	sum := sha256.New()
	for _, it := range items {
		fmt.Fprintf(sum, "%s %v\n", it.HearthID, mapping.Dumps(it.Values))
	}
	d := hex.EncodeToString(sum.Sum(nil))
	s.mu.Lock()
	if s.digest == nil {
		s.digest = map[string]string{}
	}
	same := s.digest[orgID] == d
	s.mu.Unlock()
	if same {
		return nil
	}
	if len(items) > 0 {
		res, err := s.C.BulkUpsert(ctx, items)
		if err != nil {
			return err
		}
		if len(res.Failed) > 0 {
			return fmt.Errorf("emberops: %d spaces refused, first %s: %s", len(res.Failed), res.Failed[0].HearthID, res.Failed[0].Error)
		}
	}
	// A space deleted in Hearth goes from EmberOPS too, children first.
	have, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Hearth Space", Fields: []string{"name", "hearth_id", "lft"},
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}, OrderBy: "lft desc"}, 0)
	if err != nil {
		return err
	}
	for _, h := range have {
		if !want[strings.ToLower(h.S("hearth_id"))] {
			if err := s.C.Delete(ctx, "Hearth Space", h.S("name")); err != nil && !errors.Is(err, emberops.ErrNotFound) {
				s.log().Warn("emberops: could not remove a deleted space", "space", h.S("hearth_id"), "error", err)
			}
		}
	}
	s.mu.Lock()
	s.digest[orgID] = d
	s.mu.Unlock()
	return nil
}

// Kick asks for a background Sync of the org shortly (debounced: a burst of
// edits is one copy). It never blocks and never fails the request that caused
// it; the timer catches whatever it misses.
func (s *SpaceSync) Kick(orgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orgs == nil {
		s.orgs = map[string]bool{}
	}
	s.orgs[orgID] = true
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(2*time.Second, func() {
		s.mu.Lock()
		orgs := s.orgs
		s.orgs, s.timer = nil, nil
		s.mu.Unlock()
		for o := range orgs {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if err := s.Sync(ctx, o); err != nil {
				s.log().Warn("emberops: space sync failed", "org", o, "error", err)
			}
			cancel()
		}
	})
}

// Plant serves downtime from EmberOPS (Asset Failure Log, left as a draft:
// submitting one raises a reactive work order) and the asset identity fields
// the addon stores, and keeps EmberOPS's copy of the space structure current.
// The asset tree itself, with its rollups, and space reads and permissions stay
// Hearth's own, read from the spine and Hearth's grants.
type Plant struct {
	plant.Store
	Base
	Dir    Directory
	Authz  plant.Authorizer
	Spaces *SpaceSync
	// CurrencyPerCent is the value of one cent in the site's currency, for parts cost (0 means 0.01).
	CurrencyPerCent float64
}

var _ plant.Store = (*Plant)(nil)

const failureDoctype = "Asset Failure Log"

var failureFields = []string{"name", "hearth_id", "asset", "downtime_start", "downtime_end", "downtime_minutes",
	"hearth_planned", "hearth_note", "failure_description", "maintenance_work_order", "owner", "creation"}

// ---- spaces: Hearth decides, EmberOPS keeps a copy ----

func (s *Plant) sync(ctx context.Context, orgID string) {
	if s.Spaces == nil {
		return
	}
	if err := s.Spaces.Sync(ctx, orgID); err != nil {
		// The change is made in Hearth; EmberOPS catches up on the next sync.
		slog.Default().Warn("emberops: space sync failed", "org", orgID, "error", err)
		s.Spaces.Kick(orgID)
	}
}

func (s *Plant) CreateSpace(ctx context.Context, orgID string, actor core.Subjects, name, kind string, parentID *string) (*plant.Space, error) {
	sp, err := s.Store.CreateSpace(ctx, orgID, actor, name, kind, parentID)
	if err == nil {
		s.sync(ctx, orgID)
	}
	return sp, err
}

func (s *Plant) RenameSpace(ctx context.Context, orgID string, actor core.Subjects, id, name string) error {
	err := s.Store.RenameSpace(ctx, orgID, actor, id, name)
	if err == nil {
		s.sync(ctx, orgID)
	}
	return err
}

func (s *Plant) MoveSpace(ctx context.Context, orgID string, actor core.Subjects, id string, parentID *string) error {
	err := s.Store.MoveSpace(ctx, orgID, actor, id, parentID)
	if err == nil {
		s.sync(ctx, orgID)
	}
	return err
}

func (s *Plant) DeleteSpace(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	err := s.Store.DeleteSpace(ctx, orgID, actor, id)
	if err == nil {
		s.sync(ctx, orgID)
	}
	return err
}

func (s *Plant) ReorderSpaces(ctx context.Context, orgID string, actor core.Subjects, parentID string, ids []string) error {
	err := s.Store.ReorderSpaces(ctx, orgID, actor, parentID, ids)
	if err == nil {
		s.sync(ctx, orgID)
	}
	return err
}

func (s *Plant) SaveSpaceOrder(ctx context.Context, orgID string, actor core.Subjects, ids []string) error {
	err := s.Store.SaveSpaceOrder(ctx, orgID, actor, ids)
	if err == nil {
		s.sync(ctx, orgID)
	}
	return err
}

// ---- asset identity ----

func (s *Plant) FieldsFor(ctx context.Context, orgID, assetID string) (plant.Fields, error) {
	r, err := s.C.GetByHearthID(ctx, "Asset", assetID)
	if errors.Is(err, emberops.ErrNotFound) {
		return s.Store.FieldsFor(ctx, orgID, assetID)
	}
	if err != nil {
		return plant.Fields{}, err
	}
	f := plant.Fields{Code: r.S("hearth_code"), InstalledAt: r.TP("hearth_installed_at", s.loc()), RetiredAt: r.TP("hearth_retired_at", s.loc())}
	if p := r.S("hearth_parent_asset"); p != "" {
		ids, err := s.Dir.HearthIDs(ctx, "Asset", []string{p})
		if err != nil {
			return f, err
		}
		id := ids[p]
		f.ParentAssetID = &id
	}
	return f, nil
}

// SetFields writes the spine first (it enforces edit access on the asset and
// the parent, unique codes and cycles) and then the same fields to the Asset.
func (s *Plant) SetFields(ctx context.Context, orgID string, actor core.Subjects, assetID string, f plant.Fields) error {
	if err := s.Store.SetFields(ctx, orgID, actor, assetID, f); err != nil {
		return err
	}
	vals := map[string]any{"hearth_code": orNilS(strings.TrimSpace(f.Code)),
		"hearth_installed_at": s.ts(f.InstalledAt), "hearth_retired_at": s.ts(f.RetiredAt), "hearth_parent_asset": nil}
	if f.ParentAssetID != nil && *f.ParentAssetID != "" {
		vals["hearth_parent_asset"] = emberops.Ref(*f.ParentAssetID)
	}
	if _, err := s.C.Upsert(ctx, "Asset", assetID, vals); err != nil {
		return fmt.Errorf("emberops: save asset %s: %w", assetID, err)
	}
	return nil
}

func orNilS(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Plant) ByCode(ctx context.Context, orgID string, actor core.Subjects, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", plant.ErrNotFound
	}
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Asset", Fields: []string{"hearth_id", "hearth_code", "hearth_deleted_at"}, Limit: 50,
		Filters: []emberops.Filter{{Field: "hearth_code", Op: "like", Value: code}}})
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if strings.EqualFold(r.S("hearth_code"), code) && !r.Has("hearth_deleted_at") && r.Has("hearth_id") {
			if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: r.S("hearth_id")}, core.AccessView); err != nil {
				return "", plant.ErrNotFound
			}
			return r.S("hearth_id"), nil
		}
	}
	return "", plant.ErrNotFound
}

// ---- downtime ----

func (s *Plant) assetName(ctx context.Context, assetID string) (string, error) {
	n, err := s.C.Resolve(ctx, "Asset", assetID)
	if err != nil {
		return "", err
	}
	if n == "" {
		return "", plant.ErrNotFound
	}
	return n, nil
}

func (s *Plant) openOutage(ctx context.Context, assetName string) (emberops.Row, error) {
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: failureDoctype, Fields: failureFields, Limit: 1, OrderBy: "downtime_start desc",
		Filters: []emberops.Filter{emberops.F("asset", assetName), {Field: "downtime_end", Op: "is", Value: "not set"}}})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func (s *Plant) record(ctx context.Context, actor core.Subjects, assetID string, from time.Time, to *time.Time, planned bool, woID *string, note string) (*plant.Downtime, error) {
	d := &plant.Downtime{ID: uuid.NewString(), AssetID: assetID, StartedAt: from, EndedAt: to, Planned: planned,
		WorkOrderID: woID, Note: strings.TrimSpace(note), RecordedBy: actor.Principal()}
	rec := mapping.Rec{"id": d.ID, "asset_id": assetID, "started_at": from.UTC().Format(time.RFC3339), "planned": planned, "note": d.Note}
	if to != nil {
		m := int(to.Sub(from).Minutes())
		d.Minutes = &m
		rec["ended_at"], rec["minutes"] = to.UTC().Format(time.RFC3339), float64(m)
	}
	if woID != nil {
		rec["work_order_id"] = *woID
	}
	doc := mapping.TZ{Loc: s.loc()}.DowntimeDoc(rec)
	delete(doc, "doctype")
	delete(doc, "hearth_id")
	if _, err := s.C.Upsert(ctx, failureDoctype, d.ID, doc); err != nil {
		if isForbidden(err) {
			return nil, plant.ErrDenied
		}
		return nil, err
	}
	return d, nil
}

func (s *Plant) GoDown(ctx context.Context, orgID string, actor core.Subjects, assetID string, at time.Time, planned bool, woID *string, note string) (*plant.Downtime, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	if at.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("%w: a machine cannot have stopped in the future", plant.ErrInvalid)
	}
	if woID != nil && *woID == "" {
		woID = nil
	}
	name, err := s.assetName(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if open, err := s.openOutage(ctx, name); err != nil {
		return nil, err
	} else if open != nil {
		return nil, plant.ErrAlreadyDown
	}
	return s.record(ctx, actor, assetID, at, nil, planned, woID, note)
}

func (s *Plant) LogDowntime(ctx context.Context, orgID string, actor core.Subjects, assetID string, from, to time.Time, planned bool, woID *string, note string) (*plant.Downtime, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, fmt.Errorf("%w: an outage needs a start and a later end", plant.ErrInvalid)
	}
	if to.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("%w: it cannot have ended in the future", plant.ErrInvalid)
	}
	if woID != nil && *woID == "" {
		woID = nil
	}
	if _, err := s.assetName(ctx, assetID); err != nil {
		return nil, err
	}
	return s.record(ctx, actor, assetID, from, &to, planned, woID, note)
}

func (s *Plant) ComeUp(ctx context.Context, orgID string, actor core.Subjects, assetID string, at time.Time) (*plant.Downtime, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessComment); err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	name, err := s.assetName(ctx, assetID)
	if err != nil {
		return nil, err
	}
	open, err := s.openOutage(ctx, name)
	if err != nil {
		return nil, err
	}
	if open == nil {
		return nil, plant.ErrNotDown
	}
	start := open.T("downtime_start", s.loc())
	if at.Before(start) {
		at = start
	}
	m := int(at.Sub(start).Minutes())
	if _, err := s.C.Upsert(ctx, failureDoctype, idOf(open), map[string]any{"downtime_end": s.tsv(at), "downtime_minutes": m}); err != nil {
		return nil, err
	}
	return &plant.Downtime{ID: idOf(open), AssetID: assetID, StartedAt: start, EndedAt: &at, Minutes: &m,
		Planned: open.B("hearth_planned"), Note: open.S("hearth_note")}, nil
}

func (s *Plant) RemoveDowntime(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	r, err := s.C.GetByHearthID(ctx, failureDoctype, id)
	if err != nil {
		return notFound(err, plant.ErrNotFound)
	}
	ids, err := s.Dir.HearthIDs(ctx, "Asset", []string{r.S("asset")})
	if err != nil {
		return err
	}
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: ids[r.S("asset")]}, core.AccessEdit); err != nil {
		return plant.ErrDenied
	}
	return s.C.DeleteByHearthID(ctx, failureDoctype, id)
}

func (s *Plant) DowntimeFor(ctx context.Context, orgID string, actor core.Subjects, assetID string, limit int) ([]plant.Downtime, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessView); err != nil {
		return nil, plant.ErrNotFound
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	name, err := s.assetName(ctx, assetID)
	if errors.Is(err, plant.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: failureDoctype, Fields: failureFields, Limit: limit, OrderBy: "downtime_start desc",
		Filters: []emberops.Filter{emberops.F("asset", name)}})
	if err != nil {
		return nil, err
	}
	return s.downtimes(ctx, assetID, rows)
}

func (s *Plant) downtimes(ctx context.Context, assetID string, rows []emberops.Row) ([]plant.Downtime, error) {
	users, err := s.Dir.UserIDs(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.Dir.Names(ctx)
	if err != nil {
		return nil, err
	}
	var woNames []string
	for _, r := range rows {
		if r.Has("maintenance_work_order") {
			woNames = append(woNames, r.S("maintenance_work_order"))
		}
	}
	titles := map[string]string{}
	ids := map[string]string{}
	if len(woNames) > 0 {
		var in []any
		for _, n := range woNames {
			in = append(in, n)
		}
		wos, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Maintenance Work Order", Fields: []string{"name", "hearth_id", "title"}, Limit: 500,
			Filters: []emberops.Filter{{Field: "name", Op: "in", Value: in}}})
		if err != nil {
			return nil, err
		}
		for _, w := range wos {
			titles[w.S("name")], ids[w.S("name")] = w.S("title"), idOf(w)
		}
	}
	out := make([]plant.Downtime, 0, len(rows))
	for _, r := range rows {
		d := plant.Downtime{ID: idOf(r), AssetID: assetID, StartedAt: r.T("downtime_start", s.loc()), EndedAt: r.TP("downtime_end", s.loc()),
			Planned: r.B("hearth_planned"), Note: r.S("hearth_note"), RecordedBy: users[r.S("owner")]}
		d.ByName = names[d.RecordedBy]
		if d.EndedAt != nil {
			m := int(r.I("downtime_minutes"))
			d.Minutes = &m
		}
		if w := r.S("maintenance_work_order"); w != "" {
			id := ids[w]
			d.WorkOrderID, d.WorkOrder = &id, titles[w]
		}
		out = append(out, d)
	}
	return out, nil
}

// AvailabilityFor rolls downtime up the asset tree, as the SQL does: the tree
// is Hearth's (the spine), the outages are EmberOPS's, read in two calls.
func (s *Plant) AvailabilityFor(ctx context.Context, orgID string, actor core.Subjects, assetID string, from, to time.Time) (*plant.Availability, error) {
	if err := s.Authz.Require(ctx, actor, core.Ref{Type: core.EntityDocument, ID: assetID}, core.AccessView); err != nil {
		return nil, plant.ErrNotFound
	}
	if to.Before(from) {
		from, to = to, from
	}
	av := &plant.Availability{From: from, To: to, Window: int(to.Sub(from).Minutes())}
	tree, err := s.Store.Tree(ctx, orgID, actor)
	if err != nil {
		return nil, err
	}
	kids := map[string][]string{}
	for _, a := range tree {
		if a.ParentID != nil {
			kids[*a.ParentID] = append(kids[*a.ParentID], a.ID)
		}
	}
	subtree := map[string]bool{assetID: true}
	for frontier := []string{assetID}; len(frontier) > 0; {
		var next []string
		for _, id := range frontier {
			for _, k := range kids[id] {
				if !subtree[k] {
					subtree[k] = true
					next = append(next, k)
				}
			}
		}
		frontier = next
	}
	var ids []any
	for id := range subtree {
		ids = append(ids, id)
	}
	assets, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Asset", Fields: []string{"name"}, Limit: 500,
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids}}})
	if err != nil || len(assets) == 0 {
		return av, err
	}
	var names []any
	for _, a := range assets {
		names = append(names, a.S("name"))
	}
	rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: failureDoctype,
		Fields:    []string{"downtime_start", "downtime_end", "hearth_planned"},
		Filters:   []emberops.Filter{{Field: "asset", Op: "in", Value: names}, {Field: "downtime_start", Op: "<", Value: s.tsv(to)}},
		OrFilters: []emberops.Filter{{Field: "downtime_end", Op: ">", Value: s.tsv(from)}, {Field: "downtime_end", Op: "is", Value: "not set"}}}, 0)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, r := range rows {
		start := r.T("downtime_start", s.loc())
		end := now
		if e := r.TP("downtime_end", s.loc()); e != nil {
			end = *e
		}
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		m := int(end.Sub(start).Minutes())
		if m < 0 {
			m = 0
		}
		if r.B("hearth_planned") {
			av.Planned += m
		} else {
			av.Unplanned += m
			av.Outages++
		}
	}
	return av, nil
}
