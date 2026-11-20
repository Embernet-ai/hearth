// Dragging a department or folder to reorder it among its siblings.
//
// Native drag and drop, the same shape as board.js: the row moves in the page
// first, the server is told afterwards, and a refusal reloads to the truth. A
// node's whole subtree -- the rows indented under it -- moves with it, because
// the tree is one flat list and a parent's children are the rows that follow it
// at a greater depth. Only siblings (rows under the same parent) are valid drop
// targets; moving a folder into a different department is the Edit > Move
// control, not a drag.
(function () {
  var dragging = null;

  // The dragged row and every row indented beneath it, in order.
  function blockOf(li) {
    var depth = +li.getAttribute("data-depth");
    var block = [li];
    var n = li.nextElementSibling;
    while (n && +n.getAttribute("data-depth") > depth) {
      block.push(n);
      n = n.nextElementSibling;
    }
    return block;
  }

  function clearOver() {
    document.querySelectorAll(".spacetree li.over").forEach(function (e) {
      e.classList.remove("over");
    });
  }

  document.addEventListener("dragstart", function (ev) {
    var li = ev.target.closest ? ev.target.closest("li[draggable]") : null;
    if (!li || !li.closest(".spacetree")) { return; }
    dragging = li;
    li.classList.add("dragging");
    ev.dataTransfer.setData("text/plain", li.getAttribute("data-space") || "");
    ev.dataTransfer.effectAllowed = "move";
  });

  document.addEventListener("dragend", function () {
    if (dragging) { dragging.classList.remove("dragging"); }
    clearOver();
    dragging = null;
  });

  function siblingTarget(ev) {
    if (!dragging) { return null; }
    var li = ev.target.closest ? ev.target.closest("li[data-space]") : null;
    if (!li || li === dragging) { return null; }
    if (li.getAttribute("data-parent") !== dragging.getAttribute("data-parent")) { return null; }
    return li;
  }

  document.addEventListener("dragover", function (ev) {
    var li = siblingTarget(ev);
    if (!li) { return; }
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    clearOver();
    li.classList.add("over");
  });

  document.addEventListener("dragleave", function (ev) {
    var li = ev.target.closest ? ev.target.closest("li[data-space]") : null;
    if (li) { li.classList.remove("over"); }
  });

  // Dropping only rearranges the page -- the live preview. Nothing is saved
  // until Save is pressed, so a drag can be undone by dragging again or by
  // Cancel, and the server hears one tidy request instead of one per nudge.
  document.addEventListener("drop", function (ev) {
    var target = siblingTarget(ev);
    if (!target) { return; }
    ev.preventDefault();
    target.classList.remove("over");

    var ul = dragging.parentNode;
    var block = blockOf(dragging);
    // If the dragged row is above the target, land after the target's block;
    // otherwise before it. That is what makes "drag to the bottom" land last.
    var below = dragging.compareDocumentPosition(target) & Node.DOCUMENT_POSITION_FOLLOWING;
    var ref;
    if (below) {
      var tb = blockOf(target);
      ref = tb[tb.length - 1].nextElementSibling;
    } else {
      ref = target;
    }
    block.forEach(function (n) { ul.insertBefore(n, ref); });
    markDirty();
    recompute();
  });

  // Collapse: a row is hidden when any ancestor's caret is collapsed. One pass
  // down the flat list, carrying the depths of the collapsed ancestors.
  function recompute() {
    var tree = document.querySelector(".spacetree");
    if (!tree) { return; }
    var stack = [];
    tree.querySelectorAll("li[data-space]").forEach(function (li) {
      var d = +li.getAttribute("data-depth");
      while (stack.length && stack[stack.length - 1] >= d) { stack.pop(); }
      li.hidden = stack.length > 0;
      var tog = li.querySelector(".treetoggle");
      if (tog && tog.getAttribute("aria-expanded") === "false") { stack.push(d); }
    });
  }

  document.addEventListener("click", function (ev) {
    var tog = ev.target.closest ? ev.target.closest(".treetoggle") : null;
    if (!tog) { return; }
    ev.preventDefault();
    var open = tog.getAttribute("aria-expanded") !== "false";
    tog.setAttribute("aria-expanded", open ? "false" : "true");
    tog.textContent = open ? "▸" : "▾"; // ▸ / ▾
    recompute();
  });

  function bar() { return document.getElementById("spaceorderbar"); }

  function markDirty() {
    var b = bar();
    if (b) { b.hidden = false; }
  }

  document.addEventListener("click", function (ev) {
    var t = ev.target;
    if (!t || !t.closest) { return; }
    if (t.closest("#spaceorder-cancel")) {
      location.reload();
      return;
    }
    if (!t.closest("#spaceorder-save")) { return; }
    ev.preventDefault();
    var tree = document.querySelector(".spacetree");
    if (!tree) { return; }
    var body = new URLSearchParams();
    tree.querySelectorAll("li[data-space]").forEach(function (li) {
      body.append("id", li.getAttribute("data-space"));
    });
    var save = document.getElementById("spaceorder-save");
    if (save) { save.disabled = true; }
    fetch("/spaces/save-order", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: body.toString(),
      credentials: "same-origin"
    }).then(function (res) {
      // Reload either way: on success so the sidebar rail (rendered on the
      // server) shows the new order too, not just this page; on failure to
      // snap back to the saved truth.
      location.reload();
    }).catch(function () { location.reload(); });
  });

  // Inline create: the + on a row opens a name box right there instead of
  // sending you to the dropdown. The row keeps its href, so without scripts it
  // still works the old way.
  // Open an inline name box where the new space will land: under a row for the
  // row's +, or at the top of the tree for "+ New department".
  function openCreate(parent, row) {
    var tree = document.querySelector(".spacetree");
    if (!tree) { return; }
    var existing = document.getElementById("newfolderrow");
    if (existing) { existing.remove(); }

    var depth = row ? +row.getAttribute("data-depth") : -1;
    var li = document.createElement("li");
    li.id = "newfolderrow";
    li.style.paddingLeft = ((depth + 1) * 1.3) + "rem";
    li.innerHTML =
      '<form class="row" action="/spaces" method="post">' +
      '<input type="hidden" name="parent" value="' + (parent || "") + '">' +
      '<input type="hidden" name="kind" value="folder">' +
      '<input name="name" placeholder="' + (row ? "New folder" : "New department") + '" aria-label="Name" required style="width:14rem">' +
      '<button type="submit" class="ghost small">Add</button>' +
      '<button type="button" id="newfoldercancel" class="ghost small">Cancel</button>' +
      '</form>';
    if (row) {
      // Just after the row's whole subtree, as its last child.
      var after = row, n = row.nextElementSibling;
      while (n && +n.getAttribute("data-depth") > depth) { after = n; n = n.nextElementSibling; }
      if (after.nextElementSibling) { tree.insertBefore(li, after.nextElementSibling); }
      else { tree.appendChild(li); }
    } else {
      tree.insertBefore(li, tree.firstElementChild);
    }
    var input = li.querySelector("input[name=name]");
    if (input) { input.focus(); }
    li.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { li.remove(); }
    });
  }

  document.addEventListener("click", function (ev) {
    var plus = ev.target.closest ? ev.target.closest("[data-addunder]") : null;
    if (!plus) { return; }
    ev.preventDefault();
    openCreate(plus.getAttribute("data-addunder"), plus.closest("li[data-space]"));
  });

  // The rail's + links here with ?parent=<id>; open the box for that node on
  // arrival, so the gesture finishes where it started.
  // Runs for the page now and for content that arrives later (hearth:load, which the app fires after drawing a page).
  // The marker on the tree keeps a second pass from opening the box again once it has been opened for this page.
  function openForParent() {
    var tree = document.querySelector(".spacetree");
    if (!tree || tree.hearthParentOpened) { return; }
    var m = /[?&]parent=([^&]*)/.exec(location.search);
    if (!m) { return; }
    var pid = decodeURIComponent(m[1]);
    var row = pid ? document.querySelector('li[data-space="' + pid + '"]') : null;
    if (pid && !row) { return; }
    tree.hearthParentOpened = true;
    openCreate(pid, row);
  }
  document.addEventListener("DOMContentLoaded", openForParent);
  document.addEventListener("hearth:load", openForParent);

  document.addEventListener("click", function (ev) {
    if (ev.target && ev.target.id === "newfoldercancel") {
      var li = document.getElementById("newfolderrow");
      if (li) { li.remove(); }
    }
  });
})();
