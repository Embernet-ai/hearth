package plant

import "testing"

func links(pairs ...string) []SpaceLink {
	var out []SpaceLink
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, SpaceLink{Name: pairs[i], Kind: pairs[i+1]})
	}
	return out
}

func TestReportLevels(t *testing.T) {
	for _, c := range []struct {
		name                   string
		chain                  []SpaceLink
		space, customer, phase string
	}{
		{"no space", nil, "", "", ""},
		{"a lone department", links("Sales", "folder"), "Sales", "", ""},
		{"department and project", links("Marketing", "area", "General Marketing", "project"), "Marketing", "General Marketing", ""},
		{"customer folder under a service space", links("Engineering", "area", "White Team Service", "folder", "Expert Crane", "folder"), "White Team Service", "Expert Crane", ""},
		{"customer and project", links("Engineering", "area", "White Team Service", "folder", "Cast Nylon", "folder", "CNY0324 Process Update", "project"), "White Team Service", "Cast Nylon", "CNY0324 Process Update"},
		{"deeper than three keeps every name", links("Engineering", "area", "A", "folder", "B", "folder", "C", "folder", "D", "project"), "A", "B", "C / D"},
		{"a department that is not an area is the space", links("Sales", "folder", "Pipeline", "folder", "Prospecting", "project"), "Sales", "Pipeline", "Prospecting"},
	} {
		s, cu, p := ReportLevels(c.chain)
		if s != c.space || cu != c.customer || p != c.phase {
			t.Errorf("%s: got %q, %q, %q, want %q, %q, %q", c.name, s, cu, p, c.space, c.customer, c.phase)
		}
	}
}
