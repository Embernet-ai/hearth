package mautic

// What the Marketing area needs: emails, campaigns and segments, read and write, and the counts that say how they did.
//
// Mautic answers a collection as an object keyed by id when it has rows and as [] when it has none, and writes ids and
// numbers as strings in some places and numbers in others, so every read here goes through the tolerant decoders at the
// bottom of the file.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Email is one email as the Marketing area shows it.
type Email struct {
	ID          int64
	Name        string
	Subject     string
	Type        string // "template" is sent by a campaign, "list" is sent to segments
	HTML        string
	Published   bool
	PublishUp   string // when it goes live, empty when it is live already or never
	PublishDown string
	Sent        int64
	Read        int64
	Segments    []int64
}

// EmailInput is what a person fills in to make or change an email.
type EmailInput struct {
	Name        string
	Subject     string
	Type        string
	HTML        string
	Published   bool
	PublishUp   string // an ISO time to schedule it, "" for now
	Segments    []int64
	FromName    string
	FromAddress string
}

func (in EmailInput) body() map[string]any {
	t := in.Type
	if t != "list" {
		t = "template"
	}
	b := map[string]any{"name": in.Name, "subject": in.Subject, "emailType": t, "customHtml": in.HTML,
		"isPublished": in.Published, "publishUp": nullable(in.PublishUp)}
	if in.FromName != "" {
		b["fromName"] = in.FromName
	}
	if in.FromAddress != "" {
		b["fromAddress"] = in.FromAddress
	}
	if t == "list" {
		// Mautic takes the ids of the segments, not objects.
		b["lists"] = ids(in.Segments)
	}
	return b
}

// ids is never nil, so an email with no segments sends an empty list and not null.
func ids(in []int64) []int64 {
	if in == nil {
		return []int64{}
	}
	return in
}

func nullable(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// Emails lists emails, newest first, with how many were sent and read.
func (c *Client) Emails(ctx context.Context, search string, limit, start int) ([]Email, int, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "start": {strconv.Itoa(start)}, "orderBy": {"id"}, "orderByDir": {"DESC"}}
	if search != "" {
		q.Set("search", search)
	}
	var out struct {
		Total  json.Number     `json:"total"`
		Emails json.RawMessage `json:"emails"`
	}
	if err := c.do(ctx, http.MethodGet, "/emails", q, nil, &out); err != nil {
		return nil, 0, err
	}
	total, _ := out.Total.Int64()
	return decodeEmails(out.Emails), int(total), nil
}

