//! Context doctor (#1537): where the active session's context goes.
//! Requests `inspect.prompt` over the live op route and renders the
//! engine's `prompt.effective` reply with the TUI doctor's semantics
//! (internal/frontend/tui/app/_src/modal/doctor_modal.go): per-layer
//! kind/source/mode, chars, a chars/4 token *estimate*, share of the
//! system prompt, pinned/excluded/shed kinds, request-token attribution,
//! and the 50% / 80% pressure and dominant-layer warnings. Read-only:
//! pin/exclude (`context.controls`) is a later slice. feature:context-doctor

use leptos::prelude::*;
use leptos::task::spawn_local;
use protocol_schema::{EffectivePrompt, RequestTokenAttribution, TokenCount};

use crate::model_menu::format_tokens;

/// Rough chars-per-token for size warnings only; never shown as measured.
pub const CHARS_PER_TOKEN_EST: i64 = 4;
/// Pressure thresholds as a fraction of the context window (TUI
/// doctorWarnRatio / doctorErrorRatio).
pub const WARN_RATIO: f64 = 0.50;
pub const ERROR_RATIO: f64 = 0.80;
/// Dominant-layer warning: share of the system prompt and minimum size.
const DOMINANT_PCT: i64 = 60;
const DOMINANT_MIN_CHARS: i64 = 2000;
const PREVIEW_MAX: usize = 80;
/// How long to wait for `prompt.effective` after the op was accepted.
const REPLY_TIMEOUT_MS: i32 = 8000;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Level {
    Info,
    Warn,
    Error,
}

impl Level {
    pub fn attr(self) -> &'static str {
        match self {
            Level::Info => "info",
            Level::Warn => "warn",
            Level::Error => "error",
        }
    }
}

/// One rendered layer row.
#[derive(Clone, Debug, PartialEq)]
pub struct LayerRow {
    pub head: String,
    pub detail: String,
    pub preview: String,
}

/// The engine's `prompt.effective` for the viewed root, or None. Child
/// engines answer with their own session lineage, so a child's snapshot
/// never replaces the root's.
pub fn effective_for_root(raw: &str, root: &str) -> Option<EffectivePrompt> {
    match protocol_schema::decode_event(raw) {
        Ok(protocol_schema::Event::EffectivePrompt(ev)) => {
            let mine = if ev.session_id.is_empty() || root.is_empty() {
                ev.depth == 0 && ev.parent_session_id.is_empty()
            } else {
                ev.session_id == root
            };
            mine.then_some(ev)
        }
        _ => None,
    }
}

/// Strip control characters for display (previews are engine-redacted).
pub fn sanitize(s: &str) -> String {
    s.chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect()
}

fn clip(s: &str, max: usize) -> String {
    if s.chars().count() > max {
        let mut out: String = s.chars().take(max).collect();
        out.push('…');
        out
    } else {
        s.to_string()
    }
}

pub fn scope_label(ev: &EffectivePrompt) -> &'static str {
    if ev.from_last_stream {
        "last request"
    } else {
        "current composition"
    }
}

fn system_total(ev: &EffectivePrompt) -> i64 {
    if ev.system_chars > 0 {
        ev.system_chars
    } else {
        ev.layers.iter().map(|l| l.chars.max(0)).sum()
    }
}

pub fn layer_rows(ev: &EffectivePrompt) -> Vec<LayerRow> {
    let total = system_total(ev);
    ev.layers
        .iter()
        .enumerate()
        .map(|(i, layer)| {
            let mut head = format!(
                "{}. {} [{}]",
                i + 1,
                sanitize(&layer.kind),
                sanitize(&layer.mode)
            );
            if layer.pinned {
                head.push_str(" pin");
            }
            let mut est = layer.est_tokens;
            if est <= 0 && layer.chars > 0 {
                est = (layer.chars + CHARS_PER_TOKEN_EST - 1) / CHARS_PER_TOKEN_EST;
            }
            let mut parts = vec![sanitize(&layer.source), format!("{} chars", layer.chars)];
            if est > 0 {
                parts.push(format!("~{} tok", format_tokens(est)));
            }
            if total > 0 && layer.chars > 0 {
                parts.push(format!("{}%", layer.chars * 100 / total));
            }
            LayerRow {
                head,
                detail: parts.join(" · "),
                preview: clip(&sanitize(layer.preview.trim()), PREVIEW_MAX),
            }
        })
        .collect()
}

