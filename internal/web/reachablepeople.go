package web

// Lists of colleagues, for a contractor account.
//
// An account marked as a contractor's reaches the folders it was given and
// nothing else, and that has to hold for the names too: an assignee picker, a
// filter by person, a mention. For such an account every one of those lists is
// cut to the person themselves and the people who hold a grant of their own on a
// space the account can open (core.ReachablePeople). Every other account gets
// the list exactly as it always did, and a super admin is never narrowed.

import (
	"net/http"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// keepReachable cuts a list to the people a restricted account may be shown, plus
// any named in always (the people already on the thing being looked at, so what
// is assigned never drops out of its own page). An unrestricted account's list
// comes back untouched.
func keepReachable(list []docs.People, reach map[string]bool, restricted bool, always ...string) []docs.People {
	if !restricted {
		return list
	}
	keep := make(map[string]bool, len(reach)+len(always))
	for id := range reach {
		keep[id] = true
	}
	for _, id := range always {
		keep[id] = true
	}
	out := make([]docs.People, 0, len(list))
	for _, p := range list {
		if keep[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// onlyReachable applies keepReachable for the person making the request. When
// the check cannot be made it fails closed for nobody and open for nobody: it
// asks the database, and an error leaves the list empty rather than whole.
func (s *Server) onlyReachable(r *http.Request, list []docs.People, always ...string) []docs.People {
	id := auth.From(r.Context())
	if id == nil || s.plant == nil {
		return list
	}
	reach, restricted, err := s.plant.ReachablePeople(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "reachable people", "error", err)
		return nil
	}
	return keepReachable(list, reach, restricted, always...)
}

// assignable is the picker list for the person making the request.
func (s *Server) assignable(r *http.Request, always ...string) ([]docs.People, error) {
	id := auth.From(r.Context())
	list, err := s.docs.Assignable(r.Context(), id.OrgID)
	if err != nil {
		return nil, err
	}
	return s.onlyReachable(r, list, always...), nil
}
