// Package mauticsync keeps Hearth's CRM and Mautic, the email marketing tool, in step.
//
// Hearth is the CRM and owns the people. Mautic sends the mail and sees what people do with it. Three things cross:
//
//   - Out, Hearth to Mautic. A contact with an email address becomes a Mautic contact (matched on that address, so one somebody
//     had already put in Mautic is adopted and not doubled), on a Mautic company made from its Hearth company, with the owner
//     carried across when the owner's Hearth email is also a Mautic user. Its company's types and tags become Mautic tags and
//     Mautic segments: "Hearth: all contacts", "Hearth type: Client", "Hearth tag: expo". Hearth keeps each segment's membership;
//     taking a type away takes the contact out of that segment.
//
//   - Back, Mautic to Hearth. What Mautic recorded lands on the contact's timeline as a note by "Mautic": sent, opened, clicked a
//     link, submitted a form, unsubscribed, bounced. Every one is keyed on the Mautic row it came from, so a run that is
//     repeated, or that stops half way, adds nothing twice.
//
//   - Both ways, opt-out. A contact Mautic marks do-not-contact for email (an unsubscribe, a bounce, or a person marking it) is
//     marked opted out in Hearth and stays so. A contact opted out in Hearth is marked do-not-contact in Mautic, and if Mautic ever
//     lost that, the next run puts it back. Nothing here clears an opt-out on either side, so Hearth never re-subscribes anybody.
//
// Nothing here deletes a Mautic record. A Hearth contact that is deleted, or loses its address, is taken out of every segment
// the sync put it in and tagged, so it stops being mailed. Mautic's own sending refuses a do-not-contact contact; this only
// keeps the two in step.
//
// Dry run is the default. It reads both sides and writes neither.
package mauticsync

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/mautic"
)

// Mautic is the slice of the Mautic client the sync uses. *mautic.Client is one; the tests use a fake.
type Mautic interface {
	UpsertContact(ctx context.Context, id int64, f mautic.Fields) (int64, error)
	UpsertCompany(ctx context.Context, id int64, f mautic.Fields) (int64, error)
	LinkCompany(ctx context.Context, companyID, contactID int64) error
	UnlinkCompany(ctx context.Context, companyID, contactID int64) error
	AddDoNotContact(ctx context.Context, contactID int64, reason int, comment string) error
	Segments(ctx context.Context) (map[string]int64, error)
	CreateSegment(ctx context.Context, name, alias, description string) (int64, error)
	SegmentAdd(ctx context.Context, segmentID, contactID int64) error
	SegmentRemove(ctx context.Context, segmentID, contactID int64) error
	EnsureField(ctx context.Context, object, alias, label string) error
	Users(ctx context.Context) (map[string]int64, error)
	Stats(ctx context.Context, table, order string, limit int, filters ...mautic.Where) ([]mautic.Row, error)
	Email(ctx context.Context, id int64) (mautic.Names, error)
	Form(ctx context.Context, id int64) (string, error)
}

// Options says what a run does.
type Options struct {
	// Write sends and records. Without it nothing is written to either side.
	Write bool
	// OnlyContacts limits the push to these Hearth contact ids, for a proof. The pull still reads everything Mautic has, but it
	// can only act on contacts that have a link, so it touches only what was pushed.
	OnlyContacts []string
	// Workers is how many contacts are sent at once. Zero is eight.
	Workers int
}

// Action is one thing the run did or would do.
type Action struct {
	Verb   string // create, update, same, segment, optout, note, skip, failed
	What   string // contact, company, segment, activity, opt-out, do-not-contact
	ID     string
	Name   string
	Detail string
}

// Report is what a run did.
type Report struct {
	Write   bool
	Actions []Action
	mu      sync.Mutex
}

func (r *Report) add(verb, what, id, name, detail string) {
	r.mu.Lock()
	r.Actions = append(r.Actions, Action{Verb: verb, What: what, ID: id, Name: name, Detail: detail})
	r.mu.Unlock()
}

// Count is how many actions had this verb and thing; an empty string matches any.
func (r *Report) Count(what, verb string) int {
	n := 0
	for _, a := range r.Actions {
		if (what == "" || a.What == what) && (verb == "" || a.Verb == verb) {
			n++
		}
	}
	return n
}

// Failed reports whether anything could not be written.
func (r *Report) Failed() bool { return r.Count("", "failed") > 0 }

