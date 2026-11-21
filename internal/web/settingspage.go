package web

// Settings: one entry for everybody, because everybody has personal settings.
// Administration lives inside it as a section, each group gated by the grant
// it needs -- settings.html reads .IsAdmin and .Can, which render() already
// resolves on every page, so there is nothing to check here.

import "net/http"

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings", page{Title: "Settings", ConnectorsOn: s.connect != nil})
}
