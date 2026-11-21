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
	"github.com/Embernet-ai/hearth/internal/docs"
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

	// op serialises every copy and removal. A whole copy that overlapped a one-folder copy of the same space made EmberOPS
	// refuse both ("Document has been modified after you have opened it", "being modified by another user"), found live 2.22.9.
	op sync.Mutex

	mu     sync.Mutex
	sent   map[string]map[string]string // org -> space id -> hash of what EmberOPS last accepted for it
	known  map[string]map[string]bool   // org -> space ids this copy has put in or found in EmberOPS
	listed map[string]bool              // org -> EmberOPS's spaces have been listed once, so known is complete
	stuck  map[string]map[string]bool   // org -> deleted spaces that would not go and are tried again
	timer  *time.Timer
	orgs   map[string]bool
}

func itemHash(it emberops.BulkItem) string {
	sum := sha256.Sum256([]byte(it.HearthID + " " + mapping.Dumps(it.Values)))
	return hex.EncodeToString(sum[:])
}

func (s *SpaceSync) remember(orgID string, it emberops.BulkItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent == nil {
		s.sent, s.known = map[string]map[string]string{}, map[string]map[string]bool{}
	}
	if s.sent[orgID] == nil {
		s.sent[orgID], s.known[orgID] = map[string]string{}, map[string]bool{}
	}
	id := strings.ToLower(it.HearthID)
	s.sent[orgID][id], s.known[orgID][id] = itemHash(it), true
}

func (s *SpaceSync) forget(orgID, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.ToLower(id)
	delete(s.sent[orgID], id)
	delete(s.known[orgID], id)
}

func (s *SpaceSync) markStuck(orgID, id string, stuck bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stuck == nil {
		s.stuck = map[string]map[string]bool{}
	}
	if s.stuck[orgID] == nil {
		s.stuck[orgID] = map[string]bool{}
	}
	if stuck {
		s.stuck[orgID][strings.ToLower(id)] = true
	} else {
		delete(s.stuck[orgID], strings.ToLower(id))
	}
}

