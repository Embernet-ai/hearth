package web

// The Marketing, Forms and Social areas, and the Tools launcher.
//
// Hearth is the interface and each area calls the API of the app the installation runs for it. Everything is read and
// write: list, create, edit, publish or schedule, delete, and the results. An area whose app is not set up still opens, and
// says what to set, so the rail is the same everywhere and nothing is hidden behind a switch.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/forms"
	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/social"
	"github.com/Embernet-ai/hearth/internal/tools"
)

const areaTimeout = 45 * time.Second

func (s *Server) areaCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), areaTimeout)
}

// back redirects to an area with a note.
func areaBack(w http.ResponseWriter, r *http.Request, path string, q url.Values, msg string, err error) {
	if q == nil {
		q = url.Values{}
	}
	if err != nil {
		q.Set("notice", areaErr(err))
	} else if msg != "" {
		q.Set("notice", msg)
	}
	to := path
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func areaErr(err error) string {
	switch {
	case errors.Is(err, social.ErrRateLimited):
		return err.Error()
	case errors.Is(err, forms.ErrNotFound):
		return "That form is not there any more."
	}
	var api *mautic.APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return "That is not there any more."
	}
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(err.Error(), "mautic: "), "forms: "), "social: ")
}

func intOf(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }

func idsOf(vals []string) []int64 {
	var out []int64
	for _, v := range vals {
		if n := intOf(v); n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// ---- Tools ----------------------------------------------------------------------------------------------------------

// ToolsView is the Tools page.
type ToolsView struct {
	Tools []tools.Tool
}

func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "tools", page{Title: "Tools", ToolsList: &ToolsView{Tools: s.tools}})
}

// ---- Marketing ------------------------------------------------------------------------------------------------------

// MarketingView is the Marketing page: emails, campaigns and segments, one tab at a time.
type MarketingView struct {
	Configured bool
	Tab        string
	Notice     string
	Search     string
	Emails     []mautic.Email
	Campaigns  []mautic.Campaign
	Segments   []mautic.Segment
	// Edit is the email or campaign being changed, and EditSegment the segment; New opens the form for a new one.
	Email    *mautic.Email
	Campaign *mautic.Campaign
	Segment  *mautic.Segment
	New      bool
	Total    int
}

func (s *Server) getMarketing(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := &MarketingView{Configured: s.marketing != nil, Tab: q.Get("tab"), Notice: q.Get("notice"), Search: q.Get("q"), New: q.Get("new") != ""}
	if v.Tab != "campaigns" && v.Tab != "segments" {
		v.Tab = "emails"
	}
	if s.marketing != nil {
		ctx, cancel := s.areaCtx(r)
		defer cancel()
		id := intOf(q.Get("edit"))
		var err error
		// Segments are needed on every tab: an email and a campaign both choose them.
		if v.Segments, err = s.marketing.SegmentList(ctx); err != nil {
			v.Notice = areaErr(err)
		}
		switch v.Tab {
		case "emails":
			if id > 0 {
				v.Email, err = s.marketing.GetEmail(ctx, id)
			} else if !v.New {
				v.Emails, v.Total, err = s.marketing.Emails(ctx, v.Search, 50, 0)
			}
		case "campaigns":
			if id > 0 {
				var all []mautic.Campaign
				all, _, err = s.marketing.Campaigns(ctx, "", 200, 0)
				for i := range all {
					if all[i].ID == id {
						all[i].Contacts, _ = s.marketing.CampaignContacts(ctx, id)
						v.Campaign = &all[i]
					}
				}
			} else if !v.New {
				v.Campaigns, v.Total, err = s.marketing.Campaigns(ctx, v.Search, 50, 0)
				for i := range v.Campaigns {
					v.Campaigns[i].Contacts, _ = s.marketing.CampaignContacts(ctx, v.Campaigns[i].ID)
				}
			}
			if v.Emails == nil {
				v.Emails, _, _ = s.marketing.Emails(ctx, "", 100, 0)
			}
		case "segments":
			if id > 0 {
				for i := range v.Segments {
					if v.Segments[i].ID == id {
						v.Segment = &v.Segments[i]
					}
				}
			}
		}
		if err != nil {
			v.Notice = areaErr(err)
		}
	}
	s.render(w, r, "marketing", page{Title: "Marketing", Marketing: v})
}

func (s *Server) marketingAct(w http.ResponseWriter, r *http.Request, tab, done string, act func(ctx context.Context) error) {
	if s.marketing == nil {
		s.fail(w, r, http.StatusNotFound, "Marketing is not set up on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	areaBack(w, r, "/marketing", url.Values{"tab": {tab}}, done, act(ctx))
}

// publishUp reads a schedule from the form: a date and time in the person's own terms, kept as the app wants them.
func publishUp(r *http.Request, field string) string {
	v := strings.TrimSpace(r.FormValue(field))
	if v == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02T15:04", v)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05+00:00")
}

func (s *Server) postMarketingEmail(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "emails", "Saved.", func(ctx context.Context) error {
		in := mautic.EmailInput{Name: strings.TrimSpace(r.FormValue("name")), Subject: strings.TrimSpace(r.FormValue("subject")),
			Type: r.FormValue("type"), HTML: r.FormValue("html"), Published: r.FormValue("published") == "on",
			PublishUp: publishUp(r, "publish_up"), Segments: idsOf(r.Form["segment"]),
			FromName: strings.TrimSpace(r.FormValue("from_name")), FromAddress: strings.TrimSpace(r.FormValue("from_address"))}
		if in.Name == "" || in.Subject == "" {
			return errors.New("an email needs a name and a subject")
		}
		_, err := s.marketing.SaveEmail(ctx, intOf(r.FormValue("id")), in)
		return err
	})
}

