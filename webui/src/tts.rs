//! Read replies aloud (#1384). feature:tts-playback
//!
//! Local-first speech for assistant replies through the browser's own
//! `speechSynthesis`. No cloud TTS API is called. When a browser offers
//! only network voices (the vendor's service gets the text), the control
//! says so next to itself. When it offers none, the control explains that
//! instead of pretending to play.
//!
//! Playback state is one process-wide signal: starting a reply stops the
//! previous one, and switching sessions does not stop it, because the
//! speech engine is global to the page.

use std::cell::Cell;

use leptos::prelude::*;
use wasm_bindgen::prelude::*;
use wasm_bindgen::JsCast;

/// localStorage keys for the Voice settings.
pub const VOICE_KEY: &str = "strike.tts.voice";
pub const RATE_KEY: &str = "strike.tts.rate";
/// Speed choices offered in Settings, as (value, label).
pub const RATES: [(&str, &str); 5] = [
    ("0.75", "0.75\u{00d7}"),
    ("1", "1\u{00d7}"),
    ("1.25", "1.25\u{00d7}"),
    ("1.5", "1.5\u{00d7}"),
    ("2", "2\u{00d7}"),
];

pub const NO_VOICE: &str = "This browser has no speech voice, so replies cannot be read aloud here.";
pub const NETWORK_ONLY: &str = "network voice: the browser sends the text to its speech service";

/// One voice the browser offers.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Voice {
    pub uri: String,
    pub name: String,
    pub lang: String,
    pub local: bool,
    pub default: bool,
}

/// What the engine can do right now.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Engine {
    /// No speechSynthesis API at all.
    Unavailable,
    /// The API exists but lists no voices.
    NoVoices,
    /// Only remote voices: text leaves the device.
    NetworkOnly,
    /// At least one on-device voice.
    Local,
}

pub fn engine_of(api: bool, voices: &[Voice]) -> Engine {
    if !api {
        Engine::Unavailable
    } else if voices.is_empty() {
        Engine::NoVoices
    } else if voices.iter().any(|v| v.local) {
        Engine::Local
    } else {
        Engine::NetworkOnly
    }
}

/// Playback state shared by every reply's control.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Playback {
    /// Message id currently speaking (or paused).
    pub playing: Option<u32>,
    pub paused: bool,
    /// Message id whose control shows `note` (a refusal or an error).
    pub note_for: Option<u32>,
    pub note: String,
}

thread_local! {
    static STATE: ArcRwSignal<Playback> = ArcRwSignal::new(Playback::default());
    static VOICES: ArcRwSignal<Vec<Voice>> = ArcRwSignal::new(Vec::new());
    static LISTENING: Cell<bool> = const { Cell::new(false) };
    /// Bumped on every start/stop so a late `end` from a cancelled
    /// utterance cannot clear the reply that replaced it.
    static GENERATION: Cell<u64> = const { Cell::new(0) };
}

pub fn state() -> ArcRwSignal<Playback> {
    STATE.with(|s| s.clone())
}

pub fn voices() -> ArcRwSignal<Vec<Voice>> {
    VOICES.with(|v| v.clone())
}

// ---- text cleanup -------------------------------------------------------

/// Speakable rendering of a markdown reply: code fences are announced and
/// skipped, links speak their label, bare URLs and images are named,
/// tables are announced once, and markdown punctuation is dropped.
pub fn speakable_text(md: &str) -> String {
    let mut out: Vec<String> = Vec::new();
    let mut in_fence = false;
    let mut in_table = false;
    for raw in md.lines() {
        let line = raw.trim();
        if line.starts_with("```") || line.starts_with("~~~") {
            if !in_fence {
                out.push("Code block.".into());
            }
            in_fence = !in_fence;
            continue;
        }
        if in_fence {
            continue;
        }
        if line.starts_with('|') && line.ends_with('|') && line.len() > 1 {
            if !in_table {
                out.push("Table.".into());
                in_table = true;
            }
            continue;
        }
        in_table = false;
        if line.is_empty() {
            continue;
        }
        let line = line.trim_start_matches('#').trim_start();
        let line = strip_list_marker(line);
        let line = line.trim_start_matches('>').trim_start();
        let spoken = speak_inline(line);
        if !spoken.trim().is_empty() {
            out.push(spoken.trim().to_string());
        }
    }
    out.join("\n")
}

