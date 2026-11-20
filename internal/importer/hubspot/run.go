package hubspot

// The delta import: everything HubSpot created or changed since the last import, into Hearth's CRM.
//
// What it will and will not do:
//
//   - A HubSpot record is matched to Hearth by its HubSpot id first (a record this importer made, or one the first import
//     carried through Twenty), then by web address (a company) or email address (a contact). A match is linked and
//     brought up to date; nothing matched is created twice.
//   - Writes go through Hearth's CRM service, so every record gets its grants, its event and, here, an audit row.
//   - A field somebody edited in Hearth is never overwritten. See decide.go for the rule; a field that is left alone
//     because of it is listed as a conflict.
//   - HubSpot saying nothing about a field never clears it.
//   - Nothing is deleted. A record deleted in Hearth stays deleted.
//   - A dry run (the default) reads everything, writes nothing, and reports exactly what an apply would do.
//   - Each object keeps a high-water mark, so the next run reads only what changed after this one.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/audit"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

// Person is somebody who can own a record in Hearth.
type Person struct {
	PrincipalID string
	Email       string
}

// Options steers a run.
type Options struct {
	// Apply writes. Without it nothing in Hearth changes and no high-water mark moves.
	Apply bool
	// Since is where an object with no high-water mark starts reading. It is required the first time.
	Since time.Time
	// ForceSince reads from Since even where a mark exists (the mark itself never moves backwards). It is how a field
	// that could not be filled earlier, such as an owner with no mapping yet, is filled after the mapping exists.
	ForceSince bool
	// Baseline is when the first import finished. A record it carried across whose change time is no later than this has
	// not been touched in Hearth since, so HubSpot may update it. Defaults to Since.
	Baseline time.Time
	// Only limits the run to these objects. Empty is all five.
	Only []string
	// OwnerEmails maps a HubSpot owner id to an email, for when the token cannot read owners itself.
	OwnerEmails map[string]string
	// Log gets one line per phase.
	Log func(string)
	// MaxConflicts caps the conflicts listed in the report (all are counted).
	MaxConflicts int
}

// Order is the order objects are imported in: a record is read after what it points at.
var Order = []string{Companies, Contacts, Deals, Notes, Calls}

// Count is one object's tally.
type Count struct {
	// Read is how many HubSpot returned as changed.
	Read int
	// Create is new in Hearth. Link was already in Hearth without a HubSpot id and is now tied to it. Update changed at
	// least one field. Same needed nothing. Skip could not be used (see Report.Problems or SkipWhy). Failed errored.
	Create, Link, Update, Same, Skip, Failed int
	// Conflicts is how many records had at least one field left alone because Hearth's value was not the import's.
	Conflicts int
	// Pulled is companies read by id because a contact or deal pointed at one that was not in Hearth.
	Pulled  int
	SkipWhy map[string]int
}

func (c *Count) skip(why string) {
	c.Skip++
	if c.SkipWhy == nil {
		c.SkipWhy = map[string]int{}
	}
	c.SkipWhy[why]++
}

// Conflict is one field the run left alone.
type Conflict struct {
	Object, HubSpotID, Entity, Field, Hearth, HubSpot, Trust string
}

// Report is everything a run learned.
type Report struct {
	Apply         bool
	Objects       map[string]*Count
	Since         map[string]time.Time
	HighWater     map[string]time.Time
	Conflicts     []Conflict
	ConflictTotal int
	Problems      []string
	ProblemTotal  int
	// Owners.
	OwnerSource    string
	OwnersMapped   int
	OwnersUnmapped map[string]int
	UnknownStages  map[string]int
	NotCarried     map[string]string
	// Fields counts, per object and field, the values written into an empty field (Fill) and the values that replaced
	// another (Replace), so a run that is about to change thousands of records shows what kind of change it is.
	Fields map[string]map[string]*FieldCount
	// Replaced lists the first values that replaced another, for a person to read before saying go.
	Replaced []Change
}

// Change is one value that replaced another.
type Change struct{ Object, HubSpotID, Field, From, To string }

