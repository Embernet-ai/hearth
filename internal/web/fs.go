package web

import (
	"io/fs"
)

// fsSub narrows an embedded filesystem to a subdirectory, so URLs do not carry
// the embed path.
func fsSub(f fs.FS, dir string) (fs.FS, error) { return fs.Sub(f, dir) }
