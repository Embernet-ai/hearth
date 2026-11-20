// Chooses the palette before the first paint, the same decision internal/web/static/theme.js
// makes for the classic pages and under the same key, so a person's choice carries over.
// Hearth's own palettes are "dark" and "day"; /next/ follows the same two names.
(function () {
  var KEY = "hearth-theme";
  var saved = null;
  try { saved = window.localStorage.getItem(KEY); } catch (err) { /* private mode */ }
  var device = "dark";
  try { if (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches) device = "day"; } catch (err) { /* keep dark */ }
  document.documentElement.setAttribute("data-theme", saved === "day" || saved === "dark" ? saved : device);
})();
