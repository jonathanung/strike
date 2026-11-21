//! LSP diagnostics in the cockpit (#1404).
//!
//! The daemon already serves what its language servers report at
//! `GET /v1/diagnostics` (path, 1-based line/character, severity, source,
//! code, message, count, and an honest `note` for the empty states). This
//! module renders it: a side-panel view grouped by file, where a click
//! opens the document viewer at the entry's line through the page's
//! `open_file` landing (the same `jump_line` path file chips use), and an
//! error-count badge for the file open in the editor.
//!
//! Empty and unavailable states show the daemon's own text verbatim: the
//! payload `note` ("no language servers configured …", "no live language
//! servers …", "no diagnostics") and, when the host has no LSP service,
//! the 501 body's `error` ("lsp capability unavailable on this host").
//! No LSP retry/disable controls in this slice.

use leptos::prelude::*;
use serde::Deserialize;

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct DiagItem {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub line: u32,
    #[serde(default)]
    pub character: u32,
    #[serde(default)]
    pub severity: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub code: String,
    #[serde(default)]
    pub message: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq, Eq)]
pub struct DiagResponse {
    #[serde(default)]
    pub diagnostics: Vec<DiagItem>,
    #[serde(default)]
    pub count: u32,
    #[serde(default)]
    pub note: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DiagState {
    Idle,
    Loading,
    Ready(DiagResponse),
    /// The daemon's error text (verbatim when it sent one).
    Failed(String),
}

pub const PATH: &str = "/v1/diagnostics";

/// Parse a `get_text` result into the view state. A non-2xx error is
/// `HTTP <status> <body>`; when the body is the daemon's `{"error": …}`
/// JSON (e.g. 501 "lsp capability unavailable on this host") that text is
/// shown as-is.
pub fn state_from(result: Result<String, String>) -> DiagState {
    match result {
        Ok(text) => match serde_json::from_str::<DiagResponse>(&text) {
            Ok(resp) => DiagState::Ready(resp),
            Err(e) => DiagState::Failed(format!("diagnostics response unreadable: {e}")),
        },
        Err(e) => DiagState::Failed(daemon_error(&e)),
    }
}

fn daemon_error(err: &str) -> String {
    if let Some(start) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[start..]) {
            if let Some(msg) = v.get("error").and_then(|m| m.as_str()) {
                if !msg.trim().is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

/// Entries grouped by file, files in daemon order (sorted by path there).
pub fn group_by_file(items: &[DiagItem]) -> Vec<(String, Vec<DiagItem>)> {
    let mut out: Vec<(String, Vec<DiagItem>)> = Vec::new();
    for d in items {
        match out.iter_mut().find(|(p, _)| p == &d.path) {
            Some((_, list)) => list.push(d.clone()),
            None => out.push((d.path.clone(), vec![d.clone()])),
        }
    }
    out
}

/// Known severities keep their own class; anything else renders neutral.
pub fn severity_class(sev: &str) -> &'static str {
    match sev.trim().to_ascii_lowercase().as_str() {
        "error" => "diag-sev diag-error",
        "warning" => "diag-sev diag-warning",
        "info" | "information" => "diag-sev diag-info",
        "hint" => "diag-sev diag-hint",
        _ => "diag-sev",
    }
}

fn norm(p: &str) -> String {
    p.trim().replace('\\', "/").trim_start_matches("./").to_string()
}

/// Whether a diagnostic path names the document open in the editor. The
/// daemon may report absolute paths while the viewer holds a
/// workspace-relative one (or the reverse), so a path-boundary suffix
/// match counts.
pub fn same_file(diag_path: &str, doc_path: &str) -> bool {
    let (d, o) = (norm(diag_path), norm(doc_path));
    if d.is_empty() || o.is_empty() {
        return false;
    }
    d == o || d.ends_with(&format!("/{o}")) || o.ends_with(&format!("/{d}"))
}

/// Error-severity entries for the open document (the badge counts errors
/// only; the pane lists every severity).
pub fn errors_for(resp: &DiagResponse, doc_path: &str) -> usize {
    resp.diagnostics
        .iter()
        .filter(|d| d.severity.eq_ignore_ascii_case("error") && same_file(&d.path, doc_path))
        .count()
}

pub fn badge_label(errors: usize) -> String {
    if errors == 1 {
        "1 error".into()
    } else {
        format!("{errors} errors")
    }
}

/// `source code` suffix for one entry, e.g. "gopls UnusedVar".
pub fn origin(d: &DiagItem) -> String {
    [d.source.trim(), d.code.trim()]
        .into_iter()
        .filter(|s| !s.is_empty())
        .collect::<Vec<_>>()
        .join(" ")
}

/// Side-panel diagnostics view.
#[component]
pub fn DiagnosticsPane(
    state: RwSignal<DiagState>,
    on_refresh: Callback<()>,
    on_open: Callback<(String, u32)>,
) -> impl IntoView {
    view! {
        <div class="diag" data-diagnostics-pane="">
            <div class="files-head">
                <p class="hint">"Language server diagnostics"</p>
                <button class="secondary" data-diag-refresh="" on:click=move |_| on_refresh.run(())>"Refresh"</button>
            </div>
            {move || match state.get() {
                DiagState::Idle | DiagState::Loading => view! { <p class="hint" role="status">"Loading…"</p> }.into_any(),
                DiagState::Failed(e) => view! {
                    <div class="notice" role="alert" data-diag-error="">
                        <span>{e}</span>
                        <button class="secondary" on:click=move |_| on_refresh.run(())>"Retry"</button>
                    </div>
                }.into_any(),
                DiagState::Ready(resp) => {
                    let note = resp.note.clone();
                    let groups = group_by_file(&resp.diagnostics);
                    view! {
                        {(!note.is_empty()).then(|| view! { <p class="hint" data-diag-note="">{note.clone()}</p> })}
                        <div class="diag-results">
                            {groups.into_iter().map(|(path, items)| {
                                let count = items.len();
                                view! {
                                    <section class="diag-file" data-diag-file=path.clone()>
                                        <h4 class="diag-path"><span>{path.clone()}</span><span class="diag-count">{count}</span></h4>
                                        <ul class="plain">
                                            {items.into_iter().map(|d| {
                                                let (open_path, open_line) = (d.path.clone(), d.line);
                                                let at = format!("{}:{}", d.path, d.line);
                                                let label = format!("Open {at}");
                                                let origin = origin(&d);
                                                view! {
                                                    <li>
                                                        <button class="diag-entry" data-diag-path=d.path.clone() data-diag-line=d.line.to_string() title=label.clone() aria-label=label
                                                            on:click=move |_| on_open.run((open_path.clone(), open_line))>
                                                            <span class=severity_class(&d.severity)>{d.severity.clone()}</span>
                                                            <span class="diag-msg">{d.message.clone()}</span>
                                                            <span class="diag-meta">{if origin.is_empty() { at.clone() } else { format!("{origin} · {at}") }}</span>
                                                        </button>
                                                    </li>
                                                }
                                            }).collect_view()}
                                        </ul>
                                    </section>
                                }
                            }).collect_view()}
                        </div>
                    }.into_any()
                }
            }}
        </div>
    }
}

/// Error-count badge for the document open in the editor. Renders nothing
/// when the file has no error-severity entries.
#[component]
pub fn DocBadge(
    state: RwSignal<DiagState>,
    doc_path: RwSignal<String>,
    on_click: Callback<()>,
) -> impl IntoView {
    let errors = Memo::new(move |_| {
        let path = doc_path.get();
        state.with(|s| match s {
            DiagState::Ready(resp) => errors_for(resp, &path),
            _ => 0,
        })
    });
    view! {
        <Show when=move || { errors.get() > 0 }>
            <div class="diag-badge-row">
                <button class="diag-badge" data-diag-badge="" title="Show diagnostics" on:click=move |_| on_click.run(())>
                    {move || badge_label(errors.get())}
                </button>
            </div>
        </Show>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn d(path: &str, line: u32, sev: &str) -> DiagItem {
        DiagItem {
            path: path.into(),
            line,
            character: 1,
            severity: sev.into(),
            source: String::new(),
            code: String::new(),
            message: "m".into(),
        }
    }

    #[test]
    fn state_from_ready_and_daemon_errors() {
        let ok = state_from(Ok(r#"{"diagnostics":[{"path":"a.go","line":3,"character":2,"severity":"error","message":"x"}],"count":1}"#.into()));
        match ok {
            DiagState::Ready(r) => {
                assert_eq!(r.count, 1);
                assert_eq!(r.diagnostics[0].line, 3);
            }
            other => panic!("{other:?}"),
        }
        let note = state_from(Ok(r#"{"diagnostics":[],"count":0,"note":"no language servers configured (add lsp.servers in config)"}"#.into()));
        assert_eq!(
            note,
            DiagState::Ready(DiagResponse {
                diagnostics: vec![],
                count: 0,
                note: "no language servers configured (add lsp.servers in config)".into(),
            })
        );
        let cases = [
            (
                r#"HTTP 501 {"error":"lsp capability unavailable on this host"}"#,
                "lsp capability unavailable on this host",
            ),
            ("HTTP 500", "HTTP 500"),
            ("HTTP 502 {not json", "HTTP 502 {not json"),
        ];
        for (err, want) in cases {
            assert_eq!(state_from(Err(err.into())), DiagState::Failed(want.into()));
        }
    }

    #[test]
    fn group_by_file_keeps_order() {
        let groups = group_by_file(&[d("a", 1, "error"), d("b", 2, "warning"), d("a", 9, "hint")]);
        let shape: Vec<(String, usize)> = groups.iter().map(|(p, l)| (p.clone(), l.len())).collect();
        assert_eq!(shape, vec![("a".into(), 2), ("b".into(), 1)]);
    }

    #[test]
    fn same_file_matches_on_path_boundaries() {
        let cases = [
            ("/repo/src/a.go", "src/a.go", true),
            ("src/a.go", "/repo/src/a.go", true),
            ("./src/a.go", "src/a.go", true),
            ("/repo/src/a.go", "/repo/src/a.go", true),
            ("/repo/src/xa.go", "a.go", false),
            ("/repo/src/a.go", "b.go", false),
            ("", "a.go", false),
            ("a.go", "", false),
        ];
        for (diag, doc, want) in cases {
            assert_eq!(same_file(diag, doc), want, "{diag} vs {doc}");
        }
    }

    #[test]
    fn errors_for_counts_errors_in_open_file_only() {
        let resp = DiagResponse {
            diagnostics: vec![
                d("/r/a.go", 1, "error"),
                d("/r/a.go", 2, "ERROR"),
                d("/r/a.go", 3, "warning"),
                d("/r/b.go", 1, "error"),
            ],
            count: 4,
            note: String::new(),
        };
        assert_eq!(errors_for(&resp, "a.go"), 2);
        assert_eq!(errors_for(&resp, "c.go"), 0);
        assert_eq!(badge_label(1), "1 error");
        assert_eq!(badge_label(3), "3 errors");
    }

    #[test]
    fn severity_class_and_origin() {
        assert_eq!(severity_class("Error"), "diag-sev diag-error");
        assert_eq!(severity_class("information"), "diag-sev diag-info");
        assert_eq!(severity_class("odd"), "diag-sev");
        let mut item = d("a", 1, "error");
        assert_eq!(origin(&item), "");
        item.source = "gopls".into();
        item.code = "UnusedVar".into();
        assert_eq!(origin(&item), "gopls UnusedVar");
    }
}