// Print writes the report. A long run prints a count per kind of action and then every action that is not a plain "same".
func (r *Report) Print(w io.Writer) {
	mode := "DRY RUN, nothing was written to either side"
	if r.Write {
		mode = "WRITE"
	}
	fmt.Fprintf(w, "%s\n\n", mode)
	counts := map[string]int{}
	var keys []string
	for _, a := range r.Actions {
		k := a.Verb + " " + a.What
		if counts[k] == 0 {
			keys = append(keys, k)
		}
		counts[k]++
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%6d  %s\n", counts[k], k)
	}
	fmt.Fprintln(w)
	shown := 0
	for _, a := range r.Actions {
		if a.Verb == "same" {
			continue
		}
		if shown++; shown > 400 {
			fmt.Fprintln(w, "... more actions than are listed; the counts above are complete")
			break
		}
		fmt.Fprintf(w, "%-8s %-15s %-36s %s", a.Verb, a.What, a.ID, a.Name)
		if a.Detail != "" {
			fmt.Fprintf(w, "   (%s)", a.Detail)
		}
		fmt.Fprintln(w)
	}
}

// Syncer runs one organisation's sync.
type Syncer struct {
	db  *pgxpool.Pool
	mc  Mautic
	org string

	mu       sync.Mutex
	segs     map[string]int64 // Mautic segment alias to id
	users    map[string]int64 // lower case email to Mautic user id
	company  map[string]int64 // Hearth company id to Mautic company id, made or found this run
	dnc      map[int64]bool   // Mautic contact ids that hold an email do-not-contact
	bot      string
	rep      *Report
	opt      Options
	emailCch map[int64]string
	formCch  map[int64]string
}

// New builds a Syncer.
func New(db *pgxpool.Pool, mc Mautic, orgID string) *Syncer {
	return &Syncer{db: db, mc: mc, org: orgID}
}

// Field aliases the sync owns in Mautic. Contacts and companies share one alias namespace there, so they differ.
const (
	contactIDField = "hearth_id"
	companyIDField = "hearth_company_id"
)

type contact struct {
	ID, Name, Email, Phone, Title string
	CompanyID, CompanyName        string
	OwnerEmail                    string
	OptOutAt                      *time.Time
	OptOutReason                  string
}

type company struct {
	ID, Name, Website, Street, City, State, Postcode, Country string
	OwnerEmail                                                string
}

type link struct {
	ContactID, PayloadHash string
	MauticID               int64
	Segments, Tags         []string
	CompanyMauticID        *int64
}

// Run does one sync.
func (s *Syncer) Run(ctx context.Context, opt Options) (*Report, error) {
	s.rep, s.opt = &Report{Write: opt.Write}, opt
	s.company, s.dnc = map[string]int64{}, map[int64]bool{}
	s.emailCch, s.formCch = map[int64]string{}, map[int64]string{}
	if opt.Workers <= 0 {
		opt.Workers = 8
	}

	var err error
	if s.segs, err = s.mc.Segments(ctx); err != nil {
		return s.rep, fmt.Errorf("read Mautic's segments: %w", err)
	}
	if s.users, err = s.mc.Users(ctx); err != nil {
		return s.rep, fmt.Errorf("read Mautic's users: %w", err)
	}
	if opt.Write {
		if err := s.mc.EnsureField(ctx, "contact", contactIDField, "Hearth ID"); err != nil {
			return s.rep, fmt.Errorf("make the Hearth ID field on contacts: %w", err)
		}
		if err := s.mc.EnsureField(ctx, "company", companyIDField, "Hearth company ID"); err != nil {
			return s.rep, fmt.Errorf("make the Hearth ID field on companies: %w", err)
		}
		if s.bot, err = s.botPrincipal(ctx); err != nil {
			return s.rep, err
		}
	}
	links, err := s.links(ctx)
	if err != nil {
		return s.rep, err
	}

	// Mautic's do-not-contact first: an opt-out made there since the last run has to be in Hearth before anything is pushed.
	if err := s.pullDoNotContact(ctx, links); err != nil {
		return s.rep, err
	}
	if err := s.push(ctx, links, opt.Workers); err != nil {
		return s.rep, err
	}
	if err := s.pullActivity(ctx); err != nil {
		return s.rep, err
	}
	return s.rep, nil
}

