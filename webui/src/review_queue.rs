//! Review queue (#1324): per-hunk accept, reject and comment on the live
//! session's pending working-tree changes, sent back as one review turn.
//!
//! The daemon owns every rule (`internal/frontend/server/review.go`):
//! hunks come from the same `git diff` as the Git panel, reject reverses
//! exactly one hunk with `git apply -R`, comments and rejections are stored
//! with the session until "Send review" delivers them as one user turn that
//! opens with [`REVIEW_TURN_HEADER`]. This panel only renders that state
//! and posts the operator's choices. Read-only attach shows the queue
//! disabled with the daemon's reason; reject and send wait for an idle
//! session.

use leptos::prelude::*;
use leptos::task::spawn_local;
use serde::Deserialize;

use load_guard::LoadEpoch;

use crate::api::{delete_json, get_json, post_json_value};
use crate::load_epoch::begin;
use crate::session_delete::encode_segment;

/// First line of every review turn. Keep in sync with
/// `ReviewTurnHeader` in `internal/frontend/server/review.go`.
pub const REVIEW_TURN_HEADER: &str = "Review of your pending changes (strike review queue).";

/// Whether a user message is a review turn sent from this queue, so the
/// transcript labels it as a review instead of a typed message.
pub fn is_review_turn(text: &str) -> bool {
    text.trim_start().starts_with(REVIEW_TURN_HEADER)
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ReviewHunk {
    pub id: String,
    #[serde(default)]
    pub header: String,
    #[serde(default)]
    pub new_start: u32,
    #[serde(default)]
    pub new_lines: u32,
    #[serde(default)]
    pub lines: Vec<String>,
    #[serde(default)]
    pub state: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ReviewFile {
    pub path: String,
    #[serde(default)]
    pub note: String,
    #[serde(default)]
    pub hunks: Vec<ReviewHunk>,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ReviewComment {
    pub id: String,
    pub path: String,
    #[serde(default)]
    pub hunk_id: String,
    pub start_line: u32,
    pub end_line: u32,
    pub body: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ReviewRejected {
    pub id: String,
    pub path: String,
    #[serde(default)]
    pub header: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ReviewQueue {
    #[serde(default)]
    pub read_only: bool,
    #[serde(default)]
    pub busy: bool,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default)]
    pub files: Vec<ReviewFile>,
    #[serde(default)]
    pub comments: Vec<ReviewComment>,
    #[serde(default)]
    pub rejected: Vec<ReviewRejected>,
}

/// Row class for one hunk line, matching the Git panel's diff classes.
pub fn line_class(line: &str) -> &'static str {
    match line.as_bytes().first() {
        Some(b'+') => "add",
        Some(b'-') => "del",
        Some(b'\\') => "meta",
        _ => "ctx",
    }
}

/// The new-file line range a comment on this hunk anchors to by default.
/// A pure deletion (no new lines) anchors to the line it sits after.
pub fn default_range(h: &ReviewHunk) -> (u32, u32) {
    let start = h.new_start.max(1);
    if h.new_lines == 0 {
        return (start, start);
    }
    (start, start + h.new_lines - 1)
}

/// Parse the composer's line range. Mirrors the daemon's validation so
/// the error shows before any request.
pub fn parse_range(start: &str, end: &str) -> Result<(u32, u32), &'static str> {
    let s: u32 = start.trim().parse().map_err(|_| "Start line must be a number.")?;
    let e: u32 = end.trim().parse().map_err(|_| "End line must be a number.")?;
    if s == 0 {
        return Err("Lines start at 1.");
    }
    if e < s {
        return Err("End line is before the start line.");
    }
    Ok((s, e))
}

/// Why "Send review" is unavailable, or `None` when it may run.
pub fn send_block(q: &ReviewQueue) -> Option<String> {
    if q.read_only {
        return Some(if q.reason.is_empty() {
            "This session is read-only.".to_string()
        } else {
            q.reason.clone()
        });
    }
    if q.busy {
        return Some("The session is mid-turn. Send the review once it is idle.".to_string());
    }
    if q.comments.is_empty() && q.rejected.is_empty() {
        return Some("Nothing to send yet: comment on a line or reject a hunk.".to_string());
    }
    None
}

/// "2 comments · 1 rejected hunk" for the send bar.
pub fn pending_summary(comments: usize, rejected: usize) -> String {
    let plural = |n: usize, one: &str, many: &str| {
        if n == 1 {
            format!("1 {one}")
        } else {
            format!("{n} {many}")
        }
    };
    format!(
        "{} · {}",
        plural(comments, "comment", "comments"),
        plural(rejected, "rejected hunk", "rejected hunks")
    )
}

/// Append the session scope (`root=`) the daemon uses for every route.
pub fn with_root(path: &str, root: &str) -> String {
    if root.is_empty() {
        return path.to_string();
    }
    let sep = if path.contains('?') { '&' } else { '?' };
    format!("{path}{sep}root={}", encode_segment(root))
}

/// The daemon's `error` field out of `HTTP 409 {"error":"…"}`; other
/// strings pass through unchanged.
pub fn http_message(err: &str) -> String {
    if let Some(idx) = err.find('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&err[idx..]) {
            if let Some(msg) = v.get("error").and_then(|e| e.as_str()) {
                if !msg.is_empty() {
                    return msg.to_string();
                }
            }
        }
    }
    err.to_string()
}

