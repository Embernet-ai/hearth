// Dragging cards, and drawing the lines between them.
//
// Progressive on purpose. Every card carries a form with its coordinates in
// plain number fields, and those work with no scripting at all, because a
// plant floor is exactly where a browser has scripting switched off by policy
// and a diagram nobody can rearrange is still a diagram they can read.
//
// This file makes it pleasant. It does not make it possible.
(function () {
  "use strict";

  // Run for the page now, and again whenever new content arrives (hearth:load, which the app fires after
  // drawing a page). The marker keeps a second pass from binding the same surface twice.
  function init() {
  const surface = document.querySelector(".canvas");
  if (!surface || surface.hearthCanvas) return;
  surface.hearthCanvas = true;
  const canvasID = surface.dataset.canvas;

  // ---- lines -------------------------------------------------------------
  //
  // Recomputed from the cards rather than stored, so a line follows a card
  // that moved and never has to be kept in step with one.
  function centre(node) {
    return {
      x: node.offsetLeft + node.offsetWidth / 2,
      y: node.offsetTop + node.offsetHeight / 2,
    };
  }

  function redraw() {
    surface.querySelectorAll(".canvas-edges line").forEach(function (line) {
      const from = surface.querySelector('[data-node="' + line.dataset.from + '"]');
      const to = surface.querySelector('[data-node="' + line.dataset.to + '"]');
      if (!from || !to) {
        line.style.display = "none";
        return;
      }
      const a = centre(from), b = centre(to);
      line.setAttribute("x1", a.x);
      line.setAttribute("y1", a.y);
      line.setAttribute("x2", b.x);
      line.setAttribute("y2", b.y);
    });
  }
  redraw();
  window.addEventListener("resize", redraw);

  // ---- dragging ----------------------------------------------------------

  let dragging = null;

  surface.addEventListener("pointerdown", function (e) {
    const node = e.target.closest(".node");
    // Not from a control. Starting a drag when somebody meant to press a
    // button is the thing that makes a canvas feel broken.
    if (!node || e.target.closest("input, button, a, select, form")) return;

    dragging = {
      node: node,
      dx: e.clientX - node.offsetLeft,
      dy: e.clientY - node.offsetTop,
    };
    node.setPointerCapture(e.pointerId);
    node.classList.add("dragging");
  });

  surface.addEventListener("pointermove", function (e) {
    if (!dragging) return;
    // Clamped at zero on both axes, so a card cannot be dragged above or left
    // of the surface and lost. The server clamps too; this is only so the
    // thing under the finger matches what will be saved.
    const x = Math.max(0, Math.round(e.clientX - dragging.dx));
    const y = Math.max(0, Math.round(e.clientY - dragging.dy));
    dragging.node.style.left = x + "px";
    dragging.node.style.top = y + "px";
    redraw();
  });

  function endDrag(e) {
    if (!dragging) return;
    const node = dragging.node;
    dragging = null;
    node.classList.remove("dragging");

    const form = node.querySelector(".node-move");
    if (!form) return;
    const x = node.offsetLeft, y = node.offsetTop;
    form.querySelector('[name="x"]').value = x;
    form.querySelector('[name="y"]').value = y;

    // Saved with the same form the button posts, so there is one route to the
    // server rather than two that can disagree. quiet=1 asks for a status
    // rather than a page, because reloading after every drag would lose the
    // scroll position and the drag that is halfway through.
    const body = new URLSearchParams(new FormData(form));
    body.set("quiet", "1");
    fetch("/canvas/" + canvasID + "/nodes/" + node.dataset.node, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: body.toString(),
    }).catch(function () {
      // A failed save leaves the card where it was dropped and the number in
      // the field, so the Move button is still there to press. Silently
      // snapping it back would look like the drag never happened.
    });
  }

  surface.addEventListener("pointerup", endDrag);
  surface.addEventListener("pointercancel", endDrag);
  }
  init();
  document.addEventListener("hearth:load", init);
})();
