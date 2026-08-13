/**
 * Mode secondary navigation: wrapping tab list on desktop/tablet, accessible
 * list/sheet on phone (WEBUI.12). Desktop must not clip into a horizontal scroller.
 */
import type { SurfaceDef } from "./surfaces";
import type { ShellProfile } from "./shellProfile";
import { Tabs } from "./ui";

export type SurfaceNavProps = {
  modeLabel: string;
  surfaces: SurfaceDef[];
  activeId: string;
  profile: ShellProfile;
  onChange: (id: string) => void;
  panelIdPrefix?: string;
};

/**
 * Phone: vertical list (sheet-friendly, not a compressed tab strip).
 * Desktop/tablet: Tabs that wrap to a readable list instead of overflow-x clip.
 */
export function SurfaceNav({
  modeLabel,
  surfaces,
  activeId,
  profile,
  onChange,
  panelIdPrefix = "inspector-panel",
}: SurfaceNavProps) {
  if (!surfaces.length) return null;

  if (profile === "phone") {
    return (
      <nav className="surface-nav-sheet" aria-label={`${modeLabel} surfaces`}>
        <ul className="surface-nav-list" role="listbox" aria-label={`${modeLabel} surfaces`}>
          {surfaces.map((s) => {
            const selected = s.id === activeId;
            return (
              <li key={s.id} role="option" aria-selected={selected}>
                <button
                  type="button"
                  className={selected ? "surface-nav-item active" : "surface-nav-item"}
                  aria-current={selected ? "page" : undefined}
                  onClick={() => onChange(s.id)}
                >
                  <span className="surface-nav-label">{s.label}</span>
                </button>
              </li>
            );
          })}
        </ul>
      </nav>
    );
  }

  return (
    <Tabs
      className="inspector-tabs"
      label={`${modeLabel} surfaces`}
      value={activeId}
      items={surfaces.map((s) => ({ id: s.id, label: s.label }))}
      onChange={onChange}
      panelIdPrefix={panelIdPrefix}
    />
  );
}
