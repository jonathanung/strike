import {
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
} from "react";

export type TabItem = {
  id: string;
  label: ReactNode;
  disabled?: boolean;
};

export type TabsProps = {
  items: TabItem[];
  value: string;
  onChange: (id: string) => void;
  /** Accessible name for the tablist. */
  label?: string;
  className?: string;
  /** Optional id prefix for aria-controls pairing. */
  panelIdPrefix?: string;
};

/**
 * Shared tabs: tablist/tab roles, aria-selected, aria-controls, arrow-key nav.
 */
export function Tabs({
  items,
  value,
  onChange,
  label,
  className = "",
  panelIdPrefix,
}: TabsProps) {
  const baseId = useId();
  const listRef = useRef<HTMLDivElement>(null);
  const enabled = items.filter((t) => !t.disabled);

  useEffect(() => {
    const escaped = typeof CSS !== "undefined" && typeof CSS.escape === "function"
      ? CSS.escape(value)
      : value.replace(/["\\]/g, "");
    const el = listRef.current?.querySelector<HTMLElement>(`[data-tab-id="${escaped}"]`);
    el?.scrollIntoView?.({ inline: "nearest", block: "nearest" });
  }, [value]);

  const move = (from: string, delta: number) => {
    if (!enabled.length) return;
    const idx = Math.max(0, enabled.findIndex((t) => t.id === from));
    const next = enabled[(idx + delta + enabled.length) % enabled.length];
    onChange(next.id);
    requestAnimationFrame(() => {
      const node = listRef.current?.querySelectorAll<HTMLElement>("[data-tab-id]");
      node?.forEach((el) => {
        if (el.dataset.tabId === next.id) el.focus();
      });
    });
  };

  const onKeyDown = (event: ReactKeyboardEvent<HTMLElement>, id: string) => {
    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown":
        event.preventDefault();
        move(id, 1);
        break;
      case "ArrowLeft":
      case "ArrowUp":
        event.preventDefault();
        move(id, -1);
        break;
      case "Home":
        event.preventDefault();
        if (enabled[0]) onChange(enabled[0].id);
        break;
      case "End":
        event.preventDefault();
        if (enabled.length) onChange(enabled[enabled.length - 1].id);
        break;
      default:
        break;
    }
  };

  return (
    <div
      ref={listRef}
      className={`ui-tabs ${className}`.trim()}
      role="tablist"
      aria-label={label}
    >
      {items.map((tab) => {
        const selected = tab.id === value;
        const tabId = `${baseId}-tab-${tab.id}`;
        const panelId = panelIdPrefix
          ? `${panelIdPrefix}-${tab.id}`
          : `${baseId}-panel-${tab.id}`;
        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            id={tabId}
            data-tab-id={tab.id}
            className="ui-tab"
            aria-selected={selected}
            aria-controls={panelId}
            tabIndex={selected ? 0 : -1}
            disabled={tab.disabled}
            onClick={() => onChange(tab.id)}
            onKeyDown={(event) => onKeyDown(event, tab.id)}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

export function TabPanel({
  id,
  tabId,
  active,
  children,
  className = "",
}: {
  id: string;
  tabId: string;
  active: boolean;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      role="tabpanel"
      id={id}
      aria-labelledby={tabId}
      hidden={!active}
      className={`ui-tabpanel ${className}`.trim()}
    >
      {active ? children : null}
    </div>
  );
}
