package connect

// Two-way calendar sync.
//
// A person's calendar at Google or Microsoft and their calendar in Hearth are kept the same, in both directions,
// by a window of time (a week back, ninety days forward). Each event that has crossed over has a row in
// connector_links holding two facts about the moment it last matched: the provider's own change marker, and a hash
// of the Hearth event. At the next run those two say who changed what since:
//
//	the provider's marker moved          the provider changed it   -> Hearth takes the provider's version
//	the hash of the Hearth event moved   Hearth changed it         -> the provider is updated
//	both moved                           both changed it           -> the provider wins, and the log says so
//	gone from the provider (in window)   deleted there             -> deleted here
//	gone from Hearth                     deleted here              -> deleted there
//	new on either side                   created                   -> created on the other
//
// Provider wins on a clash because it is the system the rest of the person's tools already read, and because a
// silent overwrite of the provider's copy cannot be undone by the person while the Hearth one can be re-edited.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	windowBack    = 7 * 24 * time.Hour
	windowForward = 90 * 24 * time.Hour
	primaryCal    = "primary"
)

// localEvent is a Hearth event as the sync reads it.
type localEvent struct {
	ID       string
	Title    string
	Notes    string
	Place    string
	Start    time.Time
	End      time.Time
	AllDay   bool
	Created  string
	Space    string
	Personal string
}

func (e localEvent) hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%d\x00%t", e.Title, e.Notes, e.Place,
		e.Start.UTC().Unix(), e.End.UTC().Unix(), e.AllDay)
	return hex.EncodeToString(h.Sum(nil))
}

// toRemote turns a Hearth event into what a provider is sent. Hearth holds an all-day event's last day inclusive,
// a provider holds the day after it.
func (e localEvent) toRemote() Event {
	ev := Event{Title: e.Title, Notes: e.Notes, Place: e.Place, Start: e.Start.UTC(), End: e.End.UTC(), AllDay: e.AllDay}
	if e.AllDay {
		ev.Start = dayOf(e.Start)
		ev.End = dayOf(e.End).AddDate(0, 0, 1)
	}
	return ev
}

func dayOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// fromRemote is the other way.
func fromRemote(r Event) (start, end time.Time, place string) {
	start, end = r.Start.UTC(), r.End.UTC()
	if r.AllDay {
		start = dayOf(r.Start)
		end = dayOf(r.End).AddDate(0, 0, -1)
		if end.Before(start) {
			end = start
		}
	}
	if end.Before(start) {
		end = start
	}
	place = strings.TrimSpace(r.Place)
	if place == "" {
		place = r.JoinURL
	}
	return start, end, place
}

// loadEvent reads a Hearth event as the sync needs it, from wherever this installation keeps events.
func (s *Service) loadEvent(ctx context.Context, orgID, id string) (*localEvent, error) {
	return s.events.Load(ctx, orgID, id)
}

type calLink struct {
	entityID string
	version  string
	hash     string
}

// calendarTarget is one calendar being synced and where its events live in Hearth.
type calendarTarget struct {
	id    string // the provider's calendar id, "primary" for the person's own
	space string // "" means the person's own calendar in Hearth
}

