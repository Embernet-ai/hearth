package auth

import "testing"

// The role gate, checked where the provider actually puts the claim.
//
// Keycloak files realm roles under realm_access, client roles under
// resource_access, and group membership under groups. Entra uses roles. An
// operator setting HEARTH_OIDC_REQUIRED_ROLES knows the name their identity
// team created and should not also have to know which of those four their
// provider chose -- getting it wrong fails closed at login, which is the worst
// place to find out.
func TestRequiredRolesMatchWhereverTheProviderPutsThem(t *testing.T) {
	type resource = map[string]struct {
		Roles []string `json:"roles"`
	}

	cases := []struct {
		name     string
		required []string
		realm    []string
		groups   []string
		roles    []string
		resource resource
		wantOK   bool
	}{
		{name: "no requirement lets anybody through", wantOK: true},
		{
			name: "keycloak realm role", required: []string{"fireball-staff"},
			realm: []string{"default-roles-fireball", "fireball-staff"}, wantOK: true,
		},
		{
			name: "keycloak group", required: []string{"fireball-staff"},
			groups: []string{"fireball-staff"}, wantOK: true,
		},
		{
			// Keycloak emits full paths when full.path is true, and the config
			// should not have to encode a hierarchy somebody may reorganise.
			name: "group given as a full path", required: []string{"fireball-staff"},
			groups: []string{"/Fireball/fireball-staff"}, wantOK: true,
		},
		{
			name: "entra roles claim", required: []string{"fireball-staff"},
			roles: []string{"fireball-staff"}, wantOK: true,
		},
		{
			name: "keycloak client role", required: []string{"fireball-staff"},
			resource: resource{"hearth": {Roles: []string{"fireball-staff"}}}, wantOK: true,
		},
		{
			name: "case does not decide who gets in", required: []string{"Fireball-Staff"},
			realm: []string{"fireball-staff"}, wantOK: true,
		},
		{
			name: "any one of several is enough", required: []string{"fireball-staff", "fireball-admin"},
			realm: []string{"fireball-admin"}, wantOK: true,
		},
		{
			// The case the whole control exists for: a real employee, at the
			// right domain, who has not been given this application.
			name: "somebody from the company without the role", required: []string{"fireball-staff"},
			realm: []string{"default-roles-fireball"}, groups: []string{"everyone"}, wantOK: false,
		},
		{
			name: "no claims at all", required: []string{"fireball-staff"}, wantOK: false,
		},
		{
			// A prefix must not be a match, or "fireball" lets in
			// "fireball-contractors".
			name: "a near miss is a miss", required: []string{"fireball-staff"},
			realm: []string{"fireball-staff-readonly"}, wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := &OIDC{cfg: OIDCConfig{RequiredRoles: c.required}}
			err := o.checkRoles(c.realm, c.resource, c.groups, c.roles)
			if c.wantOK && err != nil {
				t.Errorf("expected sign-in to be allowed, got %v", err)
			}
			if !c.wantOK && err == nil {
				t.Error("expected sign-in to be refused, it was allowed")
			}
		})
	}
}

// The refusal must not name the role. Telling a stranger which role they lack
// tells them exactly what to go and ask for.
func TestRoleRefusalDoesNotNameTheRole(t *testing.T) {
	o := &OIDC{cfg: OIDCConfig{RequiredRoles: []string{"fireball-staff"}}}
	err := o.checkRoles(nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if got := err.Error(); contains(got, "fireball-staff") {
		t.Errorf("the refusal names the role: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
