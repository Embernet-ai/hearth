package config

import (
	"strings"
	"testing"
)

// validEnv sets everything required, so each test can then invalidate exactly
// the one thing it is about.
func validEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HEARTH_PUBLIC_BASE_URL", "https://hearth.example.com")
	t.Setenv("HEARTH_DATABASE_URL", "postgres://u:p@db:5432/hearth")
	t.Setenv("HEARTH_S3_ENDPOINT", "minio:9000")
	t.Setenv("HEARTH_S3_PUBLIC_ENDPOINT", "files.hearth.example.com")
	t.Setenv("HEARTH_S3_ACCESS_KEY", "key")
	t.Setenv("HEARTH_S3_SECRET_KEY", "secret")
}

func TestLoadRequiresPublicBaseURL(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_PUBLIC_BASE_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected an error when HEARTH_PUBLIC_BASE_URL is unset")
	}
	if !strings.Contains(err.Error(), "HEARTH_PUBLIC_BASE_URL is required") {
		t.Fatalf("error should name the missing variable, got: %v", err)
	}
}

func TestLoadReportsEveryErrorAtOnce(t *testing.T) {
	// An operator fixing a deployment should get the whole list, not one
	// missing variable per restart.
	validEnv(t)
	t.Setenv("HEARTH_PUBLIC_BASE_URL", "")
	t.Setenv("HEARTH_DATABASE_URL", "")
	t.Setenv("HEARTH_ENV", "banana")
	t.Setenv("HEARTH_LOG_FORMAT", "morse")
	t.Setenv("HEARTH_SHUTDOWN_TIMEOUT", "ages")

	_, err := Load()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		"HEARTH_PUBLIC_BASE_URL",
		"HEARTH_DATABASE_URL",
		"HEARTH_ENV",
		"HEARTH_LOG_FORMAT",
		"HEARTH_SHUTDOWN_TIMEOUT",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the joined error to mention %s, got:\n%v", want, err)
		}
	}
}

func TestLoadRejectsRelativeBaseURL(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_PUBLIC_BASE_URL", "/hearth")

	_, err := Load()
	if err == nil {
		t.Fatal("expected a relative URL to be rejected")
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_DATABASE_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected an error when HEARTH_DATABASE_URL is unset")
	}
	if !strings.Contains(err.Error(), "HEARTH_DATABASE_URL is required") {
		t.Fatalf("error should name the missing variable, got: %v", err)
	}
}

func TestLoadRejectsNonPostgresDatabaseURL(t *testing.T) {
	validEnv(t)
	// The commonest paste mistake is a bare hostname or a JDBC URL. Accepting
	// it silently produces a connection error much later and further away.
	t.Setenv("HEARTH_DATABASE_URL", "jdbc:postgresql://db:5432/hearth")

	if _, err := Load(); err == nil {
		t.Fatal("expected a JDBC-style URL to be rejected")
	}
}

func TestLoadDefaultsAndTrimsTrailingSlash(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_PUBLIC_BASE_URL", "https://hearth.example.com/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.PublicBaseURL.String(); got != "https://hearth.example.com" {
		t.Errorf("trailing slash should be trimmed, got %q", got)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("default Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.Env != "development" {
		t.Errorf("default Env = %q, want development", cfg.Env)
	}
	if cfg.IsProduction() {
		t.Error("development must not report as production")
	}
}

// The guard that prevents the predecessor's dead-download bug from shipping.
func TestProductionRejectsIdenticalS3Endpoints(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_ENV", "production")
	// The mistake: pointing browsers at the in-cluster address. Every presigned
	// URL would then be valid and unreachable, failing with nothing in any log.
	t.Setenv("HEARTH_S3_ENDPOINT", "minio:9000")
	t.Setenv("HEARTH_S3_PUBLIC_ENDPOINT", "minio:9000")

	_, err := Load()
	if err == nil {
		t.Fatal("production accepted a public S3 endpoint identical to the internal one")
	}
	if !strings.Contains(err.Error(), "HEARTH_S3_PUBLIC_ENDPOINT") {
		t.Errorf("error should name the variable, got: %v", err)
	}
}

// Development is allowed to point both at the same place, because there is only
// one address and the browser is on the same host.
func TestDevelopmentAllowsIdenticalS3Endpoints(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_ENV", "development")
	t.Setenv("HEARTH_S3_ENDPOINT", "127.0.0.1:9000")
	t.Setenv("HEARTH_S3_PUBLIC_ENDPOINT", "127.0.0.1:9000")

	if _, err := Load(); err != nil {
		t.Fatalf("development should allow identical endpoints: %v", err)
	}
}

func TestS3EndpointRejectsAScheme(t *testing.T) {
	validEnv(t)
	// The SDK treats a scheme as part of the hostname and signs for a host that
	// does not exist, which fails in a way that looks nothing like the cause.
	t.Setenv("HEARTH_S3_ENDPOINT", "http://minio:9000")

	if _, err := Load(); err == nil {
		t.Fatal("an endpoint with a scheme was accepted")
	}
}

