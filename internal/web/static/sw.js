// The service worker: what makes this work at the machine, with no signal.
//
// # What is cached, and what deliberately is not
//
// The shell is cached and pages are not, except for an hour after their data was
// last fetched. A cached work order is a work order that might be wrong, and
// somebody reading a status from three days ago as though it were current is
// worse off than somebody who can see there is no signal. The hour exists
// because a technician who opened a job before walking into a plant room needs
// the procedure on it, and the page says when it was fetched so a stale
// reading is visibly stale rather than a quiet lie.
//
// # Why writes queue instead of failing
//
// Because the alternative is a person typing a reading twice, and the second
// time they will not bother. A form post that cannot reach the server goes
// into IndexedDB and is sent, in the order the buttons were pressed, as soon
// as there is a connection.
//
// # Why one batch rather than replaying each request
//
// So that order is kept, so that a conflict comes back as an answer rather
// than as a page to read prose out of, and so that a reply lost on a bad link
// does not apply the same forty minutes twice. Every operation carries a key
// made when the button was pressed and the server settles it.
//
// __BUILD__ is replaced with the build revision when this file is served. It
// is what makes a deploy change the worker's bytes, which is the only reason a
// browser installs a new one.

"use strict";

const BUILD = "__BUILD__";
const SHELL = "hearth-shell-" + BUILD;
const PAGES = "hearth-pages-" + BUILD;
const DB = "hearth-outbox";

// The interface's one page, kept under this name.
const PAGE_KEY = new Request("/__page");

// How long a page is offered when there is no signal. One shift's worth of a
// job somebody opened before they walked in.
const PAGE_TTL_MS = 60 * 60 * 1000;

// How many operations go in one batch, and how many photographs. Both smaller
// than the server's limits: a phone on one bar loses a large request more
// often than it loses two small ones, and the rest goes in the next batch.
const BATCH = 25;
const BATCH_FILES = 1;

// What is worth queueing. The server keeps its own copy of this list and that
// copy is the one that decides; this one only stops the phone carrying
// something that would be refused when it finally got through.
const QUEUEABLE = [
  /^\/documents\/[0-9a-f-]{36}\/status$/i,
  /^\/documents\/[0-9a-f-]{36}\/readings$/i,
  /^\/documents\/[0-9a-f-]{36}\/time$/i,
  /^\/documents\/[0-9a-f-]{36}\/files$/i,
  /^\/documents\/[0-9a-f-]{36}\/comments$/i,
  /^\/documents\/[0-9a-f-]{36}\/checklist\/[0-9a-f-]{36}$/i,
];

self.addEventListener("install", function (event) {
  event.waitUntil(
    caches.open(SHELL).then(function (cache) {
      // Only the no-signal page and the manifest. Pre-caching work orders
      // would put somebody else's jobs on this device for no reason.
      return cache.addAll(["/offline", "/manifest.webmanifest"]);
    }).catch(function () {
      // A shell that could not be primed is not fatal. Everything works with a
      // connection, and the next visit tries again.
    })
  );
  self.skipWaiting();
});

self.addEventListener("activate", function (event) {
  event.waitUntil(
    caches.keys().then(function (names) {
      return Promise.all(names.map(function (n) {
        // Anything from an older build. The revision is in the name, so a
        // deploy drops the previous shell rather than serving it forever.
        return (n !== SHELL && n !== PAGES) ? caches.delete(n) : null;
      }));
    }).then(function () {
      return self.clients.claim();
    })
  );
});

