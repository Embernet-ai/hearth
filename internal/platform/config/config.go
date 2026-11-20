// Package config loads every setting from the environment exactly once, at
// startup, and fails loudly if anything required is missing.
//
// Failing at boot is the whole point. The predecessor system had a class of bug
// where a missing or wrong value produced a process that started, reported
// healthy, and then did the wrong thing silently: a queue name used as a queue
// URL, an object-store client with no endpoint policy, a mail transport with no
// authentication. Every one of those was a value that was absent or wrong at
// startup and never checked.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole configuration surface. There is no global, and nothing
// reads os.Getenv outside this package.
type Config struct {
	Env             string
	Addr            string
	PublicBaseURL   *url.URL
	ShutdownTimeout time.Duration
	LogLevel        string
	LogFormat       string

	// DatabaseURL is the Postgres DSN. Required: there is no useful behaviour
	// without it, so the process refuses to start rather than coming up and
	// failing on the first request.
	DatabaseURL string

	// Object storage. S3Endpoint is how this process reaches it;
	// S3PublicEndpoint is how a BROWSER does. They are not interchangeable and
	// in production they must differ. See internal/platform/blob.
	S3Endpoint       string
	S3PublicEndpoint string
	S3AccessKey      string
	S3SecretKey      string
	S3Bucket         string
	// S3Region is what requests are signed for. Empty keeps the store
	// client's default, us-east-1, which is what MinIO answers to. Garage
	// refuses a request signed for any region but its own, so it must be set
	// to Garage's (the chart reads it from Garage's key Secret).
	S3Region string
	S3UseSSL bool
	// S3PathStyle forces /bucket/key addressing. Left off, the client picks per
	// endpoint: virtual-host style for AWS, path style for everything else.
	S3PathStyle    bool
	S3PublicUseSSL bool

	// Background work. The worker runs inside this binary rather than as a
	// second image, because two images is two builds, two deploys, and two
	// chances for the running versions to disagree about what a payload means.
	//
	// The flag exists so a replica can be told not to run jobs, which is how a
	// deployment scales the web tier without multiplying the workers. It is on
	// by default: a deployment that quietly processes nothing looks healthy
	// from the outside and is the worst possible default.
	WorkerEnabled     bool
	WorkerConcurrency int
	WorkerLease       time.Duration

	// Search. OpenSearch is OPTIONAL, and that is a design decision rather than
	// a convenience: Postgres full text is the floor and it cannot be down
	// while the application is up. With no cluster configured the product still
	// searches, and a small deployment does not have to run a JVM.
	OpenSearchURL      string
	OpenSearchIndex    string
	OpenSearchUsername string
	OpenSearchPassword string

	// Email, through the estate's own relay. OPTIONAL: with no host, nothing
	// is sent and the inbox still works. A transactional email provider is a
	// hyperscaler with a nicer name, and it would hold a copy of every
	// notification anybody here ever received.
	// InboundKey authenticates the estate's MTA on the one endpoint a machine
	// talks to. Empty disables inbound mail entirely.
	InboundKey string

	// EncryptionKey seals the few values that must be read back rather than
	// hashed. Today that is one thing: a Google refresh token, which Google
	// needs verbatim months after issuing it.
	//
	// base64, 32 bytes decoded. Absent is a supported configuration and
	// disables the integrations that need it, rather than storing a long-lived
	// third-party credential in the clear.
	EncryptionKey string

	// Version is the release name this binary was deployed as.
	//
	// buildinfo deliberately reads Go's VCS stamps rather than ldflags so the
	// revision cannot drift from the commit. That is right and it does not
	// answer "which release is this", because a commit hash is not a release.
	//
	// The chart passes the image tag it used to pull the image, so this is the
	// name of the thing actually running rather than a string baked in at build
	// time and hoped about. Empty falls back to the revision.
	Version string

	// Google, per user. Empty disables it entirely.
	GoogleClientID     string
	GoogleClientSecret string
	AgentsSuggestOnly  bool
	SMTPHost           string
	SMTPFrom           string
	SMTPUsername       string
	SMTPPassword       string
	SMTPStartTLS       bool

	// Single sign-on. Generic OIDC: nothing here names a vendor, because an
	// identity provider is an identity provider. Optional, and local accounts
	// keep working whether or not it is configured.
	OIDCIssuer         string
	OIDCClientID       string
	OIDCClientSecret   string
	OIDCScopes         []string
	OIDCAllowedDomains []string
	// OIDCRequiredRoles gates sign-in on a role or group the provider asserts.
	// A domain says which company somebody is from; this says whether they
	// belong in this application, and on a shared identity provider those are
	// not the same question.
	OIDCRequiredRoles []string

	// The assistant connector: people connecting an assistant to Hearth sign in with Vord
	// (internal/mcpserver). MCPAudience is the audience their access token
	// must carry -- the Vord client scope "hearth-mcp" -- and empty turns
	// person sign-in off (agent tokens keep working). MCPPublicURL is where
	// clients are told this resource lives; empty means PublicBaseURL.
	// MCPRequiredRole is the realm role a person needs to reach it at all.
	MCPAudience     string
	MCPPublicURL    *url.URL
	MCPRequiredRole string

	// TrustedProxies are the addresses and ranges of the ingress or load balancer
	// in front of Hearth (HEARTH_TRUSTED_PROXIES). The rate limits key on the
	// client address in X-Forwarded-For only when the TCP peer is one of these.
	// Empty, the default, keys on the socket address as it always has.
	TrustedProxies []string

	// Backend selects where operational data lives. "postgres" (the default)
	// is the standalone product, unchanged. "emberops" keeps tasks, work
	// orders, documents, time and the rest in EmberOPS (Frappe/ERPNext) over
	// its REST API, and leaves what EmberOPS does not model (channels, mail,
	// sessions, tokens, the search index, audit) in Hearth's own Postgres.
	// See docs/emberops-backend.md.
	Backend string
	// EmberOPSURL is the site root, e.g. https://ops.example.com.
	EmberOPSURL *url.URL
	// EmberOPSAudience is the audience a Vord token must carry for EmberOPS's
	// emberops_vord addon to accept it. A signed-in person's token is requested
	// with it, so their own permissions apply in EmberOPS.
	EmberOPSAudience string
	// EmberOPSClientID and EmberOPSClientSecret are a Vord service client
	// (client credentials) mapped to a Frappe service user. Used only where no
	// person is present: importers, rules, scheduled work.
	EmberOPSClientID     string
	EmberOPSClientSecret string
	// EmberOPSCacheTTL bounds the shared cache for slow-changing reads.
	EmberOPSCacheTTL time.Duration
	// EmberOPSTimezone is the EmberOPS site's time zone. Frappe stores naive
	// local datetimes, so Hearth's UTC instants are converted to and from it.
	EmberOPSTimezone *time.Location
	// EmberOPSCurrencyPerCent is the value of one cent in the site's currency:
	// Hearth keeps money in cents, ERPNext in currency units. 0.01 for dollars.
	EmberOPSCurrencyPerCent float64
	// EmberOPSCRMParties is where companies and contacts live in emberops mode:
	// postgres (the default, which keeps the sales side) or emberops (which
	// turns it off). Set with HEARTH_EMBEROPS_CRM_PARTIES.
	EmberOPSCRMParties string
}

