package web

// The team parts of the dashboard are for the Marketing and Social Media teams and super admins, and for nobody else.

import (
	"context"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/mautic"
	"github.com/Embernet-ai/hearth/internal/teams"
)

type fakeTeams struct {
	teams.Store
	list []*teams.Team
}

func (f fakeTeams) List(context.Context, string) ([]*teams.Team, error) { return f.list, nil }

func TestTeamPartsAreForMarketingAndSuperAdminsOnly(t *testing.T) {
	list := []*teams.Team{{ID: "t-mk", Name: "Marketing"}, {ID: "t-sm", Name: "Social Media"}, {ID: "t-sales", Name: "Sales"}}
	s := &Server{teams: fakeTeams{list: list}, marketing: &mautic.Client{}}
	in := func(subs ...core.Subject) *auth.Identity {
		return &auth.Identity{OrgID: "o", PrincipalID: "me", Subjects: core.Subjects(subs)}
	}
	team := func(id string) core.Subject { return core.Subject{Kind: core.SubjectTeam, ID: id} }
	ctx := context.Background()
	for name, c := range map[string]struct {
		id   *auth.Identity
		want bool
	}{
		"a marketing team member":   {in(team("t-mk")), true},
		"a social media member":     {in(team("t-sm")), true},
		"a super admin":             {in(core.Subject{Kind: core.SubjectSuper}), true},
		"a sales team member":       {in(team("t-sales")), false},
		"somebody on no team":       {in(), false},
		"a team that is not listed": {in(team("t-other")), false},
	} {
		if got := s.worksMarketing(ctx, c.id); got != c.want {
			t.Errorf("%s: worksMarketing = %v, want %v", name, got, c.want)
		}
	}
	// Nobody outside the teams gets a panel, even with every tool configured.
	if p := s.teamParts(ctx, in(team("t-sales"))); p != nil {
		t.Errorf("a sales team member got the team parts: %+v", p)
	}
	// No tool configured, no panel, even for a super admin.
	if p := (&Server{teams: fakeTeams{list: list}}).teamParts(ctx, in(core.Subject{Kind: core.SubjectSuper})); p != nil {
		t.Errorf("an installation with none of the tools got a panel: %+v", p)
	}
}

func TestNumWritesThousands(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 12408: "12,408", 1234567: "1,234,567", -5000: "-5,000"} {
		if got := num(in); got != want {
			t.Errorf("num(%d) = %q, want %q", in, got, want)
		}
	}
}
