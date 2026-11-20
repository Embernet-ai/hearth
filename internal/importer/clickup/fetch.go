package clickup

// Pulling straight from the API, instead of waiting for somebody to click
// Export.
//
// The file path stays -- an export somebody already has is the fastest way to
// try this, and it works with no credential. But it cannot be the only path.
// An export is a photograph: it is stale the moment it is taken, it covers
// whatever the person happened to select, and re-running the migration means
// asking them to do it again. A token walks the whole workspace and can be run
// twice.
//
// Read-only by construction: every method here is a GET, and there is no
// function in this file that writes to ClickUp. A migration that can modify the
// system it is migrating away from is a migration nobody is willing to rehearse.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// api is ClickUp's v2 base. v3 exists and covers less.
const api = "https://api.clickup.com/api/v2"

// Client reads a ClickUp workspace.
type Client struct {
	Token string
	HTTP  *http.Client
	// Archived includes archived folders and lists in the walk.
	Archived bool
	// MinInterval spaces requests out. ClickUp allows 100 a minute, and a pass
	// that reads every task's comments and attachments sits right on it; with
	// 700ms between calls it stays under without leaning on 429 retries.
	MinInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

// pace waits until at least MinInterval has passed since the last request.
func (c *Client) pace() {
	if c.MinInterval <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait := c.MinInterval - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

// NewClient returns a client with a timeout that is generous rather than
// absent. A migration reading 167 lists behind a hung connection with no
// deadline is a job that never finishes and never says why.
func NewClient(token string) *Client {
	return &Client{Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// List is one ClickUp list, carrying the space it belongs to so the caller can
// decide what its tasks become. The space is the unit people organise by, and
// it is what a per-space kind mapping keys on.
type List struct {
	ID     string
	Name   string
	Space  string
	Folder string
	Tasks  int
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	// The docs API is v3 while everything else is v2, and they differ only in
	// that path segment. A caller asking for "/v3/..." gets it verbatim rather
	// than glued onto the v2 base.
	full := api + path
	if strings.HasPrefix(path, "/v3/") {
		full = "https://api.clickup.com/api" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.Token)
	c.pace()

	// One retry, and only for 429. ClickUp rate limits per minute and a
	// workspace walk sits right on that boundary; everything else that fails is
	// reported rather than hidden behind a retry loop that turns a wrong token
	// into a four-minute wait.
	for attempt := 0; ; attempt++ {
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("clickup: GET %s: %w", path, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 10 * time.Second):
			}
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("clickup: the token was refused. A personal token " +
				"starts pk_ and is made at Settings, Apps, API Token")
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("clickup: GET %s returned %s", path, resp.Status)
		}
		return json.NewDecoder(resp.Body).Decode(into)
	}
}

// Lists walks the workspace and returns every list in it.
//
// Folderless lists are fetched separately because ClickUp models them
// separately, and a walk that only reads folders silently misses them. In the
// workspace this was written against that would have lost whole spaces.
// Lists walks the workspace.
//
// When Archived is set it walks TWICE and merges, and that is not tidiness:
// ClickUp's `archived` parameter is a FILTER, not an inclusion flag.
// archived=true returns ONLY archived items, so a single walk with it set
// returns no live lists at all -- the first run of this reported "0 lists, 0
// tasks" against a workspace of 167 and would have imported nothing while
// looking like it succeeded.
//
// Deduplicated by list id, because a list can appear in both passes if it is
// archived between them.
func (c *Client) Lists(ctx context.Context) ([]List, error) {
	live, err := c.lists(ctx, false)
	if err != nil || !c.Archived {
		return live, err
	}
	old, err := c.lists(ctx, true)
	if err != nil {
		// The live walk succeeded, so return it rather than losing everything
		// because the archive could not be read.
		return live, nil
	}
	seen := make(map[string]bool, len(live))
	for _, l := range live {
		seen[l.ID] = true
	}
	for _, l := range old {
		if !seen[l.ID] {
			live = append(live, l)
		}
	}
	return live, nil
}

func (c *Client) lists(ctx context.Context, archived bool) ([]List, error) {
	var teams struct {
		Teams []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"teams"`
	}
	if err := c.get(ctx, "/team", &teams); err != nil {
		return nil, err
	}

	type listJSON struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		TaskCount int    `json:"task_count"`
	}

	var out []List
	for _, t := range teams.Teams {
		var spaces struct {
			Spaces []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"spaces"`
		}
		if err := c.get(ctx, "/team/"+t.ID+"/space?archived="+boolParam(archived), &spaces); err != nil {
			return nil, err
		}
		for _, sp := range spaces.Spaces {
			var folders struct {
				Folders []struct {
					ID    string     `json:"id"`
					Name  string     `json:"name"`
					Lists []listJSON `json:"lists"`
				} `json:"folders"`
			}
			if err := c.get(ctx, "/space/"+sp.ID+"/folder?archived="+boolParam(archived), &folders); err != nil {
				return nil, err
			}
			for _, f := range folders.Folders {
				for _, l := range f.Lists {
					out = append(out, List{ID: l.ID, Name: l.Name, Space: sp.Name,
						Folder: f.Name, Tasks: l.TaskCount})
				}
			}

			var loose struct {
				Lists []listJSON `json:"lists"`
			}
			if err := c.get(ctx, "/space/"+sp.ID+"/list?archived="+boolParam(archived), &loose); err != nil {
				return nil, err
			}
			for _, l := range loose.Lists {
				out = append(out, List{ID: l.ID, Name: l.Name, Space: sp.Name, Tasks: l.TaskCount})
			}
		}
	}
	return out, nil
}

// TasksIn returns every task in a list, closed ones and subtasks included.
//
// include_closed matters more than it looks. The default omits closed tasks, so
// a migration run without it imports the open work and quietly loses the
// history -- which is the half that makes the rest make sense.
func (c *Client) TasksIn(ctx context.Context, listID string) ([]Task, error) {
	var all []Task
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("include_closed", "true")
		q.Set("subtasks", "true")
		q.Set("include_markdown_description", "true")
		q.Set("page", fmt.Sprint(page))

		var body json.RawMessage
		if err := c.get(ctx, "/list/"+listID+"/task?"+q.Encode(), &body); err != nil {
			return nil, err
		}
		tasks, err := Parse(strings.NewReader(string(body)))
		if err != nil {
			// An empty page parses as no tasks rather than an error shape, so
			// reaching here means the response was genuinely unexpected.
			return all, nil
		}
		if len(tasks) == 0 {
			return all, nil
		}
		all = append(all, tasks...)

		// ClickUp pages at 100 and reports last_page, but the flag has been
		// unreliable across export shapes; a short page is the dependable
		// signal that there is nothing after it.
		if len(tasks) < 100 {
			return all, nil
		}
		if page > 200 {
			// 20,000 tasks in one list is not a list, it is a runaway loop.
			return all, fmt.Errorf("clickup: list %s did not stop paginating", listID)
		}
	}
}

