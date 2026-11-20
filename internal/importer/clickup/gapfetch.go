package clickup

// Reading what the first import never read: a task's attachments, every page
// of its comments and their threaded replies, and who logged the time.
//
// Docs: Get Task returns attachments, and Get Tasks does not
// (https://developer.clickup.com/reference/gettask); comments page by
// start and start_id, 25 at a time
// (https://developer.clickup.com/reference/gettaskcomments); time entries come
// from /team/{id}/time_entries, per assignee, and only a workspace owner or
// admin can read other people's
// (https://developer.clickup.com/reference/gettimeentrieswithinadaterange).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Attachment is one file on a task.
type Attachment struct {
	ID        string
	Title     string
	Extension string
	MimeType  string
	Size      int64
	URL       string
	At        *time.Time
	// Hidden and Deleted attachments are listed so the inventory can count
	// them, and are never downloaded.
	Hidden  bool
	Deleted bool
}

// Filename is the name to store the file under.
func (a Attachment) Filename() string {
	name := strings.TrimSpace(a.Title)
	if name == "" {
		name = "attachment-" + a.ID
	}
	if a.Extension != "" && !strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(a.Extension)) {
		name += "." + a.Extension
	}
	return name
}

// numString reads a JSON value ClickUp sends as either a number or a string.
type numString string

func (n *numString) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		s = ""
	}
	*n = numString(s)
	return nil
}

func (n numString) int64() int64 {
	v, _ := strconv.ParseInt(string(n), 10, 64)
	return v
}

// AttachmentsOn returns the files on a task.
func (c *Client) AttachmentsOn(ctx context.Context, taskID string) ([]Attachment, error) {
	var resp struct {
		Attachments []struct {
			ID        string    `json:"id"`
			Date      numString `json:"date"`
			Title     string    `json:"title"`
			Extension string    `json:"extension"`
			MimeType  string    `json:"mimetype"`
			Size      numString `json:"size"`
			URL       string    `json:"url"`
			Hidden    bool      `json:"hidden"`
			Deleted   bool      `json:"deleted"`
		} `json:"attachments"`
	}
	if err := c.get(ctx, "/task/"+taskID, &resp); err != nil {
		return nil, err
	}
	out := make([]Attachment, 0, len(resp.Attachments))
	for _, a := range resp.Attachments {
		out = append(out, Attachment{
			ID: a.ID, Title: a.Title, Extension: strings.TrimPrefix(a.Extension, "."),
			MimeType: a.MimeType, Size: a.Size.int64(), URL: a.URL,
			At: parseUnixMillis(string(a.Date)), Hidden: a.Hidden, Deleted: a.Deleted,
		})
	}
	return out, nil
}

// Download opens an attachment's bytes. The token goes only to ClickUp's own
// hosts: attachments are served from a CDN, and a credential has no business
// there.
func (c *Client) Download(ctx context.Context, rawURL string) (io.ReadCloser, int64, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, 0, "", fmt.Errorf("clickup: not a downloadable address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, "", err
	}
	if host := strings.ToLower(u.Hostname()); host == "clickup.com" || strings.HasSuffix(host, ".clickup.com") {
		req.Header.Set("Authorization", c.Token)
	}
	c.pace()
	hc := &http.Client{Timeout: 10 * time.Minute}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, "", fmt.Errorf("clickup: download returned %s", resp.Status)
	}
	return resp.Body, resp.ContentLength, resp.Header.Get("Content-Type"), nil
}

