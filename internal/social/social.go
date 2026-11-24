// Package social is the Social area: the channels a company posts to, and the posts on them, read and write, through the
// public API of the social publishing app the installation runs. Hearth is the interface; the app keeps the channels'
// connections and publishes on schedule.
//
// The API allows about thirty requests an hour, so lists are cached for a short time and a post is the only write that is
// not retried.
package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrRateLimited means the app's hourly allowance for the key is used up.
var ErrRateLimited = errors.New("social: the social app's hourly request limit was reached; try again in a few minutes")

// Client talks to the public API with an API key.
type Client struct {
	base string
	key  string
	hc   *http.Client

	mu    sync.Mutex
	cache map[string]cached
	ttl   time.Duration
	now   func() time.Time
}

type cached struct {
	at   time.Time
	body []byte
}

// New builds a client. base is the app's address, with no trailing slash.
func New(base, apiKey string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), key: apiKey, hc: &http.Client{Timeout: 30 * time.Second},
		cache: map[string]cached{}, ttl: 45 * time.Second, now: time.Now}
}

// Enabled reports whether an address and a key are set.
func (c *Client) Enabled() bool { return c != nil && c.base != "" && c.key != "" }

// Channel is one connected account a post can go to.
type Channel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"identifier"`
	Picture  string `json:"picture"`
	Disabled bool   `json:"disabled"`
	Handle   string `json:"profile"`
}

// Post is one post on one channel.
type Post struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	PublishDate string `json:"publishDate"`
	URL         string `json:"releaseURL"`
	State       string `json:"state"` // QUEUE, PUBLISHED, ERROR, DRAFT
	Error       string `json:"error"`
	Channel     struct {
		ID      string `json:"id"`
		Kind    string `json:"providerIdentifier"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	} `json:"integration"`
}

