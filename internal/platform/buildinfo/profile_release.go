//go:build !dev

package buildinfo

// profile is "release" unless the binary was built with -tags dev.
//
// This is the structural replacement for the predecessor's auth-gate CI job.
// There, a development feature flag returned the login code in the API response
// and dropped rate limiting, which on a public hostname is a full auth bypass,
// and CI had to grep every pushed image to prove the flag was absent.
//
// Here, a development build is a different artifact by construction, and
// /healthz says which one it is. The acceptance test asserts it against the
// thing that actually deployed.
const profile = "release"

// DevBuild reports whether this binary carries development affordances.
func DevBuild() bool { return false }