func (s *SpaceSync) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Sync brings EmberOPS's spaces in line with Hearth's for one org. It sends only the spaces whose copy has changed since EmberOPS
// last accepted it, and removes only the spaces Hearth no longer has, so when nothing changed it writes nothing. It is the
// background job's, never a request's.
func (s *SpaceSync) Sync(ctx context.Context, orgID string) error {
	s.op.Lock()
	defer s.op.Unlock()
	ctx = emberops.WithUser(ctx, nil)
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
	want := map[string]bool{}
	var changed []emberops.BulkItem
	s.mu.Lock()
	for _, sp := range spaces {
		it := spaceItem(sp, emails, z)
		id := strings.ToLower(sp.ID)
		want[id] = true
		if s.sent[orgID][id] != itemHash(it) {
			changed = append(changed, it)
		}
	}
	s.mu.Unlock()
	if len(changed) > 0 {
		res, err := s.C.BulkUpsert(ctx, changed)
		if err != nil {
			return err
		}
		failed := map[string]bool{}
		for _, f := range res.Failed {
			failed[strings.ToLower(f.HearthID)] = true
		}
		for _, it := range changed {
			if !failed[strings.ToLower(it.HearthID)] {
				s.remember(orgID, it)
			}
		}
		if len(res.Failed) > 0 {
			return fmt.Errorf("emberops: %d spaces refused, first %s: %s", len(res.Failed), res.Failed[0].HearthID, res.Failed[0].Error)
		}
	}

	// A space deleted in Hearth goes from EmberOPS too, children first. EmberOPS is listed once, to learn what is there; after
	// that the spaces to remove are the ones this copy knows of that Hearth no longer has, and the ones that would not go.
	s.mu.Lock()
	listed := s.listed[orgID]
	gone := map[string]bool{}
	for id := range s.known[orgID] {
		if !want[id] {
			gone[id] = true
		}
	}
	for id := range s.stuck[orgID] {
		if !want[id] {
			gone[id] = true
		}
	}
	s.mu.Unlock()
	type target struct{ id, name string }
	var order []target
	if !listed {
		have, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: "Hearth Space", Fields: []string{"name", "hearth_id", "lft"},
			Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}, OrderBy: "lft desc"}, 0)
		if err != nil {
			return err
		}
		for _, h := range have {
			id := strings.ToLower(h.S("hearth_id"))
			if want[id] {
				s.mu.Lock()
				if s.known == nil {
					s.sent, s.known = map[string]map[string]string{}, map[string]map[string]bool{}
				}
				if s.known[orgID] == nil {
					s.sent[orgID], s.known[orgID] = map[string]string{}, map[string]bool{}
				}
				s.known[orgID][id] = true
				s.mu.Unlock()
			} else {
				order = append(order, target{id, h.S("name")})
			}
		}
	} else if len(gone) > 0 {
		for id := range gone {
			name, err := s.C.Resolve(ctx, "Hearth Space", id)
			if err != nil && !errors.Is(err, emberops.ErrNotFound) {
				return err
			}
			if name == "" {
				s.forget(orgID, id)
				s.markStuck(orgID, id, false)
				continue
			}
			order = append(order, target{id, name})
		}
	}
	failedAny := false
	for _, t := range order {
		if err := s.removeCopy(ctx, orgID, t.id, t.name); err != nil {
			failedAny = true
			s.log().Warn("emberops: could not remove a deleted space", "space", t.id, "error", err)
		}
	}
	if !listed && !failedAny {
		s.mu.Lock()
		if s.listed == nil {
			s.listed = map[string]bool{}
		}
		s.listed[orgID] = true
		s.mu.Unlock()
	}
	if failedAny {
		return errors.New("emberops: a deleted space would not go; it is tried again on the next copy")
	}
	return nil
}

// removeCopy deletes one space's EmberOPS record by name, taking out the work Hearth deleted that still links it first.
func (s *SpaceSync) removeCopy(ctx context.Context, orgID, id, name string) error {
	err := s.C.Delete(ctx, "Hearth Space", name)
	if err != nil && !errors.Is(err, emberops.ErrNotFound) && strings.Contains(err.Error(), "LinkExistsError") {
		// EmberOPS will not delete a space something links. Work Hearth soft deleted is taken out and the delete asked
		// again; work that is still live is never touched, and the space then stays, loudly.
		s.removeDeletedWorkIn(ctx, name)
		err = s.C.Delete(ctx, "Hearth Space", name)
	}
	if err != nil && !errors.Is(err, emberops.ErrNotFound) {
		s.markStuck(orgID, id, true)
		return err
	}
	s.forget(orgID, id)
	s.markStuck(orgID, id, false)
	return nil
}

// spaceItem is one space as EmberOPS holds it: its place in the tree, its managers and members, and its grants.
func spaceItem(sp *plant.SpaceAccess, emails map[string]string, z mapping.TZ) emberops.BulkItem {
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
	return emberops.BulkItem{Doctype: "Hearth Space", HearthID: sp.ID, Values: doc}
}

// SpaceOneSource is a source that can read one space, which is all a single create, rename or removal needs to send.
// *plant.Service provides it; a source without it falls back to the whole copy.
type SpaceOneSource interface {
	SpaceAccessOne(ctx context.Context, orgID, id string) (*plant.SpaceAccess, error)
}