// FieldCount is one field's writes.
type FieldCount struct{ Fill, Replace int }

func (r *Report) problem(format string, a ...any) {
	r.ProblemTotal++
	if len(r.Problems) < 200 {
		r.Problems = append(r.Problems, fmt.Sprintf(format, a...))
	}
}

// Failed says whether anything errored.
func (r *Report) Failed() bool {
	for _, c := range r.Objects {
		if c.Failed > 0 {
			return true
		}
	}
	return false
}

type ref struct{ etype, id string }

type snapshot struct {
	entity string
	fields map[string]string
}

// Importer carries HubSpot's changes over.
type Importer struct {
	db     *pgxpool.Pool
	svc    *crm.Service
	hs     *Client
	org    string
	actor  core.Subjects
	people []Person
	opt    Options

	rep *Report
	// refs is every external id this importer or the first import left: "source|external id" to the Hearth record.
	refs map[string]ref
	// linked is every Hearth record that is already some HubSpot record, so a second one is never matched onto it.
	linked map[string]bool
	snaps  map[string]snapshot // "object|hubspot id"
	// domains and emails are what Hearth holds now, lower case, to the record that holds it.
	domains map[string]string
	emails  map[string]string
	// owner maps a HubSpot owner id to a Hearth principal id.
	owner  map[string]string
	stages map[string]string
	calls  map[string]string
	// madeCompanies are companies this run created, which is when a deal may give them a type.
	madeCompanies map[string]string // entity id to the type given so far
	// haveState is whether migration 61's tables exist. A dry run copes without them (nothing is remembered yet);
	// an apply needs them.
	haveState bool
}

// New makes an importer. actor is who the import runs as; every record it makes is created by them.
func New(db *pgxpool.Pool, svc *crm.Service, hs *Client, orgID string, actor core.Subjects, people []Person, opt Options) *Importer {
	if opt.Log == nil {
		opt.Log = func(string) {}
	}
	if opt.MaxConflicts <= 0 {
		opt.MaxConflicts = 300
	}
	if opt.Baseline.IsZero() {
		opt.Baseline = opt.Since
	}
	return &Importer{db: db, svc: svc, hs: hs, org: orgID, actor: actor, people: people, opt: opt}
}

func (im *Importer) wants(object string) bool {
	if len(im.opt.Only) == 0 {
		return true
	}
	for _, o := range im.opt.Only {
		if o == object {
			return true
		}
	}
	return false
}

// Run reads HubSpot and writes Hearth.
func (im *Importer) Run(ctx context.Context) (*Report, error) {
	im.rep = &Report{Apply: im.opt.Apply, Objects: map[string]*Count{}, Since: map[string]time.Time{},
		HighWater: map[string]time.Time{}, OwnersUnmapped: map[string]int{}, UnknownStages: map[string]int{},
		NotCarried: map[string]string{}, Fields: map[string]map[string]*FieldCount{}}

	// One run at a time: two would each decide a record is new.
	conn, err := im.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	lock := fnvLock(im.org)
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lock).Scan(&got); err != nil {
		return nil, err
	}
	if !got {
		return nil, errors.New("another HubSpot import is running against this database")
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lock) }()

	if err := im.load(ctx); err != nil {
		return nil, err
	}
	if err := im.loadOwners(ctx); err != nil {
		return nil, err
	}
	var err2 error
	if im.stages, err2 = im.hs.DealStages(ctx); err2 != nil {
		return nil, fmt.Errorf("read the deal pipeline stages: %w", err2)
	}
	im.calls = im.hs.CallDispositions(ctx)

	for _, object := range Order {
		if !im.wants(object) {
			continue
		}
		since, err := im.sinceFor(ctx, object)
		if err != nil {
			return im.rep, err
		}
		im.rep.Since[object] = since
		cnt := &Count{}
		im.rep.Objects[object] = cnt
		im.opt.Log(fmt.Sprintf("%s changed since %s", object, since.Format(time.RFC3339)))
		var maxMod time.Time
		var batch []Object
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			err := im.process(ctx, object, batch, cnt)
			batch = batch[:0]
			return err
		}
		err = im.hs.Modified(ctx, object, props[object], since, func(o Object) error {
			cnt.Read++
			if o.Modified.After(maxMod) {
				maxMod = o.Modified
			}
			batch = append(batch, o)
			if len(batch) >= 100 {
				return flush()
			}
			return nil
		})
		if err == nil {
			err = flush()
		}
		if err != nil {
			return im.rep, fmt.Errorf("%s: %w", object, err)
		}
		hw := since
		if maxMod.After(hw) {
			hw = maxMod
		}
		im.rep.HighWater[object] = hw
		if im.opt.Apply && cnt.Failed == 0 {
			if _, err := im.db.Exec(ctx, `
				INSERT INTO hubspot_import_state (org_id, object, high_water, last_run_at) VALUES ($1,$2,$3,now())
				ON CONFLICT (org_id, object) DO UPDATE SET high_water = GREATEST(hubspot_import_state.high_water, EXCLUDED.high_water),
				                                           last_run_at = now()`, im.org, object, hw); err != nil {
				return im.rep, fmt.Errorf("remember where %s was read to: %w", object, err)
			}
		}
	}
	im.notCarried(ctx)
	return im.rep, nil
}

