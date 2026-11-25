package web

// The team's parts of the dashboard: one section per tool the installation runs (email marketing, social, forms), each
// a handful of numbers read from the tool itself, for the people who work marketing and for super admins.
//
// Who sees it is the Marketing or Social Media team, or a super admin, so Danny and Nidia get the recaps and Patrick,
// Yessid and Eric get the aggregate; everybody else gets none of it. A tool that is not configured has no section, and a
// tool that does not answer shows a line saying so instead of numbers, so one slow app never takes the page down.
//
// The tools rate limit (Postiz allows about thirty requests an hour), so the whole panel is read at most once every five
// minutes for the organisation and shared by whoever asks.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/forms"
)

// teamSection is one tool's part of the panel.
type teamSection struct {
	Key    string
	Title  string
	Href   string
	Tiles  []statTile
	Rows   []teamRow
	Notice string
}

// teamRow is one line under a section's numbers: an email, a post, a form.
type teamRow struct {
	Title  string
	Detail string
	Href   string
}

// teamPanel is everything the dashboard's team parts show.
type teamPanel struct {
	Sections []teamSection
}

type panelCache struct {
	mu   sync.Mutex
	at   time.Time
	org  string
	data *teamPanel
}

const panelTTL = 5 * time.Minute

// worksMarketing reports whether the viewer sees the team parts: a super admin, or a member of the Marketing or Social
// Media team.
func (s *Server) worksMarketing(ctx context.Context, id *auth.Identity) bool {
	if id.Subjects.Super() {
		return true
	}
	if s.teams == nil {
		return false
	}
	list, err := s.teams.List(ctx, id.OrgID)
	if err != nil {
		return false
	}
	keys := map[string]bool{}
	for _, k := range id.Subjects.Keys() {
		keys[k] = true
	}
	for _, t := range list {
		n := strings.ToLower(strings.TrimSpace(t.Name))
		if (n == "marketing" || n == "social media") && keys[core.Subject{Kind: core.SubjectTeam, ID: t.ID}.Key()] {
			return true
		}
	}
	return false
}