/// `~N tok (chars/4 est.)` for the whole system prompt, when sized.
pub fn system_estimate(ev: &EffectivePrompt) -> Option<String> {
    (ev.system_chars > 0).then(|| {
        format!(
            "~{} (chars/{CHARS_PER_TOKEN_EST} est.)",
            format_tokens(ev.system_chars / CHARS_PER_TOKEN_EST)
        )
    })
}

pub fn pressure(ev: &EffectivePrompt, limit: i64) -> Level {
    if limit <= 0 || ev.system_chars <= 0 {
        return Level::Info;
    }
    let ratio = (ev.system_chars / CHARS_PER_TOKEN_EST) as f64 / limit as f64;
    if ratio >= ERROR_RATIO {
        Level::Error
    } else if ratio >= WARN_RATIO {
        Level::Warn
    } else {
        Level::Info
    }
}

/// The doctor's warnings with the TUI wording and thresholds.
pub fn warnings(ev: &EffectivePrompt, limit: i64) -> Vec<(Level, String)> {
    let mut out = Vec::new();
    let level = pressure(ev, limit);
    if level != Level::Info {
        let pct = if level == Level::Error { ERROR_RATIO } else { WARN_RATIO };
        out.push((
            level,
            format!(
                "warning: system prompt ~{} tok est. is ≥{}% of the {} context window",
                format_tokens(ev.system_chars / CHARS_PER_TOKEN_EST),
                (pct * 100.0) as i64,
                format_tokens(limit)
            ),
        ));
    }
    let total = system_total(ev);
    if total > 0 {
        for layer in &ev.layers {
            let share = layer.chars * 100 / total;
            if share >= DOMINANT_PCT && layer.chars >= DOMINANT_MIN_CHARS {
                out.push((
                    Level::Warn,
                    format!(
                        "warning: layer {:?} is {}% of system prompt ({} chars)",
                        sanitize(&layer.kind),
                        share,
                        layer.chars
                    ),
                ));
            }
        }
    }
    out
}

fn est_count(tc: &TokenCount) -> String {
    if tc.known {
        format!("~{}", format_tokens(tc.n))
    } else {
        "—".to_string()
    }
}

/// Request-input attribution rows, or None for legacy sessions that never
/// populated it.
pub fn attribution_rows(a: &RequestTokenAttribution) -> Option<Vec<(&'static str, String)>> {
    if a.source.is_empty()
        && !a.total.known
        && !a.system.known
        && !a.tools.known
        && !a.messages.known
        && !a.tool_results.known
    {
        return None;
    }
    let source = if a.source.is_empty() {
        "estimated".to_string()
    } else {
        sanitize(&a.source)
    };
    Some(vec![
        ("system", est_count(&a.system)),
        ("tools", est_count(&a.tools)),
        ("messages", est_count(&a.messages)),
        ("tool_results", est_count(&a.tool_results)),
        ("total", est_count(&a.total)),
        ("source", source),
    ])
}

/// Honest copy for a failed `inspect.prompt` submit.
pub fn inspect_error_text(err: &str) -> String {
    let rest = err.strip_prefix("HTTP ").unwrap_or("");
    let (code, body) = rest.split_once(' ').unwrap_or((rest, ""));
    let msg = serde_json::from_str::<serde_json::Value>(body.trim())
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .unwrap_or_else(|| body.trim().to_string());
    match code {
        "503" => "No live session is running on this gateway, so there is no context to inspect.".into(),
        _ if !msg.is_empty() => format!("Inspect is unavailable: {msg}"),
        _ => format!("Inspect is unavailable: {err}"),
    }
}

pub const NO_REPLY: &str =
    "The daemon accepted the request but no breakdown arrived. Try Refresh.";

/// Page-owned doctor state. All fields are Copy signals so `main.rs`
/// only forwards stream notes and binds the palette command.
#[derive(Clone, Copy)]
pub struct DoctorState {
    pub open: RwSignal<bool>,
    pub snapshot: RwSignal<Option<EffectivePrompt>>,
    pub pending: RwSignal<bool>,
    pub error: RwSignal<String>,
    /// A turn started after the shown snapshot was taken.
    pub stale: RwSignal<bool>,
    seq: RwSignal<u32>,
}