fn strip_list_marker(line: &str) -> &str {
    for m in ["- [ ] ", "- [x] ", "- ", "* ", "+ "] {
        if let Some(rest) = line.strip_prefix(m) {
            return rest;
        }
    }
    let digits = line.chars().take_while(|c| c.is_ascii_digit()).count();
    if digits > 0 {
        let rest = &line[digits..];
        if let Some(r) = rest.strip_prefix(". ").or_else(|| rest.strip_prefix(") ")) {
            return r;
        }
    }
    line
}

/// Inline pass: `![alt](src)` → alt (or "image"), `[label](url)` → label,
/// bare http(s) URLs → "link", and `*_~\`` emphasis marks removed.
fn speak_inline(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    let mut out = String::new();
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        let image = c == '!' && chars.get(i + 1) == Some(&'[');
        if c == '[' || image {
            let open = if image { i + 1 } else { i };
            if let Some((label, end)) = bracket_link(&chars, open) {
                if image {
                    out.push_str(if label.trim().is_empty() { "image" } else { label.trim() });
                } else {
                    out.push_str(&label);
                }
                i = end;
                continue;
            }
        }
        if starts_with_at(&chars, i, "http://") || starts_with_at(&chars, i, "https://") {
            let mut j = i;
            while j < chars.len() && !chars[j].is_whitespace() {
                j += 1;
            }
            out.push_str("link");
            i = j;
            continue;
        }
        if matches!(c, '*' | '_' | '`' | '~') {
            i += 1;
            continue;
        }
        out.push(c);
        i += 1;
    }
    out
}

fn starts_with_at(chars: &[char], i: usize, pat: &str) -> bool {
    let p: Vec<char> = pat.chars().collect();
    chars.len() >= i + p.len() && chars[i..i + p.len()] == p[..]
}

/// Parses `[label](target)` starting at `open` (the `[`). Returns the label
/// and the index just past `)`.
fn bracket_link(chars: &[char], open: usize) -> Option<(String, usize)> {
    let close = (open + 1..chars.len()).find(|&k| chars[k] == ']')?;
    if chars.get(close + 1) != Some(&'(') {
        return None;
    }
    let end = (close + 2..chars.len()).find(|&k| chars[k] == ')')?;
    let label: String = chars[open + 1..close].iter().collect();
    Some((speak_inline(&label), end + 1))
}

/// Parses a stored rate, clamped to the offered range.
pub fn parse_rate(raw: &str) -> f64 {
    raw.trim()
        .parse::<f64>()
        .ok()
        .filter(|r| r.is_finite())
        .map(|r| r.clamp(0.5, 2.0))
        .unwrap_or(1.0)
}

/// Settings label for a voice; network voices say so.
pub fn voice_label(v: &Voice) -> String {
    let mut s = if v.lang.is_empty() { v.name.clone() } else { format!("{} ({})", v.name, v.lang) };
    if !v.local {
        s.push_str(" \u{2014} network");
    }
    s
}

/// Voice order for Settings: on-device first, then by name.
pub fn sort_voices(list: &mut [Voice]) {
    list.sort_by(|a, b| b.local.cmp(&a.local).then_with(|| a.name.cmp(&b.name)));
}

// ---- engine (browser only) ---------------------------------------------

fn synth() -> Option<JsValue> {
    if !cfg!(target_arch = "wasm32") {
        return None;
    }
    let win = web_sys::window()?;
    let v = js_sys::Reflect::get(win.as_ref(), &JsValue::from_str("speechSynthesis")).ok()?;
    if v.is_undefined() || v.is_null() {
        None
    } else {
        Some(v)
    }
}

fn call0(target: &JsValue, name: &str) -> Result<JsValue, JsValue> {
    let f: js_sys::Function = js_sys::Reflect::get(target, &JsValue::from_str(name))?.dyn_into()?;
    f.call0(target)
}

fn get_str(v: &JsValue, key: &str) -> String {
    js_sys::Reflect::get(v, &JsValue::from_str(key))
        .ok()
        .and_then(|x| x.as_string())
        .unwrap_or_default()
}