// -----------------------------------------------------------------------------
// What Hearth holds
// -----------------------------------------------------------------------------

func (s *Syncer) links(ctx context.Context) (map[string]*link, error) {
	rows, err := s.db.Query(ctx, `SELECT contact_id::text, mautic_id, payload_hash, segments, tags, company_mautic_id
		FROM mautic_links WHERE org_id = $1`, s.org)
	if err != nil {
		return nil, fmt.Errorf("read the Mautic links: %w", err)
	}
	defer rows.Close()
	out := map[string]*link{}
	for rows.Next() {
		l := &link{}
		if err := rows.Scan(&l.ContactID, &l.MauticID, &l.PayloadHash, &l.Segments, &l.Tags, &l.CompanyMauticID); err != nil {
			return nil, err
		}
		out[l.ContactID] = l
	}
	return out, rows.Err()
}

func (s *Syncer) contacts(ctx context.Context, only []string) ([]contact, error) {
	q := `SELECT c.id::text, c.name, c.email, c.phone, c.title, coalesce(c.company_id::text,''), coalesce(co.name,''),
	             coalesce((SELECT ui.email::text FROM user_identities ui
	                        WHERE ui.principal_id = coalesce(c.owner_id, co.owner_id) AND ui.email IS NOT NULL LIMIT 1), ''),
	             c.marketing_opt_out_at, c.marketing_opt_out_reason
	        FROM contacts c LEFT JOIN companies co ON co.id = c.company_id AND co.deleted_at IS NULL
	       WHERE c.org_id = $1 AND c.deleted_at IS NULL AND btrim(c.email) <> ''`
	args := []any{s.org}
	if len(only) > 0 {
		q += ` AND c.id = ANY($2::uuid[])`
		args = append(args, only)
	}
	q += ` ORDER BY c.created_at, c.id`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("read the contacts: %w", err)
	}
	defer rows.Close()
	var out []contact
	for rows.Next() {
		var c contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Title, &c.CompanyID, &c.CompanyName,
			&c.OwnerEmail, &c.OptOutAt, &c.OptOutReason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// labels returns, per company id, its type keys with their labels and its free tags.
func (s *Syncer) labels(ctx context.Context) (types map[string][][2]string, tags map[string][]string, err error) {
	types, tags = map[string][][2]string{}, map[string][]string{}
	rows, err := s.db.Query(ctx, `SELECT l.company_id::text, t.key, t.label
		FROM company_type_links l JOIN company_types t ON t.id = l.type_id
		WHERE t.org_id = $1 AND NOT t.archived ORDER BY t.position, t.key`, s.org)
	if err != nil {
		return nil, nil, fmt.Errorf("read the company types: %w", err)
	}
	for rows.Next() {
		var id, key, label string
		if err := rows.Scan(&id, &key, &label); err != nil {
			rows.Close()
			return nil, nil, err
		}
		types[id] = append(types[id], [2]string{key, label})
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT g.company_id::text, g.tag FROM company_tags g
		JOIN companies c ON c.id = g.company_id WHERE c.org_id = $1 ORDER BY lower(g.tag)`, s.org)
	if err != nil {
		return nil, nil, fmt.Errorf("read the company tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return nil, nil, err
		}
		tags[id] = append(tags[id], strings.TrimSpace(tag))
	}
	return types, tags, rows.Err()
}

func (s *Syncer) companyRow(ctx context.Context, id string) (*company, error) {
	c := &company{}
	err := s.db.QueryRow(ctx, `SELECT co.id::text, co.name, co.website, co.address_street, co.address_city, co.address_state,
	       co.address_postcode, co.address_country,
	       coalesce((SELECT ui.email::text FROM user_identities ui WHERE ui.principal_id = co.owner_id AND ui.email IS NOT NULL LIMIT 1), '')
	  FROM companies co WHERE co.id = $1 AND co.org_id = $2 AND co.deleted_at IS NULL`, id, s.org).
		Scan(&c.ID, &c.Name, &c.Website, &c.Street, &c.City, &c.State, &c.Postcode, &c.Country, &c.OwnerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// -----------------------------------------------------------------------------
// Segments and tags
// -----------------------------------------------------------------------------

type segSpec struct{ Key, Alias, Name string }

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func shortHash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:6]
}

// segmentsFor is the segments a contact belongs in, from its company's types and tags.
func segmentsFor(types [][2]string, tags []string) []segSpec {
	out := []segSpec{{Key: "all", Alias: "hearth-all", Name: "Hearth: all contacts"}}
	for _, t := range types {
		out = append(out, segSpec{Key: "type:" + t[0], Alias: "hearth-type-" + t[0], Name: "Hearth type: " + t[1]})
	}
	seen := map[string]bool{}
	for _, tg := range tags {
		k := "tag:" + strings.ToLower(tg)
		if tg == "" || seen[k] {
			continue
		}
		seen[k] = true
		alias := "hearth-tag-" + slug(tg)
		if slug(tg) == "" {
			alias = "hearth-tag"
		}
		out = append(out, segSpec{Key: k, Alias: alias + "-" + shortHash(k), Name: "Hearth tag: " + tg})
	}
	return out
}

func tagsFor(segs []segSpec) []string {
	out := []string{"hearth"}
	for _, g := range segs {
		switch {
		case strings.HasPrefix(g.Key, "type:"):
			out = append(out, "hearth-type-"+strings.TrimPrefix(g.Key, "type:"))
		case strings.HasPrefix(g.Key, "tag:"):
			out = append(out, "hearth-tag-"+slug(strings.TrimPrefix(g.Key, "tag:")))
		}
	}
	return uniq(out)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// segmentID finds or makes the Mautic segment for a spec. In a dry run a missing one reads as 0.
func (s *Syncer) segmentID(ctx context.Context, g segSpec) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.segs[g.Alias]; ok {
		return id, nil
	}
	if !s.opt.Write {
		s.rep.add("create", "segment", g.Alias, g.Name, "")
		s.segs[g.Alias] = 0
		return 0, nil
	}
	id, err := s.mc.CreateSegment(ctx, g.Name, g.Alias, "Kept by Hearth. Membership follows the company types and tags in the Hearth CRM; edit it there.")
	if err != nil {
		return 0, fmt.Errorf("make segment %s: %w", g.Alias, err)
	}
	s.segs[g.Alias] = id
	s.rep.add("create", "segment", g.Alias, g.Name, fmt.Sprintf("mautic %d", id))
	return id, nil
}

// aliasOf maps a stored segment key back to its alias, for removing a contact from a segment that is no longer wanted.
func aliasOf(key string) string {
	switch {
	case key == "all":
		return "hearth-all"
	case strings.HasPrefix(key, "type:"):
		return "hearth-type-" + strings.TrimPrefix(key, "type:")
	case strings.HasPrefix(key, "tag:"):
		t := strings.TrimPrefix(key, "tag:")
		a := "hearth-tag"
		if slug(t) != "" {
			a += "-" + slug(t)
		}
		return a + "-" + shortHash(key)
	}
	return ""
}

// -----------------------------------------------------------------------------
// Out: Hearth to Mautic
// -----------------------------------------------------------------------------

func splitName(full string) (first, last string) {
	f := strings.Fields(full)
	switch len(f) {
	case 0:
		return "", ""
	case 1:
		return f[0], ""
	}
	return f[0], strings.Join(f[1:], " ")
}

func hashOf(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func validEmail(e string) bool {
	e = strings.TrimSpace(e)
	at := strings.LastIndex(e, "@")
	return at > 0 && at < len(e)-3 && !strings.ContainsAny(e, " \t\r\n,;<>") && strings.Contains(e[at:], ".")
}

func dncReason(r string) int {
	switch r {
	case "unsubscribed":
		return mautic.DNCUnsubscribed
	case "bounced":
		return mautic.DNCBounced
	}
	return mautic.DNCManual
}

func (s *Syncer) push(ctx context.Context, links map[string]*link, workers int) error {
	cs, err := s.contacts(ctx, s.opt.OnlyContacts)
	if err != nil {
		return err
	}
	types, tags, err := s.labels(ctx)
	if err != nil {
		return err
	}

	// A contact that was sent before and has since been deleted, lost its address, or is out of scope is retired, not left to be mailed.
	live := map[string]bool{}
	for _, c := range cs {
		live[c.ID] = true
	}
	var retire []*link
	if len(s.opt.OnlyContacts) == 0 {
		for id, l := range links {
			if !live[id] && l.PayloadHash != "retired" {
				retire = append(retire, l)
			}
		}
	}
	sort.Slice(retire, func(i, j int) bool { return retire[i].ContactID < retire[j].ContactID })

	work := make(chan contact)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if ctx.Err() != nil {
					return
				}
				s.pushOne(ctx, c, links[c.ID], types[c.CompanyID], tags[c.CompanyID])
			}
		}()
	}
	for _, c := range cs {
		select {
		case work <- c:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, l := range retire {
		s.retire(ctx, l)
	}
	return nil
}

func (s *Syncer) pushOne(ctx context.Context, c contact, l *link, types [][2]string, tags []string) {
	if !validEmail(c.Email) {
		s.rep.add("skip", "contact", c.ID, c.Name, "the address "+quote(c.Email)+" is not a usable email address")
		return
	}
	segs := segmentsFor(types, tags)
	desiredTags := tagsFor(segs)
	keys := make([]string, 0, len(segs))
	for _, g := range segs {
		keys = append(keys, g.Key)
	}
	sort.Strings(keys)

	first, last := splitName(c.Name)
	f := mautic.Fields{"firstname": first, "lastname": last, "email": strings.TrimSpace(c.Email), "phone": c.Phone,
		"position": c.Title, "company": clip(c.CompanyName, mauticNameMax), contactIDField: c.ID}
	owner := int64(0)
	if c.OwnerEmail != "" {
		owner = s.users[strings.ToLower(strings.TrimSpace(c.OwnerEmail))]
	}
	if owner != 0 {
		f["owner"] = owner
	}

	// The company comes first so the link can be made in the same pass.
	var cmid int64
	if c.CompanyID != "" {
		cmid = s.ensureCompany(ctx, c.CompanyID)
	}

	optOut := c.OptOutAt != nil
	hash := hashOf(map[string]any{"f": f, "tags": desiredTags, "segs": keys, "co": cmid, "optout": optOut})
	if l != nil && l.PayloadHash == hash {
		// Nothing to send, but an opt-out Mautic lost is put back every run.
		if optOut && s.opt.Write && !s.ensureDNC(ctx, c, l.MauticID) {
			return
		}
		s.rep.add("same", "contact", c.ID, c.Name, "")
		return
	}
	verb := "update"
	if l == nil {
		verb = "create"
	}
	if !s.opt.Write {
		s.rep.add(verb, "contact", c.ID, c.Name, detail(desiredTags, optOut))
		for _, g := range segs {
			_, _ = s.segmentID(ctx, g)
		}
		return
	}

	// Tags that were sent before and are not wanted now are taken off with Mautic's "-tag" form.
	send := mautic.Fields{}
	for k, v := range f {
		send[k] = v
	}
	tagList := append([]string{}, desiredTags...)
	if l != nil {
		for _, old := range l.Tags {
			if !contains(desiredTags, old) {
				tagList = append(tagList, "-"+old)
			}
		}
	}
	send["tags"] = tagList

	var mid int64
	var err error
	if l != nil {
		mid, err = s.mc.UpsertContact(ctx, l.MauticID, send)
		if mautic.IsNotFound(err) { // somebody deleted it in Mautic: make it again
			mid, err = s.mc.UpsertContact(ctx, 0, send)
		}
	} else {
		mid, err = s.mc.UpsertContact(ctx, 0, send)
	}
	if err != nil {
		s.rep.add("failed", "contact", c.ID, c.Name, err.Error())
		return
	}

	// Company link.
	var oldCo *int64
	if l != nil {
		oldCo = l.CompanyMauticID
	}
	if oldCo != nil && *oldCo != cmid && l != nil && l.MauticID == mid {
		if err := s.mc.UnlinkCompany(ctx, *oldCo, mid); err != nil && !mautic.IsNotFound(err) {
			s.rep.add("failed", "contact", c.ID, c.Name, "unlink the old company: "+err.Error())
			return
		}
	}
	if cmid != 0 && (oldCo == nil || *oldCo != cmid || (l != nil && l.MauticID != mid)) {
		if err := s.mc.LinkCompany(ctx, cmid, mid); err != nil {
			s.rep.add("failed", "contact", c.ID, c.Name, "link the company: "+err.Error())
			return
		}
	}

	// Segments.
	var have []string
	if l != nil && l.MauticID == mid {
		have = l.Segments
	}
	for _, g := range segs {
		if contains(have, g.Key) {
			continue
		}
		id, err := s.segmentID(ctx, g)
		if err == nil {
			err = s.mc.SegmentAdd(ctx, id, mid)
		}
		if err != nil {
			s.rep.add("failed", "contact", c.ID, c.Name, "segment "+g.Alias+": "+err.Error())
			return
		}
	}
	for _, k := range have {
		if contains(keys, k) {
			continue
		}
		s.mu.Lock()
		id, ok := s.segs[aliasOf(k)]
		s.mu.Unlock()
		if !ok {
			continue
		}
		if err := s.mc.SegmentRemove(ctx, id, mid); err != nil && !mautic.IsNotFound(err) {
			s.rep.add("failed", "contact", c.ID, c.Name, "leave segment "+aliasOf(k)+": "+err.Error())
			return
		}
	}

	// Opt-out, Hearth to Mautic.
	if optOut && !s.ensureDNC(ctx, c, mid) {
		return
	}

	var cm any
	if cmid != 0 {
		cm = cmid
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO mautic_links (contact_id, org_id, mautic_id, payload_hash, segments, tags, company_mautic_id, pushed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (contact_id) DO UPDATE SET mautic_id=EXCLUDED.mautic_id, payload_hash=EXCLUDED.payload_hash,
		  segments=EXCLUDED.segments, tags=EXCLUDED.tags, company_mautic_id=EXCLUDED.company_mautic_id, pushed_at=now()`,
		c.ID, s.org, mid, hash, keys, desiredTags, cm); err != nil {
		s.rep.add("failed", "contact", c.ID, c.Name, "remember the Mautic id: "+err.Error())
		return
	}
	s.rep.add(verb, "contact", c.ID, c.Name, fmt.Sprintf("mautic %d", mid))
}

// ensureDNC puts the Hearth opt-out on the Mautic contact if Mautic does not hold it. It reports false when the call failed.
func (s *Syncer) ensureDNC(ctx context.Context, c contact, mid int64) bool {
	s.mu.Lock()
	held := s.dnc[mid]
	s.mu.Unlock()
	if held {
		return true
	}
	if err := s.mc.AddDoNotContact(ctx, mid, dncReason(c.OptOutReason), "Hearth: opted out ("+orDefault(c.OptOutReason, "manual")+")"); err != nil {
		s.rep.add("failed", "contact", c.ID, c.Name, "do not contact: "+err.Error())
		return false
	}
	s.mu.Lock()
	s.dnc[mid] = true
	s.mu.Unlock()
	s.rep.add("create", "do-not-contact", c.ID, c.Name, "Hearth opt-out put on the Mautic contact")
	return true
}

func detail(tags []string, optOut bool) string {
	d := strings.Join(tags, ",")
	if optOut {
		d += "; opted out"
	}
	return d
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func quote(s string) string { return `"` + s + `"` }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ensureCompany makes or updates the Mautic company for a Hearth company once per run and returns its Mautic id (0 in a dry run
// or when it could not be made).
func (s *Syncer) ensureCompany(ctx context.Context, id string) int64 {
	s.mu.Lock()
	if v, ok := s.company[id]; ok {
		s.mu.Unlock()
		return v
	}
	// Hold the lock for the whole make, so two contacts of one company do not both create it.
	defer s.mu.Unlock()
	co, err := s.companyRow(ctx, id)
	if err != nil || co == nil {
		s.company[id] = 0
		if err != nil {
			s.rep.add("failed", "company", id, "", err.Error())
		}
		return 0
	}
	f := mautic.Fields{"companyname": clip(co.Name, mauticNameMax), "companywebsite": co.Website, "companyaddress1": co.Street, "companycity": co.City,
		"companystate": co.State, "companyzipcode": co.Postcode, "companycountry": co.Country, companyIDField: co.ID}
	if co.OwnerEmail != "" {
		if u := s.users[strings.ToLower(strings.TrimSpace(co.OwnerEmail))]; u != 0 {
			f["owner"] = u
		}
	}
	hash := hashOf(f)
	var mid int64
	var oldHash string
	err = s.db.QueryRow(ctx, `SELECT mautic_id, payload_hash FROM mautic_company_links WHERE company_id = $1`, id).Scan(&mid, &oldHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.rep.add("failed", "company", id, co.Name, err.Error())
		s.company[id] = 0
		return 0
	}
	if err == nil && oldHash == hash {
		s.company[id] = mid
		s.rep.add("same", "company", id, co.Name, "")
		return mid
	}
	verb := "update"
	if errors.Is(err, pgx.ErrNoRows) {
		verb, mid = "create", 0
	}
	if !s.opt.Write {
		s.rep.add(verb, "company", id, co.Name, "")
		s.company[id] = mid
		return mid
	}
	nid, uerr := s.mc.UpsertCompany(ctx, mid, f)
	if mautic.IsNotFound(uerr) {
		nid, uerr = s.mc.UpsertCompany(ctx, 0, f)
	}
	if dropRefused(f, uerr) {
		// Mautic refused the state or country as one it does not list. The company goes without it, and the hash above is of
		// what Hearth holds, so the next run sees nothing changed and does not ask again.
		nid, uerr = s.mc.UpsertCompany(ctx, mid, f)
		if mautic.IsNotFound(uerr) {
			nid, uerr = s.mc.UpsertCompany(ctx, 0, f)
		}
	}
	if uerr != nil {
		s.rep.add("failed", "company", id, co.Name, uerr.Error())
		s.company[id] = 0
		return 0
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO mautic_company_links (company_id, org_id, mautic_id, payload_hash, pushed_at)
		VALUES ($1,$2,$3,$4,now()) ON CONFLICT (company_id) DO UPDATE SET mautic_id=EXCLUDED.mautic_id,
		  payload_hash=EXCLUDED.payload_hash, pushed_at=now()`, id, s.org, nid, hash); err != nil {
		s.rep.add("failed", "company", id, co.Name, "remember the Mautic id: "+err.Error())
	}
	s.company[id] = nid
	s.rep.add(verb, "company", id, co.Name, fmt.Sprintf("mautic %d", nid))
	return nid
}

// retire takes a contact the sync sent before, and Hearth no longer has a mailable address for, out of every segment it put it in
// and tags it, so a campaign aimed at a segment does not reach it. The Mautic record stays; its history is Mautic's to keep.
func (s *Syncer) retire(ctx context.Context, l *link) {
	if !s.opt.Write {
		s.rep.add("update", "contact", l.ContactID, "", "would leave "+fmt.Sprint(len(l.Segments))+" segments (deleted in Hearth or no address)")
		return
	}
	for _, k := range l.Segments {
		s.mu.Lock()
		id, ok := s.segs[aliasOf(k)]
		s.mu.Unlock()
		if ok && id != 0 {
			if err := s.mc.SegmentRemove(ctx, id, l.MauticID); err != nil && !mautic.IsNotFound(err) {
				s.rep.add("failed", "contact", l.ContactID, "", "leave segment: "+err.Error())
				return
			}
		}
	}
	if _, err := s.mc.UpsertContact(ctx, l.MauticID, mautic.Fields{"tags": []string{"hearth-removed"}}); err != nil && !mautic.IsNotFound(err) {
		s.rep.add("failed", "contact", l.ContactID, "", "tag as removed: "+err.Error())
		return
	}
	if _, err := s.db.Exec(ctx, `UPDATE mautic_links SET segments = '{}', payload_hash = 'retired', pushed_at = now() WHERE contact_id = $1`, l.ContactID); err != nil {
		s.rep.add("failed", "contact", l.ContactID, "", err.Error())
		return
	}
	s.rep.add("update", "contact", l.ContactID, "", "left its segments: deleted in Hearth or no address")
}

// mauticNameMax is the longest company name Mautic takes, on the company and on a contact's own company field.
const mauticNameMax = 64

// clip cuts a value to at most n characters, so a long name is stored short and not refused whole.
func clip(v string, n int) string {
	v = strings.TrimSpace(v)
	if r := []rune(v); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return v
}

// dropRefused takes the state and country out of f when Mautic's answer was a 400 over either (they are selects whose lists the API
// does not expose, and Hearth holds free text like "OH"). It says whether it took anything out, so the caller tries again.
func dropRefused(f mautic.Fields, err error) bool {
	e, ok := err.(*mautic.APIError)
	if !ok || e.Status != 400 {
		return false
	}
	if !strings.Contains(e.Body, "companystate") && !strings.Contains(e.Body, "companycountry") {
		return false
	}
	// A state belongs to a country, so when Mautic refuses either one both go, and the retry cannot be refused over the other.
	dropped := false
	for _, k := range []string{"companystate", "companycountry"} {
		if _, has := f[k]; has {
			delete(f, k)
			dropped = true
		}
	}
	return dropped
}
