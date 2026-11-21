//! Visual annotate (#1545): pin and region notes on a composer image.
//!
//! Marks live in image space, never DOM space. Coordinates are stored as
//! fractions of the rendered image (0..1) so they survive zoom and are
//! sent as percentages. On send, the composer replaces the attachment
//! with a composite (marks drawn on a canvas copy of the real image) and
//! appends a numbered text block to the message. The original file is
//! never mutated.

use std::collections::HashMap;
use std::rc::Rc;

use leptos::prelude::*;
use wasm_bindgen::JsCast;

/// Below this drag distance (fraction of the image) a region tool press
/// is ignored rather than producing a sliver.
pub const MIN_REGION: f64 = 0.01;

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Shape {
    Pin { x: f64, y: f64 },
    Region { x0: f64, y0: f64, x1: f64, y1: f64 },
}

#[derive(Clone, Debug, PartialEq)]
pub struct Mark {
    pub shape: Shape,
    pub note: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Tool {
    Pin,
    Region,
}

/// Marks per pending attachment id. Lives with the composer session.
pub type MarkStore = RwSignal<HashMap<u32, Vec<Mark>>>;

/// The composer image currently open in the overlay.
#[derive(Clone, Debug, PartialEq)]
pub struct Target {
    pub id: u32,
    pub name: String,
    pub preview: String,
}

fn clamp01(v: f64) -> f64 {
    if v.is_nan() {
        0.0
    } else {
        v.clamp(0.0, 1.0)
    }
}

/// Fraction to a whole percent, clamped to 0..=100.
pub fn pct(v: f64) -> i64 {
    (clamp01(v) * 100.0).round() as i64
}

/// Fraction of the image under a client point, given the image's rect.
pub fn to_fraction(client: (f64, f64), rect: (f64, f64, f64, f64)) -> Option<(f64, f64)> {
    let (left, top, width, height) = rect;
    if width <= 0.0 || height <= 0.0 {
        return None;
    }
    Some((clamp01((client.0 - left) / width), clamp01((client.1 - top) / height)))
}

/// Raster images the canvas composite can redraw faithfully.
pub fn can_annotate(mime: &str) -> bool {
    matches!(
        mime.trim().to_ascii_lowercase().as_str(),
        "image/png" | "image/jpeg" | "image/jpg" | "image/webp" | "image/gif"
    )
}

/// Shape for a finished press. Pin places at the release point. Region
/// normalises the corners and drops presses smaller than [`MIN_REGION`].
pub fn shape_for(tool: Tool, start: (f64, f64), end: (f64, f64)) -> Option<Shape> {
    let (sx, sy) = (clamp01(start.0), clamp01(start.1));
    let (ex, ey) = (clamp01(end.0), clamp01(end.1));
    match tool {
        Tool::Pin => Some(Shape::Pin { x: ex, y: ey }),
        Tool::Region => {
            let (x0, x1) = if sx <= ex { (sx, ex) } else { (ex, sx) };
            let (y0, y1) = if sy <= ey { (sy, ey) } else { (ey, sy) };
            if x1 - x0 < MIN_REGION || y1 - y0 < MIN_REGION {
                None
            } else {
                Some(Shape::Region { x0, y0, x1, y1 })
            }
        }
    }
}

/// `[pin x=62%, y=18%]` / `[region x=4–48%, y=55–80%]`.
pub fn shape_label(shape: &Shape) -> String {
    match *shape {
        Shape::Pin { x, y } => format!("[pin x={}%, y={}%]", pct(x), pct(y)),
        Shape::Region { x0, y0, x1, y1 } => format!(
            "[region x={}\u{2013}{}%, y={}\u{2013}{}%]",
            pct(x0),
            pct(x1),
            pct(y0),
            pct(y1)
        ),
    }
}

/// Numbered text block for one annotated image. Empty notes are kept as
/// `(no note)` so numbering matches the composite.
pub fn text_block(image_name: &str, marks: &[Mark]) -> String {
    if marks.is_empty() {
        return String::new();
    }
    let name = if image_name.trim().is_empty() { "image" } else { image_name.trim() };
    let mut out = format!("Annotations on {name} (coordinates are % of the image):");
    for (i, mark) in marks.iter().enumerate() {
        let note = mark.note.trim().replace('\n', " ");
        let note = if note.is_empty() {
            "(no note)".to_string()
        } else {
            format!("\"{}\"", note.replace('"', "'"))
        };
        out.push_str(&format!("\n{}. {} {}", i + 1, shape_label(&mark.shape), note));
    }
    out
}

/// The message text actually sent: the user's text, then each block.
pub fn compose_message(text: &str, blocks: &[String]) -> String {
    let mut parts: Vec<&str> = Vec::new();
    if !text.trim().is_empty() {
        parts.push(text.trim());
    }
    parts.extend(blocks.iter().map(|b| b.as_str()).filter(|b| !b.is_empty()));
    parts.join("\n\n")
}

/// `shot.png` → `shot-annotated.png`; the composite is always PNG.
pub fn annotated_name(name: &str) -> String {
    let name = name.trim();
    let stem = match name.rfind('.') {
        Some(i) if i > 0 => &name[..i],
        _ => name,
    };
    let stem = if stem.is_empty() { "image" } else { stem };
    format!("{stem}-annotated.png")
}

/// Confirm text for discarding marks; `None` when there is nothing to lose.
pub fn discard_body(count: usize) -> Option<String> {
    match count {
        0 => None,
        1 => Some("1 mark on this image will be discarded.".into()),
        n => Some(format!("{n} marks on this image will be discarded.")),
    }
}

/// Composite scale: cap the long edge so the PNG stays under the ingest cap.
pub fn composite_scale(width: f64, height: f64, max_edge: f64) -> f64 {
    let long = width.max(height);
    if long <= 0.0 || long <= max_edge {
        1.0
    } else {
        max_edge / long
    }
}

const MAX_COMPOSITE_EDGE: f64 = 2560.0;

fn css_color(name: &str, fallback: &str) -> String {
    let value = web_sys::window()
        .and_then(|w| {
            let root = w.document()?.document_element()?;
            w.get_computed_style(&root).ok().flatten()
        })
        .and_then(|s| s.get_property_value(name).ok())
        .unwrap_or_default();
    let value = value.trim();
    if value.is_empty() {
        fallback.into()
    } else {
        value.to_string()
    }
}

/// Draw the real image plus numbered marks onto a canvas and return the
/// PNG as standard base64 (no `data:` prefix).
pub async fn composite_png(preview_url: &str, marks: &[Mark]) -> Result<String, String> {
    let doc = web_sys::window()
        .and_then(|w| w.document())
        .ok_or("no document")?;
    let img = web_sys::HtmlImageElement::new().map_err(|_| "image element failed")?;
    img.set_src(preview_url);
    wasm_bindgen_futures::JsFuture::from(img.decode())
        .await
        .map_err(|_| "could not decode the attached image".to_string())?;
    let (nw, nh) = (img.natural_width() as f64, img.natural_height() as f64);
    if nw <= 0.0 || nh <= 0.0 {
        return Err("attached image has no size".into());
    }
    let scale = composite_scale(nw, nh, MAX_COMPOSITE_EDGE);
    let (w, h) = ((nw * scale).round(), (nh * scale).round());
    let canvas: web_sys::HtmlCanvasElement = doc
        .create_element("canvas")
        .map_err(|_| "canvas failed")?
        .unchecked_into();
    canvas.set_width(w as u32);
    canvas.set_height(h as u32);
    let ctx: web_sys::CanvasRenderingContext2d = canvas
        .get_context("2d")
        .ok()
        .flatten()
        .ok_or("canvas 2d unavailable")?
        .unchecked_into();
    ctx.draw_image_with_html_image_element_and_dw_and_dh(&img, 0.0, 0.0, w, h)
        .map_err(|_| "could not draw the image")?;
    let mark_color = css_color("--signal", "#fb7185");
    let ink = "#ffffff";
    let line = (w.max(h) / 400.0).max(2.0);
    let radius = (w.max(h) / 70.0).max(10.0);
    ctx.set_line_width(line);
    ctx.set_text_align("center");
    ctx.set_text_baseline("middle");
    ctx.set_font(&format!("bold {}px sans-serif", (radius * 1.1).round()));
    for (i, mark) in marks.iter().enumerate() {
        let label = (i + 1).to_string();
        let (cx, cy) = match mark.shape {
            Shape::Pin { x, y } => (x * w, y * h),
            Shape::Region { x0, y0, x1, y1 } => {
                ctx.set_stroke_style_str(&mark_color);
                ctx.stroke_rect(x0 * w, y0 * h, (x1 - x0) * w, (y1 - y0) * h);
                (x0 * w + radius, y0 * h + radius)
            }
        };
        ctx.begin_path();
        let _ = ctx.arc(cx, cy, radius, 0.0, std::f64::consts::PI * 2.0);
        ctx.set_fill_style_str(&mark_color);
        ctx.fill();
        ctx.set_fill_style_str(ink);
        let _ = ctx.fill_text(&label, cx, cy);
    }
    let url = canvas
        .to_data_url_with_type("image/png")
        .map_err(|_| "could not export the composite")?;
    url.split_once(',')
        .map(|(_, b64)| b64.to_string())
        .ok_or_else(|| "composite export was empty".to_string())
}

fn image_rect(img: NodeRef<leptos::html::Img>) -> Option<(f64, f64, f64, f64)> {
    let el = img.get_untracked()?;
    let r = el.get_bounding_client_rect();
    Some((r.left(), r.top(), r.width(), r.height()))
}

fn canvas_class(tool: Tool) -> &'static str {
    match tool {
        Tool::Pin => "annotate-canvas",
        Tool::Region => "annotate-canvas annotate-drawing",
    }
}

