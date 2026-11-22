package social

// Live check of the Social area's calls against the real social publishing app. Skipped unless HEARTH_LIVE_SOCIAL_URL and
// _API_KEY are set (secrets/access/hearth-apps-live.py puts them there). A post needs a connected channel, and connecting
// one is a sign-in at the network that only a person can do, so this reads what is there and, when a channel exists,
// saves a draft on it and deletes it.

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveSocial(t *testing.T) {
	base := os.Getenv("HEARTH_LIVE_SOCIAL_URL")
	if base == "" {
		t.Skip("HEARTH_LIVE_SOCIAL_URL not set: this talks to the real social app")
	}
	ctx := context.Background()
	c := New(base, os.Getenv("HEARTH_LIVE_SOCIAL_API_KEY"))
	chans, err := c.Channels(ctx)
	if err != nil {
		t.Fatalf("channels: %v", err)
	}
	posts, err := c.Posts(ctx, time.Now().Add(-30*24*time.Hour), time.Now().Add(30*24*time.Hour))
	if err != nil {
		t.Fatalf("posts: %v", err)
	}
	t.Logf("social: %d channels, %d posts in the window", len(chans), len(posts))
	if err := c.Create(ctx, Draft{Content: "x"}); err == nil {
		t.Fatal("a post with no channel was accepted")
	}
	if len(chans) == 0 {
		t.Log("social: no channel is connected, so creating a post is not exercised live")
		return
	}
	if err := c.Create(ctx, Draft{Content: "Hearth live check, safe to delete", Channels: chans[:1], Save: true}); err != nil {
		t.Fatalf("save a draft: %v", err)
	}
	after, err := c.Posts(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour*60))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range after {
		if p.Content == "Hearth live check, safe to delete" {
			if err := c.Delete(ctx, p.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
		}
	}
	t.Log("social: draft saved and deleted")
}