// CommentsAll returns every comment on a task, oldest first, with threaded
// replies. CommentsOn reads only the newest 25, which silently drops the start
// of any long conversation.
func (c *Client) CommentsAll(ctx context.Context, taskID string) ([]Comment, error) {
	type raw struct {
		ID          string    `json:"id"`
		CommentText string    `json:"comment_text"`
		Date        numString `json:"date"`
		ReplyCount  numString `json:"reply_count"`
		User        struct {
			ID       numString `json:"id"`
			Username string    `json:"username"`
			Email    string    `json:"email"`
		} `json:"user"`
	}
	conv := func(m raw, parent string) (Comment, bool) {
		body := strings.TrimSpace(m.CommentText)
		if body == "" {
			return Comment{}, false
		}
		who := m.User.Email
		if who == "" {
			who = m.User.Username
		}
		return Comment{ID: m.ID, Body: body, Author: strings.ToLower(strings.TrimSpace(who)),
			AuthorID: string(m.User.ID), At: parseUnixMillis(string(m.Date)), ParentID: parent}, true
	}

	var all []Comment
	seen := map[string]bool{}
	start, startID := "", ""
	for page := 0; page < 400; page++ {
		path := "/task/" + taskID + "/comment"
		if start != "" {
			path += "?start=" + url.QueryEscape(start) + "&start_id=" + url.QueryEscape(startID)
		}
		var resp struct {
			Comments []raw `json:"comments"`
		}
		if err := c.get(ctx, path, &resp); err != nil {
			return nil, err
		}
		if len(resp.Comments) == 0 {
			break
		}
		fresh := 0
		for _, m := range resp.Comments {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			fresh++
			if cm, ok := conv(m, ""); ok {
				all = append(all, cm)
			}
			if m.ReplyCount.int64() > 0 {
				var rr struct {
					Comments []raw `json:"comments"`
				}
				if err := c.get(ctx, "/comment/"+m.ID+"/reply", &rr); err == nil {
					for _, r := range rr.Comments {
						if seen[r.ID] {
							continue
						}
						seen[r.ID] = true
						if cm, ok := conv(r, m.ID); ok {
							all = append(all, cm)
						}
					}
				}
			}
		}
		last := resp.Comments[len(resp.Comments)-1]
		if fresh == 0 || len(resp.Comments) < 25 {
			break
		}
		start, startID = string(last.Date), last.ID
	}
	return all, nil
}

// TimeEntry is one block of time somebody logged against a task.
type TimeEntry struct {
	ID       string
	TaskID   string
	UserID   string
	Username string
	Email    string
	Start    time.Time
	Minutes  int
	Note     string
}

// TimeEntries returns the entries a user logged between two dates. Reading
// somebody else's needs a workspace owner or admin token; with any other the
// API returns the token owner's alone, and the caller can see that from who the
// entries belong to.
func (c *Client) TimeEntries(ctx context.Context, teamID, userID string, from, to time.Time) ([]TimeEntry, error) {
	q := url.Values{}
	q.Set("start_date", strconv.FormatInt(from.UnixMilli(), 10))
	q.Set("end_date", strconv.FormatInt(to.UnixMilli(), 10))
	q.Set("assignee", userID)
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Task *struct {
				ID string `json:"id"`
			} `json:"task"`
			User struct {
				ID       numString `json:"id"`
				Username string    `json:"username"`
				Email    string    `json:"email"`
			} `json:"user"`
			Start       numString `json:"start"`
			Duration    numString `json:"duration"`
			Description string    `json:"description"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/team/"+teamID+"/time_entries?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	var out []TimeEntry
	for _, e := range resp.Data {
		ms := e.Duration.int64()
		if ms <= 0 || e.Task == nil || e.Task.ID == "" {
			// A running timer has a negative duration and an entry with no task
			// belongs to nothing here.
			continue
		}
		start := parseUnixMillis(string(e.Start))
		if start == nil {
			continue
		}
		mins := int((time.Duration(ms) * time.Millisecond).Round(time.Minute) / time.Minute)
		if mins < 1 {
			mins = 1
		}
		out = append(out, TimeEntry{
			ID: e.ID, TaskID: e.Task.ID, UserID: string(e.User.ID), Username: e.User.Username,
			Email: strings.ToLower(strings.TrimSpace(e.User.Email)), Start: *start,
			Minutes: mins, Note: strings.TrimSpace(e.Description),
		})
	}
	return out, nil
}
