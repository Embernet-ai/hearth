// The three inline handlers this application used to have, moved off the page.
//
// They were `onsubmit="return confirm(...)"`, `onchange="this.form.submit()"`
// and `onclick="window.print()"`, written straight onto the element. All three
// worked, and all three were inline script as far as a Content-Security-Policy
// is concerned, which meant script-src had to carry 'unsafe-inline' and at that
// point the directive stops being a control at all: an injected <script> is
// then just as welcome as these nine were.
//
// So they are attributes now, and the behaviour lives here. The markup says
// what it wants and this decides how, which is also the easier of the two to
// change in one place.
//
//   data-confirm="Delete this?"   ask before the form submits, and stop if no
//                                 (on the form, or on the button pressed)
//   data-confirm-list="name"      on that button: list the ticked boxes named
//                                 "name" (by data-label) in the question
//   data-autosubmit               submit the owning form when the value changes
//   data-print                    print the page
//   data-subtasks="<id>"          fold or unfold the rows under that parent (lists)
//   data-access-tree              a ticked box covers the boxes below it (People)
//   data-md="raw"                 switch a rich-text box to its Markdown and back (editor.js)
//   data-md="video"               ask for a YouTube link, insert the short form
//   data-md="preview"             show or hide the live preview (POST /preview)
//   data-nav-key                  remember a sidebar section folded or open (this browser only)
//   data-drag-handle              drag the popover it sits in (any popover also drags by its heading or background)
//   any textarea                  grows as you type, so nothing hides behind a scrollbar
//   data-md                       the formatting toolbar: bold, italic, link, list, Open big
//
// Bound with delegated listeners on the document rather than by walking the
// page at load, so anything drawn afterwards behaves the same as the markup
// that arrived with the first page. A one-shot querySelectorAll at
// DOMContentLoaded would cover the first render and quietly miss every one
// after it, which is the kind of bug that only shows up on the third click.
//
// One more thing lives here now that isn't a data-attribute: restoring
// #doclist's manually-collapsed table groups after a live refresh redraws it
// (see the hearth:refresh-start and -end pair below). Not something a data-*
// attribute can express -- it needs to remember state across the swap, not
// just react to one event -- but it is the same rule as everything else in
// this file: small, reviewable, and scoped to exactly the element it is for.
(function () {
  // ---- confirm before a destructive submit ---------------------------------
  //
  // Capture phase, because a listener in the bubble phase runs after the app has
  // already decided to send the request. Cancelling there stops the post.
  document.addEventListener("submit", function (ev) {
    var form = ev.target;
    if (!form || !form.getAttribute) { return; }

    // The button pressed can carry its own question, so one form with an
    // Approve and a Say no asks only about the approval.
    var btn = ev.submitter;
    var question = (btn && btn.getAttribute && btn.getAttribute("data-confirm")) ||
      form.getAttribute("data-confirm");
    if (!question) { return; }

    // data-confirm-list="space" adds the ticked boxes of that name to the
    // question, by their data-label: what is about to be granted, as it
    // stands at the moment of pressing, not as the page first drew it.
    var listName = btn && btn.getAttribute && btn.getAttribute("data-confirm-list");
    if (listName) {
      var picked = [];
      form.querySelectorAll('input[type="checkbox"]').forEach(function (box) {
        if (box.name === listName && box.checked) {
          picked.push("  • " + (box.getAttribute("data-label") || box.value));
        }
      });
      if (picked.length === 0) {
        window.alert("Tick at least one, or say no instead.");
        ev.preventDefault();
        ev.stopPropagation();
        return;
      }
      question += "\n\n" + picked.join("\n");
    }

    if (!window.confirm(question)) {
      ev.preventDefault();
      ev.stopPropagation();
    }
  }, true);

  // ---- submit on change ----------------------------------------------------
  //
  // The grouping, column and sort pickers on the document list. There is no
  // Apply button on purpose: changing the sort order and then having to press
  // something else is a step that exists only because the page was built that
  // way.
  document.addEventListener("change", function (ev) {
    var el = ev.target;
    if (!el || !el.hasAttribute || !el.hasAttribute("data-autosubmit")) { return; }
    if (el.form) { el.form.submit(); }
  });

  // ---- print ---------------------------------------------------------------
  document.addEventListener("click", function (ev) {
    var el = ev.target;
    if (!el || !el.closest) { return; }
    if (el.closest("[data-print]")) { window.print(); }
  });

  // ---- open a date input's picker on any click into it ---------------------
  //
  // Without this, only the calendar icon itself opens the picker; clicking
  // the date text just moves a text cursor, which on a field whose only
  // purpose is picking a date reads as the control not responding. Feature
  // detected rather than assumed: a browser without showPicker still gets a
  // normal date field, typed by hand, which is what every browser already
  // did before this existed.
  document.addEventListener("click", function (ev) {
    var el = ev.target;
    if (!el || el.tagName !== "INPUT" || el.type !== "date") { return; }
    if (typeof el.showPicker !== "function") { return; }
    try { el.showPicker(); } catch (e) { /* not open to being shown right now */ }
  });

  // ---- keep a manually-collapsed table group collapsed across a live
  //      refresh --------------------------------------------------------
  //
  // A live refresh fetches the page's data again and the list is drawn from it,
  // which draws every group open -- correct for a first load, but it means a
  // group somebody had deliberately closed (to focus on the others) would
  // silently reopen the moment ANYONE anywhere edits a document, not just this
  // tab's own edit, since the refresh is org-wide. Captured right before the new
  // data is applied and reapplied right after, scoped to #doclist specifically.
  var closedGroups = null, openParents = null;
  window.addEventListener("hearth:refresh-start", function () {
    var doclist = document.getElementById("doclist");
    closedGroups = openParents = null;
    if (!doclist) { return; }
    closedGroups = [];
    doclist.querySelectorAll(".tablegroup").forEach(function (d) {
      if (!d.open) { closedGroups.push(d.getAttribute("data-value")); }
    });
    // Opened parents too, for the same reason: somebody else's edit should
    // not fold away the subtasks this person had open.
    openParents = [];
    doclist.querySelectorAll('[data-subtasks][aria-expanded="true"]').forEach(function (b) {
      openParents.push(b.getAttribute("data-subtasks"));
    });
  });
  window.addEventListener("hearth:refresh-end", function () {
    var doclist = document.getElementById("doclist");
    if (!doclist || !closedGroups) { return; }
    doclist.querySelectorAll(".tablegroup").forEach(function (d) {
      if (closedGroups.indexOf(d.getAttribute("data-value")) !== -1) {
        d.open = false;
      }
    });
    openParents.forEach(function (id) {
      doclist.querySelectorAll('[data-subtasks="' + id + '"]').forEach(function (b) {
        setSubtasks(b, true);
      });
    });
    closedGroups = openParents = null;
  });

  // ---- subtasks ------------------------------------------------------------
  //
  // data-subtasks="<id>" on a parent row's toggle shows or hides the rows
  // folded under it (data-child-of="<id>"). Opening shows the direct subtasks
  // only, each with its own toggle; closing hides every level below, so a
  // collapsed parent never leaves a grandchild floating on its own.
  function setSubtasks(btn, open) {
    var table = btn.closest("table");
    if (!table) { return; }
    var id = btn.getAttribute("data-subtasks");
    btn.setAttribute("aria-expanded", open ? "true" : "false");
    btn.textContent = btn.textContent.replace(/^[▸▾]/, open ? "▾" : "▸");
    table.querySelectorAll('tr[data-child-of="' + id + '"]').forEach(function (row) {
      row.hidden = !open;
      if (!open) {
        var inner = row.querySelector("[data-subtasks]");
        if (inner) { setSubtasks(inner, false); }
      }
    });
  }
  document.addEventListener("click", function (ev) {
    var btn = ev.target && ev.target.closest && ev.target.closest("[data-subtasks]");
    if (!btn) { return; }
    ev.preventDefault();
    setSubtasks(btn, btn.getAttribute("aria-expanded") !== "true");
  });

  // data-access-tree: in the "Can see work in" editor a ticked box covers every
  // box below it, which lock as "included". Unticking frees them again,
  // cleared, so parts can be chosen in the same save instead of two. Walks the
  // one department (its <details>) in order, remembering what is ticked above
  // each depth.
  function refreshAccess(group) {
    var tickedAt = [];
    group.querySelectorAll("input[data-depth]").forEach(function (box) {
      var depth = +box.getAttribute("data-depth");
      tickedAt.length = depth;
      var covered = tickedAt.some(Boolean);
      if (covered) {
        box.checked = true;
        box.disabled = true;
      } else if (box.disabled) {
        box.disabled = false;
        box.checked = false;
      }
      var label = box.closest(".accessnode");
      if (label) {
        label.classList.toggle("covered", covered);
        var note = label.querySelector(".incl");
        if (note) { note.hidden = !covered; }
      }
      tickedAt[depth] = box.checked;
    });
  }
  document.addEventListener("change", function (ev) {
    var box = ev.target;
    if (!box.matches || !box.matches("[data-access-tree] input[data-depth]")) { return; }
    refreshAccess(box.closest("details"));
  });

  // Any popover can be moved: by a data-drag-handle (the task panel's title
  // row), by its heading, or by its own empty background. Pointer events, so a
  // mouse or a finger both work. A press that starts on a button, link or
  // field is left alone, so those still do their own thing.
  var drag = null;
  document.addEventListener("pointerdown", function (ev) {
    var t = ev.target;
    if (!t.closest || t.closest("button, a, input, select, textarea, label, iframe, video, summary")) { return; }
    // Open big is not a popover but moves like one, by its toolbar or edges.
    var big = t.closest("form.mdbig-on");
    var pop = big || t.closest("[popover]");
    if (!pop) { return; }
    var grip = t === pop || (big ? t.closest(".mdbar") : t.closest("[data-drag-handle]") ||
      (t.closest("h1, h2, h3, h4") && !t.closest(".prose")));
    if (!grip) { return; }
    var r = pop.getBoundingClientRect();
    if (big) {
      pop.style.width = r.width + "px";
      pop.style.height = r.height + "px";
    }
    pop.style.margin = "0";
    pop.style.right = "auto";
    pop.style.bottom = "auto";
    pop.style.left = r.left + "px";
    pop.style.top = r.top + "px";
    drag = { pop: pop, dx: ev.clientX - r.left, dy: ev.clientY - r.top };
    ev.preventDefault();
  });
  document.addEventListener("pointermove", function (ev) {
    if (!drag) { return; }
    // Keep a corner on screen, so it can always be dragged back.
    var x = Math.min(Math.max(ev.clientX - drag.dx, 0), window.innerWidth - 120);
    var y = Math.min(Math.max(ev.clientY - drag.dy, 0), window.innerHeight - 60);
    drag.pop.style.left = x + "px";
    drag.pop.style.top = y + "px";
  });
  document.addEventListener("pointerup", function () { drag = null; });

  // Every textarea grows with what is typed into it, up to most of the screen,
  // then scrolls. It only grows: a box somebody dragged taller stays that tall.
  // A box marked data-grow-up (the comment box) keeps its bottom edge -- and
  // the Comment button under it -- where it is, by scrolling its column by
  // exactly as much as it grew, so it reads as growing upward.
  function scroller(el) {
    for (var p = el.parentElement; p; p = p.parentElement) {
      var o = getComputedStyle(p).overflowY;
      if ((o === "auto" || o === "scroll") && p.scrollHeight > p.clientHeight) { return p; }
    }
    return null;
  }
  function grow(box) {
    var form = box.closest("form");
    if (form && form.classList.contains("mdbig-on")) { return; }
    var limit = Math.round(window.innerHeight * 0.6);
    if (box.scrollHeight <= box.clientHeight || box.clientHeight >= limit) { return; }
    var before = box.getBoundingClientRect().bottom;
    box.style.height = Math.min(box.scrollHeight + 4, limit) + "px";
    if (box.hasAttribute("data-grow-up")) {
      var delta = box.getBoundingClientRect().bottom - before;
      var s = scroller(box);
      if (s) { s.scrollTop += delta; } else { window.scrollBy(0, delta); }
    }
  }
  document.addEventListener("input", function (ev) {
    if (ev.target.tagName === "TEXTAREA") { grow(ev.target); }
  });

  // data-md: the formatting toolbar (document.html, "mdbar"). Wraps whatever is
  // selected in the Markdown the app already renders. Inserted through
  // execCommand("insertText") where the browser has it, so Ctrl+Z undoes it
  // like typing; setRangeText otherwise.
  function put(box, text, start, end) {
    box.focus();
    box.setSelectionRange(start, end);
    var ok = false;
    try { ok = document.execCommand && document.execCommand("insertText", false, text); } catch (e) { ok = false; }
    if (!ok) {
      box.setRangeText(text, start, end, "end");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    }
  }
  function youtubeID(link) {
    var u;
    try { u = new URL(link); } catch (e) { return ""; }
    if (u.protocol !== "https:" && u.protocol !== "http:") { return ""; }
    var host = u.hostname.replace(/^(www|m)\./, ""), id = "";
    if (host === "youtu.be") {
      id = u.pathname.slice(1);
    } else if (host === "youtube.com" || host === "youtube-nocookie.com") {
      var m = u.pathname.match(/^\/(?:embed|shorts|live)\/([^/]+)/);
      id = m ? m[1] : (u.pathname === "/watch" ? u.searchParams.get("v") || "" : "");
    }
    return /^[A-Za-z0-9_-]{11}$/.test(id) ? id : "";
  }
  // A box nobody has clicked into yet reports its cursor at the very top;
  // a toolbar insert then belongs at the end, after what is already there.
  document.addEventListener("focusin", function (ev) {
    if (ev.target.tagName === "TEXTAREA") { ev.target.setAttribute("data-md-touched", ""); }
  });
  // A toolbar button never takes the cursor: pressing Bold and typing on
  // must keep typing where the cursor was, in bold, not into the button.
  document.addEventListener("mousedown", function (ev) {
    if (ev.target.closest && ev.target.closest("[data-md]:not([data-md=big]):not([data-md=preview])")) { ev.preventDefault(); }
  });

  // A box with the rich-text editor attached (editor.js, textarea.hearthRich)
  // is formatted through the editor; the same prompts, the same YouTube check.
  function formatRich(rich, kind) {
    if (kind === "link") {
      var href = window.prompt("Link address", "https://");
      if (!href || href === "https://") { return; }
      href = href.trim();
      if (!/^(https?:\/\/|\/(?!\/))/i.test(href)) { window.alert("Links start with https:// (or / for a page in Hearth)."); return; }
      rich.link(href);
    } else if (kind === "video") {
      var link = rich.selectedText() || window.prompt("Paste the YouTube link");
      if (!link) { return; }
      var id = youtubeID(link.trim());
      if (!id) { window.alert("That is not a YouTube video link."); return; }
      rich.block("https://youtu.be/" + id);
    } else {
      rich.format(kind);
    }
  }
  function format(box, kind) {
    if (box.hearthRich && box.hidden) { formatRich(box.hearthRich, kind); return; }
    if (!box.hasAttribute("data-md-touched")) { box.setSelectionRange(box.value.length, box.value.length); }
    var start = box.selectionStart, end = box.selectionEnd;
    var sel = box.value.slice(start, end);
    if (kind === "bold" || kind === "italic") {
      var mark = kind === "bold" ? "**" : "*";
      var word = sel || (kind === "bold" ? "bold text" : "italic text");
      put(box, mark + word + mark, start, end);
      box.setSelectionRange(start + mark.length, start + mark.length + word.length);
    } else if (kind === "link") {
      var label = sel || "link text";
      put(box, "[" + label + "](https://)", start, end);
      // Select the address, which is the part still to be typed.
      var at = start + label.length + 3;
      box.setSelectionRange(at, at + 8);
    } else if (kind === "video") {
      // Any YouTube link in, the short one out, on a line of its own so it
      // plays. Same shapes the server accepts (markdown.youtubeID); anything
      // else is refused here rather than posted as a link that will not play.
      var link = sel || window.prompt("Paste the YouTube link");
      if (!link) { return; }
      var id = youtubeID(link.trim());
      if (!id) { window.alert("That is not a YouTube video link."); box.focus(); return; }
      var before = start > 0 && box.value.charAt(start - 1) !== "\n" ? "\n" : "";
      var after = box.value.charAt(end) === "\n" ? "" : "\n";
      put(box, before + "https://youtu.be/" + id + after, start, end);
    } else if (kind === "list" || kind === "checklist") {
      var bullet = kind === "list" ? "- " : "- [ ] ";
      var lineStart = box.value.lastIndexOf("\n", start - 1) + 1;
      var block = box.value.slice(lineStart, end) || "item";
      var listed = block.split("\n").map(function (l) { return /^\s*- /.test(l) ? l : bullet + l; }).join("\n");
      put(box, listed, lineStart, end);
      box.setSelectionRange(lineStart + listed.length, lineStart + listed.length);
    }
    grow(box);
  }
  function setBig(form, on) {
    form.classList.toggle("mdbig-on", on);
    // Forget where it was dragged, so the next Open big starts centred and
    // the small form goes back into the page.
    ["margin", "left", "top", "right", "bottom", "width", "height"].forEach(function (k) { form.style[k] = ""; });
    var btn = form.querySelector(".mdbig-toggle");
    if (btn) { btn.innerHTML = on ? "Done" : "&#10530; Open big"; }
    var box = form.querySelector("textarea");
    if (box && box.hearthRich && box.hidden) { box.hearthRich.editor.commands.focus(); }
    else if (box) { box.focus(); }
  }
  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest && ev.target.closest("[data-md]");
    if (!btn) { return; }
    var form = btn.closest("form");
    var box = form && form.querySelector("textarea");
    if (!box) { return; }
    ev.preventDefault();
    if (btn.getAttribute("data-md") === "big") {
      setBig(form, !form.classList.contains("mdbig-on"));
    } else if (btn.getAttribute("data-md") === "preview") {
      // Shown only on request; turning it on renders what is already typed.
      var on = form.classList.toggle("mdpreview-on");
      btn.setAttribute("aria-pressed", on ? "true" : "false");
      if (on) { box.dispatchEvent(new Event("mdpreview", { bubbles: true })); }
    } else if (btn.getAttribute("data-md") === "raw") {
      // Markdown instead of the editor, and back. Only offered once the
      // editor is attached; without it the box is Markdown already.
      if (!box.hearthRich) { return; }
      btn.setAttribute("aria-pressed", box.hearthRich.toggleRaw() ? "true" : "false");
    } else {
      format(box, btn.getAttribute("data-md"));
    }
  });
  // Escape leaves the big editor first, rather than also closing the task
  // panel around it and losing the place.
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Escape") { return; }
    var open = document.querySelector("form.mdbig-on");
    if (!open) { return; }
    ev.preventDefault();
    ev.stopPropagation();
    setBig(open, false);
  }, true);

  // data-nav-key: the sidebar remembers which sections somebody folded or
  // unfolded, across page loads, in this browser only (a convenience, so
  // localStorage; it may be missing or blocked, and then the page is simply
  // as the server drew it). One exception wins over the memory: a section
  // holding the page being looked at is always open, or the reader would
  // land somewhere with their own place hidden.
  var NAV_KEY = "hearth.nav";
  var navState = {};
  try { navState = JSON.parse(window.localStorage.getItem(NAV_KEY) || "{}") || {}; } catch (e) { navState = {}; }
  document.querySelectorAll("details[data-nav-key]").forEach(function (d) {
    var saved = navState[d.getAttribute("data-nav-key")];
    if (saved === undefined) { return; }
    var want = saved || !!d.querySelector(".navitem.on");
    if (d.open === want) { return; }
    // Changed by this script, not by the reader: its toggle event (which
    // arrives later) must not be saved as their choice.
    d.setAttribute("data-nav-restoring", "");
    d.open = want;
  });
  // toggle does not bubble, so this listens in the capture phase.
  document.addEventListener("toggle", function (ev) {
    var d = ev.target;
    if (!d.matches || !d.matches("details[data-nav-key]")) { return; }
    if (d.hasAttribute("data-nav-restoring")) { d.removeAttribute("data-nav-restoring"); return; }
    navState[d.getAttribute("data-nav-key")] = d.open;
    try { window.localStorage.setItem(NAV_KEY, JSON.stringify(navState)); } catch (e) { /* nothing to keep it in */ }
  }, true);
})();