// StateLabel is what a person reads for a post's state.
func (p Post) StateLabel() string {
	switch p.State {
	case "QUEUE":
		return "Scheduled"
	case "PUBLISHED":
		return "Published"
	case "ERROR":
		return "Failed"
	}
	return "Draft"
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any, useCache bool) error {
	u := c.base + "/api/public/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	if useCache {
		c.mu.Lock()
		hit, ok := c.cache[u]
		c.mu.Unlock()
		if ok && c.now().Sub(hit.at) < c.ttl {
			return json.Unmarshal(hit.body, out)
		}
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	// The key goes in the Authorization header on its own, with no scheme in front of it.
	req.Header.Set("Authorization", c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("social: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("social: the social app refused the key (%d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		var e struct {
			Message any `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("social: %d %v", resp.StatusCode, e.Message)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("social: unreadable answer: %w", err)
		}
	}
	if useCache {
		c.mu.Lock()
		c.cache[u] = cached{at: c.now(), body: raw}
		c.mu.Unlock()
	}
	return nil
}

func (c *Client) forget() {
	c.mu.Lock()
	c.cache = map[string]cached{}
	c.mu.Unlock()
}

// Channels lists the connected accounts.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	if err := c.do(ctx, http.MethodGet, "/integrations", nil, nil, &out, true); err != nil {
		return nil, err
	}
	return out, nil
}

// Posts lists the posts between two times.
func (c *Client) Posts(ctx context.Context, from, to time.Time) ([]Post, error) {
	var out struct {
		Posts []Post `json:"posts"`
	}
	q := url.Values{"startDate": {from.UTC().Format("2006-01-02T15:04:05.000Z")}, "endDate": {to.UTC().Format("2006-01-02T15:04:05.000Z")}}
	if err := c.do(ctx, http.MethodGet, "/posts", q, nil, &out, true); err != nil {
		return nil, err
	}
	return out.Posts, nil
}

// Draft is a post to make: the words, where it goes, and when.
type Draft struct {
	Content  string
	Channels []Channel // the channels it goes to, with their kind so the settings match
	When     string    // when to publish, ISO 8601, "" with Now
	Now      bool      // publish immediately
	Save     bool      // keep as a draft
	Images   []Image
}

// Image is a file already uploaded.
type Image struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// Create makes a post on each chosen channel, as a draft, scheduled, or published now.
func (c *Client) Create(ctx context.Context, d Draft) error {
	if strings.TrimSpace(d.Content) == "" {
		return errors.New("social: a post needs some words")
	}
	if len(d.Channels) == 0 {
		return errors.New("social: choose at least one channel")
	}
	kind, when := "schedule", d.When
	switch {
	case d.Save:
		kind = "draft"
	case d.Now:
		kind = "now"
	case strings.TrimSpace(when) == "":
		return errors.New("social: choose when to publish it")
	}
	if when == "" {
		when = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	posts := make([]map[string]any, 0, len(d.Channels))
	for _, ch := range d.Channels {
		img := d.Images
		if img == nil {
			img = []Image{}
		}
		posts = append(posts, map[string]any{
			"integration": map[string]string{"id": ch.ID},
			"value":       []map[string]any{{"content": d.Content, "image": img}},
			"settings":    map[string]string{"__type": ch.Kind},
		})
	}
	body := map[string]any{"type": kind, "date": when, "shortLink": false, "tags": []any{}, "posts": posts}
	err := c.do(ctx, http.MethodPost, "/posts", nil, body, nil, false)
	c.forget()
	return err
}

// Delete removes a post, and the other channels' copies of it that were made together.
func (c *Client) Delete(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/posts/"+url.PathEscape(id), nil, nil, nil, false)
	c.forget()
	return err
}

// Metric is one analytics line: a label and its values by day.
type Metric struct {
	Label string `json:"label"`
	Data  []struct {
		Total string `json:"total"`
		Date  string `json:"date"`
	} `json:"data"`
	Change float64 `json:"percentageChange"`
}

// Analytics reads a channel's numbers for the last days.
func (c *Client) Analytics(ctx context.Context, channelID string, days int) ([]Metric, error) {
	var out []Metric
	q := url.Values{"date": {fmt.Sprint(days)}}
	if err := c.do(ctx, http.MethodGet, "/analytics/"+url.PathEscape(channelID), q, nil, &out, true); err != nil {
		return nil, err
	}
	return out, nil
}

// SetStatus moves a scheduled post back to a draft, or a draft onto the schedule at the date it already holds. Postiz keeps
// the stored date either way; its public API has no call to change a post's date.
func (c *Client) SetStatus(ctx context.Context, id, status string) error {
	if status != "draft" && status != "schedule" {
		return errors.New("social: a post is either a draft or scheduled")
	}
	err := c.do(ctx, http.MethodPut, "/posts/"+url.PathEscape(id)+"/status", nil, map[string]string{"status": status}, nil, false)
	c.forget()
	return err
}

// DeleteGroup removes every post made together, by the group they share.
func (c *Client) DeleteGroup(ctx context.Context, group string) error {
	err := c.do(ctx, http.MethodDelete, "/posts/group/"+url.PathEscape(group), nil, nil, nil, false)
	c.forget()
	return err
}

// PostAnalytics reads the numbers of one published post for the last days (likes, comments, shares, impressions, by platform).
func (c *Client) PostAnalytics(ctx context.Context, postID string, days int) ([]Metric, error) {
	var out []Metric
	q := url.Values{"date": {fmt.Sprint(days)}}
	if err := c.do(ctx, http.MethodGet, "/analytics/post/"+url.PathEscape(postID), q, nil, &out, true); err != nil {
		return nil, err
	}
	return out, nil
}

// Notification is one line of what the publishing app has to say: a post published, a post failed.
type Notification struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Link      string `json:"link"`
	CreatedAt string `json:"createdAt"`
}

// Notifications reads the most recent notifications (a hundred a page, newest first).
func (c *Client) Notifications(ctx context.Context, page int) ([]Notification, error) {
	var out struct {
		Notifications []Notification `json:"notifications"`
	}
	if err := c.do(ctx, http.MethodGet, "/notifications", url.Values{"page": {fmt.Sprint(page)}}, nil, &out, true); err != nil {
		return nil, err
	}
	return out.Notifications, nil
}

// Media is one file in the media library.
type Media struct {
	ID           string `json:"id"`
	OriginalName string `json:"originalName"`
	Path         string `json:"path"`
	CreatedAt    string `json:"createdAt"`
}

// MediaLibrary lists the uploaded files, newest first, eighteen a page, narrowed by a name.
func (c *Client) MediaLibrary(ctx context.Context, search string, page int) ([]Media, error) {
	var out struct {
		Results []Media `json:"results"`
	}
	q := url.Values{"page": {fmt.Sprint(max(page, 1))}}
	if search != "" {
		q.Set("search", search)
	}
	if err := c.do(ctx, http.MethodGet, "/media", q, nil, &out, true); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// UploadFromURL puts a file that is already on the web into the media library, and returns it ready to attach to a post.
func (c *Client) UploadFromURL(ctx context.Context, address string) (*Image, error) {
	if u, err := url.Parse(strings.TrimSpace(address)); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("social: that is not a web address")
	}
	var out Image
	err := c.do(ctx, http.MethodPost, "/upload-from-url", nil, map[string]string{"url": strings.TrimSpace(address)}, &out, false)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