impl DoctorState {
    pub fn new() -> Self {
        Self {
            open: RwSignal::new(false),
            snapshot: RwSignal::new(None),
            pending: RwSignal::new(false),
            error: RwSignal::new(String::new()),
            stale: RwSignal::new(false),
            seq: RwSignal::new(0),
        }
    }

    /// Submit `inspect.prompt` to the live root; the reply arrives on the
    /// event stream and lands through `on_envelope`.
    pub fn request(self, base: String, token: String, root: String) {
        self.seq.update(|s| *s = s.wrapping_add(1));
        let seq = self.seq.get_untracked();
        self.pending.set(true);
        self.error.set(String::new());
        spawn_local(async move {
            let sent = crate::api::post_op(&base, &token, &root, "inspect.prompt", serde_json::json!({})).await;
            if self.seq.get_untracked() != seq {
                return;
            }
            if let Err(e) = sent {
                self.pending.set(false);
                self.error.set(inspect_error_text(&e));
                return;
            }
            crate::clipboard::after_ms(REPLY_TIMEOUT_MS, move || {
                if self.seq.get_untracked() == seq && self.pending.get_untracked() {
                    self.pending.set(false);
                    self.error.set(NO_REPLY.to_string());
                }
            });
        });
    }

    /// Feed one live envelope; adopts a `prompt.effective` for `root`.
    pub fn on_envelope(self, raw: &str, root: &str) {
        if !raw.contains("prompt.effective") {
            return;
        }
        if let Some(ev) = effective_for_root(raw, root) {
            self.snapshot.set(Some(ev));
            self.pending.set(false);
            self.error.set(String::new());
            self.stale.set(false);
        }
    }

    pub fn on_turn_started(self) {
        if self.snapshot.with_untracked(|s| s.is_some()) {
            self.stale.set(true);
        }
    }

    /// The viewed session changed: drop the old session's breakdown.
    pub fn reset(self) {
        self.seq.update(|s| *s = s.wrapping_add(1));
        self.snapshot.set(None);
        self.pending.set(false);
        self.error.set(String::new());
        self.stale.set(false);
    }
}

