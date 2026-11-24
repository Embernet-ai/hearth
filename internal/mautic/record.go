package mautic

// What the CRM needs of one contact: which segments and campaigns they are in, and moving them in or out by hand.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

// SegmentsBrief lists segments by name without counting their members, which is one call each. Use it where the list is
// shown beside one person.
func (c *Client) SegmentsBrief(ctx context.Context) ([]Segment, error) {
	var out struct {
		Lists json.RawMessage `json:"lists"`
	}
	if err := c.do(ctx, http.MethodGet, "/segments", url.Values{"limit": {"500"}}, nil, &out); err != nil {
		return nil, err
	}
	var rows []Segment
	for _, raw := range rows2(out.Lists) {
		var s struct {
			ID        json.Number `json:"id"`
			Name      string      `json:"name"`
			Alias     string      `json:"alias"`
			Published bool        `json:"isPublished"`
		}
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		id, _ := s.ID.Int64()
		rows = append(rows, Segment{ID: id, Name: s.Name, Alias: s.Alias, Published: s.Published})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// CampaignsBrief lists campaigns by name.
func (c *Client) CampaignsBrief(ctx context.Context) ([]Campaign, error) {
	rows, _, err := c.Campaigns(ctx, "", 500, 0)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func idsOf(raw json.RawMessage) []int64 {
	var out []int64
	for _, r := range rows2(raw) {
		var o struct {
			ID json.Number `json:"id"`
		}
		if json.Unmarshal(r, &o) == nil {
			if id, err := o.ID.Int64(); err == nil {
				out = append(out, id)
			}
		}
	}
	return out
}

// ContactSegments is the ids of the segments a Mautic contact is in.
func (c *Client) ContactSegments(ctx context.Context, contactID int64) ([]int64, error) {
	var out struct {
		Lists json.RawMessage `json:"lists"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/contacts/%d/segments", contactID), nil, nil, &out); err != nil {
		return nil, err
	}
	return idsOf(out.Lists), nil
}

// ContactCampaigns is the ids of the campaigns a Mautic contact is in.
func (c *Client) ContactCampaigns(ctx context.Context, contactID int64) ([]int64, error) {
	var out struct {
		Campaigns json.RawMessage `json:"campaigns"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/contacts/%d/campaigns", contactID), nil, nil, &out); err != nil {
		return nil, err
	}
	return idsOf(out.Campaigns), nil
}

// CampaignAdd puts a contact in a campaign.
func (c *Client) CampaignAdd(ctx context.Context, campaignID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/campaigns/%d/contact/%d/add", campaignID, contactID), nil, nil, nil)
}

// CampaignRemove takes a contact out of a campaign.
func (c *Client) CampaignRemove(ctx context.Context, campaignID, contactID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/campaigns/%d/contact/%d/remove", campaignID, contactID), nil, nil, nil)
}