fn focus_later(el_ref: NodeRef<leptos::html::Div>) {
    let closure = wasm_bindgen::closure::Closure::once(move || {
        if let Some(el) = el_ref.get_untracked() {
            let _ = el.focus();
        }
    });
    if let Some(w) = web_sys::window() {
        let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(closure.as_ref().unchecked_ref(), 0);
    }
    closure.forget();
}

fn pct_css(v: f64) -> String {
    format!("{:.2}%", clamp01(v) * 100.0)
}

/// The annotate overlay. `target` opens it; Done writes the draft into
/// `store`, Cancel/Escape reverts (confirming when unsaved marks would be
/// lost). Nothing here touches the attachment itself.
#[component]
pub fn AnnotateOverlay(target: RwSignal<Option<Target>>, store: MarkStore) -> impl IntoView {
    let draft = RwSignal::new(Vec::<Mark>::new());
    let saved = RwSignal::new(Vec::<Mark>::new());
    let tool = RwSignal::new(Tool::Pin);
    let zoom = RwSignal::new(100u32);
    let press = RwSignal::new(None::<(f64, f64)>);
    let drag_end = RwSignal::new(None::<(f64, f64)>);
    let note = RwSignal::new(String::new());
    let img_ref = NodeRef::<leptos::html::Img>::new();
    let root_ref = NodeRef::<leptos::html::Div>::new();

    Effect::new(move |_| {
        if let Some(t) = target.get() {
            let marks = store.with_untracked(|m| m.get(&t.id).cloned().unwrap_or_default());
            saved.set(marks.clone());
            draft.set(marks);
            tool.set(Tool::Pin);
            zoom.set(100);
            press.set(None);
            drag_end.set(None);
            note.set(String::new());
            focus_later(root_ref);
        }
    });

    let close = move || {
        target.set(None);
        press.set(None);
        drag_end.set(None);
    };
    let done = move || {
        if let Some(t) = target.get_untracked() {
            let marks = draft.get_untracked();
            store.update(|m| {
                if marks.is_empty() {
                    m.remove(&t.id);
                } else {
                    m.insert(t.id, marks);
                }
            });
        }
        close();
    };
    let cancel = move || {
        let unsaved = draft.with_untracked(|d| saved.with_untracked(|s| d != s));
        let lost = draft.with_untracked(|d| d.len());
        if !unsaved {
            close();
            return;
        }
        let body = discard_body(lost).unwrap_or_else(|| "Unsaved changes to the marks will be discarded.".into());
        let opened = crate::confirm::ask(
            crate::confirm::ConfirmRequest {
                title: "Discard marks?".into(),
                body,
                confirm_label: "Discard".into(),
            },
            Rc::new(move || close()),
        );
        if !opened {
            note.set(crate::confirm::BUSY.into());
        }
    };

    let on_down = move |ev: web_sys::PointerEvent| {
        if ev.button() != 0 {
            return;
        }
        let Some(rect) = image_rect(img_ref) else { return };
        let Some(at) = to_fraction((ev.client_x() as f64, ev.client_y() as f64), rect) else {
            return;
        };
        if tool.get_untracked() == Tool::Region {
            ev.prevent_default();
            if let Some(el) = ev.target().and_then(|t| t.dyn_into::<web_sys::Element>().ok()) {
                let _ = el.set_pointer_capture(ev.pointer_id());
            }
        }
        press.set(Some(at));
        drag_end.set(Some(at));
    };
    let on_move = move |ev: web_sys::PointerEvent| {
        if press.get_untracked().is_none() || tool.get_untracked() != Tool::Region {
            return;
        }
        if let Some(rect) = image_rect(img_ref) {
            if let Some(at) = to_fraction((ev.client_x() as f64, ev.client_y() as f64), rect) {
                drag_end.set(Some(at));
            }
        }
    };
    let on_up = move |ev: web_sys::PointerEvent| {
        let Some(start) = press.get_untracked() else { return };
        press.set(None);
        drag_end.set(None);
        let Some(rect) = image_rect(img_ref) else { return };
        let Some(end) = to_fraction((ev.client_x() as f64, ev.client_y() as f64), rect) else {
            return;
        };
        if let Some(shape) = shape_for(tool.get_untracked(), start, end) {
            draft.update(|d| d.push(Mark { shape, note: String::new() }));
            note.set(String::new());
        } else if tool.get_untracked() == Tool::Region {
            note.set("Drag across the image to draw a region.".into());
        }
    };
    let on_cancel_ptr = move |_: web_sys::PointerEvent| {
        press.set(None);
        drag_end.set(None);
    };
    let on_key = move |ev: web_sys::KeyboardEvent| {
        if ev.key() == "Escape" {
            ev.stop_propagation();
            ev.prevent_default();
            cancel();
        }
    };

    view! {
        <Show when=move || target.get().is_some()>
            <div class="annotate-overlay" role="dialog" aria-modal="true" aria-label="Annotate image"
                tabindex="-1" node_ref=root_ref on:keydown=on_key data-annotate="true">
                <div class="annotate-panel">
                    <header class="annotate-head">
                        <strong class="annotate-title">{move || format!("Annotate {}", target.get().map(|t| t.name).unwrap_or_default())}</strong>
                        <span class="annotate-tools" role="group" aria-label="Mark tool">
                            <button type="button" aria-pressed=move || (tool.get() == Tool::Pin).to_string()
                                data-annotate-tool="pin" on:click=move |_| tool.set(Tool::Pin)>"Pin"</button>
                            <button type="button" aria-pressed=move || (tool.get() == Tool::Region).to_string()
                                data-annotate-tool="region" on:click=move |_| tool.set(Tool::Region)>"Region"</button>
                        </span>
                        <span class="annotate-zoom" role="group" aria-label="Zoom">
                            <button type="button" aria-label="Zoom out" on:click=move |_| zoom.update(|z| *z = (*z).saturating_sub(50).max(100))>"\u{2212}"</button>
                            <span class="annotate-zoom-level">{move || format!("{}%", zoom.get())}</span>
                            <button type="button" aria-label="Zoom in" on:click=move |_| zoom.update(|z| *z = (*z + 50).min(400))>"+"</button>
                        </span>
                    </header>
                    <p class="annotate-hint">{move || match tool.get() {
                        Tool::Pin => "Tap or click the image to drop a numbered pin.",
                        Tool::Region => "Drag across the image to draw a region.",
                    }}</p>
                    <div class="annotate-stage">
                        <div class=move || canvas_class(tool.get())
                            style:width=move || format!("{}%", zoom.get())
                            on:pointerdown=on_down on:pointermove=on_move on:pointerup=on_up on:pointercancel=on_cancel_ptr>
                            <img node_ref=img_ref draggable="false" alt="Image being annotated"
                                src=move || target.get().map(|t| t.preview).unwrap_or_default() />
                            {move || draft.get().into_iter().enumerate().map(|(i, mark)| {
                                let n = (i + 1).to_string();
                                match mark.shape {
                                    Shape::Pin { x, y } => view! {
                                        <span class="annotate-pin" aria-hidden="true"
                                            style:left=pct_css(x) style:top=pct_css(y)>{n}</span>
                                    }.into_any(),
                                    Shape::Region { x0, y0, x1, y1 } => view! {
                                        <span class="annotate-region" aria-hidden="true"
                                            style:left=pct_css(x0) style:top=pct_css(y0)
                                            style:width=pct_css(x1 - x0) style:height=pct_css(y1 - y0)>
                                            <span class="annotate-region-n">{n}</span>
                                        </span>
                                    }.into_any(),
                                }
                            }).collect_view()}
                            {move || match (press.get(), drag_end.get(), tool.get()) {
                                (Some(a), Some(b), Tool::Region) => {
                                    let (x0, x1) = if a.0 <= b.0 { (a.0, b.0) } else { (b.0, a.0) };
                                    let (y0, y1) = if a.1 <= b.1 { (a.1, b.1) } else { (b.1, a.1) };
                                    view! {
                                        <span class="annotate-region annotate-ghost" aria-hidden="true"
                                            style:left=pct_css(x0) style:top=pct_css(y0)
                                            style:width=pct_css(x1 - x0) style:height=pct_css(y1 - y0)></span>
                                    }.into_any()
                                }
                                _ => view! { <span></span> }.into_any(),
                            }}
                        </div>
                    </div>
                    <Show when=move || !note.get().is_empty()>
                        <p class="annotate-note" role="status">{move || note.get()}</p>
                    </Show>
                    <ol class="annotate-marks" data-annotate-marks="true">
                        {move || draft.get().into_iter().enumerate().map(|(i, mark)| {
                            let label = shape_label(&mark.shape);
                            let value = mark.note.clone();
                            view! {
                                <li class="annotate-mark">
                                    <span class="annotate-mark-n">{(i + 1).to_string()}</span>
                                    <span class="annotate-mark-where">{label}</span>
                                    <input type="text" class="annotate-mark-note" maxlength="300"
                                        placeholder="Note for this mark" aria-label=format!("Note for mark {}", i + 1)
                                        prop:value=value
                                        on:change=move |ev| {
                                            let v = event_target_value(&ev);
                                            draft.update(|d| if let Some(m) = d.get_mut(i) { m.note = v; });
                                        } />
                                    <button type="button" aria-label=format!("Delete mark {}", i + 1)
                                        on:click=move |_| draft.update(|d| if i < d.len() { d.remove(i); })>"Delete"</button>
                                </li>
                            }
                        }).collect_view()}
                    </ol>
                    <footer class="annotate-foot">
                        <button type="button" class="secondary" disabled=move || draft.with(|d| d.is_empty())
                            on:click=move |_| draft.set(Vec::new())>"Clear all"</button>
                        <span class="annotate-spacer"></span>
                        <button type="button" class="secondary" on:click=move |_| cancel()>"Cancel"</button>
                        <button type="button" data-annotate-done="true" on:click=move |_| done()>"Done"</button>
                    </footer>
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pin(x: f64, y: f64, note: &str) -> Mark {
        Mark { shape: Shape::Pin { x, y }, note: note.into() }
    }

    #[test]
    fn pct_rounds_and_clamps() {
        for (v, want) in [(0.0, 0), (0.624, 62), (0.625, 63), (1.2, 100), (-0.5, 0), (f64::NAN, 0)] {
            assert_eq!(pct(v), want, "v {v}");
        }
    }

    #[test]
    fn to_fraction_maps_client_points() {
        let rect = (100.0, 50.0, 200.0, 100.0);
        assert_eq!(to_fraction((200.0, 100.0), rect), Some((0.5, 0.5)));
        assert_eq!(to_fraction((50.0, 400.0), rect), Some((0.0, 1.0)));
        assert_eq!(to_fraction((1.0, 1.0), (0.0, 0.0, 0.0, 10.0)), None);
    }

    #[test]
    fn shape_for_tools() {
        assert_eq!(shape_for(Tool::Pin, (0.1, 0.1), (0.3, 0.4)), Some(Shape::Pin { x: 0.3, y: 0.4 }));
        assert_eq!(
            shape_for(Tool::Region, (0.48, 0.80), (0.04, 0.55)),
            Some(Shape::Region { x0: 0.04, y0: 0.55, x1: 0.48, y1: 0.80 })
        );
        assert_eq!(shape_for(Tool::Region, (0.5, 0.5), (0.505, 0.9)), None);
        assert_eq!(
            shape_for(Tool::Region, (-1.0, -1.0), (2.0, 2.0)),
            Some(Shape::Region { x0: 0.0, y0: 0.0, x1: 1.0, y1: 1.0 })
        );
    }

    #[test]
    fn text_block_matches_issue_format() {
        let marks = vec![
            pin(0.62, 0.18, "make this match the header button"),
            Mark {
                shape: Shape::Region { x0: 0.04, y0: 0.55, x1: 0.48, y1: 0.80 },
                note: "this card overflows on mobile".into(),
            },
            pin(0.5, 0.5, "  "),
        ];
        let got = text_block("shot.png", &marks);
        assert_eq!(
            got,
            "Annotations on shot.png (coordinates are % of the image):\n\
             1. [pin x=62%, y=18%] \"make this match the header button\"\n\
             2. [region x=4\u{2013}48%, y=55\u{2013}80%] \"this card overflows on mobile\"\n\
             3. [pin x=50%, y=50%] (no note)"
        );
        assert!(text_block("x.png", &[]).is_empty());
        assert!(text_block("", &[pin(0.0, 0.0, "a\nb \"q\"")]).contains("on image") );
        assert!(text_block("", &[pin(0.0, 0.0, "a\nb \"q\"")]).ends_with("\"a b 'q'\""));
    }

    #[test]
    fn compose_message_joins_text_and_blocks() {
        assert_eq!(compose_message("fix these", &["B1".into(), String::new(), "B2".into()]), "fix these\n\nB1\n\nB2");
        assert_eq!(compose_message("  ", &["B1".into()]), "B1");
        assert_eq!(compose_message("only text", &[]), "only text");
    }

    #[test]
    fn annotated_name_cases() {
        for (input, want) in [
            ("shot.png", "shot-annotated.png"),
            ("photo.final.jpeg", "photo.final-annotated.png"),
            (".hidden", ".hidden-annotated.png"),
            ("noext", "noext-annotated.png"),
            ("", "image-annotated.png"),
        ] {
            assert_eq!(annotated_name(input), want, "input {input:?}");
        }
    }

    #[test]
    fn can_annotate_raster_only() {
        for (mime, want) in [("image/png", true), ("IMAGE/JPEG", true), ("image/svg+xml", false), ("application/pdf", false), ("", false)] {
            assert_eq!(can_annotate(mime), want, "mime {mime:?}");
        }
    }

    #[test]
    fn discard_body_counts() {
        assert_eq!(discard_body(0), None);
        assert!(discard_body(1).unwrap().starts_with("1 mark "));
        assert!(discard_body(3).unwrap().starts_with("3 marks "));
    }

    #[test]
    fn composite_scale_caps_long_edge() {
        assert_eq!(composite_scale(1000.0, 800.0, 2560.0), 1.0);
        assert_eq!(composite_scale(5120.0, 1000.0, 2560.0), 0.5);
        assert_eq!(composite_scale(0.0, 0.0, 2560.0), 1.0);
    }
}
