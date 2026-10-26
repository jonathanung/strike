//! Model chip and menu. Self-contained composer control: the approval and
//! steer chips stay in the shell. Pins live in this browser
//! (`strike.modelPins`), same idea as session pins.

use leptos::html::{Button, Input};
use leptos::prelude::*;
use wasm_bindgen::closure::Closure;
use wasm_bindgen::JsCast;

use crate::model::ModelInfo;
use crate::model_menu::{
    build_menu, chip_label, context_view, effort_section, parse_freeform, scope_label, toggle_pin,
    ContextView, MenuRow, ModelDefaults,
};

const PIN_STORAGE: &str = "strike.modelPins";

fn storage() -> Option<web_sys::Storage> {
    web_sys::window()?.local_storage().ok().flatten()
}

fn load_pins() -> Vec<String> {
    let raw = storage()
        .and_then(|s| s.get_item(PIN_STORAGE).ok().flatten())
        .unwrap_or_default();
    serde_json::from_str(&raw).unwrap_or_default()
}

fn save_pins(pins: &[String]) {
    if let Some(s) = storage() {
        if let Ok(raw) = serde_json::to_string(pins) {
            let _ = s.set_item(PIN_STORAGE, &raw);
        }
    }
}

fn place_panel(chip_ref: NodeRef<Button>, panel_pos: RwSignal<String>) {
    let Some(btn) = chip_ref.get_untracked() else {
        return;
    };
    let Some(window) = web_sys::window() else {
        return;
    };
    let el: &web_sys::Element = btn.unchecked_ref();
    let rect = el.get_bounding_client_rect();
    let vw = window
        .inner_width()
        .ok()
        .and_then(|v| v.as_f64())
        .unwrap_or(1280.0);
    let vh = window
        .inner_height()
        .ok()
        .and_then(|v| v.as_f64())
        .unwrap_or(800.0);
    let width = 360.0_f64.min(vw * 0.92);
    let mut left = rect.left();
    if left + width > vw - 8.0 {
        left = (vw - width - 8.0).max(8.0);
    }
    if left < 8.0 {
        left = 8.0;
    }
    let bottom = (vh - rect.top() + 8.0).max(8.0);
    panel_pos.set(format!(
        "left:{left:.0}px;bottom:{bottom:.0}px;width:{width:.0}px;"
    ));
}

fn focus_filter(filter_ref: NodeRef<Input>) {
    let closure = Closure::once(move || {
        if let Some(el) = filter_ref.get() {
            let _ = el.focus();
        }
    });
    if let Some(w) = web_sys::window() {
        let _ = w.set_timeout_with_callback_and_timeout_and_arguments_0(
            closure.as_ref().unchecked_ref(),
            0,
        );
    }
    closure.forget();
}

fn meter_state(view: &ContextView) -> &'static str {
    if view.held {
        "hold"
    } else if view.warn {
        "warn"
    } else if view.limit > 0 {
        "ok"
    } else {
        "unknown"
    }
}