// The phone menu. It opens on a checkbox with no script at all; this only adds
// the keyboard: Enter or Space on the menu button opens it, Escape closes it.
(function () {
  document.addEventListener("keydown", function (ev) {
    var box = document.getElementById("navtoggle");
    if (!box) { return; }
    if (ev.key === "Escape" && box.checked) {
      box.checked = false;
      return;
    }
    var btn = ev.target.closest ? ev.target.closest(".menubtn") : null;
    if (btn && (ev.key === "Enter" || ev.key === " ")) {
      ev.preventDefault();
      box.checked = !box.checked;
    }
  });
})();

// A page restored from the back/forward cache keeps its checkbox state, so the
// phone menu would reappear open after Back. Close it whenever a page is shown.
window.addEventListener("pageshow", function () {
  var box = document.getElementById("navtoggle");
  if (box) { box.checked = false; }
});

// Phone cards for list tables: give every cell the name of its column, so the
// CSS can print "Status  Todo" when a row turns into a card. Run on load and
// again whenever a page is drawn, since filters and live updates re-render
// tables without a page load.
(function () {
  function label(root) {
    (root || document).querySelectorAll("table.list").forEach(function (t) {
      var head = t.querySelector("thead tr") || t.querySelector("tr");
      if (!head) { return; }
      var names = [];
      head.querySelectorAll("th").forEach(function (th) {
        var span = +th.getAttribute("colspan") || 1;
        for (var k = 0; k < span; k++) { names.push(th.textContent.trim()); }
      });
      if (!names.length) { return; }
      t.querySelectorAll("tbody > tr").forEach(function (tr) {
        var i = 0;
        Array.prototype.forEach.call(tr.children, function (td) {
          var span = +td.getAttribute("colspan") || 1;
          if (td.tagName === "TD" && span === 1 && names[i] && !td.hasAttribute("data-label")) {
            td.setAttribute("data-label", names[i]);
          }
          i += span;
        });
      });
    });
  }
  document.addEventListener("DOMContentLoaded", function () { label(document); });
  // A page the app has drawn announces itself.
  document.addEventListener("hearth:load", function (ev) { label(ev.target); });
})();

