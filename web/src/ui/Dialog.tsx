import {
  type DialogHTMLAttributes,
  type ReactNode,
  useEffect,
  useId,
  useRef,
} from "react";
import { focusInitial, trapTabKey } from "./focus";

export type DialogMode = "modal" | "blocking";

export type DialogProps = {
  title: string;
  children: ReactNode;
  onClose?: () => void;
  /** modal: Escape/backdrop dismiss; blocking: cancel prevented (asks). */
  mode?: DialogMode;
  className?: string;
  wide?: boolean;
  /** Optional initial focus selector inside the dialog. */
  initialFocus?: string;
  actions?: ReactNode;
} & Omit<DialogHTMLAttributes<HTMLDialogElement>, "title" | "children" | "className">;

/**
 * Shared native <dialog> primitive with focus trap + restore.
 * Blocking mode is for permission/question asks that must not dismiss on Escape.
 */
export function Dialog({
  title,
  children,
  onClose,
  mode = "modal",
  className = "",
  wide = false,
  initialFocus,
  actions,
  ...rest
}: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const invoker = useRef<Element | null>(null);
  const titleId = useId();
  const blocking = mode === "blocking";

  useEffect(() => {
    invoker.current = document.activeElement;
    const node = ref.current;
    if (!node) return;
    if (typeof node.showModal === "function" && !node.open) {
      try {
        node.showModal();
      } catch {
        node.setAttribute("open", "");
      }
    } else {
      node.setAttribute("open", "");
    }
    // Defer so dialog content is mounted.
    requestAnimationFrame(() => focusInitial(node, initialFocus));
    return () => {
      const prev = invoker.current;
      if (prev instanceof HTMLElement && document.contains(prev)) {
        try {
          prev.focus();
        } catch {
          /* ignore */
        }
      }
    };
  }, [initialFocus]);

  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape" && blocking) {
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      trapTabKey(node, event);
    };
    node.addEventListener("keydown", onKey);
    return () => node.removeEventListener("keydown", onKey);
  }, [blocking]);

  const classes = [
    "ui-dialog",
    wide ? "ui-dialog-wide" : "",
    blocking ? "ui-dialog-blocking" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <dialog
      ref={ref}
      className={classes}
      aria-labelledby={titleId}
      aria-modal="true"
      data-mode={mode}
      onClose={() => onClose?.()}
      onCancel={(event) => {
        if (blocking) {
          event.preventDefault();
          return;
        }
        event.preventDefault();
        onClose?.();
      }}
      {...rest}
    >
      <div className="dialog-rule" aria-hidden />
      <h2 id={titleId}>{title}</h2>
      <div className="ui-dialog-body">{children}</div>
      {actions ? <div className="dialog-actions">{actions}</div> : null}
    </dialog>
  );
}
