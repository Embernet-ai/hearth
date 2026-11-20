package web

// Profile pictures. Somebody puts one on their own account; everybody in the
// organisation sees it beside that person's name, because a face is found
// faster than a name. Without one, the same picture address serves their
// initials on a colour of their name, so a page never has to know who has a
// photo -- it always asks for /people/{id}/avatar.

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/Embernet-ai/hearth/internal/auth"
)

// maxAvatarBytes is plenty for a face; a phone photo straight off the camera
// is the thing it stops, and the message says to crop it.
const maxAvatarBytes = 2 << 20

// avatarTypes are the only pictures accepted, decided from the bytes, not the
// filename or what the browser claimed. SVG is deliberately not one: an SVG
// is a document that can carry script, and this is served to everybody.
var avatarTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

func (s *Server) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+64<<10)
	if err := r.ParseMultipartForm(maxAvatarBytes + 64<<10); err != nil {
		s.fail(w, r, http.StatusRequestEntityTooLarge, "that picture is too big; 2 MB at most, a cropped photo is plenty")
		return
	}
	f, _, err := r.FormFile("avatar")
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "no picture was sent")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAvatarBytes+1))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the picture")
		return
	}
	if len(data) > maxAvatarBytes {
		s.fail(w, r, http.StatusRequestEntityTooLarge, "that picture is too big; 2 MB at most, a cropped photo is plenty")
		return
	}
	kind := http.DetectContentType(data)
	if !avatarTypes[kind] {
		s.fail(w, r, http.StatusBadRequest, "that is not a picture Hearth can show; use a PNG, JPEG, GIF or WebP")
		return
	}

	stored, err := s.files.Upload(r.Context(), id.OrgID, id.PrincipalID,
		"avatar", kind, int64(len(data)), bytes.NewReader(data))
	if err != nil {
		s.log.ErrorContext(r.Context(), "store avatar", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not store the picture")
		return
	}
	if err := s.auth.SetAvatar(r.Context(), id.OrgID, id.PrincipalID, stored.ID); err != nil {
		s.log.ErrorContext(r.Context(), "set avatar", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the picture")
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Server) removeAvatar(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.auth.SetAvatar(r.Context(), id.OrgID, id.PrincipalID, ""); err != nil {
		s.log.ErrorContext(r.Context(), "remove avatar", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not remove the picture")
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// getAvatar serves somebody's picture, or their initials when they have none.
// Anyone signed in to the same organisation may see it, as they may see the
// name beside it. no-cache plus an ETag naming the picture: a browser asks
// each time but is told "unchanged" for nothing, and a new picture shows at
// once rather than whenever a cache expires.
func (s *Server) getAvatar(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	name, fileID, err := s.auth.Avatar(r.Context(), id.OrgID, r.PathValue("id"))
	if errors.Is(err, auth.ErrNoPerson) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "avatar", "error", err)
		http.Error(w, "could not load the picture", http.StatusInternalServerError)
		return
	}

	etag := `"i-` + fmt.Sprintf("%d", avatarHue(name)) + "-" + initials(name) + `"`
	if fileID != "" {
		etag = `"f-` + fileID + `"`
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if fileID != "" {
		meta, rc, err := s.files.Open(r.Context(), id.OrgID, fileID)
		if err == nil {
			defer rc.Close()
			// Checked against avatarTypes on the way in, so never SVG or HTML.
			w.Header().Set("Content-Type", meta.ContentType)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
			if _, err := io.Copy(w, rc); err != nil {
				s.log.ErrorContext(r.Context(), "avatar stream", "error", err)
			}
			return
		}
		s.log.WarnContext(r.Context(), "avatar file missing; serving initials", "file", fileID, "error", err)
	}

	// Initials. Drawn here, from nothing but two escaped letters and a number,
	// so there is nothing in it anybody typed that could be anything but text.
	w.Header().Set("Content-Type", "image/svg+xml")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">`+
		`<circle cx="32" cy="32" r="32" fill="hsl(%d 55%% 40%%)"/>`+
		`<text x="32" y="32" dy=".35em" text-anchor="middle" font-family="system-ui,Segoe UI,sans-serif" `+
		`font-size="26" font-weight="700" fill="#fff">%s</text></svg>`,
		avatarHue(name), html.EscapeString(initials(name)))
}

// initials is up to two letters of a name: "Luis Navarro" is LR.
func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		for _, c := range w {
			out = append(out, unicode.ToUpper(c))
			break
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// avatarHue is a colour for a name: the same name is the same colour
// everywhere, and different people are usually different colours.
func avatarHue(name string) int {
	h := 0
	for _, c := range name {
		h = (h*31 + int(c)) % 360
	}
	return h
}
