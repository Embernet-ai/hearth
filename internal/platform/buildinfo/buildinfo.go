// Package buildinfo reports what this binary actually is.
//
// It reads Go's embedded VCS stamps rather than taking values from ldflags, so
// the answer cannot drift from the commit that produced the binary.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"sync"
)

// Info describes the running binary.
type Info struct {
	Revision  string `json:"revision"`
	Time      string `json:"time"`
	Dirty     bool   `json:"dirty"`
	GoVersion string `json:"go_version"`

	// Profile is the build profile. It is reported by /healthz on purpose: it
	// lets the acceptance test assert, at runtime, that the thing actually
	// deployed is not a development build. Checking the artifact beats
	// grepping the source, because the source is not what shipped.
	Profile string `json:"profile"`
}

var (
	once sync.Once
	info Info
)

// Get returns the build information, computed once.
func Get() Info {
	once.Do(func() {
		info = Info{
			Revision:  "unknown",
			GoVersion: runtime.Version(),
			Profile:   profile,
		}
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Revision = s.Value
			case "vcs.time":
				info.Time = s.Value
			case "vcs.modified":
				info.Dirty = s.Value == "true"
			}
		}
	})
	return info
}
