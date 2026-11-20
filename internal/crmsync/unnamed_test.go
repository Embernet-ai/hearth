package crmsync

import "testing"

// A company with no real name stays out of the sync: none, "(unnamed N)", or only its web address.
func TestUnnamedIncludesACompanyNamedOnlyByItsWebAddress(t *testing.T) {
	for _, c := range []struct {
		name, domain string
		want         bool
	}{
		{"", "", true},
		{"(unnamed 41643296275)", "pidrobotics.com", true},
		{"pidrobotics.com", "pidrobotics.com", true},
		{"www.PidRobotics.com", "pidrobotics.com", true},
		{"pidrobotics.com", "www.pidrobotics.com", true},
		{"PID Robotics", "pidrobotics.com", false},
		{"Baldor Electric", "", false},
		{"Baldor", "baldor.com", false},
	} {
		if got := unnamed(c.name, c.domain); got != c.want {
			t.Errorf("unnamed(%q, %q) = %v, want %v", c.name, c.domain, got, c.want)
		}
	}
}
