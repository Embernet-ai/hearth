package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"strings"
)

// iconsNeedNoSignIn: the favicon and the touch icons are fetched by the browser and the phone before anybody has signed in, and
// a redirect to the sign-in page is not an image. Every address the shell links answers 200 with an image and no session.
func (r *runner) iconsNeedNoSignIn() {
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	bad := []string{}
	pngs := map[string]int{"/apple-touch-icon.png": 180, "/icon-192.png": 192, "/icon-512.png": 512, "/icon-maskable-512.png": 512}
	for _, p := range []string{"/favicon.ico", "/favicon.svg", "/icon.svg", "/icon-maskable.svg", "/apple-touch-icon.png", "/icon-192.png", "/icon-512.png", "/icon-maskable-512.png"} {
		resp, err := anon.Get(r.base + p)
		if err != nil {
			bad = append(bad, p+": "+err.Error())
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
			bad = append(bad, fmt.Sprintf("%s: HTTP %d %s", p, resp.StatusCode, resp.Header.Get("Content-Type")))
			continue
		}
		// and it decodes at the size it is named for
		if px, ok := pngs[p]; ok {
			if cfg, err := png.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != px || cfg.Height != px {
				bad = append(bad, fmt.Sprintf("%s does not decode as %dx%d", p, px, px))
			}
		}
		if p == "/favicon.ico" && (len(body) < 38 || binary.LittleEndian.Uint16(body[4:6]) != 2 || body[6] != 16 || body[22] != 32) {
			bad = append(bad, "/favicon.ico is not 16 and 32 pixels")
		}
		if strings.HasSuffix(p, ".svg") && !strings.Contains(string(body), `aria-label="Hearth"`) {
			bad = append(bad, p+" is not the Hearth mark")
		}
	}
	r.check("the icons are served with no session", len(bad) == 0, strings.Join(bad, "; "))
}
