// Reading an asset code off a label.
//
// # What this is and is not
//
// It is the camera, where the browser can decode for us, and a text box
// everywhere else. It is not a QR decoder written in JavaScript. The label
// printed by this application carries the code in large monospace under the
// symbol precisely so it can be read out loud or typed with cold hands, and a
// decoder nobody can maintain sitting between a camera and a five character
// code is a bad trade.
//
// BarcodeDetector is in the browsers on the phones this is for. Where it is
// missing the box is focused on load, the keyboard opens, and typing five
// characters takes about as long as lining up a shot.
(function () {
  // Run for the page now, and again whenever new content arrives (hearth:load, which the app fires after
  // drawing a page), so a scan page drawn after this file loaded still works.
  // The marker keeps a second pass from binding the same elements twice.
  function init() {
  var page = document.querySelector("[data-scan]");
  if (!page || page.hearthScan) { return; }
  page.hearthScan = true;

  var video = page.querySelector("video");
  var status = page.querySelector("[data-scan-status]");
  var manualForm = page.querySelector("[data-scan-manual]");
  var manual = manualForm ? manualForm.querySelector("input") : null;
  var start = page.querySelector("[data-scan-start]");
  var stream = null;
  var detector = null;
  var running = false;

  function say(text) { if (status) { status.textContent = text; } }

  // Typed in. The form submits to /scan with no script at all and the server
  // sends it on, so this only saves a round trip; it is not what makes the box
  // work.
  if (manualForm) {
    manualForm.addEventListener("submit", function (ev) {
      var value = (manual.value || "").trim();
      if (!value) { return; }
      ev.preventDefault();
      window.location.href = "/a/" + encodeURIComponent(value);
    });
  }

  if (!("BarcodeDetector" in window)) {
    say("This browser cannot use the camera to read a code. The code is " +
        "printed under the symbol on the label; type it in.");
    if (start) { start.hidden = true; }
    if (manual) { manual.focus(); }
    return;
  }

  start.addEventListener("click", function () {
    if (running) { stop(); return; }
    begin();
  });

  async function begin() {
    try {
      detector = detector || new window.BarcodeDetector({ formats: ["qr_code"] });
    } catch (err) {
      say("This browser cannot read that kind of code. Type it in instead.");
      return;
    }
    try {
      // The back camera. facingMode rather than picking a device, because the
      // device list on a phone is not in a reliable order and asking for the
      // environment is exactly what this means.
      stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: { ideal: "environment" } }, audio: false,
      });
    } catch (err) {
      say("The camera was not available. It may be blocked for this site, or " +
          "in use by something else. Type the code in instead.");
      return;
    }
    video.srcObject = stream;
    video.hidden = false;
    await video.play();
    running = true;
    start.textContent = "Stop the camera";
    say("Point it at the label.");
    tick();
  }

  function stop() {
    running = false;
    if (stream) { stream.getTracks().forEach(function (t) { t.stop(); }); }
    stream = null;
    video.hidden = true;
    start.textContent = "Use the camera";
    say("");
  }

  async function tick() {
    if (!running) { return; }
    try {
      var found = await detector.detect(video);
      if (found && found.length > 0) {
        var value = (found[0].rawValue || "").trim();
        var code = codeFrom(value);
        if (code) {
          stop();
          window.location.href = "/a/" + encodeURIComponent(code);
          return;
        }
        say("That is a code, but not one of ours: " + value.slice(0, 60));
      }
    } catch (err) {
      // A frame that could not be read is not worth reporting. The next one
      // is along in a moment.
    }
    // Four times a second. Faster is not more useful and does drain a battery
    // somebody needs for the rest of the shift.
    window.setTimeout(tick, 250);
  }

  // codeFrom accepts a full label URL or a bare code.
  //
  // The label encodes an address so that a phone's own camera application,
  // which knows nothing about this site, still lands somewhere useful. Reading
  // it here means pulling the code back out.
  function codeFrom(value) {
    var m = /\/a\/([A-Za-z0-9._-]{1,64})$/.exec(value);
    if (m) { return m[1]; }
    if (/^[A-Za-z0-9._-]{1,64}$/.test(value)) { return value; }
    return "";
  }

  window.addEventListener("pagehide", stop);
  }
  init();
  document.addEventListener("hearth:load", init);
})();
