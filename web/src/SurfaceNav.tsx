/**
 * Compact inspector navigator: single-line scroll tabs on desktop/tablet,
 * grouped native select on phone (WEBUI.12 / #1247). Never wrap into a 40vh pile.
 */
import type { SurfaceDef } from "./surfaces";
import { groupInspectorSurfaces } from "./surfaces";
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
 * Phone: one-line grouped select (Session / Code / Team / Project / Ops).
 * Desktop/tablet: labelled tablist that scrolls horizontally instead of wrapping.
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
    const groups = groupInspectorSurfaces(surfaces);
    return (
      <nav className="surface-nav" aria-label={`${modeLabel} surfaces`}>
        <label className="surface-nav-field">
          <span className="surface-nav-caption">Surface</span>
          <select
            aria-label={`${modeLabel} surfaces`}
            value={activeId}
            onChange={(event) => onChange(event.target.value)}
          >
            {groups.map((group) => (
              <optgroup key={group.id} label={group.label}>
                {group.surfaces.map((s) => (
                  <option key={s.id} value={s.id}>{s.label}</option>
                ))}
              </optgroup>
            ))}
          </select>
        </label>
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