fn get_bool(v: &JsValue, key: &str) -> bool {
    js_sys::Reflect::get(v, &JsValue::from_str(key))
        .ok()
        .and_then(|x| x.as_bool())
        .unwrap_or(false)
}

fn raw_voices(s: &JsValue) -> Vec<JsValue> {
    let Ok(list) = call0(s, "getVoices") else {
        return Vec::new();
    };
    js_sys::Array::from(&list).iter().collect()
}

/// Re-reads the browser's voice list into the shared signal and returns
/// the engine state. Also subscribes once to `voiceschanged`, since some
/// browsers fill the list late.
pub fn refresh_voices() -> Engine {
    let Some(s) = synth() else {
        voices().set(Vec::new());
        return Engine::Unavailable;
    };
    if !LISTENING.with(|l| l.replace(true)) {
        let cb = Closure::wrap(Box::new(move |_ev: JsValue| {
            let _ = refresh_voices();
        }) as Box<dyn FnMut(JsValue)>);
        let _ = js_sys::Reflect::set(&s, &JsValue::from_str("onvoiceschanged"), cb.as_ref());
        cb.forget();
    }
    let mut list: Vec<Voice> = raw_voices(&s)
        .iter()
        .map(|v| Voice {
            uri: get_str(v, "voiceURI"),
            name: get_str(v, "name"),
            lang: get_str(v, "lang"),
            local: get_bool(v, "localService"),
            default: get_bool(v, "default"),
        })
        .collect();
    sort_voices(&mut list);
    let engine = engine_of(true, &list);
    voices().set(list);
    engine
}

fn stored(key: &str) -> String {
    web_sys::window()
        .and_then(|w| w.local_storage().ok().flatten())
        .and_then(|s| s.get_item(key).ok().flatten())
        .unwrap_or_default()
}

pub fn store(key: &str, value: &str) {
    if let Some(s) = web_sys::window().and_then(|w| w.local_storage().ok().flatten()) {
        let _ = s.set_item(key, value);
    }
}

pub fn stored_voice() -> String {
    stored(VOICE_KEY)
}

pub fn stored_rate() -> String {
    let r = stored(RATE_KEY);
    if r.is_empty() {
        "1".into()
    } else {
        r
    }
}

fn note(id: u32, msg: &str) {
    state().update(|st| {
        st.note_for = Some(id);
        st.note = msg.to_string();
    });
}

/// Starts reading `markdown` for message `id`, stopping whatever was
/// playing. Returns false (and leaves a note on the control) when there
/// is no usable voice.
pub fn play(id: u32, markdown: &str) -> bool {
    let engine = refresh_voices();
    let Some(s) = synth() else {
        note(id, NO_VOICE);
        return false;
    };
    if matches!(engine, Engine::Unavailable | Engine::NoVoices) {
        note(id, NO_VOICE);
        return false;
    }
    let text = speakable_text(markdown);
    if text.trim().is_empty() {
        note(id, "Nothing to read in this reply.");
        return false;
    }
    let _ = call0(&s, "cancel");
    let generation = GENERATION.with(|g| {
        g.set(g.get() + 1);
        g.get()
    });
    let Some(win) = web_sys::window() else {
        return false;
    };
    let Ok(ctor) = js_sys::Reflect::get(win.as_ref(), &JsValue::from_str("SpeechSynthesisUtterance"))
        .and_then(|c| c.dyn_into::<js_sys::Function>().map_err(JsValue::from))
    else {
        note(id, NO_VOICE);
        return false;
    };
    let args = js_sys::Array::of1(&JsValue::from_str(&text));
    let Ok(utt) = js_sys::Reflect::construct(&ctor, &args) else {
        note(id, NO_VOICE);
        return false;
    };
    let _ = js_sys::Reflect::set(&utt, &JsValue::from_str("rate"), &JsValue::from_f64(parse_rate(&stored_rate())));
    let want = stored_voice();
    if !want.is_empty() {
        if let Some(v) = raw_voices(&s).into_iter().find(|v| get_str(v, "voiceURI") == want) {
            let _ = js_sys::Reflect::set(&utt, &JsValue::from_str("voice"), &v);
        }
    }
    let finish = move |msg: String| {
        if GENERATION.with(|g| g.get()) != generation {
            return;
        }
        state().update(|st| {
            if st.playing == Some(id) {
                st.playing = None;
                st.paused = false;
            }
            if !msg.is_empty() {
                st.note_for = Some(id);
                st.note = msg;
            }
        });
    };
    let on_end = Closure::wrap(Box::new(move |_ev: JsValue| finish(String::new())) as Box<dyn FnMut(JsValue)>);
    let _ = js_sys::Reflect::set(&utt, &JsValue::from_str("onend"), on_end.as_ref());
    on_end.forget();
    let on_error = Closure::wrap(Box::new(move |ev: JsValue| {
        let code = get_str(&ev, "error");
        // "interrupted"/"canceled" are our own stop or a newer reply.
        let msg = if code.is_empty() || code == "interrupted" || code == "canceled" {
            String::new()
        } else {
            format!("Playback stopped: {code}.")
        };
        finish(msg);
    }) as Box<dyn FnMut(JsValue)>);
    let _ = js_sys::Reflect::set(&utt, &JsValue::from_str("onerror"), on_error.as_ref());
    on_error.forget();
    state().set(Playback { playing: Some(id), ..Playback::default() });
    let speak: Result<js_sys::Function, JsValue> =
        js_sys::Reflect::get(&s, &JsValue::from_str("speak")).and_then(|f| f.dyn_into().map_err(JsValue::from));
    match speak.and_then(|f| f.call1(&s, &utt)) {
        Ok(_) => true,
        Err(_) => {
            state().set(Playback::default());
            note(id, NO_VOICE);
            false
        }
    }
}

