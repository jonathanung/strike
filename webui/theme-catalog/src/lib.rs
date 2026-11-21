//! Theme catalog logic for the cockpit picker (#1529).
//!
//! The daemon serves the theme universe at `GET /v1/themes`: bundled
//! themes plus user, project and plugin themes, each with portable colour
//! roles. This crate turns that list into picker rows and decides how a
//! saved theme id is painted. It has no browser or JSON dependency so the
//! rules run under plain `cargo test` in CI.
//!
//! Two paint paths:
//!
//! - **Bundled CSS.** The seven bundled ids have hand-tuned palettes in
//!   `webui/style.css` keyed by `:root[data-theme=…]`. A catalog entry with
//!   one of those ids and `builtin` provenance paints through that CSS.
//! - **Inline variables.** Any other catalog entry (a user theme, a project
//!   theme, a plugin theme, or a user file that overrides a bundled id)
//!   paints by setting the CSS variables from its colour roles. Roles that
//!   are missing or not a hex colour keep the Strike value, and the picker
//!   says which ones.
//!
//! The bundled list is a fallback only: it is what the picker shows while
//! the catalog is unavailable, labelled as such.

/// One bundled theme painted in `webui/style.css`.
pub struct Bundled {
    pub id: &'static str,
    pub label: &'static str,
    pub detail: &'static str,
    /// Accent shown on the picker swatch.
    pub swatch: &'static str,
}

const BUNDLED_DETAIL: &str = "Bundled theme. Surfaces it omits stay on the Strike ladder.";

pub const BUNDLED: &[Bundled] = &[
    Bundled {
        id: "strike",
        label: "Strike",
        detail: "Original palette. Royal purple, cyan while work is in flight.",
        swatch: "#7c3aed",
    },
    Bundled {
        id: "nord",
        label: "Nord",
        detail: BUNDLED_DETAIL,
        swatch: "#88c0d0",
    },
    Bundled {
        id: "dracula",
        label: "Dracula",
        detail: BUNDLED_DETAIL,
        swatch: "#bd93f9",
    },
    Bundled {
        id: "gruvbox",
        label: "Gruvbox",
        detail: BUNDLED_DETAIL,
        swatch: "#fe8019",
    },
    Bundled {
        id: "monokai",
        label: "Monokai",
        detail: BUNDLED_DETAIL,
        swatch: "#f92672",
    },
    Bundled {
        id: "tokyo-night",
        label: "Tokyo Night",
        detail: BUNDLED_DETAIL,
        swatch: "#7aa2f7",
    },
    Bundled {
        id: "catppuccin",
        label: "Catppuccin",
        detail: BUNDLED_DETAIL,
        swatch: "#cba6f7",
    },
];

pub const DEFAULT_ID: &str = "strike";

/// True for the seven ids `webui/style.css` paints.
pub fn is_bundled(id: &str) -> bool {
    BUNDLED.iter().any(|b| b.id == id)
}

/// Catalog colour role → cockpit CSS variable. Role names are the
/// `host.ThemeColors` JSON keys.
pub const ROLES: &[(&str, &str)] = &[
    ("text", "--ink"),
    ("textMuted", "--muted"),
    ("accent", "--acid"),
    ("accentAlt", "--accent-alt"),
    ("highlight", "--highlight"),
    ("success", "--success"),
    ("warning", "--warning"),
    ("error", "--signal"),
    ("danger", "--danger"),
    ("background", "--ground"),
    ("surface", "--surface"),
    ("surfaceFocus", "--raised"),
    ("surfaceMuted", "--surface-muted"),
    ("border", "--rule"),
    ("borderFocus", "--border-focus"),
    ("borderMuted", "--border-muted"),
    ("userLabel", "--user"),
    ("toolLabel", "--tool"),
    ("diffAdded", "--diff-add"),
    ("diffRemoved", "--diff-del"),
    ("overlayScrim", "--overlay"),
];

/// Variables an inline theme may set. The browser clears exactly these
/// when it leaves an inline theme, so nothing from it lingers.
pub fn inline_vars() -> impl Iterator<Item = &'static str> {
    ROLES
        .iter()
        .map(|(_, var)| *var)
        .chain(["--on-accent", "color-scheme"])
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Color {
    pub role: String,
    pub light: String,
    pub dark: String,
}

