// Choosing the palette, before the page is painted.
//
// This is deliberately its own file, loaded synchronously in the head rather
// than deferred with everything else, and that is the whole reason it exists.
//
// The palette is chosen by script: the stylesheet's own :root is the dark one
// and :root[data-theme="day"] is daylight, defined once so the contrast test
// has a single block to measure. A second copy inside a prefers-color-scheme
// media query would be a second set of colours that nothing checks, and the two
// would drift the first time one was nudged.
//
// The cost of that decision is timing. While this ran inside the deferred
// bundle, a laptop set to light rendered the entire page in the dark palette,
// then repainted to daylight once the script finally ran. Every navigation
// flashed dark, and on a slow connection it stayed dark long enough to look
// like the setting did not work at all.
//
// A synchronous script in the head runs before the first paint, so the right
// palette is on the element before anything is drawn. It is a few hundred bytes
// and it blocks for a fraction of a millisecond, which is the correct trade
// against showing somebody the wrong colours on every page load.
//
// The toggle, and following the system when it changes mid-session, stay in
// pwa.js. Only the decision has to happen this early.
(function () {
  var KEY = "hearth-theme";

  function deviceWants() {
    try {
      return window.matchMedia &&
        window.matchMedia("(prefers-color-scheme: light)").matches ? "day" : "dark";
    } catch (err) { return "dark"; }
  }

  var saved = null;
  try { saved = window.localStorage.getItem(KEY); }
  catch (err) { /* private mode. The device preference still applies. */ }

  // An explicit choice wins over the device, because it is about where somebody
  // is standing rather than what their laptop was set to last week.
  var theme = saved === "day" || saved === "dark" ? saved : deviceWants();
  document.documentElement.setAttribute("data-theme", theme);
})();