// Attachments: each [data-quickupload] form becomes one button, and choosing a
// file sends it straight away. It submits through the form's own button, so the
// submit event fires and an upload with no signal is queued like any other.
(function () {
  function arm() {
    document.querySelectorAll("form[data-quickupload]").forEach(function (f) {
      f.classList.add("quick");
    });
  }
  document.addEventListener("DOMContentLoaded", arm);
  document.addEventListener("hearth:load", arm); // idempotent, so safe for every page the rebuilt interface draws
  document.addEventListener("change", function (ev) {
    var input = ev.target;
    if (!input.matches || !input.matches("form.quick input[type=file]")) { return; }
    if (!input.files || !input.files.length) { return; }
    var form = input.form;
    var btn = form.querySelector("button[type=submit]");
    form.classList.add("sending");
    var label = form.querySelector("label");
    if (label) { label.textContent = "Uploading…"; }
    if (btn) { btn.click(); } else { form.submit(); }
  });
})();

// The Assigned popover: people already on the task come first, and the search
// box narrows the list as you type. Both are conveniences; without them the
// form is the same list in its usual order.
(function () {
  function checkedFirst() {
    document.querySelectorAll("[data-checked-first]").forEach(function (box) {
      var rows = Array.prototype.slice.call(box.children);
      rows.filter(function (r) { return r.querySelector("input[type=checkbox]:checked"); })
        .reverse().forEach(function (r) { box.insertBefore(r, box.firstChild); });
    });
  }
  document.addEventListener("DOMContentLoaded", checkedFirst);
  document.addEventListener("hearth:load", checkedFirst); // idempotent: checked rows are already first the second time
  document.addEventListener("input", function (ev) {
    var f = ev.target;
    if (!f.matches || !f.matches("[data-filter-rows]")) { return; }
    var q = f.value.trim().toLowerCase();
    var scope = f.closest("form") || document;
    scope.querySelectorAll(f.getAttribute("data-filter-rows")).forEach(function (row) {
      row.hidden = q !== "" && row.textContent.toLowerCase().indexOf(q) === -1;
    });
  });
})();

// A line that says what just happened and goes away by itself, for a change
// that moved more than the thing somebody touched (closing a task closed its
// subtasks). A status line, not a dialog: nothing to answer, nothing blocking.
window.hearthNotice = function (text) {
  if (!text) { return; }
  var box = document.getElementById("hearth-notice");
  if (!box) {
    box = document.createElement("div");
    box.id = "hearth-notice";
    box.className = "card small";
    box.setAttribute("role", "status");
    box.style.cssText = "position:fixed;left:50%;bottom:1rem;transform:translateX(-50%);z-index:50;max-width:min(32rem,calc(100vw - 2rem));padding:.5rem 1rem;";
    document.body.appendChild(box);
  }
  box.textContent = text;
  box.hidden = false;
  clearTimeout(window.hearthNoticeTimer);
  window.hearthNoticeTimer = setTimeout(function () { box.hidden = true; }, 6000);
};