/// One `/v1/themes` entry.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Entry {
    pub id: String,
    pub name: String,
    pub provenance: String,
    pub overrode: String,
    pub colors: Vec<Color>,
}

impl Entry {
    fn color(&self, role: &str, scheme: Scheme) -> Option<&str> {
        let c = self.colors.iter().find(|c| c.role == role)?;
        let v = match scheme {
            Scheme::Light => c.light.trim(),
            Scheme::Dark => c.dark.trim(),
        };
        if is_hex_color(v) {
            Some(v)
        } else {
            None
        }
    }

    /// Paints through `style.css` rather than inline variables.
    pub fn uses_bundled_css(&self) -> bool {
        is_bundled(&self.id) && (self.provenance.is_empty() || self.provenance == "builtin")
    }

    /// Roles this entry does not supply as hex for `scheme`.
    pub fn missing_roles(&self, scheme: Scheme) -> Vec<&'static str> {
        ROLES
            .iter()
            .filter(|(role, _)| self.color(role, scheme).is_none())
            .map(|(role, _)| *role)
            .collect()
    }

    /// The cockpit can paint this entry in `scheme` without a half-applied
    /// state: background and text are the floor.
    pub fn renders_in(&self, scheme: Scheme) -> bool {
        self.uses_bundled_css()
            || (self.color("background", scheme).is_some() && self.color("text", scheme).is_some())
    }

    /// Paintable in at least one scheme.
    pub fn renderable(&self) -> bool {
        self.renders_in(Scheme::Dark) || self.renders_in(Scheme::Light)
    }

    /// Inline variables for `scheme`, including `--on-accent` and
    /// `color-scheme`. Empty when the entry paints through bundled CSS or
    /// cannot render in this scheme.
    pub fn css_vars(&self, scheme: Scheme) -> Vec<(&'static str, String)> {
        if self.uses_bundled_css() || !self.renders_in(scheme) {
            return Vec::new();
        }
        let mut out: Vec<(&'static str, String)> = ROLES
            .iter()
            .filter_map(|(role, var)| self.color(role, scheme).map(|v| (*var, v.to_string())))
            .collect();
        if let Some(accent) = self.color("accent", scheme) {
            let bg = self.color("background", scheme).unwrap_or("#000000");
            let fg = self.color("text", scheme).unwrap_or("#ffffff");
            out.push(("--on-accent", on_accent(accent, bg, fg).to_string()));
        }
        out.push((
            "color-scheme",
            match scheme {
                Scheme::Light => "light".into(),
                Scheme::Dark => "dark".into(),
            },
        ));
        out
    }

    /// Browser chrome colour (`<meta name="theme-color">`).
    pub fn chrome_color(&self, scheme: Scheme) -> Option<&str> {
        self.color("background", scheme)
    }

    fn swatch(&self) -> Option<String> {
        self.color("accent", Scheme::Dark)
            .or_else(|| self.color("accent", Scheme::Light))
            .map(str::to_string)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Scheme {
    Dark,
    Light,
}

impl Scheme {
    pub fn parse(s: &str) -> Scheme {
        if s == "light" {
            Scheme::Light
        } else {
            Scheme::Dark
        }
    }
}

/// `#rgb`, `#rrggbb` or `#rrggbbaa`. Anything else (named colours, ANSI
/// numbers, CSS with `;`/`}`) is refused so a theme file can never inject
/// declarations into the page.
pub fn is_hex_color(v: &str) -> bool {
    let Some(hex) = v.strip_prefix('#') else {
        return false;
    };
    matches!(hex.len(), 3 | 6 | 8) && hex.bytes().all(|b| b.is_ascii_hexdigit())
}

fn channel(hex: &str, i: usize) -> f64 {
    let v = if hex.len() == 3 {
        let d = u8::from_str_radix(&hex[i..i + 1], 16).unwrap_or(0);
        d * 17
    } else {
        u8::from_str_radix(&hex[i * 2..i * 2 + 2], 16).unwrap_or(0)
    };
    let c = f64::from(v) / 255.0;
    if c <= 0.03928 {
        c / 12.92
    } else {
        ((c + 0.055) / 1.055).powf(2.4)
    }
}

/// WCAG relative luminance of a validated hex colour.
fn luminance(color: &str) -> f64 {
    let hex = color.trim_start_matches('#');
    0.2126 * channel(hex, 0) + 0.7152 * channel(hex, 1) + 0.0722 * channel(hex, 2)
}

fn contrast(a: &str, b: &str) -> f64 {
    let (la, lb) = (luminance(a), luminance(b));
    let (hi, lo) = if la > lb { (la, lb) } else { (lb, la) };
    (hi + 0.05) / (lo + 0.05)
}

/// Text colour for a filled accent control: whichever of the theme's
/// background and text reads better on the accent.
pub fn on_accent<'a>(accent: &str, background: &'a str, text: &'a str) -> &'a str {
    if contrast(accent, background) >= contrast(accent, text) {
        background
    } else {
        text
    }
}

/// What the catalog fetch has produced so far.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum CatalogState {
    Loading,
    Loaded(Vec<Entry>),
    Failed(String),
}

impl CatalogState {
    pub fn entries(&self) -> Option<&[Entry]> {
        match self {
            CatalogState::Loaded(list) => Some(list),
            _ => None,
        }
    }

    pub fn find(&self, id: &str) -> Option<&Entry> {
        self.entries()?.iter().find(|e| e.id == id)
    }
}

/// How a saved theme id is painted right now.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Paint {
    /// `data-theme=<id>` through bundled CSS; no inline variables.
    Bundled(String),
    /// Inline variables from a catalog entry.
    Inline(Entry),
    /// Not paintable: Strike is shown and `note` says why. `keep` is true
    /// when the saved id should be kept because the catalog has not
    /// answered yet (it may know the id once it loads).
    Fallback { requested: String, note: String, keep: bool },
}

