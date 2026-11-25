package social

import (
	"context"
	"encoding/json"
	"errors"
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

// The calls the Postiz public API documents: change a post's status, delete by group, a post's numbers, notifications, the media
// library, and a file from a web address. Each is the method and path the docs give, with the body they ask for.
func TestPostizStatusGroupAnalyticsNotificationsAndMedia(t *testing.T) {
	var calls []string
	var statusBody, uploadBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/api/public/v1/posts/p1/status":
			_ = json.Unmarshal(b, &statusBody)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/api/public/v1/analytics/post/p1":
			_, _ = w.Write([]byte(`[{"label":"Likes","data":[{"total":"4","date":"2026-11-20"}],"percentageChange":10}]`))
		case r.URL.Path == "/api/public/v1/notifications":
			_, _ = w.Write([]byte(`{"notifications":[{"id":"n1","content":"Your post to X was published successfully","createdAt":"2026-11-20T10:00:00.000Z"}],"total":1}`))
		case r.URL.Path == "/api/public/v1/media":
			_, _ = w.Write([]byte(`{"pages":1,"results":[{"id":"m1","originalName":"banner.png","path":"https://u.example/banner.png"}]}`))
		case r.URL.Path == "/api/public/v1/upload-from-url":
			_ = json.Unmarshal(b, &uploadBody)
			_, _ = w.Write([]byte(`{"id":"m2","path":"https://u.example/x.png"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	ctx := context.Background()
	if err := c.SetStatus(ctx, "p1", "draft"); err != nil || statusBody["status"] != "draft" {
		t.Fatalf("SetStatus: %v %v", err, statusBody)
	}
	if err := c.SetStatus(ctx, "p1", "published"); err == nil {
		t.Error("a status Postiz does not have was sent")
	}
	if err := c.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	if m, err := c.PostAnalytics(ctx, "p1", 7); err != nil || len(m) != 1 || m[0].Label != "Likes" {
		t.Fatalf("PostAnalytics: %+v %v", m, err)
	}
	if n, err := c.Notifications(ctx, 0); err != nil || len(n) != 1 || n[0].ID != "n1" {
		t.Fatalf("Notifications: %+v %v", n, err)
	}
	if m, err := c.MediaLibrary(ctx, "banner", 1); err != nil || len(m) != 1 || m[0].OriginalName != "banner.png" {
		t.Fatalf("MediaLibrary: %+v %v", m, err)
	}
	if img, err := c.UploadFromURL(ctx, "https://example.com/x.png"); err != nil || img.ID != "m2" || uploadBody["url"] != "https://example.com/x.png" {
		t.Fatalf("UploadFromURL: %+v %v %v", img, err, uploadBody)
	}
	if _, err := c.UploadFromURL(ctx, "ftp://nope"); err == nil {
		t.Error("a non web address was accepted")
	}
	want := []string{"PUT /api/public/v1/posts/p1/status?", "DELETE /api/public/v1/posts/group/g1?", "GET /api/public/v1/analytics/post/p1?date=7", "GET /api/public/v1/notifications?page=0",
		"GET /api/public/v1/media?page=1&search=banner", "POST /api/public/v1/upload-from-url?"}
	for i, w := range want {
		if i >= len(calls) || calls[i] != w {
			t.Errorf("call %d = %v, want %q", i, calls, w)
			break
		}
	}
}

// Live 2026-11-25: Fireball's Postiz answers the media library with 404 "Cannot GET /public/v1/media".
func TestAPublishingAppWithNoMediaLibrarySaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Cannot GET /public/v1/media?page=1","error":"Not Found","statusCode":404}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	if _, err := c.MediaLibrary(context.Background(), "", 1); !errors.Is(err, ErrNotOffered) {
		t.Errorf("MediaLibrary err = %v, want ErrNotOffered", err)
	}
	if _, err := c.UploadFromURL(context.Background(), "https://example.com/a.png"); !errors.Is(err, ErrNotOffered) {
		t.Errorf("UploadFromURL err = %v, want ErrNotOffered", err)
	}
}
