package httpx

// Response headers that constrain what a browser will do with a page, and a
// second lock on the requests that change something.
//
// The session cookie is already HttpOnly, Secure and SameSite=Lax, and Lax is
// doing most of the work here: a browser will not attach that cookie to a
// cross-site POST or to a request made inside a cross-site iframe, so the
// obvious cross-site request forgery and the obvious clickjack both arrive
// unauthenticated and get a sign-in page. None of what follows replaces that.
//
// It is here because one control is not a design. SameSite is a cookie
// attribute honoured by the browser, and every one of these is a different
// mechanism failing in a different direction: Strict-Transport-Security covers
// the case SameSite cannot touch at all, which is the first request going out
// over plain http; Content-Security-Policy is about what a page may load rather
// than what it may be sent; and the origin check catches the same-site case
// that Lax deliberately permits.

import (
	"net/http"
	"strings"
)

// Security holds the two facts the headers depend on.
type Security struct {
	// HTTPS reports whether the public base URL is https.
	//
	// HSTS is sent only when it is. A browser ignores the header on a plain
	// http response anyway, and sending it in development would pin localhost
	// to https for a year on the developer's own machine, which is a hard thing
	// to undo and a confusing thing to debug.
	HTTPS bool

	// Origin is the scheme and host the site is served from, with no trailing
	// slash, as it appears in an Origin header. Empty disables the origin check
	// rather than failing every request closed, because a deployment that has
	// not told us its own address cannot have the check applied correctly and
	// silently rejecting every write would be worse than not checking.
	Origin string

	// BlobOrigin is the scheme and host a browser fetches presigned object URLs
	// from. Files are handed out as time-limited URLs pointing straight at the
	// object store rather than proxied through this process, so the policy has
	// to name that origin or every attachment and every direct upload is
	// blocked. Empty leaves it out.
	BlobOrigin string
}

// Headers sets the response headers on everything it wraps.
//
// Deliberately unconditional and applied at the very outside, so it covers
// error pages and panics too. A 500 rendered by the recovery middleware is
// still a page in somebody's browser, and the policy that stops it being framed
// should not depend on the handler having finished.
func (s Security) Headers(next http.Handler) http.Handler {
	csp := s.policy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// Two years, subdomains included. Long because the value of HSTS is
		// entirely in the requests it covers before anybody has typed https,
		// and a short max-age reopens that window every time it lapses.
		//
		// No preload directive. Preloading is a submission to a list baked into
		// browser binaries, it takes months to reverse, and it commits every
		// current and future subdomain of the parent domain to https forever.
		// That is a decision for whoever owns the domain, not a default.
		if s.HTTPS {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		// Stops the browser second-guessing a Content-Type. Without it an
		// uploaded file served as text/plain can be sniffed into script.
		h.Set("X-Content-Type-Options", "nosniff")

		// frame-ancestors in the policy below is the one browsers actually
		// honour now. X-Frame-Options stays for the older ones, and the two
		// agree.
		h.Set("X-Frame-Options", "DENY")

		// same-origin rather than the browser default of
		// strict-origin-when-cross-origin. The default still sends the origin
		// to a third party, and the paths in here carry document and space
		// identifiers. Nothing outside needs to know which document somebody
		// was reading when they followed a link out.
		h.Set("Referrer-Policy", "same-origin")

		// A page opened from here cannot reach back through window.opener.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		// Camera is allowed because /scan is a barcode reader somebody uses
		// standing at a machine, and microphone because calls are the next
		// thing that will want it. The rest are denied outright: none of them
		// is used and each is a capability worth not having.
		h.Set("Permissions-Policy",
			"camera=(self), microphone=(self), geolocation=(), "+
				"payment=(), usb=(), magnetometer=(), gyroscope=()")

		h.Set("Content-Security-Policy", csp)

		next.ServeHTTP(w, r)
	})
}

