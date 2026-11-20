package config

import "testing"

// A private install points at whatever S3 API it has: its own endpoint, region,
// TLS and addressing style all come from the environment.
func TestS3SettingsComeFromTheEnvironment(t *testing.T) {
	validEnv(t)
	t.Setenv("HEARTH_S3_REGION", "eu-central-1")
	t.Setenv("HEARTH_S3_USE_SSL", "true")
	t.Setenv("HEARTH_S3_PATH_STYLE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3Region != "eu-central-1" || !cfg.S3UseSSL || !cfg.S3PathStyle {
		t.Fatalf("region %q ssl %v pathstyle %v", cfg.S3Region, cfg.S3UseSSL, cfg.S3PathStyle)
	}
}

func TestS3PathStyleIsOffUnlessAsked(t *testing.T) {
	validEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3PathStyle {
		t.Fatal("path style should default to off")
	}
}
