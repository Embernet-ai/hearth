package web

import (
	"io"
	"net/http"
)

// wellKnown is how a phone checks that an address is a Hearth. It is public and says only what the app is called,
// and where to sign in, and it may be asked from any origin because the person's phone is asking it from
// the connect page of another Hearth.
func (s *Server) getWellKnown(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `{"app":"hearth","name":"Hearth","login":"/login"}`+"\n")
}

// getConnect is the page where the phone app is told which Hearth server to use. It holds nothing of this installation,
// so it is outside the session wall like the manifest. It reaches other servers from the browser to check them, so its
// connect-src is wider than the rest of the site's; nothing else on it is.
func (s *Server) getConnect(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; connect-src https: http:")
	_, _ = io.WriteString(w, connectShell(assetVersion()))
}

func connectShell(version string) string {
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hearth server</title>
<style>
:root{color-scheme:dark light}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f1113;color:#e7e9ea;font:16px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{width:100%;max-width:28rem;padding:2rem}
h1{font-size:1.4rem;margin:0 0 .75rem}
p{color:#9aa3ab}
a{color:#fb923c}
label{display:block;margin:1rem 0 .3rem}
input{box-sizing:border-box;width:100%;min-height:48px;padding:.6rem .8rem;border:1px solid #272c31;border-radius:8px;background:#171a1d;color:inherit;font:inherit}
button,a.button{display:inline-block;box-sizing:border-box;margin-top:1rem;min-height:48px;padding:.8rem 1.2rem;border:1px solid #272c31;border-radius:8px;background:#fb923c;color:#0f1113;font:inherit;text-decoration:none;cursor:pointer}
a.button{color:#0f1113}
button.ghost{background:none;color:#fb923c}
.ok{color:#86efac}.err{color:#fca5a5}
.card{margin-top:1.5rem;padding:1rem;border:1px solid #272c31;border-radius:8px}
code{word-break:break-all}
</style></head>
<body><main>
<h1>Hearth server</h1>
<p>Type the address of the Hearth you want this phone to use. If your company runs its own, this is where you point the app at it. The address is checked, then you sign in on that server, and this phone remembers it.</p>
<form id="server-form" novalidate>
<label for="server">Server address</label>
<input id="server" name="server" type="url" inputmode="url" autocomplete="url" autocapitalize="none" spellcheck="false" placeholder="hearth.example.com" required>
<button type="submit">Check and sign in</button>
</form>
<p id="msg" role="status"></p>
<div id="saved" class="card" hidden>
<p>This phone uses <code id="saved-address"></code></p>
<a id="continue" class="button" href="/login">Continue to sign in</a>
<button id="forget" class="ghost" type="button">Use this server instead</button>
</div>
<p><a href="/login?local=1">Back to sign in here</a></p>
</main>
<script src="/static/` + version + `/connect.js"></script>
</body></html>
`
}