// GetEmail reads one email with its body.
func (c *Client) GetEmail(ctx context.Context, id int64) (*Email, error) {
	var out struct {
		Email json.RawMessage `json:"email"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/emails/%d", id), nil, nil, &out); err != nil {
		return nil, err
	}
	e := decodeEmail(out.Email)
	if e.ID == 0 {
		return nil, &APIError{Status: http.StatusNotFound, Method: "GET", Path: fmt.Sprintf("/emails/%d", id)}
	}
	return &e, nil
}

// SaveEmail makes an email (id 0) or changes one, and returns its id.
func (c *Client) SaveEmail(ctx context.Context, id int64, in EmailInput) (int64, error) {
	var out struct {
		Email json.RawMessage `json:"email"`
	}
	var err error
	if id == 0 {
		err = c.do(ctx, http.MethodPost, "/emails/new", nil, in.body(), &out)
	} else {
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/emails/%d/edit", id), nil, in.body(), &out)
	}
	if err != nil {
		return 0, err
	}
	return idOf(out.Email)
}

// SetEmailPublished publishes or unpublishes an email without touching anything else on it.
func (c *Client) SetEmailPublished(ctx context.Context, id int64, on bool) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/emails/%d/edit", id), nil, map[string]any{"isPublished": on}, nil)
}

// SendEmail sends a list email to the segments chosen on it, now.
func (c *Client) SendEmail(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/emails/%d/send", id), nil, nil, nil)
}

// DeleteEmail removes an email.
func (c *Client) DeleteEmail(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/emails/%d/delete", id), nil, nil, nil)
}

// Campaign is one campaign as the Marketing area shows it.
type Campaign struct {
	ID          int64
	Name        string
	Description string
	Published   bool
	PublishUp   string
	PublishDown string
	Segments    []int64
	Contacts    int64
}

// CampaignInput is what a person fills in to make or change a campaign.
type CampaignInput struct {
	Name        string
	Description string
	Published   bool
	PublishUp   string
	PublishDown string
	Segments    []int64
	// EmailID is the email the campaign sends to everybody in its segments, and DelayDays how long after they join.
	// Mautic refuses a campaign with no step, so this one step is how a campaign is made here.
	EmailID   int64
	DelayDays int
}

func (in CampaignInput) body() map[string]any {
	b := map[string]any{"name": in.Name, "description": in.Description, "isPublished": in.Published,
		"publishUp": nullable(in.PublishUp), "publishDown": nullable(in.PublishDown), "lists": ids(in.Segments)}
	if in.EmailID != 0 {
		ev := map[string]any{"id": "new_1", "name": "Send the email", "type": "email.send", "eventType": "action", "order": 1,
			"properties": map[string]any{"email": in.EmailID, "email_type": "marketing"}, "triggerMode": "immediate"}
		if in.DelayDays > 0 {
			ev["triggerMode"], ev["triggerInterval"], ev["triggerIntervalUnit"] = "interval", in.DelayDays, "d"
		}
		b["events"] = []any{ev}
		b["canvasSettings"] = map[string]any{
			"nodes": []any{map[string]any{"id": "lists", "positionX": "450", "positionY": "50"},
				map[string]any{"id": "new_1", "positionX": "450", "positionY": "200"}},
			"connections": []any{map[string]any{"sourceId": "lists", "targetId": "new_1", "anchors": map[string]string{"source": "leadsource", "target": "top"}}},
		}
	}
	return b
}

// Campaigns lists campaigns.
func (c *Client) Campaigns(ctx context.Context, search string, limit, start int) ([]Campaign, int, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "start": {strconv.Itoa(start)}, "orderBy": {"id"}, "orderByDir": {"DESC"}}
	if search != "" {
		q.Set("search", search)
	}
	var out struct {
		Total     json.Number     `json:"total"`
		Campaigns json.RawMessage `json:"campaigns"`
	}
	if err := c.do(ctx, http.MethodGet, "/campaigns", q, nil, &out); err != nil {
		return nil, 0, err
	}
	total, _ := out.Total.Int64()
	var rows []Campaign
	for _, raw := range rows2(out.Campaigns) {
		rows = append(rows, decodeCampaign(raw))
	}
	return rows, int(total), nil
}

// CampaignContacts is how many people are in a campaign, which is its result in the one number Mautic gives for it.
func (c *Client) CampaignContacts(ctx context.Context, id int64) (int64, error) {
	var out struct {
		Total json.Number `json:"total"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/campaigns/%d/contacts", id), url.Values{"limit": {"1"}}, nil, &out); err != nil {
		return 0, err
	}
	n, _ := out.Total.Int64()
	return n, nil
}

// SaveCampaign makes a campaign (id 0) or changes one, and returns its id.
func (c *Client) SaveCampaign(ctx context.Context, id int64, in CampaignInput) (int64, error) {
	var out struct {
		Campaign json.RawMessage `json:"campaign"`
	}
	var err error
	if id == 0 {
		err = c.do(ctx, http.MethodPost, "/campaigns/new", nil, in.body(), &out)
	} else {
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/campaigns/%d/edit", id), nil, in.body(), &out)
	}
	if err != nil {
		return 0, err
	}
	return idOf(out.Campaign)
}

// SetCampaignPublished publishes or unpublishes a campaign.
func (c *Client) SetCampaignPublished(ctx context.Context, id int64, on bool) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/campaigns/%d/edit", id), nil, map[string]any{"isPublished": on}, nil)
}

// DeleteCampaign removes a campaign.
func (c *Client) DeleteCampaign(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/campaigns/%d/delete", id), nil, nil, nil)
}

// Segment is one segment as the Marketing area shows it.
type Segment struct {
	ID          int64
	Name        string
	Alias       string
	Description string
	Published   bool
	Contacts    int64
}

// SegmentList lists segments with how many people are in each.
func (c *Client) SegmentList(ctx context.Context) ([]Segment, error) {
	var out struct {
		Lists json.RawMessage `json:"lists"`
	}
	if err := c.do(ctx, http.MethodGet, "/segments", url.Values{"limit": {"200"}}, nil, &out); err != nil {
		return nil, err
	}
	var rows []Segment
	for _, raw := range rows2(out.Lists) {
		var s struct {
			ID          json.Number `json:"id"`
			Name        string      `json:"name"`
			Alias       string      `json:"alias"`
			Description *string     `json:"description"`
			Published   bool        `json:"isPublished"`
		}
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		id, _ := s.ID.Int64()
		seg := Segment{ID: id, Name: s.Name, Alias: s.Alias, Published: s.Published}
		if s.Description != nil {
			seg.Description = *s.Description
		}
		rows = append(rows, seg)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	for i := range rows {
		var cnt struct {
			Total json.Number `json:"total"`
		}
		if err := c.do(ctx, http.MethodGet, "/contacts", url.Values{"search": {"segment:" + rows[i].Alias}, "limit": {"1"}}, nil, &cnt); err == nil {
			rows[i].Contacts, _ = cnt.Total.Int64()
		}
	}
	return rows, nil
}

// SaveSegment makes a segment (id 0) or changes its name, description and published flag. A segment made here has no
// filters: people are put in it by the Marketing sync or by hand.
func (c *Client) SaveSegment(ctx context.Context, id int64, name, alias, description string, published bool) (int64, error) {
	var out struct {
		List json.RawMessage `json:"list"`
	}
	var err error
	if id == 0 {
		err = c.do(ctx, http.MethodPost, "/segments/new", nil, map[string]any{"name": name, "alias": alias, "description": description,
			"isPublished": published, "isGlobal": true, "filters": []any{}}, &out)
	} else {
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/segments/%d/edit", id), nil, map[string]any{"name": name, "description": description,
			"isPublished": published}, &out)
	}
	if err != nil {
		return 0, err
	}
	return idOf(out.List)
}

