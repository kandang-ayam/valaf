// Theme toggle. The initial theme is set inline in <head> (no-FOUC); this just
// flips and persists it.
(function () {
  window.valafToggleTheme = function () {
    var el = document.documentElement;
    var next = el.getAttribute("data-theme") === "dark" ? "light" : "dark";
    el.setAttribute("data-theme", next);
    try { localStorage.setItem("valaf-theme", next); } catch (e) {}
  };
})();
