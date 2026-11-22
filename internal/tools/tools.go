// Package tools is the Tools section: a launcher of cards to the other apps a company runs, set in the installation's
// configuration. Each card is a name, a line about what it is for, and the address of the live service.
package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Tool is one card.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Href        string `json:"href"`
	Icon        string `json:"icon"`
	// Note is a short line under the card, for the one that signs in differently from the rest.
	Note string `json:"note,omitempty"`
}

// Parse reads the configured list. An empty value is no tools. A card with no name or an address that is not http or
// https is a mistake in the configuration and is refused, so it shows at startup and not as a dead card.
func Parse(raw string) ([]Tool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []Tool
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("tools: HEARTH_TOOLS is not a JSON list of cards: %w", err)
	}
	for i, t := range out {
		if strings.TrimSpace(t.Name) == "" {
			return nil, fmt.Errorf("tools: card %d has no name", i+1)
		}
		u, err := url.Parse(t.Href)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("tools: %q needs an http or https address, got %q", t.Name, t.Href)
		}
	}
	return out, nil
}