#[component]
pub fn ContextDoctorDialog(
    state: DoctorState,
    /// Resolved context window (daemon limit, else catalog), 0 = unknown.
    #[prop(into)]
    limit: Signal<i64>,
    on_refresh: Callback<()>,
) -> impl IntoView {
    let close = move || state.open.set(false);
    view! {
        <Show when=move || state.open.get()>
            <div class="confirm-backdrop" data-context-doctor="backdrop" on:click=move |_| close()>
                <div
                    class="confirm-dialog context-doctor"
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="context-doctor-title"
                    data-context-doctor="dialog"
                    data-pressure=move || state.snapshot.get().map(|ev| pressure(&ev, limit.get()).attr()).unwrap_or("info")
                    tabindex="-1"
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                    on:keydown=move |ev: web_sys::KeyboardEvent| {
                        if ev.key() == "Escape" {
                            ev.stop_propagation();
                            close();
                        }
                    }
                >
                    <h2 id="context-doctor-title">"Context doctor"</h2>
                    <p class="hint">"What fills this session's system prompt. Token figures are local chars/4 estimates, never provider-measured tokens."</p>
                    <Show when=move || !state.error.get().is_empty()>
                        <p class="hint storage-error" role="alert" data-context-doctor-error="true">{move || state.error.get()}</p>
                    </Show>
                    <Show when=move || state.pending.get() && state.snapshot.get().is_none()>
                        <p class="hint" data-context-doctor-pending="true">"Asking the engine for the current breakdown\u{2026}"</p>
                    </Show>
                    <Show when=move || state.stale.get()>
                        <p class="hint context-doctor-stale" data-context-doctor-stale="true">"A newer turn has started; this breakdown is from before it. It refreshes when the turn ends."</p>
                    </Show>
                    {move || state.snapshot.get().map(|ev| {
                        let lim = limit.get();
                        let scope = scope_label(&ev);
                        let no_stream = !ev.from_last_stream;
                        let rows = layer_rows(&ev);
                        let warns = warnings(&ev, lim);
                        let attribution = attribution_rows(&ev.attribution);
                        let sys_est = system_estimate(&ev);
                        let kv = |k: &'static str, v: String| view! {
                            <div class="context-doctor-kv"><dt>{k}</dt><dd>{v}</dd></div>
                        };
                        view! {
                            <div class="context-doctor-body" data-context-doctor-scope=scope>
                                <dl class="context-doctor-summary">
                                    {kv("scope", scope.to_string())}
                                    {kv("system", format!("{} chars", ev.system_chars))}
                                    {kv("history", format!("{} msgs", ev.message_count))}
                                    {(lim > 0).then(|| kv("limit", format!("{} context window", format_tokens(lim))))}
                                    {(lim > 0).then(|| sys_est.clone().map(|e| kv("system ~tok", e))).flatten()}
                                    {(!ev.excluded_kinds.is_empty()).then(|| kv("excluded", sanitize(&ev.excluded_kinds.join(", "))))}
                                    {(!ev.pinned_kinds.is_empty()).then(|| kv("pinned", sanitize(&ev.pinned_kinds.join(", "))))}
                                    {(!ev.shed_kinds.is_empty()).then(|| kv("shed", sanitize(&ev.shed_kinds.join(", "))))}
                                </dl>
                                {no_stream.then(|| view! {
                                    <p class="hint" data-context-doctor-no-stream="true">"No request has been sent in this session yet: this is the composition the next request will use."</p>
                                })}
                                {warns.into_iter().map(|(level, text)| view! {
                                    <p class="context-doctor-warning" role="status" data-level=level.attr()>{text}</p>
                                }).collect_view()}
                                <h3>"Layers"</h3>
                                {if rows.is_empty() {
                                    view! { <p class="hint" data-context-doctor-empty="true">"(no layers)"</p> }.into_any()
                                } else {
                                    view! {
                                        <ol class="context-doctor-layers">
                                            {rows.into_iter().map(|row| {
                                                let preview = row.preview.clone();
                                                view! {
                                                    <li class="context-doctor-layer" data-context-layer="true">
                                                        <span class="name">{row.head}</span>
                                                        <span class="path">{row.detail}</span>
                                                        {(!preview.is_empty()).then(|| view! { <span class="path context-doctor-preview">{preview.clone()}</span> })}
                                                    </li>
                                                }
                                            }).collect_view()}
                                        </ol>
                                    }.into_any()
                                }}
                                {attribution.map(|rows| view! {
                                    <h3>"Request input (est.)"</h3>
                                    <dl class="context-doctor-summary" data-context-doctor-attribution="true">
                                        {rows.into_iter().map(|(k, v)| kv(k, v)).collect_view()}
                                    </dl>
                                    <p class="hint">{format!("Local ~{CHARS_PER_TOKEN_EST} chars/token; not provider-measured.")}</p>
                                })}
                            </div>
                        }
                    })}
                    <div class="confirm-actions">
                        <button type="button" class="secondary" data-context-doctor="refresh"
                            disabled=move || state.pending.get()
                            on:click=move |_| on_refresh.run(())>
                            {move || if state.pending.get() { "Refreshing\u{2026}" } else { "Refresh" }}
                        </button>
                        <button type="button" data-context-doctor="close" on:click=move |_| close()>"Close"</button>
                    </div>
                </div>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use protocol_schema::PromptLayerInfo;

    fn layer(kind: &str, chars: i64) -> PromptLayerInfo {
        PromptLayerInfo {
            kind: kind.into(),
            source: format!("src/{kind}"),
            mode: "always".into(),
            chars,
            ..Default::default()
        }
    }

    fn fixture() -> EffectivePrompt {
        EffectivePrompt {
            session_id: "root1".into(),
            layers: vec![
                PromptLayerInfo { pinned: true, preview: "You are\nstrike".into(), ..layer("base", 1000) },
                layer("rules", 3000),
            ],
            system_chars: 4000,
            message_count: 7,
            from_last_stream: true,
            pinned_kinds: vec!["base".into()],
            excluded_kinds: vec!["skills".into()],
            shed_kinds: vec!["lean".into()],
            ..Default::default()
        }
    }

    #[test]
    fn layer_rows_match_tui_semantics() {
        let rows = layer_rows(&fixture());
        assert_eq!(rows.len(), 2);
        assert_eq!(rows[0].head, "1. base [always] pin");
        assert_eq!(rows[0].detail, "src/base · 1000 chars · ~250 tok · 25%");
        assert_eq!(rows[0].preview, "You are strike");
        assert_eq!(rows[1].detail, "src/rules · 3000 chars · ~750 tok · 75%");
    }

    #[test]
    fn est_tokens_from_engine_win_and_total_falls_back_to_layer_sum() {
        let mut ev = fixture();
        ev.system_chars = 0;
        ev.layers[1].est_tokens = 900;
        let rows = layer_rows(&ev);
        assert!(rows[1].detail.contains("~900 tok"), "{}", rows[1].detail);
        assert!(rows[1].detail.ends_with("75%"), "{}", rows[1].detail);
        assert_eq!(system_estimate(&ev), None);
    }

    #[test]
    fn estimate_is_always_labelled_as_an_estimate() {
        assert_eq!(system_estimate(&fixture()).unwrap(), "~1k (chars/4 est.)");
        for row in layer_rows(&fixture()) {
            assert!(row.detail.contains('~'), "{}", row.detail);
        }
    }

    #[test]
    fn pressure_thresholds_are_50_and_80_percent() {
        let mut ev = fixture();
        // 4000 chars → ~1000 tok.
        assert_eq!(pressure(&ev, 0), Level::Info);
        assert_eq!(pressure(&ev, 2001), Level::Info);
        assert_eq!(pressure(&ev, 2000), Level::Warn);
        assert_eq!(pressure(&ev, 1250), Level::Error);
        ev.system_chars = 0;
        assert_eq!(pressure(&ev, 10), Level::Info);
    }

    #[test]
    fn warnings_cover_pressure_and_dominant_layer() {
        let w = warnings(&fixture(), 1200);
        assert_eq!(w.len(), 2, "{w:?}");
        assert_eq!(w[0].0, Level::Error);
        assert_eq!(
            w[0].1,
            "warning: system prompt ~1k tok est. is ≥80% of the 1.2k context window"
        );
        assert_eq!(w[1].0, Level::Warn);
        assert_eq!(w[1].1, "warning: layer \"rules\" is 75% of system prompt (3000 chars)");
        // Big window, small dominant layer (< 2000 chars): no warnings.
        let mut small = fixture();
        small.layers[1].chars = 1500;
        small.system_chars = 2500;
        assert!(warnings(&small, 1_000_000).is_empty());
    }

    #[test]
    fn attribution_hidden_for_legacy_and_dashed_when_unknown() {
        assert!(attribution_rows(&RequestTokenAttribution::default()).is_none());
        let a = RequestTokenAttribution {
            system: TokenCount { n: 1200, known: true },
            tools: TokenCount { n: 0, known: true },
            ..Default::default()
        };
        let rows = attribution_rows(&a).unwrap();
        assert_eq!(rows[0], ("system", "~1.2k".to_string()));
        assert_eq!(rows[1], ("tools", "~0".to_string()));
        assert_eq!(rows[2], ("messages", "—".to_string()));
        assert_eq!(rows[5], ("source", "estimated".to_string()));
    }

    #[test]
    fn effective_for_root_ignores_children_and_other_events() {
        let env = |data: &str| format!(r#"{{"type":"prompt.effective","data":{data}}}"#);
        let mine = env(r#"{"sessionId":"root1","systemChars":10,"layers":null}"#);
        let ev = effective_for_root(&mine, "root1").expect("root snapshot");
        assert_eq!(ev.system_chars, 10);
        assert!(ev.layers.is_empty());
        assert!(effective_for_root(&env(r#"{"sessionId":"child","depth":1,"parentSessionId":"root1"}"#), "root1").is_none());
        assert!(effective_for_root(&env(r#"{"depth":0}"#), "").is_some());
        assert!(effective_for_root(r#"{"type":"turn.started","data":{}}"#, "root1").is_none());
    }

    #[test]
    fn inspect_errors_are_honest() {
        assert!(inspect_error_text("HTTP 503 live session unavailable\n").starts_with("No live session"));
        assert_eq!(
            inspect_error_text(r#"HTTP 403 {"error":"ops are read-only"}"#),
            "Inspect is unavailable: ops are read-only"
        );
        assert_eq!(inspect_error_text("network down"), "Inspect is unavailable: network down");
    }

    #[test]
    fn scope_and_sanitize() {
        let mut ev = fixture();
        assert_eq!(scope_label(&ev), "last request");
        ev.from_last_stream = false;
        assert_eq!(scope_label(&ev), "current composition");
        assert_eq!(sanitize("a\u{1b}[31mb\n"), "a [31mb ");
        assert_eq!(clip("abcdef", 3), "abc…");
    }
}