/// Stops playback (any reply).
pub fn stop() {
    GENERATION.with(|g| g.set(g.get() + 1));
    if let Some(s) = synth() {
        let _ = call0(&s, "cancel");
    }
    state().update(|st| {
        st.playing = None;
        st.paused = false;
    });
}

/// Pauses or resumes the reply that is playing.
pub fn toggle_pause() {
    let Some(s) = synth() else {
        return;
    };
    let paused = state().get_untracked().paused;
    let _ = call0(&s, if paused { "resume" } else { "pause" });
    state().update(|st| st.paused = !paused);
}

/// The per-reply control: Read aloud / Stop, Pause / Resume while
/// playing, and the engine note (network voice, or why it cannot play).
#[component]
pub fn ReadAloud(id: u32, text: String) -> impl IntoView {
    let st = state();
    let vs = voices();
    let (st_a, st_b, st_c, st_d, st_e) = (st.clone(), st.clone(), st.clone(), st.clone(), st.clone());
    let playing = move || st_a.get().playing == Some(id);
    let playing_b = move || st_b.get().playing == Some(id);
    let text_click = text.clone();
    let network_only = move || vs.get().iter().all(|v| !v.local) && !vs.get().is_empty();
    view! {
        <span class="sx-tts" data-tts-playing=move || if st_c.get().playing == Some(id) { "true" } else { "false" }>
            <button type="button" class="sx-quiet" data-action="read-aloud"
                aria-pressed=move || if playing() { "true" } else { "false" }
                title="Read this reply aloud (on-device speech when the browser has it)"
                on:click=move |_| {
                    if state().get_untracked().playing == Some(id) {
                        stop();
                    } else {
                        let _ = play(id, &text_click);
                    }
                }>
                {move || if playing_b() { "Stop" } else { "Read aloud" }}
            </button>
            <Show when=move || st_d.get().playing == Some(id)>
                {
                    let st_p = state();
                    view! {
                        <button type="button" class="sx-quiet" data-action="read-aloud-pause" on:click=move |_| toggle_pause()>
                            {move || if st_p.get().paused { "Resume" } else { "Pause" }}
                        </button>
                    }
                }
            </Show>
            {move || {
                let cur = st_e.get();
                if cur.note_for == Some(id) && !cur.note.is_empty() {
                    view! { <span class="sx-tts-note" role="status" data-tts-note="true">{cur.note.clone()}</span> }.into_any()
                } else if cur.playing == Some(id) && network_only() {
                    view! { <span class="sx-tts-note" data-tts-network="true">{NETWORK_ONLY}</span> }.into_any()
                } else {
                    view! { <span></span> }.into_any()
                }
            }}
        </span>
    }
}

