// The Hearth server address for the phone app.
//
// The page that loads this is public and holds no data. It asks the address a person types for /.well-known/hearth,
// which any Hearth answers with its name and the permission to be asked from another origin, and only a server that
// answers like a Hearth is remembered. Then it opens that server's sign-in. The address is kept in this browser, and
// /login on this origin sends a phone that has one straight to it.
(function () {
  var KEY = "hearth.server";
  var form = document.getElementById("server-form");
  var input = document.getElementById("server");
  var msg = document.getElementById("msg");
  var saved = document.getElementById("saved");
  var forget = document.getElementById("forget");

  function get() { try { return localStorage.getItem(KEY) || ""; } catch (e) { return ""; } }
  function put(v) { try { if (v) { localStorage.setItem(KEY, v); } else { localStorage.removeItem(KEY); } } catch (e) { /* not fatal */ } }
  function say(t, bad) { msg.textContent = t; msg.className = bad ? "err" : "ok"; }

  // normalize turns what was typed into an origin, or "" when it cannot be one. A bare host gets https, and a path,
  // a query or a fragment is dropped, because the address of a server is its origin.
  function normalize(v) {
    v = (v || "").trim();
    if (!v) return "";
    if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) v = "https://" + v;
    try {
      var u = new URL(v);
      if (u.protocol !== "https:" && u.protocol !== "http:") return "";
      if (!u.hostname) return "";
      return u.origin;
    } catch (e) { return ""; }
  }

  function show() {
    var s = get();
    if (s) {
      saved.hidden = false;
      document.getElementById("saved-address").textContent = s;
      document.getElementById("continue").href = s + "/login";
      input.value = s;
    } else {
      saved.hidden = true;
    }
  }

  form.addEventListener("submit", function (ev) {
    ev.preventDefault();
    var origin = normalize(input.value);
    if (!origin) { say("That is not an address. Try something like hearth.example.com.", true); return; }
    say("Checking " + origin + " ...", false);
    var ctl = new AbortController();
    var timer = setTimeout(function () { ctl.abort(); }, 10000);
    fetch(origin + "/.well-known/hearth", { signal: ctl.signal, credentials: "omit", cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("status " + r.status); return r.json(); })
      .then(function (j) {
        clearTimeout(timer);
        if (!j || j.app !== "hearth") throw new Error("not a Hearth");
        put(origin);
        say("Found " + (j.name || "Hearth") + ". Opening its sign-in.", false);
        show();
        window.location.assign(origin + (j.login || "/login"));
      })
      .catch(function () {
        clearTimeout(timer);
        say("No Hearth answered at " + origin + ". Check the address, that the server is on, and that this phone can reach it.", true);
      });
  });

  forget.addEventListener("click", function () {
    put("");
    input.value = "";
    say("Back to this server.", false);
    show();
  });

  show();
})();
