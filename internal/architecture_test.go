package internal_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// The single rule that keeps one binary from becoming a mud ball:
// internal/platform is infrastructure and may not know about any feature.
//
// This is a test rather than a linter config on purpose. A lint warning gets
// ignored; a red test does not. The house rule is "verify the guard guards", so
// there is a companion test below that proves this one can actually fail.
const platformPrefix = "github.com/Embernet-ai/hearth/internal/platform/"

// featureRoots are the domain packages. platform may never import these.
var featureRoots = []string{
	"internal/auth", "internal/authz", "internal/orgs", "internal/channels",
	"internal/docs", "internal/tasks", "internal/crm", "internal/files",
	"internal/search", "internal/notify", "internal/mailint", "internal/calls", "internal/agents", "internal/canvas", "internal/api", "internal/importer",
	"internal/canvas", "internal/agents", "internal/ghsync", "internal/realtime",
	"internal/web", "internal/app", "internal/views", "internal/jobs", "internal/rules", "internal/notify", "internal/timelog",
	"internal/maint", "internal/plant", "internal/crm",
	"internal/mailint", "internal/calls",
}

type pkg struct {
	ImportPath string
	Imports    []string
	Deps       []string
}

func listPackages(t *testing.T) []pkg {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages, so this test proves nothing")
	}
	return pkgs
}

func TestPlatformDoesNotImportFeatures(t *testing.T) {
	for _, p := range listPackages(t) {
		if !strings.HasPrefix(p.ImportPath, platformPrefix) {
			continue
		}
		for _, imp := range p.Imports {
			for _, root := range featureRoots {
				want := "github.com/Embernet-ai/hearth/" + root
				if imp == want || strings.HasPrefix(imp, want+"/") {
					t.Errorf("%s imports %s\n"+
						"internal/platform is infrastructure and must not depend on a feature.\n"+
						"Invert it: declare the interface where it is consumed.",
						p.ImportPath, imp)
				}
			}
		}
	}
}

// TestArchitectureGuardCanFail proves the check above is wired to something
// real. If go list stops reporting imports, TestPlatformDoesNotImportFeatures
// would pass vacuously forever, which is the failure mode that makes a green
// suite worthless.
func TestArchitectureGuardCanFail(t *testing.T) {
	pkgs := listPackages(t)

	var sawPlatform, sawAnyImport bool
	for _, p := range pkgs {
		if strings.HasPrefix(p.ImportPath, platformPrefix) {
			sawPlatform = true
		}
		if len(p.Imports) > 0 {
			sawAnyImport = true
		}
	}
	if !sawPlatform {
		t.Fatal("no internal/platform packages found: the guard is inspecting nothing")
	}
	if !sawAnyImport {
		t.Fatal("go list reported no imports at all: the guard would pass vacuously")
	}
}
