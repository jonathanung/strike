// App-shell cache. API traffic (/v1/, /health) is never cached.
// feature:notifications — page-posted banners. No third-party push service.
const CACHE = "strike-shell-v2";

self.addEventListener("install", (e) => {
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(self.clients.claim());
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== "GET" || url.pathname.startsWith("/v1/") || url.pathname === "/health") return;
  e.respondWith(
    fetch(e.request).then((res) => {
      const copy = res.clone();
      caches.open(CACHE).then((c) => c.put(e.request, copy));
      return res;
    }).catch(() => caches.match(e.request))
  );
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