self.addEventListener("fetch", function (event) {
  const req = event.request;
  let url;
  try {
    url = new URL(req.url);
  } catch (err) {
    return;
  }
  // Only this origin. A cross-origin request is not ours to reason about.
  if (url.origin !== self.location.origin) return;

  if (req.method === "GET") {
    event.respondWith(handleGet(req, url));
    return;
  }
  if (req.method === "POST") {
    event.respondWith(handlePost(req, url));
  }
});

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// handleGet is network first, then a recent cached copy, then the no-signal
// page.
//
// Network first and not cache first, deliberately. A stale status shown as
// current is the failure this whole design exists to avoid, and saving a round
// trip is not worth it on a page somebody is about to act on.
//
// An address answers a browser with the interface's one page and answers the
// interface with that page's data (the same address, asked for as JSON). The
// one page is the same everywhere, so a single copy of it is kept and any
// address opens with it; the data is kept per address, for the hour above.
async function handleGet(req, url) {
  // Static assets carry the build revision in their path, so they can be
  // served from the cache forever: a new build is a new URL. The interface's own
  // bundles are hashed the same way, so they get the same treatment: without
  // them the page opens with no signal as an empty shell.
  if (url.pathname.startsWith("/static/") || url.pathname.startsWith("/_astro/")) {
    const hit = await caches.match(req);
    if (hit) return hit;
    const res = await fetch(req);
    if (res.ok) (await caches.open(SHELL)).put(req, res.clone());
    return res;
  }
  // The event stream is a connection, not a page. Caching it would hand a
  // dead stream to a page that then waits on it forever.
  if (url.pathname === "/events") return fetch(req);

  const data = wantsData(req);
  try {
    const res = await fetch(req);
    // A page's data is kept, and only that: the server marks it, so no other
    // JSON is ever stored. The page itself is kept once, under one name. An
    // unhashed file of the interface (the palette script) is kept so the page
    // can open with no signal.
    if (res.ok && url.pathname === "/theme-next.js") (await caches.open(SHELL)).put(req, res.clone());
    if (res.ok && data && res.headers.get("X-Hearth-Page-Data") === "1") {
      const cache = await caches.open(PAGES);
      const headers = new Headers(res.headers);
      headers.set("X-Hearth-Cached-At", String(Date.now()));
      cache.put(dataKey(req), new Response(await res.clone().blob(), {
        status: res.status, statusText: res.statusText, headers: headers,
      }));
    } else if (res.ok && !data && (res.headers.get("Content-Type") || "").includes("text/html") && wantsPage(req)) {
      (await caches.open(SHELL)).put(PAGE_KEY, res.clone());
    }
    return res;
  } catch (err) {
    if (data) {
      const hit = await caches.match(dataKey(req), { ignoreVary: true });
      if (hit && fresh(hit)) return hit;
    } else if (url.pathname === "/theme-next.js") {
      const hit = await caches.match(req);
      if (hit) return hit;
    } else if (wantsPage(req)) {
      const page = await caches.match(PAGE_KEY, { ignoreVary: true });
      if (page) return page;
    }
    const shell = await caches.match("/offline");
    if (shell) return shell;
    return new Response("No signal, and this page was not on the phone.", {
      status: 503, headers: { "Content-Type": "text/plain; charset=utf-8" },
    });
  }
}

// What the request is asking for: the page's data, or the page.
function wantsData(req) {
  return (req.headers.get("Accept") || "").includes("application/json");
}
function wantsPage(req) {
  return req.mode === "navigate" || (req.headers.get("Accept") || "").includes("text/html");
}

// The page's data is kept under its address with a mark on it, so it can sit beside the
// page (same address, different Accept) in one cache without either replacing the other.
function dataKey(req) {
  const u = new URL(req.url);
  u.searchParams.set("__sw", "data");
  return new Request(u.toString());
}