// SyncOne copies one space into EmberOPS, as the service account, and sends nothing else. A folder made, renamed or moved used
// to send every folder again, about ten seconds with 171 of them in the request. The whole copy is the background job's.
func (s *SpaceSync) SyncOne(ctx context.Context, orgID, id string) error {
	ctx = emberops.WithUser(ctx, nil)
	one, ok := s.Source.(SpaceOneSource)
	if !ok {
		return s.Sync(ctx, orgID)
	}
	s.op.Lock()
	defer s.op.Unlock()
	if s.Real != nil {
		real, err := s.Real(ctx, orgID)
		if err != nil {
			return err
		}
		if !real {
			return nil
		}
	}
	sp, err := one.SpaceAccessOne(ctx, orgID, id)
	if err != nil {
		return err
	}
	if sp == nil {
		return nil
	}
	emails, err := s.Dir.Emails(ctx)
	if err != nil {
		return err
	}
	it := spaceItem(sp, emails, mapping.TZ{Loc: s.Loc})
	s.mu.Lock()
	same := s.sent[orgID][strings.ToLower(sp.ID)] == itemHash(it)
	s.mu.Unlock()
	if same {
		return nil
	}
	res, err := s.C.BulkUpsert(ctx, []emberops.BulkItem{it})
	if err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("emberops: space %s refused: %s", res.Failed[0].HearthID, res.Failed[0].Error)
	}
	s.remember(orgID, it)
	return nil
}

// RemoveOne takes one deleted space out of EmberOPS, as the service account, and touches no other. A space that still will not
// go is marked to be tried again on the next full copy and the error returned.
func (s *SpaceSync) RemoveOne(ctx context.Context, orgID, id string) error {
	s.op.Lock()
	defer s.op.Unlock()
	svc := emberops.WithUser(ctx, nil)
	if s.Real != nil {
		real, err := s.Real(svc, orgID)
		if err != nil {
			return err
		}
		if !real {
			return nil
		}
	}
	name, err := s.C.Resolve(svc, "Hearth Space", id)
	if err != nil && !errors.Is(err, emberops.ErrNotFound) {
		return err
	}
	if name == "" {
		s.forget(orgID, id)
		return nil
	}
	return s.removeCopy(svc, orgID, id, name)
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
	// Docs reads a document's kind, so a move of it reaches EmberOPS; nil leaves a moved document's EmberOPS link alone.
	Docs interface {
		GetAsSystem(ctx context.Context, orgID, id string) (*docs.Document, error)
	}
	// CurrencyPerCent is the value of one cent in the site's currency, for parts cost (0 means 0.01).
	CurrencyPerCent float64
}

var _ plant.Store = (*Plant)(nil)

const failureDoctype = "Asset Failure Log"

var failureFields = []string{"name", "hearth_id", "asset", "downtime_start", "downtime_end", "downtime_minutes",
	"hearth_planned", "hearth_note", "failure_description", "maintenance_work_order", "owner", "creation"}

// ---- spaces: Hearth decides, EmberOPS keeps a copy ----

// syncOne copies just this space into EmberOPS. If that fails the change is still made in Hearth, and the background copy
// (the whole set, which is not in any request) catches up.
func (s *Plant) syncOne(ctx context.Context, orgID, id string) {
	if s.Spaces == nil {
		return
	}
	if err := s.Spaces.SyncOne(ctx, orgID, id); err != nil {
		slog.Default().Warn("emberops: space copy failed", "org", orgID, "space", id, "error", err)
		s.Spaces.Kick(orgID)
	}
}

// syncSoon asks for the whole copy in the background, for the changes that touch many spaces at once.
func (s *Plant) syncSoon(orgID string) {
	if s.Spaces != nil {
		s.Spaces.Kick(orgID)
	}
}

func (s *Plant) CreateSpace(ctx context.Context, orgID string, actor core.Subjects, name, kind string, parentID *string) (*plant.Space, error) {
	sp, err := s.Store.CreateSpace(ctx, orgID, actor, name, kind, parentID)
	if err == nil {
		s.syncOne(ctx, orgID, sp.ID)
	}
	return sp, err
}

func (s *Plant) RenameSpace(ctx context.Context, orgID string, actor core.Subjects, id, name string) error {
	err := s.Store.RenameSpace(ctx, orgID, actor, id, name)
	if err == nil {
		s.syncOne(ctx, orgID, id)
	}
	return err
}