#[component]
pub fn ReviewPanel(
    base: RwSignal<String>,
    token: RwSignal<String>,
    root: RwSignal<String>,
    busy: Memo<bool>,
    active: Memo<bool>,
) -> impl IntoView {
    let queue = RwSignal::new(None::<ReviewQueue>);
    let error = RwSignal::new(String::new());
    let note = RwSignal::new(String::new());
    let working = RwSignal::new(false);
    // Hunk id whose Reject button is waiting for its confirming second tap.
    let confirm_reject = RwSignal::new(String::new());
    // Hunk id whose comment composer is open, with its file path.
    let composing = RwSignal::new(None::<(String, String)>);
    let draft = RwSignal::new(String::new());
    let start_s = RwSignal::new(String::new());
    let end_s = RwSignal::new(String::new());
    let epoch = RwSignal::new(LoadEpoch::new());

    let load = move || {
        let gen = begin(epoch);
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        spawn_local(async move {
            let res = get_json::<ReviewQueue>(&b, &t, &with_root("/v1/review", &r)).await;
            if !epoch.with_untracked(|e| e.accept(gen)) {
                return;
            }
            match res {
                Ok(q) => {
                    error.set(String::new());
                    queue.set(Some(q));
                }
                Err(e) => error.set(format!("Review queue: {}", http_message(&e))),
            }
        });
    };

    // Post one mutation, then reload so the panel shows the daemon's truth.
    let mutate = move |path: String, body: serde_json::Value, done: &'static str| {
        if working.get_untracked() {
            return;
        }
        working.set(true);
        note.set(String::new());
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        spawn_local(async move {
            let res = post_json_value(&b, &t, &with_root(&path, &r), body).await;
            working.set(false);
            match res {
                Ok(_) => note.set(done.to_string()),
                Err(e) => note.set(http_message(&e)),
            }
            load();
        });
    };

    Effect::new(move |_| {
        let on = active.get();
        let _ = (base.get(), token.get(), root.get(), busy.get());
        if on {
            load();
        }
    });
    Effect::new(move |_| {
        let _ = root.get();
        composing.set(None);
        confirm_reject.set(String::new());
        note.set(String::new());
    });

    let send = move || {
        if working.get_untracked() {
            return;
        }
        working.set(true);
        note.set(String::new());
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        spawn_local(async move {
            let res = post_json_value(&b, &t, &with_root("/v1/review/send", &r), serde_json::json!({})).await;
            working.set(false);
            match res {
                Ok(v) => {
                    let n = |k: &str| v.pointer(&format!("/sent/{k}")).and_then(|x| x.as_u64()).unwrap_or(0) as usize;
                    note.set(format!("Review sent: {}.", pending_summary(n("comments"), n("rejected"))));
                }
                // Nothing is cleared on failure: the queue still holds it all.
                Err(e) => note.set(format!("Send failed, review kept: {}", http_message(&e))),
            }
            load();
        });
    };

    let delete_comment = move |id: String| {
        if working.get_untracked() {
            return;
        }
        working.set(true);
        let (b, t, r) = (base.get_untracked(), token.get_untracked(), root.get_untracked());
        spawn_local(async move {
            let path = with_root(&format!("/v1/review/comments/{}", encode_segment(&id)), &r);
            let res = delete_json(&b, &t, &path, None).await;
            working.set(false);
            if let Err(e) = res {
                note.set(http_message(&e));
            }
            load();
        });
    };

    let open_composer = move |path: String, h: &ReviewHunk| {
        let (s, e) = default_range(h);
        start_s.set(s.to_string());
        end_s.set(e.to_string());
        draft.set(String::new());
        composing.set(Some((path, h.id.clone())));
    };

    let save_comment = move || {
        let Some((path, hunk)) = composing.get_untracked() else {
            return;
        };
        let body = draft.get_untracked();
        if body.trim().is_empty() {
            note.set("Write a comment first.".into());
            return;
        }
        let (s, e) = match parse_range(&start_s.get_untracked(), &end_s.get_untracked()) {
            Ok(r) => r,
            Err(msg) => {
                note.set(msg.into());
                return;
            }
        };
        composing.set(None);
        mutate(
            "/v1/review/comments".into(),
            serde_json::json!({"path": path, "hunk": hunk, "startLine": s, "endLine": e, "body": body}),
            "Comment added.",
        );
    };

    let hunk_view = move |path: String, h: ReviewHunk, locked: bool, busy_now: bool, comments: Vec<ReviewComment>| {
        let id = h.id.clone();
        let accepted = h.state == "accepted";
        let lines = h
            .lines
            .iter()
            .map(|l| {
                let class = line_class(l);
                let text = l.clone();
                view! { <div class=class>{text}</div> }
            })
            .collect_view();
        let (id_a, id_r, id_c, id_open) = (id.clone(), id.clone(), id.clone(), id.clone());
        let (path_r, path_c) = (path.clone(), path.clone());
        let h_c = h.clone();
        let reject_label = {
            let id = id.clone();
            move || if confirm_reject.get() == id { "Revert this hunk?" } else { "Reject" }
        };
        let hunk_comments = comments
            .into_iter()
            .filter(|c| c.hunk_id == id)
            .map(|c| {
                let range = if c.start_line == c.end_line {
                    format!("L{}", c.start_line)
                } else {
                    format!("L{}–{}", c.start_line, c.end_line)
                };
                let cid = c.id.clone();
                view! {
                    <li class="rq-comment" data-rq-comment={c.id.clone()}>
                        <span class="rq-range">{range}</span>
                        <span class="rq-body">{c.body.clone()}</span>
                        <button type="button" class="secondary rq-delete" disabled={locked}
                            aria-label="Delete comment"
                            on:click=move |_| delete_comment(cid.clone())>"Delete"</button>
                    </li>
                }
            })
            .collect_view();
        view! {
            <section class="rq-hunk" data-rq-hunk={id.clone()} data-rq-state={h.state.clone()}>
                <header class="rq-hunk-head">
                    <code class="rq-header">{h.header.clone()}</code>
                    <span class="rq-actions">
                        <button type="button" class={if accepted { "rq-accept on" } else { "secondary rq-accept" }}
                            aria-pressed={accepted.to_string()} disabled={locked}
                            on:click=move |_| mutate(
                                "/v1/review/state".into(),
                                serde_json::json!({"hunk": id_a.clone(), "state": if accepted { "" } else { "accepted" }}),
                                if accepted { "Accept cleared." } else { "Hunk accepted." },
                            )>{if accepted { "Accepted" } else { "Accept" }}</button>
                        <button type="button" class="secondary rq-reject" disabled={locked || busy_now}
                            title={if busy_now { "Wait for the turn to finish" } else { "Revert just this hunk in the working tree" }}
                            on:click=move |_| {
                                if confirm_reject.get_untracked() != id_r {
                                    confirm_reject.set(id_r.clone());
                                    return;
                                }
                                confirm_reject.set(String::new());
                                mutate(
                                    "/v1/review/reject".into(),
                                    serde_json::json!({"path": path_r.clone(), "hunk": id_r.clone()}),
                                    "Hunk reverted. It goes to the agent with the review.",
                                );
                            }>{reject_label}</button>
                        <button type="button" class="secondary rq-comment-open" disabled={locked}
                            on:click=move |_| open_composer(path_c.clone(), &h_c)>"Comment"</button>
                    </span>
                </header>
                <div class="diff rq-lines">{lines}</div>
                <ul class="rq-comments">{hunk_comments}</ul>
                <Show when=move || composing.get().map(|(_, h)| h == id_c).unwrap_or(false)>
                    <div class="rq-composer" data-rq-composer={id_open.clone()}>
                        <label class="rq-range-field">"Lines"
                            <input type="number" min="1" inputmode="numeric" aria-label="Start line"
                                prop:value=move || start_s.get()
                                on:input=move |ev| start_s.set(event_target_value(&ev)) />
                            "–"
                            <input type="number" min="1" inputmode="numeric" aria-label="End line"
                                prop:value=move || end_s.get()
                                on:input=move |ev| end_s.set(event_target_value(&ev)) />
                        </label>
                        <textarea class="git-field rq-draft" aria-label="Review comment" placeholder="What should change here?"
                            prop:value=move || draft.get()
                            on:input=move |ev| draft.set(event_target_value(&ev))
                            on:keydown=move |ev: web_sys::KeyboardEvent| {
                                if ev.key() == "Enter" && (ev.ctrl_key() || ev.meta_key()) {
                                    ev.prevent_default();
                                    save_comment();
                                } else if ev.key() == "Escape" {
                                    composing.set(None);
                                }
                            }></textarea>
                        <span class="rq-actions">
                            <button type="button" class="rq-save" on:click=move |_| save_comment()>"Add comment"</button>
                            <button type="button" class="secondary" on:click=move |_| composing.set(None)>"Cancel"</button>
                        </span>
                    </div>
                </Show>
            </section>
        }
    };

    view! {
        <div class="review-queue" id="review-panel" data-review-queue="true">
            <header class="rq-head">
                <span class="hint">"Review queue"</span>
                <button type="button" class="secondary rq-refresh" on:click=move |_| load()>"Refresh"</button>
            </header>
            <Show when=move || !error.get().is_empty()>
                <p class="hint rq-error" role="alert">{move || error.get()}</p>
            </Show>
            {move || {
                let Some(q) = queue.get() else {
                    return view! { <p class="hint">"Loading pending changes…"</p> }.into_any();
                };
                let locked = q.read_only || working.get();
                let busy_now = q.busy;
                let block = send_block(&q);
                let summary = pending_summary(q.comments.len(), q.rejected.len());
                let banner = if q.read_only {
                    Some(if q.reason.is_empty() { "This session is read-only.".to_string() } else { q.reason.clone() })
                } else if q.busy {
                    Some("The session is mid-turn: accept and comment now; reject and send once it is idle.".to_string())
                } else {
                    None
                };
                let rejected = q.rejected.clone();
                let comments = q.comments.clone();
                let files = q.files.clone();
                let empty = files.is_empty();
                let truncated = q.truncated;
                let send_disabled = block.is_some() || working.get();
                let send_title = block.clone().unwrap_or_default();
                let send_why = if q.read_only { None } else { block };
                view! {
                    <div class="rq-body-wrap">
                    {banner.map(|b| view! { <p class="hint rq-banner" data-rq-disabled="true">{b}</p> })}
                    <div class="rq-send">
                        <span class="rq-summary" data-rq-summary="true">{summary}</span>
                        <button type="button" class="rq-send-btn" disabled={send_disabled}
                            title={send_title}
                            on:click=move |_| send()>"Send review"</button>
                    </div>
                    {send_why.map(|b| view! { <p class="hint rq-send-why">{b}</p> })}
                    <Show when=move || !note.get().is_empty()>
                        <p class="hint rq-note" role="status">{move || note.get()}</p>
                    </Show>
                    {(!rejected.is_empty()).then(|| view! {
                        <div class="rq-rejected">
                            <span class="hint">"Rejected and reverted (sent with the review)"</span>
                            <ul>{rejected.into_iter().map(|r| view! {
                                <li data-rq-rejected={r.id.clone()}><span class="name">{r.path.clone()}</span>" "<code>{r.header.clone()}</code></li>
                            }).collect_view()}</ul>
                        </div>
                    })}
                    {truncated.then(|| view! { <p class="hint">"The diff is large; the last file was left out. Commit or stash some changes to review the rest."</p> })}
                    {empty.then(|| view! { <p class="hint rq-empty">"No pending changes in the working tree."</p> })}
                    {files.into_iter().map(|f| {
                        let path = f.path.clone();
                        let file_comments: Vec<ReviewComment> = comments.iter().filter(|c| c.path == path).cloned().collect();
                        // Comments whose hunk is gone (fixed, or reverted)
                        // still go with the review; list them under the file.
                        let live_ids: Vec<String> = f.hunks.iter().map(|h| h.id.clone()).collect();
                        let loose: Vec<ReviewComment> = file_comments.iter().filter(|c| !live_ids.contains(&c.hunk_id)).cloned().collect();
                        view! {
                            <article class="rq-file" data-rq-file={path.clone()}>
                                <h4 class="name">{path.clone()}</h4>
                                {(!f.note.is_empty()).then(|| view! { <p class="hint">{f.note.clone()}</p> })}
                                {f.hunks.into_iter().map(|h| hunk_view(path.clone(), h, locked, busy_now, file_comments.clone())).collect_view()}
                                {(!loose.is_empty()).then(|| view! {
                                    <ul class="rq-comments rq-loose">{loose.into_iter().map(|c| {
                                        let cid = c.id.clone();
                                        view! {
                                            <li class="rq-comment" data-rq-comment={c.id.clone()}>
                                                <span class="rq-range">{format!("L{}–{}", c.start_line, c.end_line)}</span>
                                                <span class="rq-body">{c.body.clone()}</span>
                                                <button type="button" class="secondary rq-delete" disabled={locked}
                                                    on:click=move |_| delete_comment(cid.clone())>"Delete"</button>
                                            </li>
                                        }
                                    }).collect_view()}</ul>
                                })}
                            </article>
                        }
                    }).collect_view()}
                    </div>
                }.into_any()
            }}
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn hunk(new_start: u32, new_lines: u32) -> ReviewHunk {
        ReviewHunk { new_start, new_lines, ..ReviewHunk::default() }
    }

    #[test]
    fn review_turns_are_recognised_by_their_header() {
        assert!(is_review_turn(&format!("{REVIEW_TURN_HEADER}\n### a.go")));
        assert!(!is_review_turn("please review my pending changes"));
        assert!(!is_review_turn(""));
    }

    #[test]
    fn line_classes_follow_the_diff_marker() {
        for (line, want) in [("+x", "add"), ("-x", "del"), (" x", "ctx"), ("", "ctx"), ("\\ No newline at end of file", "meta")] {
            assert_eq!(line_class(line), want, "{line:?}");
        }
    }

    #[test]
    fn default_range_covers_the_new_side() {
        assert_eq!(default_range(&hunk(10, 4)), (10, 13));
        assert_eq!(default_range(&hunk(7, 1)), (7, 7));
        assert_eq!(default_range(&hunk(5, 0)), (5, 5));
        assert_eq!(default_range(&hunk(0, 0)), (1, 1));
    }

    #[test]
    fn parse_range_matches_daemon_rules() {
        assert_eq!(parse_range("3", " 5 "), Ok((3, 5)));
        assert_eq!(parse_range("4", "4"), Ok((4, 4)));
        assert!(parse_range("0", "1").is_err());
        assert!(parse_range("5", "4").is_err());
        assert!(parse_range("x", "4").is_err());
        assert!(parse_range("1", "").is_err());
    }

    #[test]
    fn send_block_explains_every_refusal() {
        let mut q = ReviewQueue { read_only: true, reason: "attach only".into(), ..ReviewQueue::default() };
        assert_eq!(send_block(&q).as_deref(), Some("attach only"));
        q.read_only = false;
        q.busy = true;
        assert!(send_block(&q).unwrap().contains("mid-turn"));
        q.busy = false;
        assert!(send_block(&q).unwrap().contains("Nothing to send"));
        q.rejected.push(ReviewRejected::default());
        assert_eq!(send_block(&q), None);
    }

    #[test]
    fn summary_and_scope_helpers() {
        assert_eq!(pending_summary(1, 0), "1 comment · 0 rejected hunks");
        assert_eq!(pending_summary(2, 1), "2 comments · 1 rejected hunk");
        assert_eq!(with_root("/v1/review", ""), "/v1/review");
        assert_eq!(with_root("/v1/review", "a/b"), "/v1/review?root=a%2Fb");
        assert_eq!(with_root("/v1/review?x=1", "s1"), "/v1/review?x=1&root=s1");
        assert_eq!(http_message(r#"HTTP 409 {"error":"busy"}"#), "busy");
        assert_eq!(http_message("offline"), "offline");
    }

    #[test]
    fn queue_decodes_the_daemon_payload() {
        let raw = r#"{"sessionId":"s","readOnly":false,"busy":true,"truncated":false,
            "files":[{"path":"a.go","hunks":[{"id":"h1","header":"@@ -1 +1 @@","oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-a","+b"],"state":"accepted"}]}],
            "comments":[{"id":"c1","path":"a.go","hunkId":"h1","startLine":1,"endLine":1,"body":"x","createdAt":"t"}],
            "rejected":[{"id":"h2","path":"a.go","header":"@@ -9 +9 @@","lines":["-q"],"at":"t"}]}"#;
        let q: ReviewQueue = serde_json::from_str(raw).unwrap();
        assert!(q.busy);
        assert_eq!(q.files[0].hunks[0].state, "accepted");
        assert_eq!(q.comments[0].hunk_id, "h1");
        assert_eq!(q.rejected[0].header, "@@ -9 +9 @@");
    }
}