/// Voice settings: voice and speed, stored in this browser.
#[component]
pub fn VoiceSettings() -> impl IntoView {
    let engine = RwSignal::new(refresh_voices());
    let voice = RwSignal::new(stored_voice());
    let rate = RwSignal::new(stored_rate());
    let vs_list = voices();
    view! {
        <div class="tts-settings">
            <p class="hint" data-tts-engine=move || format!("{:?}", engine.get()).to_lowercase()>
                {move || match engine.get() {
                    Engine::Local => "Replies are read with on-device voices. Text stays on this device.",
                    Engine::NetworkOnly => "This browser only has network voices: reading a reply sends its text to the browser's speech service.",
                    Engine::NoVoices | Engine::Unavailable => NO_VOICE,
                }}
            </p>
            <label>"Voice "
                <select data-tts-voice="true"
                    prop:value=move || voice.get()
                    on:change=move |ev| {
                        let v = event_target_value(&ev);
                        store(VOICE_KEY, &v);
                        voice.set(v);
                    }>
                    <option value="">"Browser default"</option>
                    {move || vs_list.get().into_iter().map(|v| {
                        let label = voice_label(&v);
                        view! { <option value=v.uri.clone()>{label}</option> }
                    }).collect_view()}
                </select>
            </label>
            <label>"Speed "
                <select data-tts-rate="true"
                    prop:value=move || rate.get()
                    on:change=move |ev| {
                        let v = event_target_value(&ev);
                        store(RATE_KEY, &v);
                        rate.set(v);
                    }>
                    {RATES.iter().map(|(value, label)| view! { <option value=*value>{*label}</option> }).collect_view()}
                </select>
            </label>
            <button type="button" class="secondary" data-action="tts-refresh-voices" on:click=move |_| engine.set(refresh_voices())>"Check voices"</button>
        </div>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn code_fences_are_announced_and_skipped() {
        let md = "Run this:\n```sh\nrm -rf build\nmake\n```\nThen check.";
        assert_eq!(speakable_text(md), "Run this:\nCode block.\nThen check.");
    }

    #[test]
    fn links_speak_labels_and_urls_are_named() {
        assert_eq!(speakable_text("See [the docs](https://x.dev/a) now"), "See the docs now");
        assert_eq!(speakable_text("Open https://example.com/a?b=1 please"), "Open link please");
        assert_eq!(speakable_text("![diagram](a.png) and ![](b.png)"), "diagram and image");
    }

    #[test]
    fn tables_are_announced_once() {
        let md = "Results:\n| a | b |\n|---|---|\n| 1 | 2 |\nDone.";
        assert_eq!(speakable_text(md), "Results:\nTable.\nDone.");
    }

    #[test]
    fn markdown_punctuation_is_dropped() {
        let md = "# Title\n- **bold** item\n2. `code` step\n> quoted _text_";
        assert_eq!(speakable_text(md), "Title\nbold item\ncode step\nquoted text");
    }

    #[test]
    fn engine_states() {
        let local = Voice { local: true, ..Default::default() };
        let remote = Voice { local: false, ..Default::default() };
        assert_eq!(engine_of(false, &[local.clone()]), Engine::Unavailable);
        assert_eq!(engine_of(true, &[]), Engine::NoVoices);
        assert_eq!(engine_of(true, &[remote.clone()]), Engine::NetworkOnly);
        assert_eq!(engine_of(true, &[remote, local]), Engine::Local);
    }

    #[test]
    fn rates_clamp() {
        assert_eq!(parse_rate("1.5"), 1.5);
        assert_eq!(parse_rate("9"), 2.0);
        assert_eq!(parse_rate("nope"), 1.0);
        assert_eq!(parse_rate("NaN"), 1.0);
    }

    #[test]
    fn voices_sort_local_first_and_label_network() {
        let mut v = vec![
            Voice { name: "B".into(), local: false, ..Default::default() },
            Voice { name: "A".into(), local: true, lang: "en-US".into(), ..Default::default() },
        ];
        sort_voices(&mut v);
        assert_eq!(v[0].name, "A");
        assert_eq!(voice_label(&v[0]), "A (en-US)");
        assert_eq!(voice_label(&v[1]), "B \u{2014} network");
    }
}
