// Installing the worker, and showing what it is holding.
//
// The service worker does the work. This is the part on the page: it registers
// the worker, keeps the outbox count in the top bar honest, draws the list of
// what is still on the phone, and remembers whether somebody asked for the
// daylight palette.
(function () {
  // A phone told to use another Hearth (the connect page) goes there for sign-in. /login?local=1 stays here.
  try {
    var saved = localStorage.getItem("hearth.server");
    if (saved && location.pathname === "/login" && location.search.indexOf("local=1") < 0 && saved !== location.origin) {
      location.replace(saved + "/login");
      return;
    }
  } catch (err) { /* no storage: stay here */ }
  var badge = null;

  // ---- the worker ---------------------------------------------------------
  if ("serviceWorker" in navigator) {
    // After load, so registering never competes with rendering the page
    // somebody is waiting on. But this script is itself added to the page after
    // the interface has fetched the page's data, which can be after the load
    // event has already fired: a listener added then never runs, the worker was
    // never installed, and a phone with no signal had nothing to open. So when
    // the page has finished loading, register now.
    var register = function () {
      navigator.serviceWorker.register("/sw.js", { scope: "/" }).then(function (reg) {
        // A worker that is already controlling this page can be asked what it
        // is holding straight away.
        ask("hearth-status");
        // And check for a new build now rather than on the next visit.
        if (reg && reg.update) { try { reg.update(); } catch (err) { /* not fatal */ } }
      }).catch(function () {
        // No worker. The site works exactly as it did before, online only,
        // which is the right thing for a browser that will not have one.
      });
    };
    if (document.readyState === "complete") {
      register();
    } else {
      window.addEventListener("load", register);
    }

    navigator.serviceWorker.addEventListener("message", function (ev) {
      var data = ev.data || {};
      if (data.type === "hearth-outbox") { render(data); }
    });

    // Coming back into signal is the moment to send. The worker registers a
    // background sync where the browser has one, and this covers the browsers
    // that do not, plus the case where the phone was awake the whole time.
    window.addEventListener("online", function () { ask("hearth-replay"); });
    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState === "visible" && navigator.onLine) {
        ask("hearth-replay");
      }
    });
  }

  function ask(type, extra) {
    if (!navigator.serviceWorker || !navigator.serviceWorker.controller) return;
    var msg = { type: type };
    if (extra) { for (var k in extra) { msg[k] = extra[k]; } }
    navigator.serviceWorker.controller.postMessage(msg);
  }

  // ---- the count in the top bar -------------------------------------------
  function render(data) {
    // A page drawn by the rebuilt interface replaces the element, so a kept reference can point at one that is gone.
    badge = (badge && badge.isConnected) ? badge : document.querySelector("[data-outbox-count]");
    if (badge) {
      // The server's number is on the element already: those are the questions
      // waiting for an answer. This adds what has not been sent yet.
      var decided = Number(badge.getAttribute("data-outbox-count") || 0);
      var total = decided + (data.waiting || 0) +
        (data.items || []).filter(function (i) { return i.status === "problem"; }).length;
      badge.textContent = total > 0 ? String(total) : "";
      badge.hidden = total === 0;
    }
    var list = document.querySelector("[data-queue]");
    if (list) { drawQueue(list, data); }
    var warn = document.querySelector("[data-signin-needed]");
    if (warn) { warn.hidden = data.problem !== "signin"; }
  }

  // ---- what is still on the phone -----------------------------------------
  //
  // Drawn here rather than on the server because the server has never seen it.
  // That is the whole point of the queue.
  function drawQueue(list, data) {
    var items = data.items || [];
    list.textContent = "";
    var empty = document.querySelector("[data-queue-empty]");
    if (empty) { empty.hidden = items.length > 0; }
    if (items.length === 0) { return; }

    items.forEach(function (item) {
      var li = document.createElement("li");
      li.setAttribute("data-queued", item.key);

      var what = document.createElement("strong");
      what.textContent = item.what;
      li.appendChild(what);

      var when = document.createElement("span");
      when.className = "muted small";
      when.textContent = ago(item.at);
      li.appendChild(when);

      var state = document.createElement("span");
      state.className = "status " + (item.status === "problem" ? "blocked" : "todo");
      state.textContent = item.status === "problem" ? "needs you" : "waiting for a signal";
      li.appendChild(state);

      if (item.message) {
        var why = document.createElement("span");
        why.className = "muted small";
        why.textContent = item.message;
        li.appendChild(why);
      }

      var open = document.createElement("a");
      open.href = item.path.replace(/\/(status|readings|time|files|comments)$/, "")
                           .replace(/\/checklist\/[0-9a-f-]{36}$/i, "");
      open.textContent = "Open the job";
      li.appendChild(open);

      if (item.status === "problem") {
        li.appendChild(button("It did not land, send it again", function () {
          ask("hearth-retry", { id: item.id });
        }));
      }
      li.appendChild(button("Throw it away", function () {
        ask("hearth-discard", { id: item.id });
      }, "ghost small"));

      list.appendChild(li);
    });
  }

  function button(text, fn, cls) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = cls || "";
    b.textContent = text;
    b.addEventListener("click", fn);
    return b;
  }

  function ago(ms) {
    var s = Math.max(0, Math.round((Date.now() - ms) / 1000));
    if (s < 60) { return s + "s ago"; }
    if (s < 3600) { return Math.round(s / 60) + "m ago"; }
    if (s < 86400) { return Math.round(s / 3600) + "h ago"; }
    return Math.round(s / 86400) + "d ago";
  }

  // ---- daylight -----------------------------------------------------------
  //
  // A switch rather than only prefers-color-scheme, because the phone is set to
  // dark and the person carrying it has walked outside. The choice is kept on
  // the device: it is about where somebody is standing, not who they are, so
  // putting it in their account would follow them indoors.
  var KEY = "hearth-theme";

  // The device decides until a person does.
  //
  // Set here rather than duplicated into a prefers-color-scheme block in the
  // stylesheet. The daylight palette is defined once, under
  // :root[data-theme="day"], and the contrast test reads exactly that block --
  // a second copy inside a media query would be a second set of colours that
  // nothing checks, and the two would drift the first time one was nudged.
  //
  // Without a script the dark palette applies, which is the documented default
  // and readable everywhere.
  function deviceWants() {
    try {
      return window.matchMedia &&
        window.matchMedia("(prefers-color-scheme: light)").matches ? "day" : "dark";
    } catch (err) { return "dark"; }
  }
  function apply(theme) {
    if (theme === "day") {
      document.documentElement.setAttribute("data-theme", "day");
    } else {
      document.documentElement.setAttribute("data-theme", "dark");
    }
  }

  // The initial choice is made in theme.js, which runs in the head before the
  // first paint. Repeating it here would set the same attribute a second time,
  // later, for no benefit. What is left below is following the system when it
  // changes mid-session, and the toggle.

  // Somebody changing their system theme mid-session should be followed, but
  // only if they have not made a choice here. An explicit choice is about where
  // they are standing and outlives the system flipping at sunset.
  try {
    var mq = window.matchMedia("(prefers-color-scheme: light)");
    var onChange = function () {
      var chosen = null;
      try { chosen = window.localStorage.getItem(KEY); } catch (e) { /* ignore */ }
      if (!chosen) { apply(deviceWants()); }
    };
    if (mq.addEventListener) { mq.addEventListener("change", onChange); }
    else if (mq.addListener) { mq.addListener(onChange); }
  } catch (err) { /* no matchMedia. The applied palette stands. */ }

  document.addEventListener("click", function (ev) {
    var el = ev.target.closest ? ev.target.closest("[data-theme-toggle]") : null;
    if (!el) { return; }
    ev.preventDefault();
    var now = document.documentElement.getAttribute("data-theme") === "day" ? "dark" : "day";
    document.documentElement.setAttribute("data-theme", now);
    try { window.localStorage.setItem(KEY, now); } catch (err) { /* nothing to do */ }
  });
})();
