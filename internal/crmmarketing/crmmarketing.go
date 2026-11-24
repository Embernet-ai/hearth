// Package crmmarketing shows a contact's place in Mautic on the contact's own page, and moves them by hand.
//
// Mautic sends the email and owns its segments and campaigns, and the Marketing sync already puts what Mautic recorded
// (sent, opened, clicked, form, unsubscribed, bounced) on the timeline. This is the other half a salesperson needs at the
// record: which segments and campaigns this person is in, and a way to put them in or take them out. Segments the sync
// keeps ("Hearth: ...") are shown and are not togglable, because the next run would undo a change.
package crmmarketing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/mautic"
)

// Mautic is the slice of the Mautic client used here.
type Mautic interface {
	SegmentsBrief(ctx context.Context) ([]mautic.Segment, error)
	CampaignsBrief(ctx context.Context) ([]mautic.Campaign, error)
	ContactSegments(ctx context.Context, contactID int64) ([]int64, error)
	ContactCampaigns(ctx context.Context, contactID int64) ([]int64, error)
	SegmentAdd(ctx context.Context, segmentID, contactID int64) error
	SegmentRemove(ctx context.Context, segmentID, contactID int64) error
	CampaignAdd(ctx context.Context, campaignID, contactID int64) error
	CampaignRemove(ctx context.Context, campaignID, contactID int64) error
}

// ErrNotLinked is a contact the Marketing sync has not sent to Mautic yet (no address, or not yet run).
var ErrNotLinked = errors.New("crmmarketing: this person is not in Mautic yet")

// ErrLocked is a segment the sync keeps.
var ErrLocked = errors.New("crmmarketing: the sync keeps that segment, so a change here would be undone")

// Item is one segment or campaign and whether the contact is in it.
type Item struct {
	ID     int64
	Name   string
	Member bool
	Locked bool
}

// Panel is a contact's place in Mautic.
type Panel struct {
	Segments  []Item
	Campaigns []Item
	OptedOut  bool
}

// Service reads and moves.
type Service struct {
	db *pgxpool.Pool
	mc Mautic
}

// New returns a Service.
func New(db *pgxpool.Pool, mc Mautic) *Service { return &Service{db: db, mc: mc} }

func (s *Service) mauticID(ctx context.Context, orgID, contactID string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT mautic_id FROM mautic_links WHERE org_id=$1 AND contact_id=$2`, orgID, contactID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && id == 0) {
		return 0, ErrNotLinked
	}
	if err != nil {
		return 0, fmt.Errorf("crmmarketing: find the Mautic contact: %w", err)
	}
	return id, nil
}

func locked(name string) bool { return strings.HasPrefix(strings.ToLower(name), "hearth") }

// For reads the panel. A contact that is not in Mautic yet answers ErrNotLinked.
func (s *Service) For(ctx context.Context, orgID, contactID string, optedOut bool) (*Panel, error) {
	mid, err := s.mauticID(ctx, orgID, contactID)
	if err != nil {
		return nil, err
	}
	segs, err := s.mc.SegmentsBrief(ctx)
	if err != nil {
		return nil, fmt.Errorf("crmmarketing: segments: %w", err)
	}
	in, err := s.mc.ContactSegments(ctx, mid)
	if err != nil {
		return nil, fmt.Errorf("crmmarketing: this contact's segments: %w", err)
	}
	camps, err := s.mc.CampaignsBrief(ctx)
	if err != nil {
		return nil, fmt.Errorf("crmmarketing: campaigns: %w", err)
	}
	inC, err := s.mc.ContactCampaigns(ctx, mid)
	if err != nil {
		return nil, fmt.Errorf("crmmarketing: this contact's campaigns: %w", err)
	}
	has := func(list []int64, id int64) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}
	p := &Panel{OptedOut: optedOut}
	for _, sg := range segs {
		p.Segments = append(p.Segments, Item{ID: sg.ID, Name: sg.Name, Member: has(in, sg.ID), Locked: locked(sg.Name)})
	}
	for _, c := range camps {
		if c.Published || has(inC, c.ID) {
			p.Campaigns = append(p.Campaigns, Item{ID: c.ID, Name: c.Name, Member: has(inC, c.ID)})
		}
	}
	return p, nil
}

// SetSegment puts the contact in a segment or takes them out. A segment the sync keeps answers ErrLocked.
func (s *Service) SetSegment(ctx context.Context, orgID, contactID string, segmentID int64, on bool) error {
	mid, err := s.mauticID(ctx, orgID, contactID)
	if err != nil {
		return err
	}
	segs, err := s.mc.SegmentsBrief(ctx)
	if err != nil {
		return fmt.Errorf("crmmarketing: segments: %w", err)
	}
	for _, sg := range segs {
		if sg.ID != segmentID {
			continue
		}
		if locked(sg.Name) {
			return ErrLocked
		}
		if on {
			return s.mc.SegmentAdd(ctx, segmentID, mid)
		}
		return s.mc.SegmentRemove(ctx, segmentID, mid)
	}
	return fmt.Errorf("crmmarketing: no such segment")
}

// SetCampaign puts the contact in a campaign or takes them out. Mautic itself refuses to mail a person who unsubscribed.
func (s *Service) SetCampaign(ctx context.Context, orgID, contactID string, campaignID int64, on bool) error {
	mid, err := s.mauticID(ctx, orgID, contactID)
	if err != nil {
		return err
	}
	if on {
		return s.mc.CampaignAdd(ctx, campaignID, mid)
	}
	return s.mc.CampaignRemove(ctx, campaignID, mid)
}
