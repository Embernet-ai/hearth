package web

// The time report's filters.
//
// A time report is read by somebody who has to act on it (bill from it, sign off
// from it), so every shape of it can be narrowed the same way and every file says
// where each hour stands. The filters are query parameters, so a link or a saved
// address reproduces a report exactly:
//
//	approval=approved|pending|rejected   repeat or comma-separate to ask for more than one
//	team=<id>                            the people in a team (repeat for several)
//	space=<id>                           entries on work in a space or any space under it (repeat)
//	tag=<name>                           entries carrying that time tag (repeat)
//	people=<principal id>                only these people: on the page, in every file and in the details export (repeat or
//	                                     comma-separate); none means everybody. all=1 still means everybody.
//	person                               one person's page
//	from, to                             the dates, as the report already took them
//
// Hearth has no billable flag on an hour. A time tag is how a team marks one, so
// "billable" is the tag by that name, picked like any other.

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// reportFilter is what a request asked the report to keep. A nil or empty set
// means that dimension is not narrowed.
type reportFilter struct {
	approvals map[string]bool
	people    map[string]bool // from the chosen teams; nil when no team was chosen
	spaces    map[string]bool // the chosen spaces and everything under them; nil when none was chosen
	tags      map[string]bool // lower-cased
	who       map[string]bool // the people picked in the People selector; nil when nobody was, which means everybody
	// the parameters as the request spelled them, for the links the page builds
	approvalQ, teamQ, spaceQ, tagQ, whoQ []string
}

func (f reportFilter) active() bool {
	return len(f.approvals) > 0 || f.people != nil || f.spaces != nil || len(f.tags) > 0 || f.who != nil
}

