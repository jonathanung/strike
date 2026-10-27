(function () {
  var meta = document.querySelector('meta[name="strike-desktop-gateway"]');
  var name = meta ? meta.getAttribute("content") : "Gateway";
  if (!name) name = "Gateway";
  var link = document.createElement("a");
  link.id = "strike-desktop-chrome";
  link.href = "/_desktop/";
  link.title = "Gateways (Alt+G)";
  link.textContent = name;
  document.body.appendChild(link);
  document.addEventListener("keydown", function (ev) {
    if (ev.altKey && (ev.key === "g" || ev.key === "G")) {
      ev.preventDefault();
      location.href = "/_desktop/";
    }
  });
})();
