package social

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostsChannelsAndCreateAgainstTheAppsShape(t *testing.T) {
	var gets int32
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/public/v1/integrations":
			atomic.AddInt32(&gets, 1)
			_, _ = w.Write([]byte(`[{"id":"c1","name":"Fireball","identifier":"linkedin","disabled":false,"profile":"fireball"}]`))
		case r.URL.Path == "/api/public/v1/posts" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"posts":[{"id":"p1","content":"Hello","publishDate":"2026-11-22T10:00:00.000Z","state":"QUEUE","integration":{"id":"c1","providerIdentifier":"linkedin","name":"Fireball"}}]}`))
		case r.URL.Path == "/api/public/v1/posts" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			_, _ = w.Write([]byte(`[{"postId":"p2"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		ch, err := c.Channels(ctx)
		if err != nil || len(ch) != 1 || ch[0].Kind != "linkedin" {
			t.Fatalf("Channels = %+v, %v", ch, err)
		}
	}
	if gets != 1 {
		t.Errorf("the app was asked %d times for the channels, want 1 (the rate limit is 30 an hour)", gets)
	}
	ps, err := c.Posts(ctx, time.Now(), time.Now().Add(time.Hour))
	if err != nil || len(ps) != 1 || ps[0].State != "QUEUE" || ps[0].Channel.Name != "Fireball" {
		t.Fatalf("Posts = %+v, %v", ps, err)
	}
	if err := c.Create(ctx, Draft{Content: "Hi", Channels: []Channel{{ID: "c1", Kind: "linkedin"}}, Now: true}); err != nil || created["type"] != "now" {
		t.Errorf("Create sent %v, %v", created, err)
	}
	if err := c.Create(ctx, Draft{Content: " ", Channels: []Channel{{ID: "c1"}}, Now: true}); err == nil {
		t.Error("an empty post is refused")
	}
	if err := c.Create(ctx, Draft{Content: "Hi", Channels: []Channel{{ID: "c1"}}}); err == nil {
		t.Error("a post with no time and not now is refused")
	}
	if err := c.Create(ctx, Draft{Content: "Hi"}); err == nil {
		t.Error("a post with no channel is refused")
	}
}

func TestARateLimitSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer srv.Close()
	if _, err := New(srv.URL, "k").Channels(context.Background()); err != ErrRateLimited {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}