// values reads a repeatable, comma-separable parameter, each value once and trimmed.
func values(q url.Values, key string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range q[key] {
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" && !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
}

// parseReportFilter reads the filters from a request. A non-empty message, for the person,
// means a value is not one the report understands or a team could not be read.
func (s *Server) parseReportFilter(ctx context.Context, r *http.Request) (f reportFilter, msg string) {
	q := r.URL.Query()
	for _, a := range values(q, "approval") {
		a = strings.ToLower(a)
		if a == "all" {
			continue
		}
		switch a {
		case timelog.ApprovalApproved, timelog.ApprovalPending, timelog.ApprovalRejected:
		default:
			return f, "Approval is approved, pending or rejected."
		}
		if f.approvals == nil {
			f.approvals = map[string]bool{}
		}
		f.approvals[a] = true
		f.approvalQ = append(f.approvalQ, a)
	}
	if teams := values(q, "team"); len(teams) > 0 {
		f.teamQ = teams
		f.people = map[string]bool{}
		id := auth.From(ctx)
		for _, t := range teams {
			if s.teams == nil || id == nil {
				return f, "Teams are not available here."
			}
			members, err := s.teams.Members(ctx, id.OrgID, t)
			if err != nil {
				s.log.WarnContext(ctx, "time report: team members", "team", t, "error", err)
				return f, "That team could not be read."
			}
			for _, m := range members {
				f.people[m.PrincipalID] = true
			}
		}
	}
	if spaces := values(q, "space"); len(spaces) > 0 {
		f.spaceQ = spaces
		f.spaces = map[string]bool{}
		for _, sp := range spaces {
			f.spaces[sp] = true
		}
		// A space stands for the work under it too: the entry sits on a document in a leaf, and the
		// person picked the department.
		if id := auth.From(ctx); s.plant != nil && id != nil {
			if tree, err := s.plant.SpaceTree(ctx, id.OrgID, id.Subjects); err == nil {
				for _, sp := range withDescendants(tree, f.spaces) {
					f.spaces[sp] = true
				}
			} else {
				s.log.WarnContext(ctx, "time report: space tree", "error", err)
			}
		}
	}
	if who := values(q, "people"); len(who) > 0 && q.Get("all") != "1" {
		f.whoQ = who
		f.who = map[string]bool{}
		for _, p := range who {
			f.who[p] = true
		}
	}
	if tags := values(q, "tag"); len(tags) > 0 {
		f.tagQ = tags
		f.tags = map[string]bool{}
		for _, t := range tags {
			f.tags[strings.ToLower(t)] = true
		}
	}
	return f, ""
}

// withDescendants lists every space in the tree that sits under one of the chosen ids.
func withDescendants(tree []*plant.Space, chosen map[string]bool) []string {
	parent := map[string]string{}
	for _, sp := range tree {
		if sp.ParentID != nil {
			parent[sp.ID] = *sp.ParentID
		}
	}
	var out []string
	for _, sp := range tree {
		for at, hops := sp.ID, 0; at != "" && hops < 64; at, hops = parent[at], hops+1 {
			if chosen[at] {
				out = append(out, sp.ID)
				break
			}
		}
	}
	return out
}

// keep reports whether a row survives every filter that was asked for.
func (f reportFilter) keep(row timelog.ReportRow) bool {
	if len(f.approvals) > 0 && !f.approvals[row.Approval] {
		return false
	}
	if f.people != nil && !f.people[row.PrincipalID] {
		return false
	}
	if f.who != nil && !f.who[row.PrincipalID] {
		return false
	}
	if f.spaces != nil && !f.spaces[row.SpaceID] {
		return false
	}
	if len(f.tags) > 0 {
		hit := false
		for _, t := range row.Tags {
			if f.tags[strings.ToLower(t)] {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// query is the filters spelled as the parameters that asked for them, for links and
// forms on the page that must carry them. It starts with & so it can follow others.
func (f reportFilter) query() string {
	v := url.Values{}
	for _, a := range f.approvalQ {
		v.Add("approval", a)
	}
	for _, t := range f.teamQ {
		v.Add("team", t)
	}
	for _, sp := range f.spaceQ {
		v.Add("space", sp)
	}
	for _, t := range f.tagQ {
		v.Add("tag", t)
	}
	for _, p := range f.whoQ {
		v.Add("people", p)
	}
	if len(v) == 0 {
		return ""
	}
	return "&" + v.Encode()
}

// approvalLabel is the words a person reads for an approval state.
func approvalLabel(a string) string {
	switch a {
	case timelog.ApprovalApproved:
		return "Approved"
	case timelog.ApprovalRejected:
		return "Rejected"
	default:
		return "Pending"
	}
}

// totalsFromRows is the whole-org roll-up rebuilt from filtered rows, in the order the
// stores give their own: most time first, then by name.
func totalsFromRows(rows []timelog.ReportRow) []timelog.ReportTotal {
	idx := map[string]int{}
	var out []timelog.ReportTotal
	for _, r := range rows {
		i, ok := idx[r.PrincipalID]
		if !ok {
			i = len(out)
			idx[r.PrincipalID] = i
			out = append(out, timelog.ReportTotal{PrincipalID: r.PrincipalID, PersonName: r.PersonName})
		}
		out[i].Minutes += r.Minutes
	}
	sortTotals(out)
	return out
}

func sortTotals(out []timelog.ReportTotal) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Minutes != out[j].Minutes {
			return out[i].Minutes > out[j].Minutes
		}
		return strings.ToLower(out[i].PersonName) < strings.ToLower(out[j].PersonName)
	})
}

// reportLevels returns what fills a row's Space, Customer/Project and Service/Project Phase from the space tree, read
// once for the request. If the tree cannot be read the row keeps the space it already names, in the Space column, and
// the failure is logged; the other two stay empty rather than guessed.
func (s *Server) reportLevels(ctx context.Context, orgID string) func(*timelog.ReportRow) {
	var chains map[string][]plant.SpaceLink
	if s.plant != nil {
		var err error
		if chains, err = s.plant.SpaceChains(ctx, orgID); err != nil {
			s.log.WarnContext(ctx, "time report: space chains", "error", err)
			chains = nil
		}
	}
	return func(row *timelog.ReportRow) {
		if chain, ok := chains[row.SpaceID]; ok && row.SpaceID != "" {
			row.LevelSpace, row.LevelCustomer, row.LevelPhase = plant.ReportLevels(chain)
			return
		}
		row.LevelSpace = row.SpaceName
	}
}
