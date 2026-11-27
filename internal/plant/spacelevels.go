package plant

import (
	"context"
	"fmt"
	"strings"
)

// SpaceLink is one step on the way down to a space: its name and what kind of space it is.
type SpaceLink struct {
	Name string
	Kind string
}

// SpaceChains returns, for every space in the org, the links from its root down to itself. It reads the whole tree
// with no access filter, because a report that already names a space has no reason to hide the names above it, and a
// chain cut at the first space somebody cannot open would put the wrong name in the wrong column.
func (s *Service) SpaceChains(ctx context.Context, orgID string) (map[string][]SpaceLink, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, parent_id::text, name, kind FROM spaces WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("plant: space chains: %w", err)
	}
	defer rows.Close()
	type node struct {
		parent *string
		link   SpaceLink
	}
	byID := map[string]node{}
	for rows.Next() {
		var id string
		var n node
		if err := rows.Scan(&id, &n.parent, &n.link.Name, &n.link.Kind); err != nil {
			return nil, fmt.Errorf("plant: scan space chain: %w", err)
		}
		byID[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("plant: space chains: %w", err)
	}
	out := make(map[string][]SpaceLink, len(byID))
	for id := range byID {
		var up []SpaceLink
		for at, hops := id, 0; hops < maxDepth+1; hops++ {
			n, ok := byID[at]
			if !ok {
				break
			}
			up = append(up, n.link)
			if n.parent == nil {
				break
			}
			at = *n.parent
		}
		chain := make([]SpaceLink, len(up))
		for i, l := range up {
			chain[len(up)-1-i] = l
		}
		out[id] = chain
	}
	return out, nil
}

// ReportLevels reads a space's chain as the three space columns of a time report: Space, Customer/Project, and
// Service/Project Phase. A task is the fourth column and is the entry's own document, so it is not here.
//
// A department (an area) at the top only groups the spaces below it when there are at least two more levels under it
// (Engineering holds White Team Service, which holds the customer, which holds the project). Then the department is
// left out and the next level is the Space. With fewer levels the department is the Space, as Marketing is for its
// General Marketing project. Anything below the third level stays in the phase column, joined with " / ", so no name
// is lost. A level that does not exist is empty; nothing is filled in.
func ReportLevels(chain []SpaceLink) (space, customer, phase string) {
	if len(chain) >= 3 && chain[0].Kind == "area" {
		chain = chain[1:]
	}
	if len(chain) > 0 {
		space = chain[0].Name
	}
	if len(chain) > 1 {
		customer = chain[1].Name
	}
	if len(chain) > 2 {
		names := make([]string, 0, len(chain)-2)
		for _, l := range chain[2:] {
			names = append(names, l.Name)
		}
		phase = strings.Join(names, " / ")
	}
	return space, customer, phase
}