// policy builds the Content-Security-Policy.
//
// script-src is 'self' with no 'unsafe-inline' and no 'unsafe-eval', which is
// the directive worth having and the one that is usually surrendered first.
// It is affordable here because every script on the page is a file: the shell
// loads the app and the behavior scripts by src and there is not one
// inline <script> in it. Keep it that way. The moment somebody adds
// an inline handler this has to be weakened to 'unsafe-inline', and at that
// point the directive stops meaning anything at all.
//
// style-src does carry 'unsafe-inline', and that is not a shrug. There are
// style attributes across the views and a style attribute is inline style
// as far as this directive is concerned. Removing them is worth doing and is
// not worth blocking every page on today.
func (s Security) policy() string {
	img := []string{"'self'", "data:", "blob:"}
	media := []string{"'self'", "blob:"}
	connect := []string{"'self'"}
	if s.BlobOrigin != "" {
		img = append(img, s.BlobOrigin)
		media = append(media, s.BlobOrigin)
		// Direct uploads PUT to a presigned URL from the page, which is a
		// connect-src fetch rather than a form post.
		connect = append(connect, s.BlobOrigin)
	}

	return strings.Join([]string{
		"default-src 'self'",
		"base-uri 'self'",
		"object-src 'none'",
		// The clickjacking control. 'none' rather than 'self': nothing in here
		// frames anything, and the chart already records why this application
		// is a top-level subdomain rather than an iframed one.
		"frame-ancestors 'none'",
		// One frame source: YouTube's no-cookie player, which is what a
		// YouTube link on its own line renders as (markdown.go). Nothing else
		// may be framed.
		"frame-src https://www.youtube-nocookie.com",
		"form-action 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src " + strings.Join(img, " "),
		"media-src " + strings.Join(media, " "),
		"connect-src " + strings.Join(connect, " "),
		"font-src 'self'",
		// The service worker in pwa.js, which is what makes the offline
		// surfaces work.
		"worker-src 'self'",
		"manifest-src 'self'",
	}, "; ")
}

// unsafeMethod reports whether a request is one that can change something.
func unsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// SameOrigin rejects a state-changing request that the browser itself says came
// from somewhere else.
//
// This is the second lock, and it is worth being precise about what it adds
// over SameSite=Lax, because it is not much and the little it is matters.
//
// Lax withholds the session cookie from a cross-SITE POST. Site, not origin:
// everything under the registrable domain counts as the same site, so a POST
// from another host on the same parent domain arrives WITH the cookie and Lax
// is content. That is the gap. A foothold on any sibling subdomain is enough,
// and on an estate that runs a dozen applications under one domain that is not
// a hypothetical shape.
//
// So same-site is rejected here as firmly as cross-site. Nothing legitimately
// posts into this application from another host: sign-in returns through a
// redirect, which is a navigation and not a post, and uploads go to the object
// store rather than to us.
//
// A request carrying NEITHER header is allowed. Every browser that has shipped
// in years sends Origin on a POST, so in practice that case is a command-line
// client, the acceptance suite, or a monitoring probe, none of which have an
// ambient session to abuse. Refusing them would break real callers to close a
// hole that the cookie has already closed.
func (s Security) SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Origin == "" || !unsafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Sec-Fetch-Site is the browser's own account of the relationship and
		// cannot be set by script, so it is preferred where it exists.
		//
		// "none" is a user-initiated load with no initiator page at all, which
		// is a typed address or a bookmark. It is allowed because it is not a
		// forgery: there is no attacker page in the story.
		switch r.Header.Get("Sec-Fetch-Site") {
		case "same-origin", "none":
			next.ServeHTTP(w, r)
			return
		case "same-site", "cross-site":
			refuse(w)
			return
		}

		// Older browsers, and anything that sends Origin without Sec-Fetch-Site.
		if origin := r.Header.Get("Origin"); origin != "" &&
			!strings.EqualFold(strings.TrimSuffix(origin, "/"), s.Origin) {
			refuse(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// refuse answers a rejected write.
//
// 403 with a flat sentence and nothing else. The request was not made by
// somebody reading this, so there is no one to help, and naming the expected
// origin would only tell whoever did make it what to send next time.
func refuse(w http.ResponseWriter) {
	http.Error(w, "this request did not come from here", http.StatusForbidden)
}
