if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}

// Web Push helper for the notification settings (#1564). The Rust side calls
// these through window.strikePush; each async method returns a string.
(function () {
  function keyBytes(b64url) {
    const pad = "=".repeat((4 - (b64url.length % 4)) % 4);
    const raw = atob((b64url + pad).replace(/-/g, "+").replace(/_/g, "/"));
    const out = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
  }
  function support() {
    if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) {
      return "unsupported";
    }
    const ios = /iPad|iPhone|iPod/.test(navigator.userAgent || "");
    const standalone = (window.matchMedia && window.matchMedia("(display-mode: standalone)").matches) || navigator.standalone === true;
    if (ios && !standalone) return "ios-not-installed";
    if (Notification.permission === "denied") return "denied";
    return "ok";
  }
  async function registration() {
    return navigator.serviceWorker.ready;
  }
  window.strikePush = {
    support,
    async current() {
      if (support() !== "ok") return "";
      const reg = await registration();
      const sub = await reg.pushManager.getSubscription();
      return sub ? sub.endpoint : "";
    },
    async enable(publicKey) {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") throw new Error(perm === "denied" ? "denied" : "Permission was not granted.");
      const reg = await registration();
      let sub = await reg.pushManager.getSubscription();
      if (!sub) {
        sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(publicKey) });
      }
      return JSON.stringify(sub.toJSON());
    },
    async disable() {
      const reg = await registration();
      const sub = await reg.pushManager.getSubscription();
      if (!sub) return "";
      const endpoint = sub.endpoint;
      await sub.unsubscribe();
      return endpoint;
    },
  };
})();
