(function () {
  var listEl = document.getElementById("list");
  var emptyEl = document.getElementById("empty");
  var errorEl = document.getElementById("error");
  var form = document.getElementById("add");
  var themeBtn = document.getElementById("theme");
  var trustDlg = document.getElementById("trust");
  var helpDlg = document.getElementById("help");
  var fpEl = document.getElementById("fp");
  var selected = 0;
  var rows = [];
  var pendingTrust = null;

  function scheme() {
    return document.documentElement.getAttribute("data-scheme") === "light" ? "light" : "dark";
  }

  function setScheme(next) {
    document.documentElement.setAttribute("data-scheme", next);
    try { localStorage.setItem("strike.scheme", next); } catch (e) {}
    themeBtn.setAttribute("aria-pressed", next === "light" ? "true" : "false");
    themeBtn.textContent = next === "light" ? "Dark" : "Light";
  }

  themeBtn.addEventListener("click", function () {
    setScheme(scheme() === "light" ? "dark" : "light");
  });
  setScheme(scheme());

  function showError(text, kind) {
    if (!text) {
      errorEl.hidden = true;
      errorEl.textContent = "";
      return;
    }
    errorEl.hidden = false;
    errorEl.dataset.kind = kind || "warn";
    errorEl.textContent = text;
  }

  function chipClass(status) {
    if (status === "connected") return "chip ok";
    if (status === "bad_token" || status === "unreachable" || status === "sidecar" || status === "ssh") return "chip bad";
    if (status === "idle") return "chip";
    return "chip warn";
  }

  function label(row) {
    var map = {
      idle: "Idle",
      connected: "Connected",
      unreachable: "Unreachable",
      bad_token: "Bad token",
      needs_token: "Needs token",
      bad_cert: "Untrusted certificate",
      pin_mismatch: "Pin mismatch",
      version_mismatch: "Version mismatch",
      cleartext_refused: "Cleartext blocked",
      pairing_unsupported: "Pairing unavailable",
      keyring: "Keyring error"
    };
    return map[row.status] || row.status || "Idle";
  }

  async function api(path, options) {
    var res = await fetch(path, options);
    var text = await res.text();
    var body = {};
    try { body = text ? JSON.parse(text) : {}; } catch (e) { body = { detail: text }; }
    if (!res.ok) {
      var err = new Error(body.detail || body.error || ("HTTP " + res.status));
      err.body = body;
      err.status = res.status;
      throw err;
    }
    return body;
  }

  function render(data) {
    rows = data.gateways || [];
    listEl.replaceChildren();
    emptyEl.hidden = rows.length !== 0;
    if (selected >= rows.length) selected = Math.max(0, rows.length - 1);
    rows.forEach(function (row, index) {
      var li = document.createElement("li");
      var btn = document.createElement("div");
      btn.className = "gw" + (index === selected ? " on" : "");
      btn.setAttribute("role", "option");
      btn.setAttribute("aria-selected", index === selected ? "true" : "false");
      var text = document.createElement("span");
      var name = document.createElement("span");
      name.className = "name";
      name.textContent = row.name;
      var path = document.createElement("span");
      path.className = "path";
      path.textContent = row.url || row.mode;
      text.append(name, path);
      var actions = document.createElement("span");
      actions.className = "actions";
      var chip = document.createElement("span");
      chip.className = chipClass(row.status);
      chip.textContent = label(row);
      var connect = document.createElement("button");
      connect.type = "button";
      connect.className = "primary";
      connect.textContent = "Connect";
      connect.addEventListener("click", function (ev) {
        ev.stopPropagation();
        selected = index;
        connectId(row.id);
      });
      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "secondary";
      remove.textContent = "Remove";
      remove.addEventListener("click", function (ev) {
        ev.stopPropagation();
        removeId(row.id);
      });
      actions.append(chip, connect, remove);
      btn.append(text, actions);
      btn.addEventListener("click", function () {
        selected = index;
        render(data);
      });
      li.append(btn);
      listEl.append(li);
    });
  }

  async function refresh() {
    try {
      render(await api("/_desktop/api/gateways"));
    } catch (err) {
      showError(err.message, "bad");
    }
  }

  async function connectId(id) {
    showError("");
    try {
      var body = await api("/_desktop/api/active", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ id: id })
      });
      if (body.cockpit) location.href = body.cockpit;
    } catch (err) {
      var detail = err.body || {};
      if ((detail.error === "bad_cert" || detail.error === "pin_mismatch") && detail.fingerprint) {
        pendingTrust = { id: id, fingerprint: detail.fingerprint };
        fpEl.textContent = detail.fingerprint.replace(/(.{2})/g, "$1 ").trim();
        trustDlg.showModal();
      }
      showError(detail.label ? detail.label + ". " + (detail.detail || "") : err.message, "bad");
      refresh();
    }
  }

  async function removeId(id) {
    try {
      await api("/_desktop/api/gateways/" + encodeURIComponent(id), { method: "DELETE" });
      showError("");
      refresh();
    } catch (err) {
      showError(err.message, "bad");
    }
  }

  form.addEventListener("submit", async function (ev) {
    ev.preventDefault();
    var payload = {
      name: document.getElementById("name").value,
      link: document.getElementById("link").value,
      token: document.getElementById("token").value,
      provider: document.getElementById("provider").value,
      cleartext: document.getElementById("cleartext").checked
    };
    document.getElementById("token").value = "";
    try {
      await api("/_desktop/api/gateways", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(payload)
      });
      document.getElementById("link").value = "";
      showError("");
      refresh();
    } catch (err) {
      var detail = (err.body && (err.body.detail || err.body.label)) || err.message;
      showError(detail, "bad");
    }
  });

  document.getElementById("local").addEventListener("click", async function () {
    try {
      await api("/_desktop/api/gateways", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          name: document.getElementById("name").value || "This computer",
          link: "sidecar",
          provider: document.getElementById("provider").value
        })
      });
      showError("");
      refresh();
    } catch (err) {
      showError(err.message, "bad");
    }
  });

  document.getElementById("trust-yes").addEventListener("click", async function () {
    if (!pendingTrust) return;
    var trust = pendingTrust;
    pendingTrust = null;
    try {
      var body = await api("/_desktop/api/gateways/" + encodeURIComponent(trust.id) + "/trust", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ fingerprint: trust.fingerprint })
      });
      if (body.cockpit) location.href = body.cockpit;
    } catch (err) {
      showError((err.body && err.body.detail) || err.message, "bad");
      refresh();
    }
  });

  document.addEventListener("keydown", function (ev) {
    var typing = document.activeElement && (document.activeElement.tagName === "INPUT" || document.activeElement.tagName === "TEXTAREA");
    if (ev.key === "?" && !typing) {
      ev.preventDefault();
      helpDlg.showModal();
      return;
    }
    if (ev.key === "t" && !typing && !ev.metaKey && !ev.ctrlKey) {
      ev.preventDefault();
      setScheme(scheme() === "light" ? "dark" : "light");
      return;
    }
    if (ev.key === "n" && !typing) {
      ev.preventDefault();
      document.getElementById("name").focus();
      return;
    }
    if ((ev.key === "j" || ev.key === "k") && !typing) {
      ev.preventDefault();
      if (!rows.length) return;
      selected = ev.key === "j" ? Math.min(rows.length - 1, selected + 1) : Math.max(0, selected - 1);
      refresh();
      return;
    }
    if (ev.key === "Enter" && !typing && rows[selected]) {
      ev.preventDefault();
      connectId(rows[selected].id);
    }
  });

  refresh();
})();
