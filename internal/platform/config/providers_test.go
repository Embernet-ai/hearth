package config

import (
	"strings"
	"testing"
)

func TestTheMicrosoftPresetNeedsATenantAndTakesItsClientFromTheConnectorsRegistration(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_OIDC_PROVIDER", "microsoft")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HEARTH_MICROSOFT_TENANT") {
		t.Fatalf("no tenant: %v", err)
	}
	t.Setenv("HEARTH_MICROSOFT_TENANT", "8f1c2b7e-0000-4a1d-9c3e-5d2f7a6b1c90")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "client id and secret") {
		t.Fatalf("no client: %v", err)
	}
	t.Setenv("HEARTH_MICROSOFT_CLIENT_ID", "cid")
	t.Setenv("HEARTH_MICROSOFT_CLIENT_SECRET", "sec")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SSOEnabled() || cfg.OIDCClientID != "cid" || cfg.OIDCIssuer != "" {
		t.Errorf("sso %v client %q issuer %q: the preset derives the issuer and borrows the registration", cfg.SSOEnabled(), cfg.OIDCClientID, cfg.OIDCIssuer)
	}
}

func TestTheGooglePresetNeedsADomain(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_OIDC_PROVIDER", "google")
	t.Setenv("HEARTH_GOOGLE_CLIENT_ID", "cid")
	t.Setenv("HEARTH_GOOGLE_CLIENT_SECRET", "sec")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HEARTH_GOOGLE_HOSTED_DOMAIN") {
		t.Fatalf("no domain: %v", err)
	}
	t.Setenv("HEARTH_GOOGLE_HOSTED_DOMAIN", "Example.com")
	cfg, err := Load()
	if err != nil || cfg.GoogleHostedDomain != "example.com" {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestAPresetOtherThanTheTwoIsRefusedAndTheGenericPathIsUnchanged(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_OIDC_PROVIDER", "okta")
	if _, err := Load(); err == nil {
		t.Fatal("an unknown preset was accepted")
	}
	t.Setenv("HEARTH_OIDC_PROVIDER", "")
	t.Setenv("HEARTH_OIDC_ISSUER", "https://idp.example.com")
	t.Setenv("HEARTH_OIDC_CLIENT_ID", "a")
	t.Setenv("HEARTH_OIDC_CLIENT_SECRET", "b")
	cfg, err := Load()
	if err != nil || !cfg.SSOEnabled() {
		t.Fatalf("the generic path broke: %v", err)
	}
	t.Setenv("HEARTH_OIDC_CLIENT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("a partial generic configuration was accepted")
	}
}

func TestTheRoleMapAndTheConnectorsIntervalAreChecked(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_OIDC_ROLE_MAP", "grp-audit=audit, grp-fin = finance")
	t.Setenv("HEARTH_CONNECTORS_INTERVAL", "15m")
	cfg, err := Load()
	if err != nil || cfg.OIDCRoleMap["grp-audit"] != "audit" || cfg.OIDCRoleMap["grp-fin"] != "finance" || cfg.ConnectorsEvery.Minutes() != 15 {
		t.Fatalf("%+v %v", cfg, err)
	}
	t.Setenv("HEARTH_OIDC_ROLE_MAP", "nonsense")
	if _, err := Load(); err == nil {
		t.Error("a role map entry with no = was accepted")
	}
	t.Setenv("HEARTH_OIDC_ROLE_MAP", "")
	t.Setenv("HEARTH_CONNECTORS_INTERVAL", "5s")
	if _, err := Load(); err == nil {
		t.Error("an interval of seconds was accepted: the providers would be hammered")
	}
}
