package crmmarketing_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/crmmarketing"
	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fakeMautic struct {
	in, inC []int64
	calls   []string
}

func (f *fakeMautic) SegmentsBrief(context.Context) ([]mautic.Segment, error) {
	return []mautic.Segment{{ID: 1, Name: "Expo"}, {ID: 2, Name: "Hearth: all contacts"}}, nil
}
func (f *fakeMautic) CampaignsBrief(context.Context) ([]mautic.Campaign, error) {
	return []mautic.Campaign{{ID: 7, Name: "Welcome", Published: true}, {ID: 8, Name: "Old", Published: false}}, nil
}
func (f *fakeMautic) ContactSegments(context.Context, int64) ([]int64, error)  { return f.in, nil }
func (f *fakeMautic) ContactCampaigns(context.Context, int64) ([]int64, error) { return f.inC, nil }
func (f *fakeMautic) SegmentAdd(_ context.Context, s, c int64) error {
	f.calls = append(f.calls, "seg+")
	return nil
}
func (f *fakeMautic) SegmentRemove(_ context.Context, s, c int64) error {
	f.calls = append(f.calls, "seg-")
	return nil
}
func (f *fakeMautic) CampaignAdd(_ context.Context, s, c int64) error {
	f.calls = append(f.calls, "camp+")
	return nil
}
func (f *fakeMautic) CampaignRemove(_ context.Context, s, c int64) error {
	f.calls = append(f.calls, "camp-")
	return nil
}

func TestAContactsPlaceInMauticIsShownAndMovable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	org, who, linked, unlinked := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mkt Test')`, org, "mk-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Pat')`, who, org)
	exec(`INSERT INTO contacts (id, org_id, name, email, created_by) VALUES ($1,$2,'Linked','l@x.example',$3), ($4,$2,'Unlinked','u@x.example',$3)`, linked, org, who, unlinked)
	exec(`INSERT INTO mautic_links (contact_id, org_id, mautic_id, payload_hash, segments, tags) VALUES ($1,$2,55,'h','{}','{}')`, linked, org)
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM mautic_links WHERE org_id=$1`, `DELETE FROM contacts WHERE org_id=$1`} {
			_, _ = pools.Web.Exec(context.Background(), q, org)
		}
	})
	mc := &fakeMautic{in: []int64{1, 2}, inC: nil}
	s := crmmarketing.New(pools.Web, mc)
	p, err := s.For(ctx, org, linked, true)
	if err != nil || len(p.Segments) != 2 || !p.Segments[0].Member || p.Segments[0].Locked || !p.Segments[1].Locked || !p.OptedOut {
		t.Fatalf("panel = %+v (%v), want Expo togglable, the Hearth segment locked, opted out", p, err)
	}
	if len(p.Campaigns) != 1 || p.Campaigns[0].Name != "Welcome" || p.Campaigns[0].Member {
		t.Errorf("campaigns = %+v, want only the published one, not joined", p.Campaigns)
	}
	if _, err := s.For(ctx, org, unlinked, false); !errors.Is(err, crmmarketing.ErrNotLinked) {
		t.Errorf("a contact not in Mautic: got %v, want ErrNotLinked", err)
	}
	if err := s.SetSegment(ctx, org, linked, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSegment(ctx, org, linked, 2, false); !errors.Is(err, crmmarketing.ErrLocked) {
		t.Errorf("a segment the sync keeps: got %v, want ErrLocked", err)
	}
	if err := s.SetSegment(ctx, org, linked, 99, true); err == nil {
		t.Error("a segment that does not exist was accepted")
	}
	if err := s.SetCampaign(ctx, org, linked, 7, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCampaign(ctx, org, unlinked, 7, true); !errors.Is(err, crmmarketing.ErrNotLinked) {
		t.Errorf("campaign for a contact not in Mautic: got %v", err)
	}
	if len(mc.calls) != 2 || mc.calls[0] != "seg-" || mc.calls[1] != "camp+" {
		t.Errorf("calls = %v, want seg- then camp+ and nothing for the refused ones", mc.calls)
	}
}