// DeleteSegment removes a segment.
func (c *Client) DeleteSegment(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/segments/%d/delete", id), nil, nil, nil)
}

// ---- tolerant decoding ---------------------------------------------------------------------------------------------

// rows2 turns Mautic's object-keyed-by-id or empty-array collection into a slice of raw rows in id order.
func rows2(raw json.RawMessage) []json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, _ := strconv.Atoi(keys[i])
			b, _ := strconv.Atoi(keys[j])
			return a > b
		})
		out := make([]json.RawMessage, 0, len(keys))
		for _, k := range keys {
			out = append(out, m[k])
		}
		return out
	}
	var l []json.RawMessage
	if json.Unmarshal(raw, &l) == nil {
		return l
	}
	return nil
}

func decodeEmails(raw json.RawMessage) []Email {
	var out []Email
	for _, r := range rows2(raw) {
		out = append(out, decodeEmail(r))
	}
	return out
}

func int64of(n json.Number) int64 { v, _ := n.Int64(); return v }

func decodeEmail(raw json.RawMessage) Email {
	var e struct {
		ID          json.Number `json:"id"`
		Name        string      `json:"name"`
		Subject     string      `json:"subject"`
		EmailType   string      `json:"emailType"`
		CustomHTML  *string     `json:"customHtml"`
		Published   bool        `json:"isPublished"`
		PublishUp   *string     `json:"publishUp"`
		PublishDown *string     `json:"publishDown"`
		Sent        json.Number `json:"sentCount"`
		Read        json.Number `json:"readCount"`
		Lists       []struct {
			ID json.Number `json:"id"`
		} `json:"lists"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return Email{}
	}
	out := Email{ID: int64of(e.ID), Name: e.Name, Subject: e.Subject, Type: e.EmailType, Published: e.Published,
		Sent: int64of(e.Sent), Read: int64of(e.Read)}
	if e.CustomHTML != nil {
		out.HTML = *e.CustomHTML
	}
	if e.PublishUp != nil {
		out.PublishUp = *e.PublishUp
	}
	if e.PublishDown != nil {
		out.PublishDown = *e.PublishDown
	}
	for _, l := range e.Lists {
		out.Segments = append(out.Segments, int64of(l.ID))
	}
	return out
}

func decodeCampaign(raw json.RawMessage) Campaign {
	var c struct {
		ID          json.Number `json:"id"`
		Name        string      `json:"name"`
		Description *string     `json:"description"`
		Published   bool        `json:"isPublished"`
		PublishUp   *string     `json:"publishUp"`
		PublishDown *string     `json:"publishDown"`
		Lists       []struct {
			ID json.Number `json:"id"`
		} `json:"lists"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return Campaign{}
	}
	out := Campaign{ID: int64of(c.ID), Name: c.Name, Published: c.Published}
	if c.Description != nil {
		out.Description = *c.Description
	}
	if c.PublishUp != nil {
		out.PublishUp = *c.PublishUp
	}
	if c.PublishDown != nil {
		out.PublishDown = *c.PublishDown
	}
	for _, l := range c.Lists {
		out.Segments = append(out.Segments, int64of(l.ID))
	}
	return out
}

// ContactTotal is how many contacts Mautic holds.
func (c *Client) ContactTotal(ctx context.Context) (int, error) {
	var out struct {
		Total json.Number `json:"total"`
	}
	if err := c.do(ctx, http.MethodGet, "/contacts", url.Values{"limit": {"1"}}, nil, &out); err != nil {
		return 0, err
	}
	return int(int64of(out.Total)), nil
}

// SendEmailTo sends one email to one contact now, the way Mautic's own "send example" does, so a campaign can be tried on
// a test address before it goes to a segment.
func (c *Client) SendEmailTo(ctx context.Context, emailID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/emails/%d/contact/%d/send", emailID, contactID), nil, map[string]any{}, nil)
}

