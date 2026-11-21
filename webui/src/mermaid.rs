//! Mermaid diagrams in the transcript and the document viewer (#1368).
//!
//! A ```` ```mermaid ```` fence renders as an SVG diagram drawn from the
//! native `mermaid-lite` scene (flowchart + sequenceDiagram in slice 1).
//! Every label goes into the DOM as a text node through Leptos, never as
//! markup, so agent-written labels stay inert. Controls: zoom out / in /
//! fit, and a toggle back to the original code block; the canvas scrolls
//! in both directions to pan (mouse wheel, trackpad, touch, or arrow
//! keys once focused). A block that does not parse — or a diagram type
//! outside the subset — shows the original code block plus a short
//! "Diagram didn't parse" note; never an error box, never a blank region.

use leptos::prelude::*;
use mermaid_lite::{Scene, Shape, Stroke};

/// Zoom steps offered by the controls; index [`FIT`] is "fit to width".
pub const ZOOMS: [f64; 7] = [0.5, 0.75, 1.0, 1.25, 1.5, 2.0, 3.0];
const FIT: usize = 2;
const LINE_H: f64 = 17.0;

/// True for a fence info string that names Mermaid (`mermaid`, any case,
/// optional trailing attributes).
pub fn is_mermaid(lang: &str) -> bool {
    lang.split_whitespace()
        .next()
        .is_some_and(|w| w.eq_ignore_ascii_case("mermaid"))
}

/// Next zoom index for a control press, clamped to the steps.
pub fn step_zoom(idx: usize, up: bool) -> usize {
    if up {
        (idx + 1).min(ZOOMS.len() - 1)
    } else {
        idx.saturating_sub(1)
    }
}

/// Note under a block that did not render.
pub fn failure_note(e: &mermaid_lite::ParseError) -> String {
    format!("Diagram didn't parse ({e}); showing the source.")
}

/// Toggle label: names the view the button switches to.
pub fn toggle_label(showing_source: bool) -> &'static str {
    if showing_source {
        "Show diagram"
    } else {
        "Show source"
    }
}

fn dash(stroke: Stroke) -> Option<&'static str> {
    match stroke {
        Stroke::Dashed => Some("5 4"),
        _ => None,
    }
}

fn points_attr(points: &[(f64, f64)]) -> String {
    points
        .iter()
        .map(|(x, y)| format!("{x:.1},{y:.1}"))
        .collect::<Vec<_>>()
        .join(" ")
}

fn shape_view(shape: Shape) -> AnyView {
    match shape {
        Shape::Rect { x, y, w, h, rx, role } => view! {
            <rect class=role.class() x=format!("{x:.1}") y=format!("{y:.1}") width=format!("{w:.1}") height=format!("{h:.1}") rx=format!("{rx:.1}") />
        }
        .into_any(),
        Shape::Ellipse { cx, cy, rx, ry, role } => view! {
            <ellipse class=role.class() cx=format!("{cx:.1}") cy=format!("{cy:.1}") rx=format!("{rx:.1}") ry=format!("{ry:.1}") />
        }
        .into_any(),
        Shape::Polygon { points, role } => view! {
            <polygon class=role.class() points=points_attr(&points) />
        }
        .into_any(),
        Shape::Line { points, stroke, role } => {
            let cls = if stroke == Stroke::Thick { format!("{} mmd-thick", role.class()) } else { role.class().to_string() };
            view! {
                <polyline class=cls points=points_attr(&points) stroke-dasharray=dash(stroke) />
            }
            .into_any()
        }
        Shape::Text { x, y, lines, start, role } => {
            let anchor = if start { "start" } else { "middle" };
            let cls = format!("mmd-text {}", role.class());
            view! {
                <text class=cls text-anchor=anchor dominant-baseline="central">
                    {lines
                        .into_iter()
                        .enumerate()
                        .map(|(i, line)| {
                            let ly = y + i as f64 * LINE_H;
                            view! { <tspan x=format!("{x:.1}") y=format!("{ly:.1}")>{line}</tspan> }
                        })
                        .collect_view()}
                </text>
            }
            .into_any()
        }
    }
}

fn scene_label(scene: &Scene) -> String {
    let kind = if scene.kind == "sequence" { "Sequence diagram" } else { "Flowchart" };
    let mut texts = scene.texts();
    texts.dedup();
    let mut label = format!("{kind}: {}", texts.join(", "));
    if label.chars().count() > 300 {
        label = label.chars().take(299).collect::<String>() + "…";
    }
    label
}