impl Paint {
    /// The theme id the picker should mark as current.
    pub fn current_id(&self) -> &str {
        match self {
            Paint::Bundled(id) => id,
            Paint::Inline(e) => &e.id,
            Paint::Fallback { .. } => DEFAULT_ID,
        }
    }

    pub fn note(&self) -> &str {
        match self {
            Paint::Fallback { note, .. } => note,
            _ => "",
        }
    }
}

/// Decide how `id` is painted. `cached` is the last inline entry this
/// browser painted (so a reload paints a user theme before the catalog
/// answers). Coercion to Strike only happens for ids the catalog does not
/// know, and always carries a note.
pub fn resolve(id: &str, scheme: Scheme, catalog: &CatalogState, cached: Option<&Entry>) -> Paint {
    let id = id.trim();
    if id.is_empty() {
        return Paint::Bundled(DEFAULT_ID.into());
    }
    let paint_entry = |e: &Entry| -> Paint {
        if e.uses_bundled_css() {
            Paint::Bundled(e.id.clone())
        } else if e.renders_in(scheme) {
            Paint::Inline(e.clone())
        } else {
            Paint::Fallback {
                requested: e.id.clone(),
                note: format!(
                    "Theme \"{}\" has no usable {} background and text colours, so Strike is shown.",
                    display_name(e),
                    scheme_word(scheme)
                ),
                keep: true,
            }
        }
    };
    match catalog {
        CatalogState::Loaded(list) => match list.iter().find(|e| e.id == id) {
            Some(e) => paint_entry(e),
            None if is_bundled(id) => Paint::Bundled(id.into()),
            None => Paint::Fallback {
                requested: id.into(),
                note: format!(
                    "Theme \"{id}\" is not in this daemon's theme catalog, so Strike is shown."
                ),
                keep: false,
            },
        },
        CatalogState::Loading | CatalogState::Failed(_) => {
            if let Some(e) = cached.filter(|e| e.id == id) {
                return paint_entry(e);
            }
            if is_bundled(id) {
                return Paint::Bundled(id.into());
            }
            let why = match catalog {
                CatalogState::Failed(err) => format!("the theme catalog could not be loaded ({err})"),
                _ => "the theme catalog is still loading".into(),
            };
            Paint::Fallback {
                requested: id.into(),
                note: format!("Theme \"{id}\" is shown as Strike because {why}."),
                keep: true,
            }
        }
    }
}

fn scheme_word(s: Scheme) -> &'static str {
    match s {
        Scheme::Dark => "dark",
        Scheme::Light => "light",
    }
}

fn display_name(e: &Entry) -> &str {
    if e.name.trim().is_empty() {
        &e.id
    } else {
        e.name.trim()
    }
}

/// One picker row.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Row {
    pub id: String,
    pub label: String,
    pub detail: String,
    pub badge: &'static str,
    /// Set when the row is listed but cannot be picked, with the reason.
    pub disabled: Option<String>,
    /// Inline swatch colour for non-bundled themes; bundled swatches come
    /// from `style.css` by id.
    pub swatch: Option<String>,
}

