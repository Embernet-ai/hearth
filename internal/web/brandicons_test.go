package web

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A browser asks for /favicon.ico and a phone for /apple-touch-icon.png before anybody has signed in, so every icon answers 200,
// with its own type and bytes, from the whole routing table and with no session.
func TestTheIconsAnswerWithoutASession(t *testing.T) {
	h := newServer(t).Routes()
	want := map[string]struct {
		typ   string
		magic []byte
	}{
		"/favicon.ico":           {"image/x-icon", []byte{0, 0, 1, 0}},
		"/favicon.svg":           {"image/svg+xml", []byte("<svg")},
		"/icon.svg":              {"image/svg+xml", []byte("<svg")},
		"/icon-maskable.svg":     {"image/svg+xml", []byte("<svg")},
		"/apple-touch-icon.png":  {"image/png", []byte("\x89PNG")},
		"/icon-192.png":          {"image/png", []byte("\x89PNG")},
		"/icon-512.png":          {"image/png", []byte("\x89PNG")},
		"/icon-maskable-512.png": {"image/png", []byte("\x89PNG")},
	}
	for path, w := range want {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != w.typ {
			t.Errorf("%s: %d %q, want 200 %q (a redirect here is a broken icon)", path, rec.Code, rec.Header().Get("Content-Type"), w.typ)
			continue
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), w.magic) || rec.Body.Len() == 0 {
			t.Errorf("%s: not the file it names", path)
		}
	}
}

// Every icon is the Hearth H with the fire, decodes at the size it is named for, and nothing of the old hand drawn house is left. A
// phone that is handed a 512 file that is really 96 pixels, or a favicon with one size, shows a blur.
func TestTheIconsAreTheRightSizesAndTheOldOneIsGone(t *testing.T) {
	h := newServer(t).Routes()
	get := func(path string) []byte {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		return rec.Body.Bytes()
	}
	for path, px := range map[string]int{"/apple-touch-icon.png": 180, "/icon-192.png": 192, "/icon-512.png": 512, "/icon-maskable-512.png": 512} {
		cfg, err := png.DecodeConfig(bytes.NewReader(get(path)))
		if err != nil || cfg.Width != px || cfg.Height != px {
			t.Errorf("%s decodes as %dx%d (%v), want %dx%d", path, cfg.Width, cfg.Height, err, px, px)
		}
	}
	ico := get("/favicon.ico")
	if len(ico) < 6+32 || binary.LittleEndian.Uint16(ico[2:4]) != 1 || binary.LittleEndian.Uint16(ico[4:6]) != 2 {
		t.Fatalf("favicon.ico is not an icon of two sizes")
	}
	var sizes []int
	for i := 0; i < 2; i++ {
		sizes = append(sizes, int(ico[6+16*i]))
	}
	if sizes[0] != 16 || sizes[1] != 32 {
		t.Errorf("favicon.ico sizes %v, want 16 and 32", sizes)
	}
	for _, path := range []string{"/favicon.svg", "/icon.svg", "/icon-maskable.svg"} {
		svg := string(get(path))
		if !strings.Contains(svg, `viewBox="0 0 64 64"`) || !strings.Contains(svg, `aria-label="Hearth"`) || !strings.Contains(svg, "#D92D20") {
			t.Errorf("%s is not the Hearth mark in the brand ember", path)
		}
	}
	// the old orange house is not served, and not left in the static folder
	files, _ := fs.Glob(staticFS, "static/*")
	for _, f := range files {
		if !(strings.HasSuffix(f, ".svg") || strings.HasSuffix(f, ".png") || strings.HasSuffix(f, ".ico")) {
			continue
		}
		raw, _ := fs.ReadFile(staticFS, f)
		if bytes.Contains(raw, []byte("#fb923c")) || bytes.Contains(raw, []byte("#0f1113")) {
			t.Errorf("%s still carries the old icon's colours", f)
		}
	}
}