function fresh(res) {
  const at = Number(res.headers.get("X-Hearth-Cached-At") || 0);
  return at > 0 && Date.now() - at < PAGE_TTL_MS;
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

async function handlePost(req, url) {
  try {
    return await fetch(req.clone());
  } catch (err) {
    // No signal.
  }
  if (!queueable(url.pathname)) {
    // Honest rather than helpful. Pretending to have saved something this
    // worker cannot send later is worse than saying it did not go.
    return new Response(offlinePage(
      "That one needs a connection",
      "It has not been saved. Status changes, readings, time, notes, ticking " +
      "a step off, and photographs are all kept until there is a signal."), {
      status: 503, headers: { "Content-Type": "text/html; charset=utf-8" },
    });
  }

  let item;
  try {
    item = await capture(req, url);
  } catch (err) {
    return new Response(offlinePage("That could not be kept",
      String(err && err.message ? err.message : err)), {
      status: 503, headers: { "Content-Type": "text/html; charset=utf-8" },
    });
  }
  await enqueue(item);
  await announce();
  await requestSync();

  // Back where they were, the same way a successful post would have sent them,
  // so the flow at the machine is the flow at the desk.
  const back = url.pathname.replace(/\/(status|readings|time|files|comments)$/, "")
                           .replace(/\/checklist\/[0-9a-f-]{36}$/i, "");
  return Response.redirect(back || "/", 303);
}

function queueable(path) {
  return QUEUEABLE.some(function (re) { return re.test(path); });
}

// capture turns a request into something that can be sent later.
async function capture(req, url) {
  const item = {
    key: newKey(),
    path: url.pathname,
    at: Date.now(),
    body: "",
    file: null,
    status: "waiting",
    message: "",
  };

  const type = req.headers.get("Content-Type") || "";
  if (type.startsWith("multipart/form-data")) {
    const form = await req.formData();
    const fields = new URLSearchParams();
    let file = null;
    for (const [name, value] of form.entries()) {
      if (typeof value === "string") {
        fields.append(name, value);
        continue;
      }
      if (name === "file" && value && value.size > 0) file = value;
    }
    if (!file) throw new Error("there was no photograph in that");
    item.body = fields.toString();
    const buf = await file.arrayBuffer();
    item.file = {
      name: file.name || "photo.jpg",
      type: file.type || "image/jpeg",
      // Taken here, at the moment of capture, from the bytes as they are on
      // the phone. Computing it at send time would prove only that the bytes
      // survived IndexedDB, which is not the thing that goes wrong.
      digest: await sha256Hex(buf),
      blob: file,
    };
    return item;
  }

  item.body = await req.text();
  return item;
}

function newKey() {
  if (self.crypto && self.crypto.randomUUID) return self.crypto.randomUUID();
  // Every browser that has a service worker has getRandomValues.
  const b = new Uint8Array(16);
  self.crypto.getRandomValues(b);
  return Array.from(b, function (v) {
    return v.toString(16).padStart(2, "0");
  }).join("");
}

async function sha256Hex(buf) {
  const digest = await self.crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(digest), function (v) {
    return v.toString(16).padStart(2, "0");
  }).join("");
}

function base64(bytes) {
  // In chunks, because String.fromCharCode applied to a whole photograph at
  // once overflows the argument list and throws.
  let out = "";
  const view = new Uint8Array(bytes);
  const step = 0x8000;
  for (let i = 0; i < view.length; i += step) {
    out += String.fromCharCode.apply(null, view.subarray(i, i + step));
  }
  return self.btoa(out);
}

// ---------------------------------------------------------------------------
// The queue
// ---------------------------------------------------------------------------
//
// IndexedDB rather than localStorage, because a photograph does not fit in
// localStorage and because localStorage blocks the thread somebody is
// scrolling with. The store is auto-incrementing, so reading it back gives the
// order the buttons were pressed, which is the order that means something.

function db() {
  return new Promise(function (resolve, reject) {
    const open = indexedDB.open(DB, 1);
    open.onupgradeneeded = function () {
      open.result.createObjectStore("queue", { keyPath: "id", autoIncrement: true });
    };
    open.onsuccess = function () { resolve(open.result); };
    open.onerror = function () { reject(open.error); };
  });
}

function tx(mode, fn) {
  return db().then(function (conn) {
    return new Promise(function (resolve, reject) {
      const t = conn.transaction("queue", mode);
      const out = fn(t.objectStore("queue"));
      t.oncomplete = function () { resolve(out && out.result !== undefined ? out.result : out); };
      t.onerror = function () { reject(t.error); };
      t.onabort = function () { reject(t.error); };
    });
  });
}

function enqueue(item) { return tx("readwrite", function (s) { return s.add(item); }); }
function pending() { return tx("readonly", function (s) { return s.getAll(); }); }
function drop(id) { return tx("readwrite", function (s) { return s.delete(id); }); }
function put(item) { return tx("readwrite", function (s) { return s.put(item); }); }

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

let sending = false;

