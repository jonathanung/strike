// App shell only. API traffic (/v1/, /health) is never cached.
const CACHE = "strike-shell-v2";
const SHELL = ["/", "/manifest.webmanifest", "/assets/icon.svg", "/assets/register.js"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) =>
      Promise.all(SHELL.map((url) => cache.add(url).catch(() => {})))
    ).then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/v1/") || url.pathname === "/health") return;
  const cacheKey = new Request(url.pathname, { method: "GET" });
  event.respondWith((async () => {
    try {
      const res = await fetch(event.request);
      if (res && res.ok) {
        const copy = res.clone();
        caches.open(CACHE).then((cache) => cache.put(cacheKey, copy)).catch(() => {});
      }
      return res;
    } catch (err) {
      const cached = await caches.match(cacheKey);
      if (cached) return cached;
      if (event.request.mode === "navigate") {
        const shell = await caches.match("/");
        if (shell) return shell;
      }
      throw err;
    }
  })());
});

function clip(value, max) {
  if (typeof value !== "string") return "";
  const one = value.replace(/\s+/g, " ").trim();
  if (one.startsWith("{") || one.startsWith("[")) return "";
  return Array.from(one).slice(0, max).join("");
}

// OS banners are generic text derived ONLY from the notification kind.
// Session/child names and handoff summaries never appear in a banner,
// even scrubbed or clamped; they stay in the notification centre only.
// Any title/body on the message is ignored here as a second line of
// defence behind the page-side banner_text(kind) mapping.
const BANNER_BODIES = {
  "approval.needed": "An agent needs your input",
  "child.finished": "A delegated run finished",
  "child.blocked": "A delegated run is blocked",
  "session.finished": "A run finished",
  "gateway.disconnected": "The gateway disconnected",
};

function asNote(data) {
  if (!data || data.type !== "notify") return null;
  const title = "Strike";
  const body = BANNER_BODIES[data.kind] || "Something needs your attention";
  const href = typeof data.href === "string" && data.href.startsWith("/#") ? data.href : "/";
  const tag = clip(data.tag, 64).replace(/[^a-zA-Z0-9._:-]/g, "") || "strike";
  return { title, body, href, tag };
}

self.addEventListener("message", (event) => {
  const note = asNote(event.data);
  if (!note) return;
  event.waitUntil(
    self.registration.showNotification(note.title, {
      body: note.body,
      tag: note.tag,
      data: { href: note.href },
    }).catch(() => {})
  );
});

self.addEventListener("notificationclick", (event) => {
  const href = event.notification && event.notification.data && typeof event.notification.data.href === "string"
    ? event.notification.data.href
    : "/";
  event.notification.close();
  const target = new URL(href, self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
      for (const client of list) {
        if (!client.url.startsWith(self.location.origin)) continue;
        client.postMessage({ type: "notify-open", href });
        if (typeof client.focus === "function") return client.focus();
        return undefined;
      }
      if (typeof self.clients.openWindow === "function") return self.clients.openWindow(target);
      return undefined;
    })
  );


});
