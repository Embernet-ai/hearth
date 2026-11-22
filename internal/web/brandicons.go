package web

// The brand icons at addresses a browser or a phone asks for without being told.
//
// The shell links them, the manifest lists them, and /favicon.ico and /apple-touch-icon.png are fetched by
// convention. They are served outside the session wall: the sign-in page needs its icon, and a redirect to
// sign in is not an image. They are the one thing here that is not versioned by build, so they cache for a
// day, not a year.

import (
	"io/fs"
	"net/http"
)

// brandIcons maps each public address to its file in static/.
var brandIcons = map[string]string{
	"/favicon.ico":           "favicon.ico",
	"/favicon.svg":           "favicon.svg",
	"/icon.svg":              "icon.svg",
	"/vord.svg":              "vord.svg",
	"/icon-maskable.svg":     "icon-maskable.svg",
	"/apple-touch-icon.png":  "apple-touch-icon.png",
	"/icon-192.png":          "icon-192.png",
	"/icon-512.png":          "icon-512.png",
	"/icon-maskable-512.png": "icon-maskable-512.png",
}

func brandIcon(static fs.FS, file string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := fs.ReadFile(static, file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case file == "favicon.ico":
			w.Header().Set("Content-Type", "image/x-icon")
		case len(file) > 4 && file[len(file)-4:] == ".png":
			w.Header().Set("Content-Type", "image/png")
		default:
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(raw)
	})
}