// replay sends what is queued.
//
// One batch at a time, oldest first, and it stops at the first thing that is
// not an answer. Carrying on past a failed send drains the queue into the void
// the moment a signal flickers.
async function replay() {
  if (sending) return { sent: 0, kept: 0 };
  sending = true;
  try {
    let sent = 0, kept = 0;
    for (;;) {
      const all = await pending();
      const ready = all.filter(function (i) { return i.status === "waiting"; });
      kept = all.length - ready.length;
      if (ready.length === 0) break;

      const batch = takeBatch(ready);
      let body;
      try {
        body = JSON.stringify({ ops: await Promise.all(batch.map(wire)) });
      } catch (err) {
        // A photograph that cannot be read back out of storage. Marking it
        // rather than looping on it forever.
        for (const item of batch) {
          item.status = "problem";
          item.message = "this could not be read back off the phone";
          await put(item);
        }
        continue;
      }

      let res;
      try {
        res = await fetch("/offline/sync", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: body,
        });
      } catch (err) {
        break; // Still no signal. Everything stays where it is.
      }
      // A redirect to the sign-in page comes back as a page, not as an answer.
      // The queue is kept and the pages are told, because the fix is a person
      // signing in rather than anything this worker can do.
      const ct = res.headers.get("Content-Type") || "";
      if (!res.ok || !ct.includes("application/json")) {
        await announce(res.status === 401 || res.redirected ? "signin" : "server");
        break;
      }

      const answer = await res.json();
      const byKey = {};
      for (const r of (answer.results || [])) byKey[r.key] = r;
      let progressed = false;
      for (const item of batch) {
        const r = byKey[item.key];
        if (!r) continue;
        progressed = true;
        if (r.status === "applied" || r.status === "rejected") {
          if (r.status === "applied") sent++;
          await drop(item.id);
          continue;
        }
        // conflict, unknown, or pending. Kept, because none of those is an
        // answer the phone can act on by itself.
        item.status = r.status === "pending" ? "waiting" : "problem";
        item.message = r.message || r.status;
        item.serverStatus = r.status;
        await put(item);
      }
      if (!progressed) break;
    }
    await announce();
    return { sent: sent, kept: kept };
  } finally {
    sending = false;
  }
}

// takeBatch is one send's worth: bounded by count, and by how many
// photographs, because two photographs in one request over a bad link is two
// photographs to send again.
function takeBatch(ready) {
  const out = [];
  let files = 0;
  for (const item of ready) {
    if (item.file) {
      if (files >= BATCH_FILES) break;
      files++;
    }
    out.push(item);
    if (out.length >= BATCH) break;
  }
  return out;
}

async function wire(item) {
  const op = { key: item.key, path: item.path, body: item.body, at: item.at };
  if (item.file) {
    const buf = await item.file.blob.arrayBuffer();
    op.file = {
      name: item.file.name,
      type: item.file.type,
      digest: item.file.digest,
      data: base64(buf),
    };
  }
  return op;
}

// ---------------------------------------------------------------------------
// Telling the pages
// ---------------------------------------------------------------------------

async function announce(problem) {
  const items = await pending();
  const all = await self.clients.matchAll({ includeUncontrolled: true });
  const message = {
    type: "hearth-outbox",
    waiting: items.filter(function (i) { return i.status === "waiting"; }).length,
    problem: problem || "",
    items: items.map(function (i) {
      return {
        id: i.id, key: i.key, path: i.path, at: i.at,
        status: i.status, message: i.message || "",
        photo: !!i.file,
        // A title for the row, worked out from the path, because the phone
        // does not have the document to ask.
        what: describe(i.path),
      };
    }),
  };
  for (const client of all) client.postMessage(message);
}

