// Live updates. About forty lines, no framework, no build step.
//
// Two jobs:
//   1. When the server says something changed, refetch the page's data. The
//      server never sends the data, only that something changed, so the
//      refetch goes through the normal handler and the normal permission check.
//   2. When the server reports a build revision different from the one this page
//      was loaded with, reload. That is how an open tab picks up a deploy.
(function () {
  // The document this tab has open travels with the subscription, so the
  // server can put this connection on that document's topic. It is checked
  // there before it is trusted; naming one you cannot see subscribes to
  // nothing. The app sets data-doc on the body as it draws a page, and a
  // different document means a new subscription.
  var es = null;
  var subscribedTo = null;
  var loadedBuild = "";
  var reloading = false;

  function connect() {
    // The app sets data-build on the body only for a page somebody is signed in to. Before that, /events would answer with
    // the sign-in page, which an event stream refuses for good, and it must not stay refused after they sign in.
    if (!document.body.getAttribute("data-build")) { return; }
    var open = document.body.getAttribute("data-doc") || "";
    if (es && es.readyState !== 2 && subscribedTo === open) { return; }
    if (es) { es.close(); }
    subscribedTo = open;
    es = new EventSource("/events" + (open ? "?doc=" + encodeURIComponent(open) : ""));
    es.addEventListener("build", function (ev) {
      // The revision this page was drawn by, or else the first one the server reports.
      loadedBuild = document.body.getAttribute("data-build") || loadedBuild;
      if (!loadedBuild) { loadedBuild = ev.data; return; }
      if (ev.data && ev.data !== loadedBuild && !reloading) {
        reloading = true;
        // A new build is serving. Reload once, and let the browser fetch the
        // revision-stamped assets that came with it.
        window.location.reload();
      }
    });
    es.addEventListener("documents", refresh);
    es.addEventListener("document", refresh);
    // A conversation. Named topics rather than the org-wide one, because a
    // message is the first event frequent enough that refetching every list in
    // every tab would be felt.
    es.addEventListener("message", refresh);
    es.addEventListener("comment", refresh);
  }

  // When something the page has tagged data-live may have changed, the app is asked to fetch the page's data again
  // and update in place (the app listens for hearth:refresh).
  function refresh() {
    if (document.querySelector("[data-live]")) { window.dispatchEvent(new CustomEvent("hearth:refresh")); }
  }
  // EventSource reconnects on its own, but a page that has been asleep may have
  // missed events entirely, so catch up when it comes back.
  connect();
  document.addEventListener("hearth:load", connect);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "visible") { refresh(); }
  });
})();