func (s *Server) postMarketingEmailPublish(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "emails", "Done.", func(ctx context.Context) error {
		return s.marketing.SetEmailPublished(ctx, intOf(r.PathValue("id")), r.FormValue("on") == "on")
	})
}

func (s *Server) postMarketingEmailSend(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "emails", "Sent to its segments.", func(ctx context.Context) error {
		return s.marketing.SendEmail(ctx, intOf(r.PathValue("id")))
	})
}

func (s *Server) postMarketingEmailDelete(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "emails", "Deleted.", func(ctx context.Context) error {
		return s.marketing.DeleteEmail(ctx, intOf(r.PathValue("id")))
	})
}

func (s *Server) postMarketingCampaign(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "campaigns", "Saved.", func(ctx context.Context) error {
		in := mautic.CampaignInput{Name: strings.TrimSpace(r.FormValue("name")), Description: strings.TrimSpace(r.FormValue("description")),
			Published: r.FormValue("published") == "on", PublishUp: publishUp(r, "publish_up"), PublishDown: publishUp(r, "publish_down"),
			Segments: idsOf(r.Form["segment"]), EmailID: intOf(r.FormValue("email")), DelayDays: int(intOf(r.FormValue("delay_days")))}
		if in.Name == "" {
			return errors.New("a campaign needs a name")
		}
		if intOf(r.FormValue("id")) == 0 && (in.EmailID == 0 || len(in.Segments) == 0) {
			return errors.New("a new campaign needs the email it sends and the segments it sends to")
		}
		_, err := s.marketing.SaveCampaign(ctx, intOf(r.FormValue("id")), in)
		return err
	})
}

func (s *Server) postMarketingCampaignPublish(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "campaigns", "Done.", func(ctx context.Context) error {
		return s.marketing.SetCampaignPublished(ctx, intOf(r.PathValue("id")), r.FormValue("on") == "on")
	})
}

func (s *Server) postMarketingCampaignDelete(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "campaigns", "Deleted.", func(ctx context.Context) error {
		return s.marketing.DeleteCampaign(ctx, intOf(r.PathValue("id")))
	})
}

func (s *Server) postMarketingSegment(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "segments", "Saved.", func(ctx context.Context) error {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			return errors.New("a segment needs a name")
		}
		alias := strings.Trim(strings.Map(func(c rune) rune {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
				return c
			case c >= 'A' && c <= 'Z':
				return c + 32
			}
			return '-'
		}, name), "-")
		_, err := s.marketing.SaveSegment(ctx, intOf(r.FormValue("id")), name, alias, strings.TrimSpace(r.FormValue("description")), r.FormValue("published") == "on")
		return err
	})
}

func (s *Server) postMarketingSegmentDelete(w http.ResponseWriter, r *http.Request) {
	s.marketingAct(w, r, "segments", "Deleted.", func(ctx context.Context) error {
		return s.marketing.DeleteSegment(ctx, intOf(r.PathValue("id")))
	})
}

// ---- Forms ----------------------------------------------------------------------------------------------------------

// FormsView is the Forms page.
type FormsView struct {
	Configured bool
	Notice     string
	Forms      []FormRow
	Form       *FormRow
	Questions  string // the editor's text for Form
	New        bool
	Results    *forms.Results
	// Headings maps a question id to what it asks, so a result column reads as the question.
	Headings map[string]string
	Order    []string
}

// FormRow is a form with how to show its state.
type FormRow struct {
	forms.Form
	Label       string
	PublishDate string
	CloseDate   string
}

func rowOf(f forms.Form) FormRow {
	row := FormRow{Form: f, Label: f.Status.Label(), PublishDate: forms.Date(f.PublishOn), CloseDate: forms.Date(f.CloseOn)}
	if f.IsScheduled(time.Now()) {
		row.Label = "Scheduled"
	}
	return row
}

func (s *Server) getForms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := &FormsView{Configured: s.forms.Enabled(), Notice: q.Get("notice"), New: q.Get("new") != ""}
	if v.Configured {
		ctx, cancel := s.areaCtx(r)
		defer cancel()
		if id := q.Get("id"); id != "" {
			f, err := s.forms.Get(ctx, id)
			if err != nil {
				v.Notice = areaErr(err)
			} else {
				row := rowOf(*f)
				v.Form, v.Questions = &row, forms.QuestionsText(f.Questions)
				v.Headings = map[string]string{}
				for _, qq := range f.Questions {
					v.Headings[qq.ID] = qq.Headline
					v.Order = append(v.Order, qq.ID)
				}
				if q.Get("results") != "" {
					if v.Results, err = s.forms.Results(ctx, id); err != nil {
						v.Notice = areaErr(err)
					}
				}
			}
		} else if !v.New {
			list, err := s.forms.Forms(ctx)
			if err != nil {
				v.Notice = areaErr(err)
			}
			for _, f := range list {
				v.Forms = append(v.Forms, rowOf(f))
			}
		}
	}
	s.render(w, r, "forms", page{Title: "Forms", FormsArea: v})
}