// MoveSpace copies the moved space at once and the rest in the background: moving it changes who reaches everything under it.
func (s *Plant) MoveSpace(ctx context.Context, orgID string, actor core.Subjects, id string, parentID *string) error {
	err := s.Store.MoveSpace(ctx, orgID, actor, id, parentID)
	if err == nil {
		s.syncOne(ctx, orgID, id)
		s.syncSoon(orgID)
	}
	return err
}

func (s *Plant) DeleteSpace(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	err := s.Store.DeleteSpace(ctx, orgID, actor, id)
	if err == nil && s.Spaces != nil {
		if rerr := s.Spaces.RemoveOne(ctx, orgID, id); rerr != nil {
			slog.Default().Warn("emberops: could not remove a deleted space", "org", orgID, "space", id, "error", rerr)
			s.Spaces.Kick(orgID)
		}
	}
	return err
}

func (s *Plant) ReorderSpaces(ctx context.Context, orgID string, actor core.Subjects, parentID string, ids []string) error {
	err := s.Store.ReorderSpaces(ctx, orgID, actor, parentID, ids)
	if err == nil {
		s.syncSoon(orgID)
	}
	return err
}

func (s *Plant) SaveSpaceOrder(ctx context.Context, orgID string, actor core.Subjects, ids []string) error {
	err := s.Store.SaveSpaceOrder(ctx, orgID, actor, ids)
	if err == nil {
		s.syncSoon(orgID)
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

// deletedWork is where Hearth's soft deleted work can still sit in EmberOPS, and
// the field that says it was deleted.
var deletedWork = []struct{ doctype, field string }{
	{"Task", "hearth_deleted_at"},
	{"Maintenance Work Order", "hearth_deleted_at"},
	{"Hearth Document", "deleted_at"},
}

// removeDeletedWorkIn deletes the records in a space that Hearth has deleted.
func (s *SpaceSync) removeDeletedWorkIn(ctx context.Context, spaceName string) {
	for _, d := range deletedWork {
		rows, err := s.C.ListAll(ctx, emberops.ListQuery{Doctype: d.doctype, Fields: []string{"name"},
			Filters: []emberops.Filter{emberops.F("hearth_space", spaceName), {Field: d.field, Op: "is", Value: "set"}}}, 0)
		if err != nil {
			s.log().Warn("emberops: could not list deleted work in a deleted space", "doctype", d.doctype, "error", err)
			continue
		}
		for _, r := range rows {
			if err := s.C.Delete(ctx, d.doctype, r.S("name")); err != nil && !errors.Is(err, emberops.ErrNotFound) {
				s.log().Warn("emberops: could not remove deleted work", "doctype", d.doctype, "name", r.S("name"), "error", err)
			}
		}
	}
}

// MoveDocument files a document in another space, or takes it out, and moves its EmberOPS record with it. The spine decides
// the space, but the EmberOPS record carries a link to it: left behind, a document taken out of a space kept that space in
// EmberOPS for good, and a space with a record linked can never be deleted there.
func (s *Plant) MoveDocument(ctx context.Context, orgID string, actor core.Subjects, docID string, spaceID *string) error {
	if err := s.Store.MoveDocument(ctx, orgID, actor, docID, spaceID); err != nil {
		return err
	}
	if s.Docs == nil {
		return nil
	}
	d, err := s.Docs.GetAsSystem(ctx, orgID, docID)
	if err != nil {
		return err
	}
	dt := doctypeFor(d.Kind)
	if dt == "" {
		return nil
	}
	var link any
	if spaceID != nil && *spaceID != "" {
		link = "hearth:" + *spaceID
	}
	if _, err := s.C.Upsert(ctx, dt, docID, map[string]any{"hearth_space": link}); err != nil && !errors.Is(err, emberops.ErrNotFound) {
		return fmt.Errorf("emberops: move %s %s: %w", dt, docID, err)
	}
	return nil
}