// -----------------------------------------------------------------------------
// Contacts: the people the emails go to
// -----------------------------------------------------------------------------

// Contact is one person in the email marketing app.
type Contact struct {
	ID         int64
	First      string
	Last       string
	Email      string
	Company    string
	Tags       []string
	Points     int64
	LastActive string
	// DoNotEmail is true when the person has unsubscribed, bounced or been marked do not contact for email.
	DoNotEmail bool
}

// Name is what a person reads for a contact.
func (c Contact) Name() string {
	if n := strings.TrimSpace(c.First + " " + c.Last); n != "" {
		return n
	}
	return c.Email
}

// ContactInput is what a person fills in to make or change a contact.
type ContactInput struct {
	First, Last, Email, Company string
	Tags                        []string
}

func (in ContactInput) body() map[string]any {
	b := map[string]any{"firstname": in.First, "lastname": in.Last, "email": in.Email, "company": in.Company}
	if in.Tags != nil {
		b["tags"] = in.Tags
	}
	return b
}

func decodeContact(raw json.RawMessage) Contact {
	var e struct {
		ID         json.Number `json:"id"`
		Points     json.Number `json:"points"`
		LastActive *string     `json:"lastActive"`
		Fields     struct {
			All map[string]any `json:"all"`
		} `json:"fields"`
		Tags []struct {
			Tag string `json:"tag"`
		} `json:"tags"`
		DNC []struct {
			Channel string `json:"channel"`
		} `json:"doNotContact"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return Contact{}
	}
	str := func(k string) string {
		s, _ := e.Fields.All[k].(string)
		return s
	}
	c := Contact{ID: int64of(e.ID), First: str("firstname"), Last: str("lastname"), Email: str("email"), Company: str("company"), Points: int64of(e.Points)}
	if e.LastActive != nil {
		c.LastActive = *e.LastActive
	}
	for _, t := range e.Tags {
		c.Tags = append(c.Tags, t.Tag)
	}
	for _, d := range e.DNC {
		if d.Channel == "email" {
			c.DoNotEmail = true
		}
	}
	return c
}

// Contacts lists contacts, newest first, narrowed by a search (a name, an address, "tag:vip", "email:a@b.c").
func (c *Client) Contacts(ctx context.Context, search string, limit, start int) ([]Contact, int, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}, "start": {strconv.Itoa(start)}, "orderBy": {"id"}, "orderByDir": {"DESC"}}
	if search != "" {
		q.Set("search", search)
	}
	var out struct {
		Total    json.Number     `json:"total"`
		Contacts json.RawMessage `json:"contacts"`
	}
	if err := c.do(ctx, http.MethodGet, "/contacts", q, nil, &out); err != nil {
		return nil, 0, err
	}
	var list []Contact
	for _, r := range rows2(out.Contacts) {
		list = append(list, decodeContact(r))
	}
	return list, int(int64of(out.Total)), nil
}

// FindContactByEmail returns the contact with that address, or nil.
func (c *Client) FindContactByEmail(ctx context.Context, email string) (*Contact, error) {
	list, _, err := c.Contacts(ctx, "email:"+strings.TrimSpace(email), 5, 0)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Email, strings.TrimSpace(email)) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// SaveContact makes a contact (id 0) or changes one, and returns its id.
func (c *Client) SaveContact(ctx context.Context, id int64, in ContactInput) (int64, error) {
	var out struct {
		Contact json.RawMessage `json:"contact"`
	}
	var err error
	if id == 0 {
		err = c.do(ctx, http.MethodPost, "/contacts/new", nil, in.body(), &out)
	} else {
		err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/contacts/%d/edit", id), nil, in.body(), &out)
	}
	if err != nil {
		return 0, err
	}
	return idOf(out.Contact)
}

// DeleteContact removes a contact from Mautic.
func (c *Client) DeleteContact(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/contacts/%d/delete", id), nil, nil, nil)
}

// SetDoNotEmail marks a contact do not contact for email (reason 3, manual), or takes the mark off.
func (c *Client) SetDoNotEmail(ctx context.Context, id int64, on bool, comment string) error {
	if on {
		return c.AddDoNotContact(ctx, id, 3, comment)
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/contacts/%d/dnc/email/remove", id), nil, map[string]any{}, nil)
}

// GetContact reads one contact.
func (c *Client) GetContact(ctx context.Context, id int64) (*Contact, error) {
	var out struct {
		Contact json.RawMessage `json:"contact"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/contacts/%d", id), nil, nil, &out); err != nil {
		return nil, err
	}
	ct := decodeContact(out.Contact)
	if ct.ID == 0 {
		return nil, fmt.Errorf("mautic: no contact %d", id)
	}
	return &ct, nil
}
