//go:build dev

package buildinfo

// profile is "dev" when built with -tags dev.
//
// Anything guarded by DevBuild() must never be reachable in a release binary.
// The CI image job builds without this tag, and the acceptance test asserts
// /healthz reports "release" against the deployed pod.
const profile = "dev"

// DevBuild reports whether this binary carries development affordances.
func DevBuild() bool { return true }
