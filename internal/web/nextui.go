package web

// The interface, served from inside the binary.
//
// internal/web/next/dist is embedded at build time. In a checkout it holds only a
// placeholder, so `go build` and the Go tests never need Node; the Containerfile builds
// ui/ (Astro and Svelte) and puts the result there before compiling. When no build is
// embedded, a page request says so rather than serving a blank page.
//
// It is one page with a client side router. Every address a person can open is a page
// of it: a browser navigation to /documents gets the shell, and the shell then asks
// that same address for its data (Accept: application/json). So a bookmark, a link in
// an email, or one written into another system by the CRM sync lands where it always
// did, and a reload on /contacts/{id} gets the shell back and not a 404. The hashed
// files under /_astro/ never change for a given name, so they cache for a year; the
// shell itself is always revalidated, which is what makes a new release show up on the
// next load instead of a stale shell pointing at bundles the new binary no longer has.

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:next/dist
var nextFS embed.FS

// built is the embedded build, rooted at its index.html.
func built() fs.FS {
	dist, err := fs.Sub(nextFS, "next/dist")
	if err != nil {
		panic(err) // the directory is embedded above; cannot happen
	}
	return dist
}

// shell is the page every address answers a browser with. It is read from the embed
// each time: the embed is in the binary, so this is a memory copy, and a test can swap
// the filesystem.
var shellFS = built

func shellHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	// The address answers with the page for a browser and with data for the interface, so a
	// shared cache must keep the two apart.
	w.Header().Add("Vary", "Accept")
}

// serveShell writes the interface's one page.
func serveShell(w http.ResponseWriter, r *http.Request) {
	index, err := fs.ReadFile(shellFS(), "index.html")
	if err != nil {
		http.Error(w, "The interface is not built into this binary.", http.StatusNotFound)
		return
	}
	shellHeaders(w)
	_, _ = w.Write(index)
}

// builtFiles serves the files the build produced next to the shell: the hashed bundles
// under /_astro/ and the one script the shell loads from the root.
func builtFiles() http.Handler {
	return serveBuilt(shellFS())
}

// serveBuilt serves a built interface out of dist. Split from builtFiles so a test can
// hand it a directory that has a build in it.
func serveBuilt(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if fi, err := fs.Stat(dist, rel); err == nil && !fi.IsDir() && rel != "index.html" {
			if strings.HasPrefix(rel, "_astro/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}
		// A missing hashed asset is a stale page asking for last release's bundle. Handing it the
		// shell would fail as a MIME error with no hint why; a 404 says what happened.
		http.NotFound(w, r)
	})
}

// movedFromNext sends an address from the first release of the interface, which lived under
// /next/, to the same address at the root. A permanent redirect that keeps the query.
func movedFromNext(w http.ResponseWriter, r *http.Request) {
	to := strings.TrimPrefix(r.URL.Path, "/next")
	if to == "" {
		to = "/"
	}
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusPermanentRedirect)
}