func (s *Server) formsAct(w http.ResponseWriter, r *http.Request, back url.Values, done string, act func(ctx context.Context) error) {
	if !s.forms.Enabled() {
		s.fail(w, r, http.StatusNotFound, "Forms is not set up on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	areaBack(w, r, "/forms", back, done, act(ctx))
}

func (s *Server) postFormsSave(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	s.formsAct(w, r, url.Values{}, "Saved.", func(ctx context.Context) error {
		in := forms.Input{Name: strings.TrimSpace(r.FormValue("name")), Questions: forms.ParseQuestions(r.FormValue("questions"))}
		if len(in.Questions) == 0 {
			return errors.New("a form needs at least one question")
		}
		if id == "" {
			_, err := s.forms.Create(ctx, in)
			return err
		}
		return s.forms.Update(ctx, id, in)
	})
}

func (s *Server) postFormsStatus(w http.ResponseWriter, r *http.Request) {
	st := forms.Status(r.FormValue("status"))
	s.formsAct(w, r, url.Values{}, "Done.", func(ctx context.Context) error {
		switch st {
		case forms.InProgress, forms.Paused, forms.Completed, forms.Draft:
			return s.forms.SetStatus(ctx, r.PathValue("id"), st)
		}
		return errors.New("choose publish, pause, close or draft")
	})
}

func (s *Server) postFormsSchedule(w http.ResponseWriter, r *http.Request) {
	s.formsAct(w, r, url.Values{"id": {r.PathValue("id")}}, "Scheduled.", func(ctx context.Context) error {
		return s.forms.Schedule(ctx, r.PathValue("id"), strings.TrimSpace(r.FormValue("publish_on")), strings.TrimSpace(r.FormValue("close_on")))
	})
}

func (s *Server) postFormsDelete(w http.ResponseWriter, r *http.Request) {
	s.formsAct(w, r, url.Values{}, "Deleted.", func(ctx context.Context) error {
		return s.forms.Delete(ctx, r.PathValue("id"))
	})
}

// ---- Social ---------------------------------------------------------------------------------------------------------

// SocialView is the Social page.
type SocialView struct {
	Configured bool
	Notice     string
	Channels   []social.Channel
	Posts      []social.Post
	Metrics    []social.Metric
	Channel    string // the channel whose numbers are shown
}

func (s *Server) getSocial(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := &SocialView{Configured: s.social.Enabled(), Notice: q.Get("notice"), Channel: q.Get("channel")}
	if v.Configured {
		ctx, cancel := s.areaCtx(r)
		defer cancel()
		var err error
		if v.Channels, err = s.social.Channels(ctx); err != nil {
			v.Notice = areaErr(err)
		} else if v.Posts, err = s.social.Posts(ctx, time.Now().AddDate(0, 0, -30), time.Now().AddDate(0, 0, 90)); err != nil {
			v.Notice = areaErr(err)
		}
		if v.Channel != "" && err == nil {
			if v.Metrics, err = s.social.Analytics(ctx, v.Channel, 30); err != nil {
				v.Notice = areaErr(err)
			}
		}
	}
	s.render(w, r, "social", page{Title: "Social", SocialArea: v})
}

func (s *Server) postSocialPost(w http.ResponseWriter, r *http.Request) {
	if !s.social.Enabled() {
		s.fail(w, r, http.StatusNotFound, "Social is not set up on this installation")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	all, err := s.social.Channels(ctx)
	if err != nil {
		areaBack(w, r, "/social", nil, "", err)
		return
	}
	chosen := map[string]bool{}
	for _, id := range r.Form["channel"] {
		chosen[id] = true
	}
	d := social.Draft{Content: strings.TrimSpace(r.FormValue("content"))}
	for _, c := range all {
		if chosen[c.ID] && !c.Disabled {
			d.Channels = append(d.Channels, c)
		}
	}
	switch r.FormValue("when") {
	case "now":
		d.Now = true
	case "draft":
		d.Save = true
	default:
		if t, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("at"), time.UTC); err == nil {
			d.When = t.Format("2006-01-02T15:04:05.000Z")
		}
	}
	areaBack(w, r, "/social", nil, "Saved.", s.social.Create(ctx, d))
}

func (s *Server) postSocialDelete(w http.ResponseWriter, r *http.Request) {
	if !s.social.Enabled() {
		s.fail(w, r, http.StatusNotFound, "Social is not set up on this installation")
		return
	}
	ctx, cancel := s.areaCtx(r)
	defer cancel()
	areaBack(w, r, "/social", nil, "Deleted.", s.social.Delete(ctx, r.PathValue("id")))
}