// teamParts builds the panel for a viewer who may see it, nil for everybody else and for an installation with none of the tools.
func (s *Server) teamParts(ctx context.Context, id *auth.Identity) *teamPanel {
	if s.marketing == nil && s.social == nil && s.forms == nil {
		return nil
	}
	if !s.worksMarketing(ctx, id) {
		return nil
	}
	c := &s.panel
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil && c.org == id.OrgID && time.Since(c.at) < panelTTL {
		return c.data
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p := &teamPanel{}
	if s.marketing != nil {
		p.Sections = append(p.Sections, s.marketingSection(rctx))
	}
	if s.social != nil {
		p.Sections = append(p.Sections, s.socialSection(rctx))
	}
	if s.forms != nil {
		p.Sections = append(p.Sections, s.formsSection(rctx))
	}
	c.data, c.org, c.at = p, id.OrgID, time.Now()
	return p
}

// num writes a count with thousands separators, 12,408.
func num(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func pct(n, of int64) string {
	if of <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", n*100/of)
}

func (s *Server) marketingSection(ctx context.Context) teamSection {
	sec := teamSection{Key: "marketing", Title: "Email marketing", Href: "/marketing"}
	emails, total, err := s.marketing.Emails(ctx, "", 200, 0)
	if err != nil {
		sec.Notice = "Email marketing did not answer: " + areaErr(err)
		return sec
	}
	var sent, read int64
	live := 0
	for _, e := range emails {
		sent += e.Sent
		read += e.Read
		if e.Published {
			live++
		}
	}
	contacts, _ := s.marketing.ContactTotal(ctx)
	_, campaigns, _ := s.marketing.Campaigns(ctx, "", 1, 0)
	segs, _ := s.marketing.SegmentList(ctx)
	sec.Tiles = []statTile{
		{Label: "Emails sent", Value: num(sent), Period: "all time", Href: "/marketing"},
		{Label: "Opened", Value: pct(read, sent), Detail: num(read) + " opens", Period: "of what was sent", Href: "/marketing"},
		{Label: "Contacts", Value: num(int64(contacts)), Period: fmt.Sprintf("in %d segments", len(segs)), Href: "/marketing?tab=segments"},
		{Label: "Emails and campaigns", Value: fmt.Sprint(total), Detail: fmt.Sprintf("%d live", live), Period: fmt.Sprintf("%d campaigns", campaigns), Href: "/marketing?tab=campaigns"},
	}
	// The five emails that went to the most people.
	sort.SliceStable(emails, func(i, j int) bool { return emails[i].Sent > emails[j].Sent })
	for _, e := range emails {
		if e.Sent == 0 || len(sec.Rows) >= 5 {
			continue
		}
		sec.Rows = append(sec.Rows, teamRow{Title: e.Name, Detail: num(e.Sent) + " sent, " + pct(e.Read, e.Sent) + " opened",
			Href: fmt.Sprintf("/marketing?edit=%d", e.ID)})
	}
	return sec
}

func (s *Server) socialSection(ctx context.Context) teamSection {
	sec := teamSection{Key: "social", Title: "Social", Href: "/social"}
	now := time.Now()
	chans, err := s.social.Channels(ctx)
	if err != nil {
		sec.Notice = "Social did not answer: " + areaErr(err)
		return sec
	}
	week := now.AddDate(0, 0, 7)
	ahead, err := s.social.Posts(ctx, now, week)
	if err != nil {
		sec.Notice = "Social did not answer: " + areaErr(err)
		return sec
	}
	back, _ := s.social.Posts(ctx, now.AddDate(0, 0, -7), now)
	active := 0
	for _, c := range chans {
		if !c.Disabled {
			active++
		}
	}
	var queued, published, failed int
	for _, p := range ahead {
		if p.State == "QUEUE" {
			queued++
		}
	}
	for _, p := range back {
		switch p.State {
		case "PUBLISHED":
			published++
		case "ERROR":
			failed++
		}
	}
	sec.Tiles = []statTile{
		{Label: "Scheduled", Value: fmt.Sprint(queued), Period: "next 7 days", Href: "/social"},
		{Label: "Published", Value: fmt.Sprint(published), Period: "last 7 days", Href: "/social"},
		{Label: "Failed", Value: fmt.Sprint(failed), Period: "last 7 days", Href: "/social", Tone: toneIf(failed > 0)},
		{Label: "Channels", Value: fmt.Sprint(active), Period: fmt.Sprintf("%d connected", len(chans)), Href: "/social"},
	}
	for _, p := range ahead {
		if p.State != "QUEUE" || len(sec.Rows) >= 5 {
			continue
		}
		text := strings.Join(strings.Fields(p.Content), " ")
		if len(text) > 90 {
			text = text[:90] + "..."
		}
		sec.Rows = append(sec.Rows, teamRow{Title: text, Detail: p.Channel.Name + ", " + p.PublishDate, Href: "/social"})
	}
	return sec
}

func (s *Server) formsSection(ctx context.Context) teamSection {
	sec := teamSection{Key: "forms", Title: "Forms", Href: "/forms"}
	all, err := s.forms.Forms(ctx)
	if err != nil {
		sec.Notice = "Forms did not answer: " + areaErr(err)
		return sec
	}
	var live, drafts, closed int
	for _, f := range all {
		switch f.Status {
		case forms.InProgress, forms.Scheduled:
			live++
		case forms.Completed:
			closed++
		default:
			drafts++
		}
	}
	responses, finished := 0, 0
	for i, f := range all {
		if f.Status != forms.InProgress || i > 8 {
			continue
		}
		r, err := s.forms.Results(ctx, f.ID)
		if err != nil {
			continue
		}
		responses += r.Total
		finished += r.Finished
		sec.Rows = append(sec.Rows, teamRow{Title: f.Name, Detail: fmt.Sprintf("%d answers, %d finished", r.Total, r.Finished), Href: "/forms"})
	}
	if len(sec.Rows) > 5 {
		sec.Rows = sec.Rows[:5]
	}
	sec.Tiles = []statTile{
		{Label: "Live forms", Value: fmt.Sprint(live), Period: "taking answers or scheduled", Href: "/forms"},
		{Label: "Answers", Value: fmt.Sprint(responses), Detail: fmt.Sprintf("%d finished", finished), Period: "on live forms", Href: "/forms"},
		{Label: "Drafts", Value: fmt.Sprint(drafts), Period: "not published", Href: "/forms"},
		{Label: "Closed", Value: fmt.Sprint(closed), Period: "no longer taking answers", Href: "/forms"},
	}
	return sec
}