#[component]
pub fn ModelMenu(
    open: RwSignal<bool>,
    provider: RwSignal<String>,
    model: RwSignal<String>,
    effort: RwSignal<String>,
    context_used: RwSignal<i64>,
    context_limit: RwSignal<i64>,
    context_fit: RwSignal<String>,
    catalog: RwSignal<Vec<ModelInfo>>,
    catalog_note: RwSignal<String>,
    model_note: RwSignal<String>,
    defaults: RwSignal<ModelDefaults>,
    busy: Memo<bool>,
    on_select: Callback<(String, String)>,
    on_default_model: Callback<(String, String)>,
    on_effort: Callback<String>,
    on_default_effort: Callback<String>,
) -> impl IntoView {
    let filter = RwSignal::new(String::new());
    let cursor = RwSignal::new(0usize);
    let pins = RwSignal::new(load_pins());
    let filter_ref = NodeRef::<Input>::new();
    let chip_ref = NodeRef::<Button>::new();
    let panel_pos = RwSignal::new(String::new());

    {
        let chip_ref = chip_ref;
        let panel_pos = panel_pos;
        let open = open;
        let on_resize = Closure::<dyn FnMut()>::new(move || {
            if open.get_untracked() {
                place_panel(chip_ref, panel_pos);
            }
        });
        if let Some(window) = web_sys::window() {
            let _ = window
                .add_event_listener_with_callback("resize", on_resize.as_ref().unchecked_ref());
        }
        on_resize.forget();
    }

    Effect::new(move |_| {
        if open.get() {
            place_panel(chip_ref, panel_pos);
            focus_filter(filter_ref);
        } else {
            filter.set(String::new());
            cursor.set(0);
        }
    });

    let view_now = move || {
        let models = catalog.get();
        let ctx = crate::model_menu::active_context(&models, &provider.get(), &model.get());
        context_view(
            context_used.get(),
            context_limit.get(),
            ctx,
            &context_fit.get(),
        )
    };

    let select_row = move |row: MenuRow| {
        if busy.get_untracked() {
            model_note.set(
                "This turn is still running. Session model and effort changes wait until it finishes."
                    .into(),
            );
            return;
        }
        on_select.run((row.provider, row.id));
        open.set(false);
    };

    view! {
        <span class="model-menu">
            <Show when=move || open.get()>
                <button
                    type="button"
                    class="model-menu-backdrop"
                    aria-label="Close models"
                    on:click=move |_| open.set(false)
                ></button>
                <div
                    class="model-menu-panel"
                    role="dialog"
                    aria-label="Models"
                    data-model-menu="true"
                    style=move || panel_pos.get()
                    on:click=move |ev: web_sys::MouseEvent| ev.stop_propagation()
                >
                    {move || {
                        let state = meter_state(&view_now());
                        view! {
                            <div class="model-meter" data-context-meter="true" data-state=state>
                                <Show when=move || { view_now().limit > 0 }>
                                    <div class="model-meter-track" aria-hidden="true">
                                        <span class="model-meter-fill" style:width=move || format!("{}%", view_now().percent)></span>
                                    </div>
                                </Show>
                                <p class="model-meter-summary">{move || view_now().summary}</p>
                                <Show when=move || view_now().warn>
                                    <p class="model-warn">{move || view_now().warn_message}</p>
                                </Show>
                            </div>
                        }
                    }}
                    <p class="hint" data-model-scope="true">{move || {
                        let d = defaults.get();
                        scope_label(&provider.get(), &model.get(), &d.provider, &d.model).to_string()
                    }}</p>
                    <Show when=move || busy.get()>
                        <p class="hint">"This turn is still running. Session model and effort changes wait until it finishes."</p>
                    </Show>
                    <Show when=move || !model_note.get().is_empty()>
                        <p class="hint" data-model-note="true">{move || model_note.get()}</p>
                    </Show>
                    <Show when=move || !catalog_note.get().is_empty()>
                        <p class="hint">{move || catalog_note.get()}</p>
                    </Show>
                    {move || {
                        let models = catalog.get();
                        let info = crate::model_menu::active_model(&models, &provider.get(), &model.get()).cloned();
                        let section = effort_section(info.as_ref(), &effort.get());
                        let current = effort.get();
                        let def = defaults.get().effort;
                        if !section.shown {
                            let reason = section.reason.clone();
                            view! {
                                <p class="hint" data-effort="hidden">{reason}</p>
                            }.into_any()
                        } else {
                            let reason = section.reason.clone();
                            let show_reason = !reason.is_empty();
                            let choices = section.choices.clone();
                            let def_label = if def.is_empty() {
                                String::new()
                            } else {
                                format!("Saved default: {def}")
                            };
                            view! {
                                <div class="model-effort" data-effort="shown">
                                    <div class="model-effort-head">
                                        <span class="hint">"Effort"</span>
                                        <span class="hint">{def_label}</span>
                                        <button
                                            type="button"
                                            class="secondary model-text"
                                            disabled=move || effort.get().trim().is_empty()
                                            on:click=move |_| {
                                                let level = effort.get_untracked();
                                                if !level.trim().is_empty() {
                                                    on_default_effort.run(level);
                                                }
                                            }
                                        >"Save effort as default"</button>
                                    </div>
                                    <Show when=move || show_reason>
                                        <p class="hint">{reason.clone()}</p>
                                    </Show>
                                    <div class="model-effort-row">
                                        {choices.into_iter().map(|choice| {
                                            let level = choice.level.clone();
                                            let label = choice.label.clone();
                                            let detail = choice.detail.clone();
                                            let sendable = !level.is_empty();
                                            let on = sendable && current == level;
                                            let klass = if on { "chip on" } else { "chip" };
                                            let level_attr = level.clone();
                                            view! {
                                                <button
                                                    type="button"
                                                    class=klass
                                                    title=detail
                                                    data-effort-level=level_attr
                                                    disabled=move || !sendable || busy.get()
                                                    on:click=move |_| {
                                                        if busy.get_untracked() {
                                                            model_note.set("This turn is still running. Session model and effort changes wait until it finishes.".into());
                                                            return;
                                                        }
                                                        if !level.is_empty() {
                                                            on_effort.run(level.clone());
                                                        }
                                                    }
                                                >{label}</button>
                                            }
                                        }).collect_view()}
                                    </div>
                                </div>
                            }.into_any()
                        }
                    }}
                    <label class="model-filter-label">
                        "Filter models"
                    <input
                        node_ref=filter_ref
                        data-model-filter="true"
                        aria-label="Filter models"
                        placeholder="name or provider/model"
                        autocomplete="off"
                        prop:value=move || filter.get()
                        on:input=move |ev| {
                            filter.set(event_target_value(&ev));
                            cursor.set(0);
                        }
                        on:keydown=move |ev: web_sys::KeyboardEvent| {
                            let key = ev.key();
                            let models = catalog.get_untracked();
                            let menu = build_menu(
                                &models,
                                &filter.get_untracked(),
                                &pins.get_untracked(),
                                &provider.get_untracked(),
                                &model.get_untracked(),
                            );
                            let len = menu.flat.len();
                            let idx = if len == 0 { 0 } else { cursor.get_untracked().min(len - 1) };
                            if key == "ArrowDown" {
                                ev.prevent_default();
                                if len > 0 && idx + 1 < len {
                                    cursor.set(idx + 1);
                                }
                            } else if key == "ArrowUp" {
                                ev.prevent_default();
                                if idx > 0 {
                                    cursor.set(idx - 1);
                                }
                            } else if key == "Escape" {
                                ev.prevent_default();
                                open.set(false);
                            } else if (ev.ctrl_key() || ev.meta_key()) && key.eq_ignore_ascii_case("d") {
                                ev.prevent_default();
                                if let Some(row) = menu.flat.get(idx) {
                                    on_default_model.run((row.provider.clone(), row.id.clone()));
                                }
                            } else if key == "Enter" {
                                ev.prevent_default();
                                ev.stop_propagation();
                                if len == 0 {
                                    let raw = filter.get_untracked();
                                    if raw.trim().is_empty() {
                                        return;
                                    }
                                    let (p, m) = parse_freeform(&raw, &provider.get_untracked());
                                    select_row(MenuRow {
                                        provider: p,
                                        id: m.clone(),
                                        label: m.clone(),
                                        detail: String::new(),
                                        caps: String::new(),
                                        current: false,
                                        pinned: false,
                                        key: String::new(),
                                    });
                                } else if let Some(row) = menu.flat.get(idx) {
                                    select_row(row.clone());
                                }
                            }
                        }
                    />
                    </label>
                    <div class="model-menu-list">
                        {move || {
                            let models = catalog.get();
                            let menu = build_menu(
                                &models,
                                &filter.get(),
                                &pins.get(),
                                &provider.get(),
                                &model.get(),
                            );
                            let len = menu.flat.len();
                            let idx = if len == 0 { 0 } else { cursor.get().min(len - 1) };
                            if menu.sections.is_empty() {
                                let raw = filter.get();
                                let message = if catalog.get().is_empty() && catalog_note.get().is_empty() && raw.trim().is_empty() {
                                    "Loading models.".into()
                                } else if raw.trim().is_empty() {
                                    "No models in the catalog.".into()
                                } else {
                                    format!("No models match. Enter uses \"{}\" for this session.", raw.trim())
                                };
                                return view! { <p class="hint" data-model-empty="true">{message}</p> }.into_any();
                            }
                            let mut flat_i = 0usize;
                            let mut blocks = Vec::new();
                            for section in menu.sections {
                                let title = section.title.clone();
                                blocks.push(view! { <h3 class="model-menu-section" data-model-group=title.clone()>{title.clone()}</h3> }.into_any());
                                for row in section.rows {
                                    let cursor_here = flat_i == idx;
                                    flat_i += 1;
                                    let klass = match (row.current, cursor_here) {
                                        (true, true) => "palette-row on cursor",
                                        (true, false) => "palette-row on",
                                        (false, true) => "palette-row cursor",
                                        (false, false) => "palette-row",
                                    };
                                    let pick = row.clone();
                                    let pin_key = row.key.clone();
                                    let pin_label = if row.pinned { "Unpin" } else { "Pin" };
                                    let provider_attr = row.provider.clone();
                                    let id_attr = row.id.clone();
                                    blocks.push(view! {
                                        <div class="model-menu-row">
                                            <button
                                                type="button"
                                                class=klass
                                                data-model-row=id_attr
                                                data-provider=provider_attr
                                                data-cursor=cursor_here.to_string()
                                                on:click=move |_| select_row(pick.clone())
                                            >
                                                <span class="name">{row.label.clone()}</span>
                                                <span class="path">{row.detail.clone()}</span>
                                                <span class="path" data-capability="true">{row.caps.clone()}</span>
                                            </button>
                                            <button
                                                type="button"
                                                class="model-pin"
                                                data-model-pin=pin_key.clone()
                                                on:click=move |ev: web_sys::MouseEvent| {
                                                    ev.prevent_default();
                                                    ev.stop_propagation();
                                                    let key = pin_key.clone();
                                                    pins.update(|list| toggle_pin(list, &key));
                                                    save_pins(&pins.get_untracked());
                                                }
                                            >{pin_label}</button>
                                        </div>
                                    }.into_any());
                                }
                            }
                            let _ = flat_i;
                            view! {
                                <div>
                                    {blocks}
                                    <button
                                        type="button"
                                        class="secondary model-text"
                                        on:click=move |_| {
                                            let models = catalog.get_untracked();
                                            let menu = build_menu(
                                                &models,
                                                &filter.get_untracked(),
                                                &pins.get_untracked(),
                                                &provider.get_untracked(),
                                                &model.get_untracked(),
                                            );
                                            let len = menu.flat.len();
                                            if len == 0 {
                                                return;
                                            }
                                            let idx = cursor.get_untracked().min(len - 1);
                                            if let Some(row) = menu.flat.get(idx) {
                                                on_default_model.run((row.provider.clone(), row.id.clone()));
                                            }
                                        }
                                    >"Save highlighted as default"</button>
                                </div>
                            }.into_any()
                        }}
                    </div>
                    <p class="hint keys">"Arrow keys move. Enter uses the model for this session. Ctrl+D saves the global default. Esc closes. Alt+M opens this menu."</p>
                </div>
            </Show>
            <button
                type="button"
                class=move || {
                    let scope = {
                        let d = defaults.get();
                        scope_label(&provider.get(), &model.get(), &d.provider, &d.model)
                    };
                    let live = view_now();
                    let mut class = String::from("chip");
                    if live.held {
                        class.push_str(" hold");
                    } else if live.warn {
                        class.push_str(" warn");
                    } else if scope == "This session only" {
                        class.push_str(" session");
                    }
                    class
                }
                node_ref=chip_ref
                data-model-chip="true"
                aria-haspopup="dialog"
                aria-expanded=move || if open.get() { "true" } else { "false" }
                title=move || {
                    let d = defaults.get();
                    let scope = scope_label(&provider.get(), &model.get(), &d.provider, &d.model);
                    let live = view_now();
                    format!("{scope}. {}. Alt+M.", live.summary)
                }
                on:click=move |_| open.update(|v| *v = !*v)
            >{move || chip_label(&model.get())}</button>
        </span>
    }
}