// Backend names.
const (
	BackendPostgres = "postgres"
	BackendEmberOPS = "emberops"
)

// EmberOPSBackend reports whether operational data is stored in EmberOPS.
func (c *Config) EmberOPSBackend() bool { return c.Backend == BackendEmberOPS }

// Load reads the environment and returns a validated Config, or every error it
// found at once.
//
// Collecting all errors rather than returning the first is deliberate. An
// operator fixing a deployment wants the whole list, not one missing variable
// per restart.
func Load() (*Config, error) {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	cfg := &Config{
		Env:       envOr("HEARTH_ENV", "development"),
		Addr:      envOr("HEARTH_ADDR", ":8080"),
		LogLevel:  envOr("HEARTH_LOG_LEVEL", "info"),
		LogFormat: envOr("HEARTH_LOG_FORMAT", "json"),
	}

	switch cfg.Env {
	case "development", "staging", "production":
	default:
		fail("HEARTH_ENV must be development, staging, or production, got %q", cfg.Env)
	}

	switch cfg.LogFormat {
	case "json", "text":
	default:
		fail("HEARTH_LOG_FORMAT must be json or text, got %q", cfg.LogFormat)
	}

	// The public base URL is required and must be absolute. The predecessor
	// hardcoded http://localhost:{port} into every browser-facing URL it
	// generated, which is dead on any real hostname and produced links that
	// only failed once a user clicked them.
	raw := os.Getenv("HEARTH_PUBLIC_BASE_URL")
	switch {
	case raw == "":
		fail("HEARTH_PUBLIC_BASE_URL is required (e.g. https://hearth.example.com)")
	default:
		u, err := url.Parse(raw)
		switch {
		case err != nil:
			fail("HEARTH_PUBLIC_BASE_URL is not a valid URL: %w", err)
		case u.Scheme != "http" && u.Scheme != "https":
			fail("HEARTH_PUBLIC_BASE_URL must be http or https, got %q", u.Scheme)
		case u.Host == "":
			fail("HEARTH_PUBLIC_BASE_URL has no host: %q", raw)
		default:
			u.Path = strings.TrimSuffix(u.Path, "/")
			cfg.PublicBaseURL = u
		}
	}

	cfg.DatabaseURL = os.Getenv("HEARTH_DATABASE_URL")
	switch {
	case cfg.DatabaseURL == "":
		fail("HEARTH_DATABASE_URL is required (e.g. postgres://user:pass@host:5432/hearth)")
	case !strings.HasPrefix(cfg.DatabaseURL, "postgres://") &&
		!strings.HasPrefix(cfg.DatabaseURL, "postgresql://"):
		// A DSN in libpq keyword form also works, but accepting it silently
		// hides the far commoner mistake of pasting a hostname or a JDBC URL.
		fail("HEARTH_DATABASE_URL must start with postgres:// or postgresql://")
	}

	cfg.S3Endpoint = os.Getenv("HEARTH_S3_ENDPOINT")
	cfg.S3PublicEndpoint = os.Getenv("HEARTH_S3_PUBLIC_ENDPOINT")
	cfg.S3AccessKey = os.Getenv("HEARTH_S3_ACCESS_KEY")
	cfg.S3SecretKey = os.Getenv("HEARTH_S3_SECRET_KEY")
	cfg.S3Bucket = envOr("HEARTH_S3_BUCKET", "hearth")
	cfg.S3Region = os.Getenv("HEARTH_S3_REGION")
	cfg.S3UseSSL = envBool("HEARTH_S3_USE_SSL", false)
	cfg.S3PathStyle = envBool("HEARTH_S3_PATH_STYLE", false)
	cfg.S3PublicUseSSL = envBool("HEARTH_S3_PUBLIC_USE_SSL", true)

	for name, v := range map[string]string{
		"HEARTH_S3_ENDPOINT":        cfg.S3Endpoint,
		"HEARTH_S3_PUBLIC_ENDPOINT": cfg.S3PublicEndpoint,
		"HEARTH_S3_ACCESS_KEY":      cfg.S3AccessKey,
		"HEARTH_S3_SECRET_KEY":      cfg.S3SecretKey,
	} {
		if v == "" {
			fail("%s is required", name)
		}
	}

	// An endpoint carrying a scheme is the commonest mistake here, and the SDK
	// treats it as part of the hostname, producing a signature for a host that
	// does not exist.
	for name, v := range map[string]string{
		"HEARTH_S3_ENDPOINT":        cfg.S3Endpoint,
		"HEARTH_S3_PUBLIC_ENDPOINT": cfg.S3PublicEndpoint,
	} {
		if strings.Contains(v, "://") {
			fail("%s must be host:port with no scheme, got %q", name, v)
		}
	}

	// THE guard. In production these must differ.
	//
	// If the public endpoint equals the in-cluster one, every presigned URL
	// handed to a browser points at a hostname the browser cannot resolve. The
	// URL is cryptographically valid, the request never arrives, and nothing
	// appears in any log because no server is ever contacted. That is exactly
	// how the predecessor shipped a build where every upload succeeded and
	// every download was dead, and it went unnoticed because uploads report
	// success either way.
	if cfg.Env == "production" && cfg.S3Endpoint == cfg.S3PublicEndpoint {
		fail("HEARTH_S3_PUBLIC_ENDPOINT must differ from HEARTH_S3_ENDPOINT in production: " +
			"presigned URLs are handed to browsers and must not point at an in-cluster address")
	}

	cfg.WorkerEnabled = envBool("HEARTH_WORKER_ENABLED", true)
	cfg.WorkerConcurrency = envInt("HEARTH_WORKER_CONCURRENCY", 4)
	if cfg.WorkerConcurrency < 1 || cfg.WorkerConcurrency > 64 {
		fail("HEARTH_WORKER_CONCURRENCY must be between 1 and 64, got %d", cfg.WorkerConcurrency)
	}
	if d, err := time.ParseDuration(envOr("HEARTH_WORKER_LEASE", "5m")); err != nil {
		fail("HEARTH_WORKER_LEASE is not a valid duration: %w", err)
	} else if d < 30*time.Second {
		// Shorter than the slowest legitimate job and a healthy worker gets its
		// job taken out from under it while it is still running, which turns
		// one slow reindex into two and then four.
		fail("HEARTH_WORKER_LEASE must be at least 30s, got %s", d)
	} else {
		cfg.WorkerLease = d
	}

	cfg.OpenSearchURL = strings.TrimSpace(os.Getenv("HEARTH_OPENSEARCH_URL"))
	cfg.OpenSearchIndex = envOr("HEARTH_OPENSEARCH_INDEX", "hearth")
	cfg.OpenSearchUsername = os.Getenv("HEARTH_OPENSEARCH_USERNAME")
	cfg.OpenSearchPassword = os.Getenv("HEARTH_OPENSEARCH_PASSWORD")
	if cfg.OpenSearchURL != "" && !strings.HasPrefix(cfg.OpenSearchURL, "http://") &&
		!strings.HasPrefix(cfg.OpenSearchURL, "https://") {
		// Unlike the object store endpoint, this one IS a URL. The two
		// conventions differ because their clients do, and mixing them up
		// produces a client that signs requests for a host that does not exist.
		fail("HEARTH_OPENSEARCH_URL must include a scheme, got %q", cfg.OpenSearchURL)
	}

	// The shared secret the estate's MTA presents on the inbound endpoint.
	// Empty disables inbound mail, which is the right default: a path that
	// files anything it is sent as a work order is not a path to leave open,
	// and a deployment that wants it will say so.
	cfg.InboundKey = strings.TrimSpace(os.Getenv("HEARTH_INBOUND_KEY"))
	cfg.EncryptionKey = strings.TrimSpace(os.Getenv("HEARTH_ENCRYPTION_KEY"))
	cfg.Version = strings.TrimSpace(os.Getenv("HEARTH_VERSION"))
	cfg.GoogleClientID = strings.TrimSpace(os.Getenv("HEARTH_GOOGLE_CLIENT_ID"))
	cfg.GoogleClientSecret = os.Getenv("HEARTH_GOOGLE_CLIENT_SECRET")

	// The deployment-wide safety catch on agents. On means every agent write
	// becomes a suggestion regardless of what its token was given, for a site
	// that wants agents reading and proposing and nothing else. Defaults to
	// off, because a token that says write and does not is worse than either.
	cfg.AgentsSuggestOnly = envBool("HEARTH_AGENTS_SUGGEST_ONLY", false)

	cfg.SMTPHost = strings.TrimSpace(os.Getenv("HEARTH_SMTP_HOST"))
	cfg.SMTPFrom = strings.TrimSpace(os.Getenv("HEARTH_SMTP_FROM"))
	cfg.SMTPUsername = os.Getenv("HEARTH_SMTP_USERNAME")
	cfg.SMTPPassword = os.Getenv("HEARTH_SMTP_PASSWORD")
	cfg.SMTPStartTLS = envBool("HEARTH_SMTP_STARTTLS", true)
	if cfg.SMTPHost != "" {
		if !strings.Contains(cfg.SMTPHost, ":") {
			// A relay on the wrong port fails at send time, hours later, in a
			// job nobody is watching. Say it now.
			fail("HEARTH_SMTP_HOST must be host:port, got %q", cfg.SMTPHost)
		}
		if cfg.SMTPFrom == "" {
			fail("HEARTH_SMTP_FROM is required when HEARTH_SMTP_HOST is set")
		}
	}

	cfg.OIDCIssuer = strings.TrimSpace(os.Getenv("HEARTH_OIDC_ISSUER"))
	cfg.OIDCClientID = strings.TrimSpace(os.Getenv("HEARTH_OIDC_CLIENT_ID"))
	cfg.OIDCClientSecret = os.Getenv("HEARTH_OIDC_CLIENT_SECRET")
	cfg.OIDCScopes = splitList(os.Getenv("HEARTH_OIDC_SCOPES"))
	cfg.OIDCAllowedDomains = splitList(os.Getenv("HEARTH_OIDC_ALLOWED_DOMAINS"))
	cfg.OIDCRequiredRoles = splitList(os.Getenv("HEARTH_OIDC_REQUIRED_ROLES"))

	// All three together, or none. A PARTIAL configuration is rejected rather
	// than half-applied: a half-configured provider fails at login time rather
	// than at startup, and "SSO is set up and every login fails" is a far worse
	// place to discover a typo than "SSO is not set up".
	set := 0
	for _, v := range []string{cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret} {
		if v != "" {
			set++
		}
	}
	if set > 0 && set < 3 {
		fail("HEARTH_OIDC_ISSUER, HEARTH_OIDC_CLIENT_ID and HEARTH_OIDC_CLIENT_SECRET " +
			"must all be set together, or none of them")
	}
	cfg.TrustedProxies = splitList(os.Getenv("HEARTH_TRUSTED_PROXIES"))
	cfg.MCPAudience = strings.TrimSpace(os.Getenv("HEARTH_MCP_AUDIENCE"))
	cfg.MCPRequiredRole = strings.TrimSpace(os.Getenv("HEARTH_MCP_REQUIRED_ROLE"))
	cfg.MCPPublicURL = cfg.PublicBaseURL
	if v := strings.TrimSpace(os.Getenv("HEARTH_MCP_PUBLIC_URL")); v != "" {
		u, err := url.Parse(strings.TrimSuffix(v, "/"))
		if err != nil || !u.IsAbs() {
			fail("HEARTH_MCP_PUBLIC_URL must be an absolute URL, got %q", v)
		} else {
			cfg.MCPPublicURL = u
		}
	}
	if cfg.MCPAudience != "" && cfg.OIDCIssuer == "" {
		fail("HEARTH_MCP_AUDIENCE needs HEARTH_OIDC_ISSUER: tokens are checked against that issuer")
	}
	if cfg.MCPAudience != "" && cfg.MCPAudience == cfg.OIDCClientID {
		// The browser login's client is not a resource. A token minted for the
		// login page must not double as a key to the connector endpoint.
		fail("HEARTH_MCP_AUDIENCE must not be the browser login's client id (%q)", cfg.OIDCClientID)
	}
	if cfg.MCPRequiredRole != "" && cfg.MCPAudience == "" {
		fail("HEARTH_MCP_REQUIRED_ROLE is set but HEARTH_MCP_AUDIENCE is not, so nobody can sign in to check it")
	}

	if cfg.OIDCIssuer != "" && !strings.HasPrefix(cfg.OIDCIssuer, "https://") &&
		cfg.Env == "production" {
		fail("HEARTH_OIDC_ISSUER must be https in production, got %q", cfg.OIDCIssuer)
	}

	loadBackend(cfg, fail)

	d, err := time.ParseDuration(envOr("HEARTH_SHUTDOWN_TIMEOUT", "20s"))
	if err != nil {
		fail("HEARTH_SHUTDOWN_TIMEOUT is not a valid duration: %w", err)
	} else if d <= 0 {
		fail("HEARTH_SHUTDOWN_TIMEOUT must be positive, got %s", d)
	} else {
		cfg.ShutdownTimeout = d
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// loadBackend reads and checks the storage back end settings. An invalid
// combination fails here, at startup, rather than on the first request that
// reaches for a store that was never configured.
func loadBackend(cfg *Config, fail func(string, ...any)) {
	cfg.Backend = strings.ToLower(strings.TrimSpace(envOr("HEARTH_BACKEND", BackendPostgres)))
	raw := strings.TrimSpace(os.Getenv("HEARTH_EMBEROPS_URL"))
	cfg.EmberOPSAudience = envOr("HEARTH_EMBEROPS_AUDIENCE", "emberops")
	cfg.EmberOPSClientID = strings.TrimSpace(os.Getenv("HEARTH_EMBEROPS_CLIENT_ID"))
	cfg.EmberOPSClientSecret = os.Getenv("HEARTH_EMBEROPS_CLIENT_SECRET")

	switch cfg.Backend {
	case BackendPostgres:
		// Settings for a back end that is not selected are a mistake worth
		// stopping on: someone believes the switch is made and it is not.
		for k, v := range map[string]string{
			"HEARTH_EMBEROPS_URL":           raw,
			"HEARTH_EMBEROPS_CLIENT_ID":     cfg.EmberOPSClientID,
			"HEARTH_EMBEROPS_CLIENT_SECRET": cfg.EmberOPSClientSecret,
		} {
			if v != "" {
				fail("%s is set but HEARTH_BACKEND is %q; set HEARTH_BACKEND=emberops or unset it", k, BackendPostgres)
			}
		}
		return
	case BackendEmberOPS:
	default:
		fail("HEARTH_BACKEND must be %s or %s, got %q", BackendPostgres, BackendEmberOPS, cfg.Backend)
		return
	}

	if raw == "" {
		fail("HEARTH_EMBEROPS_URL is required when HEARTH_BACKEND=emberops (e.g. https://ops.example.com)")
	} else if u, err := url.Parse(strings.TrimSuffix(raw, "/")); err != nil || !u.IsAbs() || u.Host == "" {
		fail("HEARTH_EMBEROPS_URL must be an absolute URL, got %q", raw)
	} else if u.Scheme != "https" && u.Scheme != "http" {
		fail("HEARTH_EMBEROPS_URL must be http or https, got %q", raw)
	} else if u.Scheme != "https" && cfg.Env == "production" {
		fail("HEARTH_EMBEROPS_URL must be https in production, got %q", raw)
	} else {
		cfg.EmberOPSURL = u
	}

	if cfg.EmberOPSClientID == "" || cfg.EmberOPSClientSecret == "" {
		fail("HEARTH_EMBEROPS_CLIENT_ID and HEARTH_EMBEROPS_CLIENT_SECRET are required when " +
			"HEARTH_BACKEND=emberops: importers, rules and scheduled work have no signed-in person")
	}
	// The person's own Vord token is what EmberOPS authorises, so there must be
	// a Vord sign-in to take it from, and a key to keep it sealed at rest.
	if cfg.OIDCIssuer == "" || cfg.OIDCClientID == "" || cfg.OIDCClientSecret == "" {
		fail("HEARTH_BACKEND=emberops needs single sign-on (HEARTH_OIDC_*): EmberOps is called with the signed-in person's Vord token")
	}
	if cfg.EncryptionKey == "" {
		fail("HEARTH_ENCRYPTION_KEY is required when HEARTH_BACKEND=emberops: access and refresh tokens are kept sealed in the session")
	}
	if cfg.EmberOPSAudience == cfg.OIDCClientID {
		fail("HEARTH_EMBEROPS_AUDIENCE must not be the browser login's client id (%q)", cfg.OIDCClientID)
	}

	tzName := envOr("HEARTH_EMBEROPS_TIMEZONE", "UTC")
	if loc, err := time.LoadLocation(tzName); err != nil {
		fail("HEARTH_EMBEROPS_TIMEZONE is not a time zone: %q", tzName)
	} else {
		cfg.EmberOPSTimezone = loc
	}

	cfg.EmberOPSCurrencyPerCent = 0.01
	cfg.EmberOPSCRMParties = envOr("HEARTH_EMBEROPS_CRM_PARTIES", "postgres")
	if cfg.EmberOPSCRMParties != "postgres" && cfg.EmberOPSCRMParties != "emberops" {
		fail("HEARTH_EMBEROPS_CRM_PARTIES must be postgres or emberops, got %q", cfg.EmberOPSCRMParties)
	}
	if raw := strings.TrimSpace(os.Getenv("HEARTH_EMBEROPS_CURRENCY_PER_CENT")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 10 {
			fail("HEARTH_EMBEROPS_CURRENCY_PER_CENT must be a number above 0 and at most 10 (0.01 for dollars), got %q", raw)
		} else {
			cfg.EmberOPSCurrencyPerCent = v
		}
	}

	ttl, err := time.ParseDuration(envOr("HEARTH_EMBEROPS_CACHE_TTL", "5s"))
	switch {
	case err != nil:
		fail("HEARTH_EMBEROPS_CACHE_TTL is not a valid duration: %w", err)
	case ttl < 0 || ttl > time.Minute:
		fail("HEARTH_EMBEROPS_CACHE_TTL must be between 0 and 1m, got %s", ttl)
	default:
		cfg.EmberOPSCacheTTL = ttl
	}
}

// MailEnabled reports whether there is anywhere to send.
func (c *Config) MailEnabled() bool { return c.SMTPHost != "" && c.SMTPFrom != "" }

// SearchClusterEnabled reports whether an external search engine is configured.
func (c *Config) SearchClusterEnabled() bool { return c.OpenSearchURL != "" }

// SSOEnabled reports whether single sign-on is configured.
func (c *Config) SSOEnabled() bool {
	return c.OIDCIssuer != "" && c.OIDCClientID != "" && c.OIDCClientSecret != ""
}

// splitList parses a comma separated setting, ignoring blanks and spacing.
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsProduction reports whether guardrails that only matter on a real hostname
// should be active.
func (c *Config) IsProduction() bool { return c.Env == "production" }

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// envInt parses a whole-number setting. An unparseable value falls back to the
// default and is then range checked by the caller, so a typo produces a working
// deployment with a sane value rather than a pod that will not start.
func envInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// envBool parses a boolean setting, falling back rather than failing, because
// none of its callers are security relevant.
func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
