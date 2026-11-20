// Dragging a card between board columns.
//
// Native HTML5 drag and drop, no library. The card moves in the page first and
// the server is told afterwards, because a drag that waits for a round trip
// before the card moves feels broken on a plant floor connection. If the server
// refuses, the card goes back where it came from and says why: an optimistic
// move that silently reverts is worse than one that never moved.
//
// Delegated from the document, so a board swapped in by a live refresh keeps
// working without rebinding anything.
(function () {
  var dragging = null;
  var origin = null;

  function boardOf(el) {
    return el && el.closest ? el.closest(".board") : null;
  }

  document.addEventListener("dragstart", function (ev) {
    var card = ev.target.closest ? ev.target.closest(".card2[draggable]") : null;
    if (!card) { return; }
    dragging = card;
    origin = card.parentNode;
    card.classList.add("dragging");
    // Firefox will not start a drag at all without data on the transfer.
    ev.dataTransfer.setData("text/plain", card.getAttribute("data-id") || "");
    ev.dataTransfer.effectAllowed = "move";
  });

  document.addEventListener("dragend", function () {
    if (dragging) { dragging.classList.remove("dragging"); }
    document.querySelectorAll(".cards.over").forEach(function (el) {
      el.classList.remove("over");
    });
    dragging = null;
  });

  document.addEventListener("dragover", function (ev) {
    var zone = ev.target.closest ? ev.target.closest("[data-drop]") : null;
    if (!zone || !dragging) { return; }
    // Cancelling the event is what marks this a valid drop target. Without it
    // the browser refuses every drop and the whole thing looks inert.
    ev.preventDefault();
    ev.dataTransfer.dropEffect = "move";
    zone.classList.add("over");
  });

  document.addEventListener("dragleave", function (ev) {
    var zone = ev.target.closest ? ev.target.closest("[data-drop]") : null;
    if (zone) { zone.classList.remove("over"); }
  });

  document.addEventListener("drop", function (ev) {
    var zone = ev.target.closest ? ev.target.closest("[data-drop]") : null;
    if (!zone || !dragging) { return; }
    ev.preventDefault();
    zone.classList.remove("over");

    var card = dragging;
    var from = origin;
    var board = boardOf(zone);
    var column = zone.closest(".boardcol");
    if (!board || !column || from === zone) { return; }

    var field = board.getAttribute("data-group") || "status";
    var value = column.getAttribute("data-value") || "";
    var id = card.getAttribute("data-id");

    zone.appendChild(card);
    card.classList.add("pending");

    var body = new URLSearchParams();
    body.set("field", field);
    body.set("value", value);

    // The task board posts to /documents; another board (the deal pipeline)
    // names its own prefix with data-move, and nothing else about the drag
    // changes.
    var prefix = board.getAttribute("data-move") || "/documents";
    fetch(prefix + "/" + id + "/move", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: body.toString(),
      credentials: "same-origin"
    }).then(function (res) {
      card.classList.remove("pending");
      if (res.ok) {
        // A card dropped into done took its open subtasks with it: say so.
        var note = res.headers.get("X-Hearth-Notice");
        if (note && window.hearthNotice) { window.hearthNotice(note); }
        return;
      }
      // Put it back, and say so. Silence here means somebody believes a job
      // was reassigned when it was not.
      from.appendChild(card);
      return res.text().then(function (msg) {
        card.classList.add("refused");
        card.setAttribute("title", msg || "that move was refused");
      });
    }).catch(function () {
      card.classList.remove("pending");
      from.appendChild(card);
      card.classList.add("refused");
      card.setAttribute("title", "could not reach the server");
    });
  });
})();
