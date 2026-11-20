package api

// The CRM through the connector. Every tool is one route of the CRM API run through its own
// handler, with the caller's own subject set on the request: the same validation,
// the same visibility, the same idempotency key (external_id and source) and the
// same Sales-team sharing as the REST call, because it is the REST call. Nothing
// is copied into the connector side, so nothing can drift from /api/v1, and nothing is
// left out of it.
//
// A write tool is offered only to a caller whose writes land directly, the test
// the document tools use, so a read-only token is never told about doors it
// cannot open.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/agents"
)

type crmTool struct {
	name, method, path, desc string
	write                    bool
	props                    map[string]any
	required                 []string
}

func p(kind, desc string) map[string]any { return map[string]any{"type": kind, "description": desc} }

var idempotency = map[string]any{
	"external_id": p("string", "Optional. The id this came from (a chat, a survey response, a campaign event). Sending the same external_id and source again changes nothing and returns the record that exists."),
	"source":      p("string", "Optional. What external_id belongs to, such as chatwoot. Defaults to api."),
}

func merge(parts ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range parts {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

var crmTools = []crmTool{
	{name: "crm_stats", method: "GET", path: "/v1/crm/stats",
		desc: "Counts for the CRM: companies, contacts, deals, open deals and their value in cents, and the count and value per stage."},
	{name: "crm_find_contacts", method: "GET", path: "/v1/contacts",
		desc: "List people in the CRM, or find one by email. Use email to answer \"do we know this person\". Visibility is the caller's, so an empty answer means none you can see.",
		props: map[string]any{"email": p("string", "Optional. An exact, case-insensitive email address."),
			"q": p("string", "Optional. Search by name, email or phone."), "company_id": p("string", "Optional. Only the people at this company."),
			"owner": p("string", "Optional. A person id, or none for unowned."), "limit": p("integer", "Up to 200. Default 50."), "offset": p("integer", "Optional.")}},
	{name: "crm_get_contact", method: "GET", path: "/v1/contacts/{id}", desc: "Read one person in full.",
		props: map[string]any{"id": p("string", "The contact id.")}, required: []string{"id"}},
	{name: "crm_list_companies", method: "GET", path: "/v1/companies",
		desc: "List companies, with how many contacts and deals each has.",
		props: map[string]any{"q": p("string", "Optional. Search by name or domain."), "kind": p("string", "Optional. prospect, customer, partner, vendor or other."),
			"type": p("string", "Optional. A company type key, or none."), "tag": p("string", "Optional. A tag."), "owner": p("string", "Optional. A person id, or none."),
			"limit": p("integer", "Up to 200. Default 50."), "offset": p("integer", "Optional.")}},
	{name: "crm_get_company", method: "GET", path: "/v1/companies/{id}", desc: "Read one company in full.",
		props: map[string]any{"id": p("string", "The company id.")}, required: []string{"id"}},
	{name: "crm_list_deals", method: "GET", path: "/v1/deals",
		desc: "List deals with stage and amount. Stages are new, screening, meeting, proposal, customer, won, lost and no_response.",
		props: map[string]any{"q": p("string", "Optional. Search by name."), "stage": p("string", "Optional. One stage."), "company_id": p("string", "Optional."),
			"contact_id": p("string", "Optional. Deals a person is the point of contact on."), "owner": p("string", "Optional."),
			"limit": p("integer", "Up to 200. Default 50."), "offset": p("integer", "Optional.")}},
	{name: "crm_get_deal", method: "GET", path: "/v1/deals/{id}", desc: "Read one deal in full.",
		props: map[string]any{"id": p("string", "The deal id.")}, required: []string{"id"}},
	{name: "crm_timeline", method: "GET", path: "/v1/{kind}/{id}/activities",
		desc:  "The notes and calls on a company, contact or deal, newest first.",
		props: map[string]any{"kind": p("string", "companies, contacts or deals."), "id": p("string", "The record id.")}, required: []string{"kind", "id"}},

	{name: "crm_create_contact", method: "POST", path: "/v1/contacts", write: true,
		desc: "Add a person. An email somebody already holds is refused and names the existing contact (existing_id), so look first with crm_find_contacts.",
		props: merge(map[string]any{"name": p("string", "Their name."), "email": p("string", "Email."), "phone": p("string", "Phone."), "title": p("string", "Job title."),
			"notes": p("string", "Notes."), "company_id": p("string", "The company they are at."), "owner_id": p("string", "A person id.")}, idempotency)},
	{name: "crm_update_contact", method: "PATCH", path: "/v1/contacts/{id}", write: true,
		desc: "Change only the fields named on a person.",
		props: map[string]any{"id": p("string", "The contact id."), "name": p("string", ""), "email": p("string", ""), "phone": p("string", ""),
			"title": p("string", ""), "notes": p("string", ""), "company_id": p("string", ""), "owner_id": p("string", "")}, required: []string{"id"}},
	{name: "crm_create_company", method: "POST", path: "/v1/companies", write: true,
		desc: "Add a company.",
		props: merge(map[string]any{"name": p("string", "The company's name."), "domain": p("string", "Web domain."), "kind": p("string", "prospect, customer, partner, vendor or other."),
			"website": p("string", ""), "notes": p("string", ""), "owner_id": p("string", "A person id.")}, idempotency), required: []string{"name"}},
	{name: "crm_update_company", method: "PATCH", path: "/v1/companies/{id}", write: true,
		desc: "Change only the fields named on a company.",
		props: map[string]any{"id": p("string", "The company id."), "name": p("string", ""), "domain": p("string", ""), "kind": p("string", ""),
			"website": p("string", ""), "notes": p("string", ""), "owner_id": p("string", "")}, required: []string{"id"}},
	{name: "crm_create_deal", method: "POST", path: "/v1/deals", write: true,
		desc: "Open a deal. It starts in stage new unless a stage is given.",
		props: merge(map[string]any{"name": p("string", "The deal's name."), "stage": p("string", "new, screening, meeting, proposal, customer, won, lost or no_response."),
			"amount_cents": p("integer", "Amount in cents."), "currency": p("string", "Default USD."), "close_date": p("string", "YYYY-MM-DD."),
			"company_id": p("string", ""), "contact_id": p("string", "The point of contact."), "owner_id": p("string", ""), "notes": p("string", "")}, idempotency), required: []string{"name"}},
	{name: "crm_update_deal", method: "PATCH", path: "/v1/deals/{id}", write: true,
		desc: "Change only the fields named on a deal. Moving a deal is a change of stage.",
		props: map[string]any{"id": p("string", "The deal id."), "name": p("string", ""), "stage": p("string", ""), "amount_cents": p("integer", ""),
			"close_date": p("string", ""), "company_id": p("string", ""), "contact_id": p("string", ""), "owner_id": p("string", ""), "notes": p("string", "")}, required: []string{"id"}},
	{name: "crm_add_activity", method: "POST", path: "/v1/activities", write: true,
		desc: "Put a note or a call on the timeline of one or more companies, contacts or deals.",
		props: merge(map[string]any{"kind": p("string", "note or call."), "title": p("string", "A short title."), "body": p("string", "The text."),
			"occurred_at": p("string", "RFC 3339 time. Default now."), "direction": p("string", "For a call: inbound or outbound."), "disposition": p("string", "For a call: the outcome."),
			"duration_ms": p("integer", "For a call."),
			"targets": map[string]any{"type": "array", "description": "What it is about: a list of {type, id} with type company, contact or deal.",
				"items": map[string]any{"type": "object", "properties": map[string]any{"type": p("string", "company, contact or deal."), "id": p("string", "The record id.")}}}}, idempotency),
		required: []string{"kind", "targets"}},
}

type crmMCP struct {
	mux   *http.ServeMux
	byKey map[string]crmTool
}

// CRMTools is the CRM as an connector tool set, or nil when this deployment has no CRM
// side (its companies and contacts live in EmberOps).
func (s *Service) CRMTools() agents.ToolSet {
	if s.crmStore == nil || s.sales == nil {
		return nil
	}
	mux := http.NewServeMux()
	s.crmRoutes(mux)
	t := &crmMCP{mux: mux, byKey: map[string]crmTool{}}
	for _, x := range crmTools {
		t.byKey[x.name] = x
	}
	return t
}

func (t *crmMCP) Tools(c *agents.Caller, write bool) []agents.Tool {
	var out []agents.Tool
	for _, x := range crmTools {
		if x.write && !write {
			continue
		}
		schema := map[string]any{"type": "object", "properties": x.props}
		if x.props == nil {
			schema["properties"] = map[string]any{}
		}
		if len(x.required) > 0 {
			schema["required"] = x.required
		}
		out = append(out, agents.Tool{Name: x.name, Description: x.desc, InputSchema: schema})
	}
	return out
}

func (t *crmMCP) Call(ctx context.Context, c *agents.Caller, name string, args map[string]any, write bool) (*agents.ToolResult, bool) {
	x, ok := t.byKey[name]
	if !ok {
		return nil, false
	}
	if x.write && !write {
		return text("this token cannot change the CRM", true), true
	}
	rest := map[string]any{}
	for k, v := range args {
		rest[k] = v
	}
	path := x.path
	for _, key := range []string{"id", "kind"} {
		if strings.Contains(path, "{"+key+"}") {
			v, _ := rest[key].(string)
			v = strings.TrimSpace(v)
			if v == "" || strings.ContainsAny(v, "/?# ") {
				return text(fmt.Sprintf("%s needs %s", name, key), true), true
			}
			if key == "kind" && v != "companies" && v != "contacts" && v != "deals" {
				return text("kind is companies, contacts or deals", true), true
			}
			path = strings.ReplaceAll(path, "{"+key+"}", v)
			delete(rest, key)
		}
	}
	var req *http.Request
	if x.method == http.MethodGet {
		q := url.Values{}
		keys := make([]string, 0, len(rest))
		for k := range rest {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch v := rest[k].(type) {
			case string:
				q.Set(k, v)
			case float64:
				q.Set(k, fmt.Sprintf("%d", int64(v)))
			case bool:
				if v {
					q.Set(k, "1")
				}
			}
		}
		req = httptest.NewRequest(x.method, path+"?"+q.Encode(), nil)
	} else {
		body, err := json.Marshal(rest)
		if err != nil {
			return text("those arguments could not be sent", true), true
		}
		req = httptest.NewRequest(x.method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	caller := &Caller{OrgID: c.OrgID, PrincipalID: c.AgentID, Name: c.AgentName, TokenID: c.TokenID, Scopes: c.Scopes, Subjects: c.Subjects}
	req = req.WithContext(context.WithValue(ctx, callerKey{}, caller))
	rec := httptest.NewRecorder()
	t.mux.ServeHTTP(rec, req)
	out := strings.TrimSpace(rec.Body.String())
	if rec.Header().Get("Idempotent-Replay") == "true" {
		out = "Already existed, nothing changed: " + out
	}
	return text(out, rec.Code >= 400), true
}

func text(s string, isErr bool) *agents.ToolResult {
	return &agents.ToolResult{Content: []agents.Content{{Type: "text", Text: s}}, IsError: isErr}
}
