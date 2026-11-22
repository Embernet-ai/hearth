package mautic

// Live check of the Marketing area's calls against the real marketing app. Skipped unless HEARTH_LIVE_MARKETING_URL,
// _CLIENT_ID and _CLIENT_SECRET are set (secrets/access/hearth-apps-live.py puts them there). It makes a segment, an
// email and a campaign, changes each, and deletes all three.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveMarketing(t *testing.T) {
	base := os.Getenv("HEARTH_LIVE_MARKETING_URL")
	if base == "" {
		t.Skip("HEARTH_LIVE_MARKETING_URL not set: this talks to the real marketing app")
	}
	ctx := context.Background()
	c := New(base, os.Getenv("HEARTH_LIVE_MARKETING_CLIENT_ID"), os.Getenv("HEARTH_LIVE_MARKETING_CLIENT_SECRET"))
	stamp := time.Now().UTC().Format("150405")

	segID, err := c.SaveSegment(ctx, 0, "Hearth live proof "+stamp, "hearth-live-proof-"+stamp, "made by the live check", true)
	if err != nil {
		t.Fatalf("make segment: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteSegment(ctx, segID) })
	if _, err := c.SaveSegment(ctx, segID, "Hearth live proof "+stamp+" edited", "", "edited", true); err != nil {
		t.Fatalf("edit segment: %v", err)
	}
	segs, err := c.SegmentList(ctx)
	found := false
	for _, s := range segs {
		found = found || (s.ID == segID && strings.HasSuffix(s.Name, "edited"))
	}
	if err != nil || !found {
		t.Fatalf("segment list: %v found=%v", err, found)
	}
	t.Logf("marketing: segment made, edited, listed (%d segments)", len(segs))

	emailID, err := c.SaveEmail(ctx, 0, EmailInput{Name: "Hearth live proof " + stamp, Subject: "Hello from the live check", Type: "list",
		HTML: "<p>Hello</p>", Segments: []int64{segID}})
	if err != nil {
		t.Fatalf("make email: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteEmail(ctx, emailID) })
	if _, err := c.SaveEmail(ctx, emailID, EmailInput{Name: "Hearth live proof " + stamp, Subject: "Edited subject", Type: "list",
		HTML: "<p>Hello again</p>", Segments: []int64{segID}, PublishUp: time.Now().UTC().Add(48 * time.Hour).Format("2006-01-02T15:04:05+00:00"), Published: true}); err != nil {
		t.Fatalf("edit and schedule email: %v", err)
	}
	e, err := c.GetEmail(ctx, emailID)
	if err != nil || e.Subject != "Edited subject" || !e.Published || e.PublishUp == "" || len(e.Segments) != 1 {
		t.Fatalf("read email back %+v %v", e, err)
	}
	if err := c.SetEmailPublished(ctx, emailID, false); err != nil {
		t.Fatalf("unpublish email: %v", err)
	}
	list, total, err := c.Emails(ctx, "Hearth live proof "+stamp, 10, 0)
	if err != nil || total < 1 || len(list) < 1 || list[0].Published {
		t.Fatalf("email list %+v total %d %v", list, total, err)
	}
	t.Logf("marketing: email made, edited, scheduled, unpublished, listed (sent %d, read %d)", list[0].Sent, list[0].Read)

	campID, err := c.SaveCampaign(ctx, 0, CampaignInput{Name: "Hearth live proof " + stamp, Description: "live", Segments: []int64{segID}, EmailID: emailID, DelayDays: 1})
	if err != nil {
		t.Fatalf("make campaign: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteCampaign(ctx, campID) })
	if _, err := c.SaveCampaign(ctx, campID, CampaignInput{Name: "Hearth live proof " + stamp + " edited", Description: "edited", Segments: []int64{segID}, EmailID: emailID}); err != nil {
		t.Fatalf("edit campaign: %v", err)
	}
	if err := c.SetCampaignPublished(ctx, campID, true); err != nil {
		t.Fatalf("publish campaign: %v", err)
	}
	camps, _, err := c.Campaigns(ctx, "Hearth live proof "+stamp, 10, 0)
	if err != nil || len(camps) < 1 || !camps[0].Published || !strings.HasSuffix(camps[0].Name, "edited") {
		t.Fatalf("campaign list %+v %v", camps, err)
	}
	n, err := c.CampaignContacts(ctx, campID)
	if err != nil {
		t.Fatalf("campaign contacts: %v", err)
	}
	t.Logf("marketing: campaign made, edited, published, listed (%d contacts)", n)
}