/// Picker rows plus the honesty note shown above them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Picker {
    pub rows: Vec<Row>,
    /// Non-empty when the rows are the bundled fallback.
    pub note: String,
}

fn source_label(provenance: &str) -> String {
    match provenance {
        "" | "builtin" => "Bundled theme".into(),
        "user" => "User theme (~/.strike/themes)".into(),
        "project" => "Project theme (.strike/themes)".into(),
        p => match p.strip_prefix("plugin:") {
            Some(id) if !id.is_empty() => format!("Plugin theme from {id}"),
            _ => "Plugin theme".into(),
        },
    }
}

fn bundled_row(b: &Bundled) -> Row {
    Row {
        id: b.id.into(),
        label: b.label.into(),
        detail: b.detail.into(),
        badge: if b.id == DEFAULT_ID { "Default" } else { "" },
        disabled: None,
        swatch: None,
    }
}

fn catalog_row(e: &Entry) -> Row {
    if e.uses_bundled_css() {
        if let Some(b) = BUNDLED.iter().find(|b| b.id == e.id) {
            let mut row = bundled_row(b);
            if !e.name.trim().is_empty() {
                row.label = e.name.trim().into();
            }
            return row;
        }
    }
    let mut detail = source_label(&e.provenance);
    if !e.overrode.is_empty() {
        detail.push_str(&format!(", overrides the {} theme with this id", e.overrode));
    }
    detail.push('.');
    let disabled = if e.renderable() {
        let mut missing: Vec<&str> = e.missing_roles(Scheme::Dark);
        for role in e.missing_roles(Scheme::Light) {
            if !missing.contains(&role) {
                missing.push(role);
            }
        }
        if !missing.is_empty() {
            detail.push_str(&format!(
                " Roles it does not set as hex colours stay on Strike: {}.",
                missing.join(", ")
            ));
        }
        let light = e.renders_in(Scheme::Light);
        let dark = e.renders_in(Scheme::Dark);
        if light != dark {
            detail.push_str(if dark {
                " Dark scheme only: light shows Strike."
            } else {
                " Light scheme only: dark shows Strike."
            });
        }
        None
    } else {
        Some("No hex background and text colours the browser can paint.".to_string())
    };
    Row {
        id: e.id.clone(),
        label: display_name(e).into(),
        detail,
        badge: if e.id == DEFAULT_ID { "Default" } else { "" },
        disabled,
        swatch: e.swatch(),
    }
}

/// Picker rows: the catalog in daemon order when loaded, otherwise the
/// bundled fallback with a note saying so.
pub fn picker(catalog: &CatalogState) -> Picker {
    match catalog {
        CatalogState::Loaded(list) if !list.is_empty() => Picker {
            rows: list.iter().map(catalog_row).collect(),
            note: String::new(),
        },
        CatalogState::Loaded(_) => Picker {
            rows: BUNDLED.iter().map(bundled_row).collect(),
            note: "The daemon theme catalog is empty. Showing the bundled themes only.".into(),
        },
        CatalogState::Failed(err) => Picker {
            rows: BUNDLED.iter().map(bundled_row).collect(),
            note: format!(
                "Could not load the daemon theme catalog ({err}). Showing the bundled themes only; user themes are not listed."
            ),
        },
        CatalogState::Loading => Picker {
            rows: BUNDLED.iter().map(bundled_row).collect(),
            note: "Loading the daemon theme catalog. Showing the bundled themes until it answers."
                .into(),
        },
    }
}

