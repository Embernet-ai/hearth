// Package migrations embeds the SQL migration files into the binary.
//
// It lives in the migrations directory itself because go:embed patterns cannot
// traverse upward with "..", so the only way to embed these is from a package
// sitting alongside them.
//
// Embedding matters operationally: the binary carries its own schema, so there
// is no separate migration image, no init container, and no way for the two to
// be at different versions.
package migrations

import "embed"

// FS holds every migration, in version order by filename.
//
//go:embed *.sql
var FS embed.FS

// Dir is the path within FS. embed flattens to the package directory, so
// migrations sit at the root of this filesystem.
const Dir = "."