/// A Mermaid fence. `children` is the caller's ordinary code-block view
/// (language bar + Copy), shown for "Show source" and as the fallback.
#[component]
pub fn MermaidBlock(source: String, children: Children) -> impl IntoView {
    let code = children();
    let scene = match mermaid_lite::render(&source) {
        Ok(scene) => scene,
        Err(e) => {
            return view! {
                <div class="mmd mmd-failed" data-mermaid-failed=true>
                    {code}
                    <p class="mmd-fail-note" role="note">{failure_note(&e)}</p>
                </div>
            }
            .into_any();
        }
    };
    let source_on = RwSignal::new(false);
    let zoom = RwSignal::new(FIT);
    let (w, h) = (scene.width, scene.height);
    let kind = scene.kind;
    let label = scene_label(&scene);
    let shapes = scene.shapes.into_iter().map(shape_view).collect_view();
    // Sizing goes through the width/height attributes and a class, never
    // a `style` attribute: the server CSP is `style-src 'self'`. Fit uses
    // the natural size and `.mmd-fit` caps it at the column width (never
    // upscaled); other steps size explicitly and the canvas scrolls.
    let width = move || format!("{:.0}", w * ZOOMS[zoom.get()]);
    let height = move || format!("{:.0}", h * ZOOMS[zoom.get()]);
    let svg_class = move || if zoom.get() == FIT { "mmd-svg mmd-fit" } else { "mmd-svg" };
    view! {
        <div class="mmd" data-mermaid=kind>
            <div class="sx-fence-bar mmd-bar" role="toolbar" aria-label="Diagram controls">
                <span class="sx-lang">"mermaid"</span>
                <span class="mmd-zoom" hidden=move || source_on.get()>
                    <button type="button" class="sx-quiet" data-mermaid-zoom="out" aria-label="Zoom out"
                        disabled=move || zoom.get() == 0
                        on:click=move |_| zoom.update(|z| *z = step_zoom(*z, false))>"−"</button>
                    <button type="button" class="sx-quiet" data-mermaid-zoom="fit" aria-label="Fit diagram to width"
                        on:click=move |_| zoom.set(FIT)>
                        {move || if zoom.get() == FIT { "Fit".to_string() } else { format!("{:.0}%", ZOOMS[zoom.get()] * 100.0) }}
                    </button>
                    <button type="button" class="sx-quiet" data-mermaid-zoom="in" aria-label="Zoom in"
                        disabled=move || zoom.get() + 1 >= ZOOMS.len()
                        on:click=move |_| zoom.update(|z| *z = step_zoom(*z, true))>"+"</button>
                </span>
                <button type="button" class="sx-quiet" data-mermaid-toggle=true
                    aria-pressed=move || if source_on.get() { "true" } else { "false" }
                    on:click=move |_| source_on.update(|s| *s = !*s)>
                    {move || toggle_label(source_on.get())}
                </button>
            </div>
            <div class="mmd-canvas" tabindex="0" aria-label="Diagram canvas; scroll to pan" hidden=move || source_on.get()>
                <svg
                    class=svg_class
                    role="img"
                    aria-label=label
                    viewBox=format!("0 0 {w:.1} {h:.1}")
                    preserveAspectRatio="xMinYMin meet"
                    width=width
                    height=height
                >
                    {shapes}
                </svg>
            </div>
            <div class="mmd-source" hidden=move || !source_on.get()>{code}</div>
        </div>
    }
    .into_any()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mermaid_fence_detection() {
        for yes in ["mermaid", "Mermaid", "MERMAID", "mermaid  {theme}"] {
            assert!(is_mermaid(yes), "{yes}");
        }
        for no in ["", "rust", "mermaidjs", "text mermaid"] {
            assert!(!is_mermaid(no), "{no}");
        }
    }

    #[test]
    fn zoom_steps_clamp() {
        assert_eq!(step_zoom(FIT, true), FIT + 1);
        assert_eq!(step_zoom(FIT, false), FIT - 1);
        assert_eq!(step_zoom(0, false), 0);
        assert_eq!(step_zoom(ZOOMS.len() - 1, true), ZOOMS.len() - 1);
        assert_eq!(ZOOMS[FIT], 1.0);
    }

    #[test]
    fn toggle_round_trip_labels() {
        assert_eq!(toggle_label(false), "Show source");
        assert_eq!(toggle_label(true), "Show diagram");
    }

    #[test]
    fn failure_note_names_the_problem() {
        let e = mermaid_lite::render("graph TD\nA ?? B").unwrap_err();
        let note = failure_note(&e);
        assert!(note.starts_with("Diagram didn't parse (line 2:"), "{note}");
        assert!(note.ends_with("showing the source."), "{note}");
    }

    #[test]
    fn points_attr_formats_pairs() {
        assert_eq!(points_attr(&[(1.0, 2.25), (3.5, 4.0)]), "1.0,2.2 3.5,4.0");
    }

    #[test]
    fn scene_label_lists_text_and_is_bounded() {
        let scene = mermaid_lite::render("graph TD\nA[Start]-->B[End]").unwrap();
        let label = scene_label(&scene);
        assert!(label.starts_with("Flowchart: ") && label.contains("Start") && label.contains("End"), "{label}");
        let long: String = (0..60).map(|i| format!("N{i}[label number {i}]\n")).collect();
        let big = mermaid_lite::render(&format!("graph TD\n{long}")).unwrap();
        assert!(scene_label(&big).chars().count() <= 300);
    }
}
