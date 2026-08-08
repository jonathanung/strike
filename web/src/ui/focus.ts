/** Focus helpers for modal dialogs/sheets (WEBUI.2). */

const FOCUSABLE =
  'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

export function listFocusable(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => !el.hasAttribute("disabled") && el.tabIndex !== -1 && el.offsetParent !== null,
  );
}

/** Trap Tab within root. Returns true when the event was handled. */
export function trapTabKey(root: HTMLElement, event: KeyboardEvent): boolean {
  if (event.key !== "Tab") return false;
  const nodes = listFocusable(root);
  if (!nodes.length) {
    event.preventDefault();
    root.focus();
    return true;
  }
  const first = nodes[0];
  const last = nodes[nodes.length - 1];
  const active = document.activeElement as HTMLElement | null;
  if (event.shiftKey) {
    if (!active || active === first || !root.contains(active)) {
      event.preventDefault();
      last.focus();
      return true;
    }
  } else if (!active || active === last || !root.contains(active)) {
    event.preventDefault();
    first.focus();
    return true;
  }
  return false;
}

export function focusInitial(root: HTMLElement, prefer?: string): void {
  if (prefer) {
    const target = root.querySelector<HTMLElement>(prefer);
    if (target) {
      target.focus();
      return;
    }
  }
  const autofocus = root.querySelector<HTMLElement>("[autofocus]");
  if (autofocus) {
    autofocus.focus();
    return;
  }
  const nodes = listFocusable(root);
  (nodes[0] || root).focus();
}
