(function () {
  try {
    var q = new URLSearchParams(location.search).get("scheme");
    var s = q || localStorage.getItem("strike.scheme") || "dark";
    if (s !== "light") s = "dark";
    document.documentElement.setAttribute("data-scheme", s);
    if (q === "light" || q === "dark") localStorage.setItem("strike.scheme", s);
  } catch (e) {}
})();