// targets lists the calendars a person syncs: their own, plus any they mapped to a department or turned on.
func (s *Service) targets(ctx context.Context, principalID string, p Provider) ([]calendarTarget, error) {
	out := []calendarTarget{{id: primaryCal}}
	rows, err := s.db.Query(ctx, `
		SELECT calendar_id, COALESCE(space_id::text, '')
		  FROM connector_calendars WHERE principal_id = $1 AND provider = $2 ORDER BY created_at`,
		principalID, string(p))
	if err != nil {
		return nil, fmt.Errorf("connect: read calendars: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t calendarTarget
		if err := rows.Scan(&t.id, &t.space); err != nil {
			return nil, err
		}
		if t.id != primaryCal {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// SyncCalendar runs every calendar one person has set up, and returns how many events changed on either side.
func (s *Service) SyncCalendar(ctx context.Context, orgID, principalID string, p Provider) (int, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Calendar)
	if err != nil {
		return 0, err
	}
	st, _ := s.settingFor(ctx, orgID, p, Calendar)
	targets, err := s.targets(ctx, principalID, p)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, t := range targets {
		n, err := s.syncOneCalendar(ctx, orgID, principalID, p, sess.Calendar(), t, st.MeetLinks)
		total += n
		if err != nil {
			return total, fmt.Errorf("calendar %s: %w", t.id, err)
		}
	}
	if p == Google {
		// The older read-only copy of the same calendar would show every event twice.
		_, _ = s.db.Exec(ctx, `DELETE FROM google_events WHERE principal_id = $1`, principalID)
	}
	return total, nil
}

func (s *Service) settingFor(ctx context.Context, orgID string, p Provider, k Kind) (Setting, error) {
	st := Setting{Provider: p, Kind: k}
	err := s.db.QueryRow(ctx, `
		SELECT enabled, meet_links FROM connector_settings WHERE org_id=$1 AND provider=$2 AND connector=$3`,
		orgID, string(p), string(k)).Scan(&st.Enabled, &st.MeetLinks)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	return st, err
}

func (s *Service) syncOneCalendar(ctx context.Context, orgID, principalID string, p Provider,
	api CalendarAPI, t calendarTarget, meeting bool) (int, error) {

	now := s.now().UTC()
	from, to := now.Add(-windowBack), now.Add(windowForward)
	remote, err := api.Events(ctx, t.id, from, to)
	if err != nil {
		return 0, err
	}
	extKey := func(id string) string { return t.id + "|" + id }

	// What is already linked for this calendar.
	links := map[string]calLink{}
	rows, err := s.db.Query(ctx, `
		SELECT external_id, entity_id::text, remote_version, local_hash
		  FROM connector_links
		 WHERE principal_id=$1 AND provider=$2 AND connector='calendar' AND external_id LIKE $3`,
		principalID, string(p), likePrefix(t.id)+"|%")
	if err != nil {
		return 0, fmt.Errorf("connect: read links: %w", err)
	}
	for rows.Next() {
		var ext string
		var l calLink
		if err := rows.Scan(&ext, &l.entityID, &l.version, &l.hash); err != nil {
			rows.Close()
			return 0, err
		}
		links[ext] = l
	}
	rows.Close()

	changed := 0
	seen := map[string]bool{}
	for _, r := range remote {
		key := extKey(r.ID)
		seen[key] = true
		l, linked := links[key]
		if !linked {
			if err := s.createLocal(ctx, orgID, principalID, p, t, r); err != nil {
				return changed, err
			}
			changed++
			continue
		}
		le, err := s.loadEvent(ctx, orgID, l.entityID)
		if err != nil {
			return changed, err
		}
		if le == nil {
			// Deleted in Hearth: take it off the provider too.
			if err := api.Delete(ctx, t.id, r.ID); err != nil {
				return changed, err
			}
			s.dropLink(ctx, principalID, p, key)
			changed++
			continue
		}
		remoteMoved := r.Version != l.version
		localMoved := le.hash() != l.hash
		switch {
		case remoteMoved:
			if localMoved {
				s.log.Info("connect: calendar clash, the provider's version wins",
					"provider", p, "event", l.entityID)
			}
			if err := s.updateLocal(ctx, orgID, principalID, p, key, l.entityID, r); err != nil {
				return changed, err
			}
			changed++
		case localMoved:
			out, err := api.Update(ctx, t.id, withID(le.toRemote(), r.ID))
			if err != nil {
				return changed, err
			}
			if err := s.setLink(ctx, orgID, principalID, p, key, l.entityID, out.Version, le.hash()); err != nil {
				return changed, err
			}
			changed++
		}
	}

	// Linked here, gone there. Only events inside the window count: one outside it was never asked about. One event that
	// cannot be removed never stops the others: its link stays so the next run tries it again, and the first error is
	// returned once the rest are done. (Map order decided which events a single refusal used to leave behind.)
	var firstErr error
	for key, l := range links {
		if seen[key] {
			continue
		}
		le, err := s.loadEvent(ctx, orgID, l.entityID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		inWindow := le != nil && !le.Start.Before(from) && le.Start.Before(to)
		if inWindow {
			if err := s.events.Delete(ctx, orgID, l.entityID); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("connect: delete event: %w", err)
				}
				continue
			}
			changed++
		}
		if le == nil || inWindow {
			s.dropLink(ctx, principalID, p, key)
		}
	}
	if firstErr != nil {
		return changed, firstErr
	}

	// New in Hearth: send it across.
	owned, err := s.unlinkedLocal(ctx, orgID, principalID, t, from, to)
	if err != nil {
		return changed, err
	}
	for _, le := range owned {
		out, err := api.Create(ctx, t.id, le.toRemote(), meeting)
		if err != nil {
			return changed, err
		}
		if err := s.setLink(ctx, orgID, principalID, p, extKey(out.ID), le.ID, out.Version, le.hash()); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func withID(e Event, id string) Event { e.ID = id; return e }

func likePrefix(id string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(id)
}

// unlinkedLocal finds Hearth events that have never been sent anywhere. For a person's own calendar that is what
// they created; for a department's calendar it is everything in the department.
func (s *Service) unlinkedLocal(ctx context.Context, orgID, principalID string, t calendarTarget, from, to time.Time) ([]localEvent, error) {
	ids, err := s.events.Candidates(ctx, orgID, principalID, t.space, from, to)
	if err != nil {
		return nil, fmt.Errorf("connect: find new events: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	linked := map[string]bool{}
	rows, err := s.db.Query(ctx, `SELECT entity_id::text FROM connector_links WHERE entity_type='event' AND entity_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("connect: find new events: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		linked[id] = true
	}
	rows.Close()
	var out []localEvent
	for _, id := range ids {
		if linked[id] {
			continue
		}
		le, err := s.loadEvent(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		if le != nil {
			out = append(out, *le)
		}
	}
	return out, nil
}

func (s *Service) createLocal(ctx context.Context, orgID, principalID string, p Provider, t calendarTarget, r Event) error {
	start, end, place := fromRemote(r)
	title := strings.TrimSpace(r.Title)
	if title == "" {
		title = "(no title)"
	}
	id, err := s.events.Create(ctx, orgID, principalID, t.space, localEvent{Title: title, Notes: r.Notes, Place: place, Start: start, End: end, AllDay: r.AllDay})
	if err != nil {
		return fmt.Errorf("connect: create event: %w", err)
	}
	le, err := s.loadEvent(ctx, orgID, id)
	if err != nil || le == nil {
		return fmt.Errorf("connect: read back event: %v", err)
	}
	return s.setLink(ctx, orgID, principalID, p, t.id+"|"+r.ID, id, r.Version, le.hash())
}

func (s *Service) updateLocal(ctx context.Context, orgID, principalID string, p Provider, key, id string, r Event) error {
	start, end, place := fromRemote(r)
	title := strings.TrimSpace(r.Title)
	if title == "" {
		title = "(no title)"
	}
	if err := s.events.Update(ctx, orgID, id, localEvent{Title: title, Notes: r.Notes, Place: place, Start: start, End: end, AllDay: r.AllDay}); err != nil {
		return fmt.Errorf("connect: update event: %w", err)
	}
	le, err := s.loadEvent(ctx, orgID, id)
	if err != nil || le == nil {
		return fmt.Errorf("connect: read back event: %v", err)
	}
	return s.setLink(ctx, orgID, principalID, p, key, id, r.Version, le.hash())
}

func (s *Service) setLink(ctx context.Context, orgID, principalID string, p Provider, key, entityID, version, hash string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO connector_links (org_id, principal_id, provider, connector, external_id, entity_type, entity_id,
		                             remote_version, local_hash)
		VALUES ($1,$2,$3,'calendar',$4,'event',$5,$6,$7)
		ON CONFLICT (principal_id, provider, connector, external_id) DO UPDATE
		   SET entity_id=EXCLUDED.entity_id, remote_version=EXCLUDED.remote_version,
		       local_hash=EXCLUDED.local_hash, synced_at=now()`,
		orgID, principalID, string(p), key, entityID, version, hash)
	if err != nil {
		return fmt.Errorf("connect: save link: %w", err)
	}
	return nil
}

func (s *Service) dropLink(ctx context.Context, principalID string, p Provider, key string) {
	_, _ = s.db.Exec(ctx, `
		DELETE FROM connector_links WHERE principal_id=$1 AND provider=$2 AND connector='calendar' AND external_id=$3`,
		principalID, string(p), key)
}

// -----------------------------------------------------------------------------
// Which calendars
// -----------------------------------------------------------------------------

// CalendarChoice is a calendar a person can read and whether, and where to, it is synced.
type CalendarChoice struct {
	ID       string
	Name     string
	Primary  bool
	Writable bool
	Synced   bool
	SpaceID  string
}

// Calendars lists a person's calendars with what they chose.
func (s *Service) Calendars(ctx context.Context, orgID, principalID string, p Provider) ([]CalendarChoice, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Calendar)
	if err != nil {
		return nil, err
	}
	infos, err := sess.Calendar().Calendars(ctx)
	if err != nil {
		return nil, err
	}
	chosen := map[string]string{}
	rows, err := s.db.Query(ctx, `
		SELECT calendar_id, COALESCE(space_id::text,'') FROM connector_calendars
		 WHERE principal_id=$1 AND provider=$2`, principalID, string(p))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, sp string
		if err := rows.Scan(&id, &sp); err != nil {
			rows.Close()
			return nil, err
		}
		chosen[id] = sp
	}
	rows.Close()
	var out []CalendarChoice
	for _, ci := range infos {
		c := CalendarChoice{ID: ci.ID, Name: ci.Name, Primary: ci.Primary, Writable: ci.Writable}
		if ci.Primary {
			c.ID, c.Synced = primaryCal, true
		} else if sp, ok := chosen[ci.ID]; ok {
			c.Synced, c.SpaceID = true, sp
		}
		out = append(out, c)
	}
	return out, nil
}

// ErrNotManager is returned when somebody maps a calendar onto a department they do not manage.
var ErrNotManager = errors.New("connect: only a manager of a department can sync a calendar into it")

// SyncCalendarInto turns on syncing for an extra calendar. With a space it becomes that department's calendar, so
// everybody Hearth gives the department can see it; the person must manage the department. Without one it is
// another calendar of their own.
func (s *Service) SyncCalendarInto(ctx context.Context, orgID, principalID string, p Provider, calendarID, name, spaceID string) error {
	if calendarID == "" || calendarID == primaryCal {
		return errors.New("connect: the primary calendar is always synced")
	}
	if spaceID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM space_managers m JOIN spaces sp ON sp.id = m.space_id
			                WHERE m.space_id = $1::uuid AND m.principal_id = $2 AND sp.org_id = $3)`,
			spaceID, principalID, orgID).Scan(&ok); err != nil {
			return fmt.Errorf("connect: check department: %w", err)
		}
		if !ok {
			return ErrNotManager
		}
	}
	var space any
	if spaceID != "" {
		space = spaceID
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO connector_calendars (org_id, principal_id, provider, calendar_id, name, space_id)
		VALUES ($1,$2,$3,$4,$5,$6::uuid)
		ON CONFLICT (principal_id, provider, calendar_id) DO UPDATE SET name=EXCLUDED.name, space_id=EXCLUDED.space_id`,
		orgID, principalID, string(p), calendarID, name, space)
	if err != nil {
		var pe interface{ SQLState() string }
		if errors.As(err, &pe) && pe.SQLState() == "23505" {
			return errors.New("connect: that calendar is already synced into a department by somebody else")
		}
		return fmt.Errorf("connect: save calendar: %w", err)
	}
	return nil
}

// StopSyncingCalendar stops syncing an extra calendar. What it brought in stays in Hearth.
func (s *Service) StopSyncingCalendar(ctx context.Context, principalID string, p Provider, calendarID string) error {
	if _, err := s.db.Exec(ctx, `
		DELETE FROM connector_calendars WHERE principal_id=$1 AND provider=$2 AND calendar_id=$3`,
		principalID, string(p), calendarID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		DELETE FROM connector_links WHERE principal_id=$1 AND provider=$2 AND connector='calendar' AND external_id LIKE $3`,
		principalID, string(p), likePrefix(calendarID)+"|%")
	return err
}

// Space is a department a person manages.
type Space struct{ ID, Name string }

// ManagedSpaces lists the departments a person manages, which are the ones they may sync a calendar into.
func (s *Service) ManagedSpaces(ctx context.Context, orgID, principalID string) ([]Space, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sp.id::text, sp.name FROM space_managers m JOIN spaces sp ON sp.id = m.space_id
		 WHERE m.principal_id = $1 AND sp.org_id = $2 ORDER BY sp.name`, principalID, orgID)
	if err != nil {
		return nil, fmt.Errorf("connect: list departments: %w", err)
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		var sp Space
		if err := rows.Scan(&sp.ID, &sp.Name); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}