// fnvLock is the advisory lock key that serialises imports into one organisation.
func fnvLock(org string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("hubspot-import\x00" + org))
	return int64(h.Sum64())
}

func (im *Importer) sinceFor(ctx context.Context, object string) (time.Time, error) {
	var hw time.Time
	err := pgx.ErrNoRows
	if im.opt.ForceSince && !im.opt.Since.IsZero() {
		return im.opt.Since.UTC(), nil
	}
	if im.haveState {
		err = im.db.QueryRow(ctx, `SELECT high_water FROM hubspot_import_state WHERE org_id=$1 AND object=$2`, im.org, object).Scan(&hw)
	}
	switch {
	case err == nil:
		return hw.UTC(), nil
	case errors.Is(err, pgx.ErrNoRows):
		if im.opt.Since.IsZero() {
			return time.Time{}, fmt.Errorf("there is no high-water mark for %s yet: say where to start with -since (when the last import ran)", object)
		}
		return im.opt.Since.UTC(), nil
	}
	return time.Time{}, err
}

// notCarried counts what HubSpot changed that Hearth has no place for, so it is stated and not silently dropped.
func (im *Importer) notCarried(ctx context.Context) {
	since := im.opt.Since
	if since.IsZero() {
		since = im.rep.Since[Companies]
	}
	if since.IsZero() {
		return
	}
	for object, why := range map[string]string{
		"meetings": "Hearth's CRM has no meeting record, and the first import did not carry them either",
		"tasks":    "HubSpot tasks are not carried into Hearth's task list",
	} {
		n, err := im.hs.CountModified(ctx, object, since)
		if err != nil {
			continue
		}
		im.rep.NotCarried[object] = fmt.Sprintf("%d changed since %s: %s", n, since.Format("2006-01-02"), why)
	}
}

