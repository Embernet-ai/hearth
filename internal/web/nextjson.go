package web

// The data behind every page, as JSON.
//
// The interface (ui/, embedded in internal/web/next) is a Svelte app, and what it
// shows is decided here: there is no second API per page. Every page is rendered
// through Server.render with one `page` value; when a request asks for JSON,
// render writes that value, and when a browser asks, it gets the app (nextui.go). The
// handler, its permission checks and everything render resolves for the caller
// (the rail's capabilities, the running timer, the unread count) ran before
// this point, so the Svelte page cannot see a thing the template could not.
//
// POST routes are not touched: a form in the new interface posts to the same
// address with the same fields and follows the same redirect.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
)

// wantsJSON reports whether this request is asking for a page's data and not the
// shell. A browser navigation sends Accept: text/html, so it is not mistaken for it.
func wantsJSON(r *http.Request) bool {
	if r.URL.Query().Get("_data") == "1" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// pageEnvelope is what a page's data is wrapped in: which template would have
// drawn it, so the client knows which view to open, and the data itself.
type pageEnvelope struct {
	Template string `json:"template"`
	Page     any    `json:"page"`
	// Assets are the stylesheet and behavior scripts, at the revision-stamped paths the interface loads them
	// from. The scripts act on data-* attributes through delegated listeners, so markup that carries those
	// attributes has the behavior.
	Assets nextAssets `json:"assets"`
}

type nextAssets struct {
	CSS     []string `json:"css"`
	Scripts []string `json:"scripts"`
}

// The behavior scripts, in the order they load. theme.js is left out: the interface
// chooses its palette itself, under the same key. pwa.js registers the service worker and runs the
// outbox, which has to keep working wherever somebody is.
var nextScripts = []string{"ui.js", "editor.js", "live.js", "board.js", "spaces.js", "canvas.js", "pwa.js", "scan.js"}

func currentAssets() nextAssets {
	a := nextAssets{CSS: []string{"/static/" + assetVersion() + "/hearth.css"}}
	for _, n := range nextScripts {
		a.Scripts = append(a.Scripts, "/static/"+assetVersion()+"/"+n)
	}
	return a
}

func writePageJSON(w http.ResponseWriter, name string, p page) error {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	// The data is per person and changes with a role, so it is never cached,
	// and Vary keeps a shared cache from handing the markup to a data request.
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept")
	// The service worker keeps a page's data for reading with no signal, and keeps only what carries this mark.
	h.Set("X-Hearth-Page-Data", "1")
	return json.NewEncoder(w).Encode(pageEnvelope{Template: name, Page: tree(reflect.ValueOf(p), true), Assets: currentAssets()})
}