function describe(path) {
  if (/\/status$/.test(path)) return "a status change";
  if (/\/readings$/.test(path)) return "a meter reading";
  if (/\/time$/.test(path)) return "time logged";
  if (/\/files$/.test(path)) return "a photograph";
  if (/\/comments$/.test(path)) return "a note";
  if (/\/checklist\//.test(path)) return "a step ticked off";
  return "something";
}

async function requestSync() {
  // Background sync where the browser has it, so the queue goes even if the
  // page has been closed. Where it does not, the page's own online event calls
  // replay, which is the same code.
  if (self.registration && self.registration.sync) {
    try {
      await self.registration.sync.register("hearth-outbox");
      return;
    } catch (err) {
      // Registration can be refused, and falling through to sending now is
      // better than not sending at all.
    }
  }
  replay();
}

self.addEventListener("sync", function (event) {
  if (event.tag === "hearth-outbox") event.waitUntil(replay());
});

self.addEventListener("message", function (event) {
  const data = event.data || {};
  switch (data.type) {
    case "hearth-replay":
      event.waitUntil(replay());
      break;
    case "hearth-status":
      event.waitUntil(announce());
      break;
    case "hearth-discard":
      event.waitUntil(drop(data.id).then(function () { return announce(); }));
      break;
    case "hearth-retry":
      // A person looked at it and said it did not land. A NEW key, because the
      // server settled the old one and would answer with that settlement
      // rather than doing anything. This is a fresh attempt and it says so.
      event.waitUntil(retry(data.id));
      break;
  }
});

async function retry(id) {
  const items = await pending();
  const item = items.find(function (i) { return i.id === id; });
  if (!item) return;
  item.key = newKey();
  item.status = "waiting";
  item.message = "";
  item.serverStatus = "";
  await put(item);
  await replay();
}

// offlinePage is the answer to a post this worker will not keep. Self
// contained, because a page that needs a stylesheet renders as nothing on the
// one occasion it is ever shown.
function offlinePage(title, detail) {
  const esc = function (v) {
    return String(v).replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  };
  return '<!doctype html><html lang="en"><head><meta charset="utf-8">' +
    '<meta name="viewport" content="width=device-width, initial-scale=1">' +
    "<title>" + esc(title) + " · Hearth</title><style>" +
    "body{margin:0;min-height:100vh;display:flex;align-items:center;" +
    "justify-content:center;background:#0f1113;color:#e7e9ea;font:16px/1.6 " +
    'ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}' +
    "main{max-width:28rem;padding:2rem}h1{font-size:1.3rem;margin:0 0 .75rem}" +
    "p{color:#9aa3ab}a{color:#fb923c;display:inline-block;margin-top:1rem;" +
    "padding:.85rem 1.2rem;border:1px solid #272c31;border-radius:8px;" +
    "text-decoration:none;min-height:48px}</style></head><body><main><h1>" +
    esc(title) + "</h1><p>" + esc(detail) +
    '</p><a href="/outbox">Outbox</a></main></body></html>';
}

// ---------------------------------------------------------------------------
// Web Push
// ---------------------------------------------------------------------------
//
// The server sends a push only for something already in the person's inbox, and it carries the one-line
// sentence and where to go, never the item's content. A notification has to be shown for every push (the
// browsers refuse a subscription that is not allowed to, and iOS stops delivering after a silent one), so
// anything this cannot read still shows as plain "Hearth".

// Where a click goes: a path on this site, nothing else. The payload comes from our own server, but a path
// that is not ours would open somebody else's page inside the installed app.
function pushTarget(url) {
  if (typeof url === "string" && url.charAt(0) === "/" && url.charAt(1) !== "/" && url.indexOf("\\") === -1) {
    return url;
  }
  return "/inbox";
}

self.addEventListener("push", function (event) {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch (err) {
    data = { body: event.data ? event.data.text() : "" };
  }
  const title = (typeof data.title === "string" && data.title) || "Hearth";
  const options = {
    body: typeof data.body === "string" ? data.body : "",
    icon: "/icon-192.png",
    badge: "/icon-192.png",
    // The same thing again replaces the earlier notification instead of stacking.
    tag: typeof data.tag === "string" && data.tag ? data.tag : undefined,
    renotify: typeof data.tag === "string" && !!data.tag,
    data: { url: pushTarget(data.url) },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", function (event) {
  event.notification.close();
  const target = pushTarget(event.notification.data && event.notification.data.url);
  event.waitUntil((async function () {
    const all = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const c of all) {
      // An open window of the app takes the click, so a person already in Hearth is not given a second copy.
      if (c.url && new URL(c.url).origin === self.location.origin && "focus" in c) {
        try { await c.focus(); } catch (err) { /* not fatal */ }
        if ("navigate" in c) { try { await c.navigate(target); } catch (err) { /* opens below */ } }
        return;
      }
    }
    await self.clients.openWindow(target);
  })());
});

// The browser rotated the subscription (the push service asked it to). Subscribe again with the same key and
// hand the new one to the server, or the device would quietly stop ringing.
self.addEventListener("pushsubscriptionchange", function (event) {
  event.waitUntil((async function () {
    try {
      const old = event.oldSubscription;
      const key = old && old.options && old.options.applicationServerKey;
      if (!key) return;
      const sub = await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
      await fetch("/push/subscribe", {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json", "Accept": "application/json" },
        body: JSON.stringify(Object.assign({}, sub.toJSON(), { label: "" })),
      });
    } catch (err) { /* the person turns it on again from the settings */ }
  })());
});