/// True when the picker may offer `id` (used for "use daemon theme").
pub fn selectable(id: &str, catalog: &CatalogState) -> bool {
    match catalog.find(id) {
        Some(e) => e.renderable(),
        None => is_bundled(id),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn color(role: &str, light: &str, dark: &str) -> Color {
        Color {
            role: role.into(),
            light: light.into(),
            dark: dark.into(),
        }
    }

    fn bundled(id: &str) -> Entry {
        Entry {
            id: id.into(),
            name: id.into(),
            provenance: "builtin".into(),
            ..Entry::default()
        }
    }

    fn sunset() -> Entry {
        Entry {
            id: "sunset".into(),
            name: "Sunset".into(),
            provenance: "user".into(),
            overrode: String::new(),
            colors: vec![
                color("background", "#fff8f0", "#1b1020"),
                color("text", "#2a1a10", "#ffe8d0"),
                color("accent", "#c2410c", "#fb923c"),
            ],
        }
    }

    fn loaded() -> CatalogState {
        CatalogState::Loaded(vec![bundled("strike"), bundled("nord"), sunset()])
    }

    #[test]
    fn hex_validation() {
        let cases = [
            ("#abc", true),
            ("#AABBCC", true),
            ("#aabbccdd", true),
            ("#abcd", false),
            ("abc", false),
            ("red", false),
            ("205", false),
            ("#fff;}body{display:none", false),
            ("", false),
            ("#gggggg", false),
        ];
        for (v, want) in cases {
            assert_eq!(is_hex_color(v), want, "{v}");
        }
    }

    #[test]
    fn picker_lists_catalog_with_user_theme() {
        let p = picker(&loaded());
        assert!(p.note.is_empty());
        let ids: Vec<&str> = p.rows.iter().map(|r| r.id.as_str()).collect();
        assert_eq!(ids, ["strike", "nord", "sunset"]);
        let user = &p.rows[2];
        assert_eq!(user.label, "Sunset");
        assert!(user.detail.starts_with("User theme"), "{}", user.detail);
        assert!(user.detail.contains("stay on Strike"), "{}", user.detail);
        assert_eq!(user.swatch.as_deref(), Some("#fb923c"));
        assert!(user.disabled.is_none());
        assert_eq!(p.rows[0].badge, "Default");
        assert!(p.rows[1].swatch.is_none(), "bundled swatch comes from CSS");
    }

    #[test]
    fn picker_falls_back_to_bundled_with_note() {
        let cases = [
            (CatalogState::Failed("HTTP 501".into()), "HTTP 501"),
            (CatalogState::Loading, "Loading"),
            (CatalogState::Loaded(vec![]), "empty"),
        ];
        for (state, want) in cases {
            let p = picker(&state);
            assert_eq!(p.rows.len(), BUNDLED.len());
            assert!(p.note.contains(want), "{:?}: {}", state, p.note);
            assert!(p.note.contains("bundled"), "{}", p.note);
        }
    }

    #[test]
    fn unrenderable_theme_is_listed_disabled() {
        let bad = Entry {
            id: "ansi".into(),
            name: "ANSI".into(),
            provenance: "project".into(),
            colors: vec![color("background", "0", "0"), color("text", "15", "15")],
            ..Entry::default()
        };
        let p = picker(&CatalogState::Loaded(vec![bundled("strike"), bad.clone()]));
        let row = &p.rows[1];
        assert!(row.disabled.is_some());
        assert!(row.detail.starts_with("Project theme"));
        assert!(!selectable("ansi", &CatalogState::Loaded(vec![bad])));
    }

    #[test]
    fn plugin_and_override_details() {
        let mut e = sunset();
        e.id = "dracula".into();
        e.provenance = "plugin:retro".into();
        e.overrode = "builtin".into();
        let row = catalog_row(&e);
        assert!(row.detail.contains("Plugin theme from retro"), "{}", row.detail);
        assert!(row.detail.contains("overrides the builtin theme"), "{}", row.detail);
        assert!(!e.uses_bundled_css(), "an override paints inline");
    }

    #[test]
    fn resolve_cases() {
        let sun = sunset();
        let cases: Vec<(&str, &str, CatalogState, Option<Entry>, &str, bool)> = vec![
            // (name, id, catalog, cached, want current id, want note)
            ("empty id is strike", "", loaded(), None, "strike", false),
            ("bundled through css", "nord", loaded(), None, "nord", false),
            ("user theme from catalog", "sunset", loaded(), None, "sunset", false),
            // Reload: catalog not answered yet, cache paints the user theme.
            ("user theme survives reload via cache", "sunset", CatalogState::Loading, Some(sun.clone()), "sunset", false),
            ("cache survives a failed catalog", "sunset", CatalogState::Failed("HTTP 501".into()), Some(sun.clone()), "sunset", false),
            ("cache for another id is ignored", "other", CatalogState::Loading, Some(sun.clone()), "strike", true),
            ("unknown to catalog falls back", "gone", loaded(), None, "strike", true),
            ("bundled id missing from catalog still paints", "gruvbox", loaded(), None, "gruvbox", false),
            ("bundled while loading", "dracula", CatalogState::Loading, None, "dracula", false),
        ];
        for (name, id, catalog, cached, want, note) in cases {
            let got = resolve(id, Scheme::Dark, &catalog, cached.as_ref());
            assert_eq!(got.current_id(), want, "{name}: {got:?}");
            assert_eq!(!got.note().is_empty(), note, "{name}: {got:?}");
        }
    }

    #[test]
    fn user_override_of_strike_paints_inline() {
        let mut mine = sunset();
        mine.id = "strike".into();
        mine.overrode = "builtin".into();
        let catalog = CatalogState::Loaded(vec![mine.clone()]);
        assert!(matches!(resolve("strike", Scheme::Dark, &catalog, None), Paint::Inline(_)));
        assert_eq!(
            resolve("strike", Scheme::Dark, &CatalogState::Loading, None),
            Paint::Bundled("strike".into())
        );
        assert!(matches!(
            resolve("strike", Scheme::Dark, &CatalogState::Loading, Some(&mine)),
            Paint::Inline(_)
        ));
        assert_eq!(
            resolve("strike", Scheme::Dark, &CatalogState::Loaded(vec![bundled("strike")]), None),
            Paint::Bundled("strike".into())
        );
    }

    #[test]
    fn resolve_keep_flag() {
        // Unknown while loading keeps the saved id; unknown once loaded drops it.
        match resolve("x", Scheme::Dark, &CatalogState::Loading, None) {
            Paint::Fallback { keep, .. } => assert!(keep),
            p => panic!("{p:?}"),
        }
        match resolve("x", Scheme::Dark, &loaded(), None) {
            Paint::Fallback { keep, note, .. } => {
                assert!(!keep);
                assert!(note.contains("not in this daemon's theme catalog"), "{note}");
            }
            p => panic!("{p:?}"),
        }
        let failed = resolve("x", Scheme::Dark, &CatalogState::Failed("HTTP 500".into()), None);
        assert!(failed.note().contains("HTTP 500"), "{failed:?}");
    }

    #[test]
    fn scheme_only_theme() {
        let dark_only = Entry {
            id: "night".into(),
            provenance: "user".into(),
            colors: vec![color("background", "", "#000000"), color("text", "", "#ffffff")],
            ..Entry::default()
        };
        assert!(dark_only.renderable());
        assert!(matches!(
            resolve("night", Scheme::Light, &CatalogState::Loaded(vec![dark_only.clone()]), None),
            Paint::Fallback { keep: true, .. }
        ));
        assert!(matches!(
            resolve("night", Scheme::Dark, &CatalogState::Loaded(vec![dark_only.clone()]), None),
            Paint::Inline(_)
        ));
        assert!(catalog_row(&dark_only).detail.contains("Dark scheme only"));
    }

    #[test]
    fn css_vars_map_roles() {
        let vars = sunset().css_vars(Scheme::Dark);
        let get = |k: &str| vars.iter().find(|(v, _)| *v == k).map(|(_, x)| x.as_str());
        assert_eq!(get("--ground"), Some("#1b1020"));
        assert_eq!(get("--ink"), Some("#ffe8d0"));
        assert_eq!(get("--acid"), Some("#fb923c"));
        assert_eq!(get("color-scheme"), Some("dark"));
        assert!(get("--on-accent").is_some());
        assert!(get("--surface").is_none(), "missing role keeps Strike");
        let light = sunset().css_vars(Scheme::Light);
        assert!(light.contains(&("--ground", "#fff8f0".to_string())));
        assert!(bundled("nord").css_vars(Scheme::Dark).is_empty());
        for (var, _) in &vars {
            assert!(inline_vars().any(|v| v == *var), "{var} not cleared on exit");
        }
    }

    #[test]
    fn on_accent_picks_readable_side() {
        assert_eq!(on_accent("#fde047", "#111111", "#fafafa"), "#111111");
        assert_eq!(on_accent("#1e3a8a", "#111111", "#fafafa"), "#fafafa");
        assert_eq!(on_accent("#fff", "#000", "#eee"), "#000");
    }

    #[test]
    fn roles_map_to_distinct_vars() {
        let mut vars: Vec<&str> = ROLES.iter().map(|(_, v)| *v).collect();
        vars.sort_unstable();
        vars.dedup();
        assert_eq!(vars.len(), ROLES.len());
    }
}