func TestS3CredentialsAreRequired(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_S3_ACCESS_KEY", "")
	t.Setenv("HEARTH_S3_SECRET_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("missing S3 credentials were accepted")
	}
	for _, want := range []string{"HEARTH_S3_ACCESS_KEY", "HEARTH_S3_SECRET_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s, got: %v", want, err)
		}
	}
}

// The region is read as given, and empty means the client's own default
// (us-east-1, MinIO's). Garage only answers requests signed for its region.
func TestS3RegionIsReadAndOptional(t *testing.T) {
	validEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.S3Region != "" {
		t.Errorf("no HEARTH_S3_REGION set, yet the region is %q", cfg.S3Region)
	}

	t.Setenv("HEARTH_S3_REGION", "garage")
	if cfg, err = Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.S3Region != "garage" {
		t.Errorf("HEARTH_S3_REGION=garage read as %q", cfg.S3Region)
	}
}

func emberopsEnv(t *testing.T) {
	t.Helper()
	validEnv(t)
	t.Setenv("HEARTH_BACKEND", "emberops")
	t.Setenv("HEARTH_EMBEROPS_URL", "https://ops.example.com/")
	t.Setenv("HEARTH_EMBEROPS_CLIENT_ID", "hearth-service")
	t.Setenv("HEARTH_EMBEROPS_CLIENT_SECRET", "s3cret")
	t.Setenv("HEARTH_OIDC_ISSUER", "https://vord.example.com/realms/fireball")
	t.Setenv("HEARTH_OIDC_CLIENT_ID", "hearth")
	t.Setenv("HEARTH_OIDC_CLIENT_SECRET", "x")
	t.Setenv("HEARTH_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
}

func TestBackendDefaultsToPostgres(t *testing.T) {
	validEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != BackendPostgres || cfg.EmberOPSBackend() {
		t.Fatalf("default backend = %q", cfg.Backend)
	}
}

func TestBackendEmberOPSValid(t *testing.T) {
	emberopsEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EmberOPSBackend() || cfg.EmberOPSURL.String() != "https://ops.example.com" {
		t.Fatalf("got %q %v", cfg.Backend, cfg.EmberOPSURL)
	}
	if cfg.EmberOPSAudience != "emberops" {
		t.Fatalf("audience = %q", cfg.EmberOPSAudience)
	}
}

func TestBackendRejectsInvalidCombinations(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T)
		want   string
	}{
		"unknown backend": {func(t *testing.T) { t.Setenv("HEARTH_BACKEND", "mongo") }, "HEARTH_BACKEND must be"},
		"no url":          {func(t *testing.T) { t.Setenv("HEARTH_EMBEROPS_URL", "") }, "HEARTH_EMBEROPS_URL is required"},
		"relative url":    {func(t *testing.T) { t.Setenv("HEARTH_EMBEROPS_URL", "ops.example.com") }, "absolute URL"},
		"http in prod": {func(t *testing.T) {
			t.Setenv("HEARTH_ENV", "production")
			t.Setenv("HEARTH_EMBEROPS_URL", "http://ops.example.com")
		}, "must be https in production"},
		"no client": {func(t *testing.T) { t.Setenv("HEARTH_EMBEROPS_CLIENT_SECRET", "") }, "HEARTH_EMBEROPS_CLIENT_ID and"},
		"no sso": {func(t *testing.T) {
			t.Setenv("HEARTH_OIDC_ISSUER", "")
			t.Setenv("HEARTH_OIDC_CLIENT_ID", "")
			t.Setenv("HEARTH_OIDC_CLIENT_SECRET", "")
		}, "needs single sign-on"},
		"no key":       {func(t *testing.T) { t.Setenv("HEARTH_ENCRYPTION_KEY", "") }, "HEARTH_ENCRYPTION_KEY is required"},
		"bad ttl":      {func(t *testing.T) { t.Setenv("HEARTH_EMBEROPS_CACHE_TTL", "1h") }, "HEARTH_EMBEROPS_CACHE_TTL"},
		"bad currency": {func(t *testing.T) { t.Setenv("HEARTH_EMBEROPS_CURRENCY_PER_CENT", "-1") }, "HEARTH_EMBEROPS_CURRENCY_PER_CENT"},
		"audience":     {func(t *testing.T) { t.Setenv("HEARTH_EMBEROPS_AUDIENCE", "hearth") }, "must not be the browser login"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			emberopsEnv(t)
			c.mutate(t)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestBackendPostgresRejectsEmberOPSSettings(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_EMBEROPS_URL", "https://ops.example.com")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "HEARTH_EMBEROPS_URL is set but HEARTH_BACKEND") {
		t.Fatalf("got %v", err)
	}
}