func (im *Importer) load(ctx context.Context) error {
	im.refs, im.linked, im.snaps = map[string]ref{}, map[string]bool{}, map[string]snapshot{}
	rows, err := im.db.Query(ctx, `SELECT source, external_id, entity_type, entity_id::text FROM external_refs
		WHERE org_id=$1 AND source IN ($2,$3)`, im.org, Source, LegacySource)
	if err != nil {
		return fmt.Errorf("load earlier imports: %w", err)
	}
	for rows.Next() {
		var s, ext, et, id string
		if err := rows.Scan(&s, &ext, &et, &id); err != nil {
			rows.Close()
			return err
		}
		im.refs[s+"|"+ext] = ref{et, id}
		im.linked[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var reg *string
	if err := im.db.QueryRow(ctx, `SELECT to_regclass('hubspot_import_records')::text`).Scan(&reg); err != nil {
		return err
	}
	im.haveState = reg != nil
	if !im.haveState && im.opt.Apply {
		return errors.New("the database has no HubSpot import tables yet (migration 61): run with -migrate, or deploy the release that carries it")
	}
	if err := im.loadSnapshots(ctx); err != nil {
		return err
	}
	im.domains, im.emails = map[string]string{}, map[string]string{}
	d, err := im.db.Query(ctx, `SELECT lower(domain), id::text FROM companies WHERE org_id=$1 AND deleted_at IS NULL AND domain <> ''`, im.org)
	if err != nil {
		return err
	}
	for d.Next() {
		var k, id string
		if err := d.Scan(&k, &id); err != nil {
			d.Close()
			return err
		}
		im.domains[k] = id
	}
	d.Close()
	e, err := im.db.Query(ctx, `SELECT lower(email), id::text FROM contacts WHERE org_id=$1 AND deleted_at IS NULL AND email <> ''`, im.org)
	if err != nil {
		return err
	}
	for e.Next() {
		var k, id string
		if err := e.Scan(&k, &id); err != nil {
			e.Close()
			return err
		}
		im.emails[k] = id
	}
	e.Close()
	im.madeCompanies = map[string]string{}
	return e.Err()
}

func (im *Importer) loadSnapshots(ctx context.Context) error {
	if !im.haveState {
		return nil
	}
	srows, err := im.db.Query(ctx, `SELECT object, hubspot_id, entity_id::text, fields FROM hubspot_import_records WHERE org_id=$1`, im.org)
	if err != nil {
		return fmt.Errorf("load the import's snapshots: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var o, hid, ent string
		var f map[string]string
		if err := srows.Scan(&o, &hid, &ent, &f); err != nil {
			return err
		}
		im.snaps[o+"|"+hid] = snapshot{ent, f}
	}
	return srows.Err()
}

func (im *Importer) loadOwners(ctx context.Context) error {
	byEmail := map[string]string{}
	for _, p := range im.people {
		if p.Email != "" {
			byEmail[strings.ToLower(strings.TrimSpace(p.Email))] = p.PrincipalID
		}
	}
	im.owner = map[string]string{}
	email := map[string]string{}
	owners, err := im.hs.Owners(ctx)
	switch {
	case err == nil:
		for _, o := range owners {
			email[o.ID] = o.Email
		}
		im.rep.OwnerSource = "HubSpot owners"
	case errors.Is(err, ErrScope):
		im.rep.OwnerSource = "none from HubSpot (the token lacks the owners scope)"
	default:
		return fmt.Errorf("read HubSpot owners: %w", err)
	}
	for id, e := range im.opt.OwnerEmails {
		email[id] = e
		if im.rep.OwnerSource != "HubSpot owners" {
			im.rep.OwnerSource = "the owner file"
		}
	}
	for id, e := range email {
		if pid, ok := byEmail[strings.ToLower(strings.TrimSpace(e))]; ok {
			im.owner[id] = pid
			im.rep.OwnersMapped++
		}
	}
	return nil
}

// ownerOf is the Hearth account for a HubSpot owner id, empty when there is none (and then counted, by id).
func (im *Importer) ownerOf(hsOwner string) string {
	hsOwner = strings.TrimSpace(hsOwner)
	if hsOwner == "" {
		return ""
	}
	if pid, ok := im.owner[hsOwner]; ok {
		return pid
	}
	im.rep.OwnersUnmapped[hsOwner]++
	return ""
}

// lookup finds the Hearth record a HubSpot record already is.
func (im *Importer) lookup(object, hid string) (ref, string, bool) {
	if r, ok := im.refs[Source+"|"+key(object, hid)]; ok {
		return r, "hubspot", true
	}
	if k := legacyKey(object, hid); k != "" {
		if r, ok := im.refs[LegacySource+"|"+k]; ok {
			return r, "legacy", true
		}
	}
	return ref{}, "", false
}

func (im *Importer) fakeID(object, hid string) string { return "dry-run:" + object + ":" + hid }

func (im *Importer) record(ctx context.Context, object, hid, etype, entity string, fields map[string]string, mod time.Time) error {
	im.refs[Source+"|"+key(object, hid)] = ref{etype, entity}
	im.linked[entity] = true
	if !im.opt.Apply {
		return nil
	}
	if _, err := im.db.Exec(ctx, `
		INSERT INTO external_refs (org_id, source, external_id, entity_type, entity_id) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (org_id, source, external_id) DO UPDATE SET entity_id = EXCLUDED.entity_id, updated_at = now()`,
		im.org, Source, key(object, hid), etype, entity); err != nil {
		return fmt.Errorf("record that HubSpot %s %s is %s: %w", object, hid, entity, err)
	}
	if fields == nil {
		return nil
	}
	var m any
	if !mod.IsZero() {
		m = mod
	}
	if _, err := im.db.Exec(ctx, `
		INSERT INTO hubspot_import_records (org_id, object, hubspot_id, entity_id, fields, hubspot_modified)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (org_id, object, hubspot_id) DO UPDATE SET entity_id=EXCLUDED.entity_id, fields=EXCLUDED.fields,
		       hubspot_modified=EXCLUDED.hubspot_modified, updated_at=now()`,
		im.org, object, hid, entity, fields, m); err != nil {
		return fmt.Errorf("remember what was imported for %s %s: %w", object, hid, err)
	}
	im.snaps[object+"|"+hid] = snapshot{entity, fields}
	return nil
}

func (im *Importer) audit(ctx context.Context, action, etype, entity, object, hid string, before, after any) error {
	if !im.opt.Apply {
		return nil
	}
	return audit.Record(ctx, im.db, audit.Entry{OrgID: im.org, ActorID: im.actor.Principal(), Action: action,
		EntityType: etype, EntityID: entity,
		Before: before, After: map[string]any{"hubspot_object": object, "hubspot_id": hid, "fields": after}})
}

// countFields records what a plan will write, by field.
func (im *Importer) countFields(object, hid string, p fieldPlan, cur map[string]string) {
	for f := range p.set {
		m := im.rep.Fields[object]
		if m == nil {
			m = map[string]*FieldCount{}
			im.rep.Fields[object] = m
		}
		c := m[f]
		if c == nil {
			c = &FieldCount{}
			m[f] = c
		}
		if strings.TrimSpace(cur[f]) == "" {
			c.Fill++
		} else {
			c.Replace++
			if len(im.rep.Replaced) < 250 {
				im.rep.Replaced = append(im.rep.Replaced, Change{object, hid, f, cur[f], p.set[f]})
			}
		}
	}
}

func (im *Importer) addConflicts(object, hid, entity string, p fieldPlan, cur map[string]string, mode trust) {
	if len(p.conflicts) == 0 {
		return
	}
	fields := make([]string, 0, len(p.conflicts))
	for f := range p.conflicts {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		im.rep.ConflictTotal++
		if len(im.rep.Conflicts) < im.opt.MaxConflicts {
			im.rep.Conflicts = append(im.rep.Conflicts, Conflict{Object: object, HubSpotID: hid, Entity: entity, Field: f,
				Hearth: cur[f], HubSpot: p.conflicts[f], Trust: mode.String()})
		}
	}
}

func (im *Importer) process(ctx context.Context, object string, objs []Object, cnt *Count) error {
	switch object {
	case Companies:
		return im.companies(ctx, objs, cnt)
	case Contacts:
		return im.contacts(ctx, objs, cnt)
	case Deals:
		return im.deals(ctx, objs, cnt)
	case Notes, Calls:
		return im.activities(ctx, object, objs, cnt)
	}
	return fmt.Errorf("%q is not an object this imports", object)
}

// modeFor is how far an existing record's fields may be replaced.
func (im *Importer) modeFor(object, hid, via string, updated time.Time) (trust, map[string]string) {
	if s, ok := im.snaps[object+"|"+hid]; ok {
		return snapshotted, s.fields
	}
	if via == "legacy" && !updated.After(im.opt.Baseline) {
		return untouched, nil
	}
	return fillOnly, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