// Members returns everybody in the workspace.
//
// Fetched so assignees have somebody to resolve to. A workspace with 23 members
// importing into a Hearth with one account leaves 67% of tasks unassigned, and
// "who is this for" is the first question anybody asks of a queue.
func (c *Client) Members(ctx context.Context) ([]Person, error) {
	var teams struct {
		Teams []struct {
			Members []struct {
				User struct {
					ID       int64  `json:"id"`
					Username string `json:"username"`
					Email    string `json:"email"`
				} `json:"user"`
			} `json:"members"`
		} `json:"teams"`
	}
	if err := c.get(ctx, "/team", &teams); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []Person
	for _, t := range teams.Teams {
		for _, m := range t.Members {
			email := strings.ToLower(strings.TrimSpace(m.User.Email))
			// No address, nothing to match on and nothing to adopt later.
			// Skipped rather than invented.
			if email == "" || seen[email] {
				continue
			}
			seen[email] = true
			out = append(out, Person{
				ExternalID:  fmt.Sprint(m.User.ID),
				DisplayName: strings.TrimSpace(m.User.Username),
				Email:       email,
			})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Docs
// ---------------------------------------------------------------------------

// Doc is a ClickUp document: a page of prose rather than a unit of work.
//
// These are the knowledge base -- "Getting Started Guide", "Embedded Devices",
// "Knowledge Base Guide" -- and they live behind a different API from tasks, in
// a different version of it, which is why an import that only walks lists
// misses them entirely.
type Doc struct {
	ID        string
	Name      string
	Content   string
	CreatedAt *time.Time
	UpdatedAt *time.Time
	Creator   string
}

// Docs returns every document in the workspace, with its pages already joined.
//
// The page limit is 50 and this is not documented anywhere useful: asking for
// 100 returns an empty list rather than an error or a clamped page, so a naive
// "give me everything" reads as "there are no documents here" and the whole
// knowledge base is silently skipped. Verified against this workspace.
func (c *Client) Docs(ctx context.Context, teamID string) ([]Doc, error) {
	var out []Doc
	cursor := ""
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("limit", "50")
		if cursor != "" {
			q.Set("next_cursor", cursor)
		}
		var listing struct {
			Docs []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DateCreated int64  `json:"date_created"`
				DateUpdated int64  `json:"date_updated"`
				Deleted     bool   `json:"deleted"`
			} `json:"docs"`
			NextCursor string `json:"next_cursor"`
		}
		if err := c.get(ctx, "/v3/workspaces/"+teamID+"/docs?"+q.Encode(), &listing); err != nil {
			return out, err
		}
		for _, d := range listing.Docs {
			if d.Deleted {
				continue
			}
			doc := Doc{ID: d.ID, Name: strings.TrimSpace(d.Name)}
			doc.CreatedAt = millisToTime(d.DateCreated)
			doc.UpdatedAt = millisToTime(d.DateUpdated)
			body, creator, err := c.docPages(ctx, teamID, d.ID)
			if err != nil {
				// A document whose pages cannot be read still has a name worth
				// keeping; the body arrives empty rather than the doc missing.
				body = ""
			}
			doc.Content, doc.Creator = body, creator
			out = append(out, doc)
		}
		if listing.NextCursor == "" || len(listing.Docs) == 0 {
			return out, nil
		}
		cursor = listing.NextCursor
		if page > 200 {
			return out, fmt.Errorf("clickup: docs did not stop paginating")
		}
	}
}

// docPages joins a document's pages into one body.
//
// ClickUp nests pages within a doc and Hearth has one body per document, so
// they are concatenated with their titles as headings. The content is already
// markdown, which is what body_format has always said, so nothing is converted.
func (c *Client) docPages(ctx context.Context, teamID, docID string) (string, string, error) {
	var pages []struct {
		Name      string `json:"name"`
		Content   string `json:"content"`
		CreatorID int64  `json:"creator_id"`
		Archived  bool   `json:"archived"`
		Deleted   bool   `json:"deleted"`
	}
	if err := c.get(ctx, "/v3/workspaces/"+teamID+"/docs/"+docID+"/pages", &pages); err != nil {
		return "", "", err
	}
	var b strings.Builder
	creator := ""
	for _, p := range pages {
		if p.Deleted || p.Archived {
			continue
		}
		if creator == "" && p.CreatorID != 0 {
			creator = fmt.Sprint(p.CreatorID)
		}
		if n := strings.TrimSpace(p.Name); n != "" && len(pages) > 1 {
			b.WriteString("## " + n + "\n\n")
		}
		b.WriteString(strings.TrimSpace(p.Content))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String()), creator, nil
}

func millisToTime(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// TeamID returns the workspace's id.
//
// The docs API needs it in the path where the task API infers it from the
// token, so it is fetched once rather than threaded through every caller.
// Refuses on more than one, rather than silently picking the first: importing
// the wrong workspace is not something to discover afterwards.
func (c *Client) TeamID(ctx context.Context) (string, error) {
	var teams struct {
		Teams []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"teams"`
	}
	if err := c.get(ctx, "/team", &teams); err != nil {
		return "", err
	}
	switch len(teams.Teams) {
	case 0:
		return "", fmt.Errorf("clickup: this token can see no workspace")
	case 1:
		return teams.Teams[0].ID, nil
	default:
		names := make([]string, 0, len(teams.Teams))
		for _, t := range teams.Teams {
			names = append(names, t.Name)
		}
		return "", fmt.Errorf("clickup: this token can see %d workspaces (%s); "+
			"refusing to guess which to import", len(teams.Teams), strings.Join(names, ", "))
	}
}

// Comment is one thing somebody said on a task.
type Comment struct {
	ID       string
	Body     string
	Author   string
	AuthorID string
	At       *time.Time
	// ParentID is set on a threaded reply.
	ParentID string
}

// CommentsOn returns a task's comments, replies included.
//
// One request per task, which is the whole cost of this: a workspace of 1,132
// tasks is 1,132 requests against a limit of 100 a minute. That is why it is
// optional and why it is the last thing the import does -- so a run that is
// killed part way through has already brought the work across, and only the
// discussion is missing.
func (c *Client) CommentsOn(ctx context.Context, taskID string) ([]Comment, error) {
	var resp struct {
		Comments []struct {
			ID          string `json:"id"`
			CommentText string `json:"comment_text"`
			Date        string `json:"date"`
			User        struct {
				Username string `json:"username"`
				Email    string `json:"email"`
			} `json:"user"`
			Resolved bool   `json:"resolved"`
			Parent   string `json:"parent"`
		} `json:"comments"`
	}
	if err := c.get(ctx, "/task/"+taskID+"/comment", &resp); err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(resp.Comments))
	for _, m := range resp.Comments {
		body := strings.TrimSpace(m.CommentText)
		if body == "" {
			continue
		}
		who := m.User.Email
		if who == "" {
			who = m.User.Username
		}
		out = append(out, Comment{
			ID: m.ID, Body: body, Author: strings.ToLower(strings.TrimSpace(who)),
			At: parseUnixMillis(m.Date), ParentID: m.Parent,
		})
	}
	return out, nil
}

// boolParam renders a query flag ClickUp understands.
func boolParam(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
