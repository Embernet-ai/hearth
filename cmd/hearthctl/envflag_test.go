package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Help never prints a flag's value taken from the environment, on any subcommand's flag set.
func TestEnvFlagKeepsTheSecretOutOfHelp(t *testing.T) {
	const secret = "postgres://hearth:hunter2@db.example/hearth"
	t.Setenv("HEARTH_DATABASE_URL", secret)
	t.Setenv("CLICKUP_TOKEN", "pk_hunter2_token")
	var out bytes.Buffer
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(&out)
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres URL, or HEARTH_DATABASE_URL")
	tok := envFlag(fs, "token", "CLICKUP_TOKEN", "ClickUp token, or CLICKUP_TOKEN")
	if *dsn != secret || *tok != "pk_hunter2_token" {
		t.Fatalf("the environment is not the fallback: %q %q", *dsn, *tok)
	}
	fs.PrintDefaults()
	if h := out.String(); strings.Contains(h, "hunter2") || strings.Contains(h, "default") || !strings.Contains(h, "HEARTH_DATABASE_URL") {
		t.Fatalf("help leaks or loses the variable name:\n%s", h)
	}
	if err := fs.Parse([]string{"-dsn", "postgres://other"}); err != nil || *dsn != "postgres://other" {
		t.Fatalf("a flag on the command line must win: %q %v", *dsn, err)
	}
}

// Every flag that falls back to the environment goes through envFlag, so no subcommand's -h can print a secret: a flag
// default read straight from the environment is what leaked the database URL.
func TestNoFlagDefaultReadsTheEnvironmentDirectly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`fs\.(String|Var)\([^\n]*os\.Getenv`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for range direct.FindAllStringIndex(src, -1) {
			t.Errorf("%s: a flag default is read from the environment; use envFlag", f)
		}
	}
}
