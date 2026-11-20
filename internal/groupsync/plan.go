// Package groupsync makes Google Workspace group membership follow Hearth.
//
// A person's team and department assignments in Hearth are the source. A group
// in the directory is a view of them, named in a mapping file the operator
// owns, so no group address lives in this code. The sync only ever adds and
// removes plain members: owners and managers are set by hand in the directory
// and are never removed here, and an address on the protect list is never
// removed from anything. Suspended accounts are not wanted, so they come off.
package groupsync

import (
	"sort"
	"strings"
)

// Mapping ties one directory group to the Hearth teams and departments whose
// active people belong in it.
type Mapping struct {
	Group       string   `json:"group"`
	Teams       []string `json:"teams"`
	Departments []string `json:"departments"`
}

// Config is the mapping file.
type Config struct {
	Mappings []Mapping `json:"mappings"`
	// Protect lists addresses the sync never removes from any group.
	Protect []string `json:"protect"`
}

// Member is one entry of a directory group. Role is MEMBER, MANAGER or OWNER.
type Member struct {
	Email string
	Role  string
}

// Change is one add or remove.
type Change struct {
	Group  string
	Email  string
	Remove bool
	Why    string
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Plan compares who should be in a group with who is, and returns the changes.
// want is the set of active Hearth addresses for the group, have is the
// directory's current membership. Output is sorted so a dry run diffs cleanly.
func Plan(group string, want []string, have []Member, protect []string) []Change {
	w := map[string]bool{}
	for _, e := range want {
		if e = norm(e); e != "" {
			w[e] = true
		}
	}
	p := map[string]bool{}
	for _, e := range protect {
		p[norm(e)] = true
	}
	h := map[string]Member{}
	for _, m := range have {
		h[norm(m.Email)] = m
	}
	var out []Change
	for e := range w {
		if _, ok := h[e]; !ok {
			out = append(out, Change{Group: group, Email: e, Why: "on the Hearth team or department"})
		}
	}
	for e, m := range h {
		if w[e] || p[e] {
			continue
		}
		if r := strings.ToUpper(m.Role); r == "OWNER" || r == "MANAGER" {
			continue
		}
		out = append(out, Change{Group: group, Email: e, Remove: true, Why: "not an active member of the Hearth team or department"})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Remove != out[j].Remove {
			return !out[i].Remove
		}
		return out[i].Email < out[j].Email
	})
	return out
}
