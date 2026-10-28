//! GET /v1/local-files. The daemon reports the paths it reads. The page does
//! not invent a list when that request fails.

use leptos::prelude::*;
use serde::Deserialize;

use crate::api::get_json;

#[derive(Clone, Debug, Default, Deserialize)]
pub struct LocalFiles {
    #[serde(default)]
    pub telemetry: bool,
    #[serde(default)]
    pub auth: String,
    #[serde(default, rename = "workDir")]
    pub work_dir: String,
    #[serde(default, rename = "globalRoot")]
    pub global_root: String,
    #[serde(default, rename = "projectKey")]
    pub project_key: String,
    #[serde(default)]
    pub note: String,
    #[serde(default)]
    pub files: Vec<LocalFile>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct LocalFile {
    pub path: String,
    #[serde(default)]
    pub group: String,
    #[serde(default)]
    pub access: String,
    #[serde(default)]
    pub detail: String,
    #[serde(default)]
    pub exists: bool,
}

const GROUPS: &[(&str, &str)] = &[
    ("credentials", "Credentials"),
    ("config", "Config"),
    ("instructions", "Instructions"),
    ("catalog", "Agents, skills, workflows, themes"),
    ("state", "State this process opens"),
];

pub fn access_label(access: &str) -> &'static str {
    match access {
        "created-at-startup" => "Opened at startup",
        "read-each-request" => "Read on each request",
        _ => "Read when the file is there",
    }
}

pub async fn fetch_local_files(base: &str, token: &str) -> Result<LocalFiles, String> {
    get_json(base, token, "/v1/local-files").await
}

#[component]
pub fn FileList(doc: LocalFiles) -> impl IntoView {
    let files = doc.files.clone();
    let present = files.iter().filter(|f| f.exists).count();
    let total = files.len();
    view! {
        <div class="file-groups">
            <p class="hint">{format!("{present} on disk, {total} paths checked. Missing optional files are skipped.")}</p>
            {GROUPS.iter().filter_map(|(id, title)| {
                let rows: Vec<_> = files.iter().filter(|f| f.group == *id).cloned().collect();
                if rows.is_empty() {
                    return None;
                }
                let title = title.to_string();
                Some(view! {
                    <section class="file-group">
                        <h3>{title}</h3>
                        <ul class="plain">
                            {rows.into_iter().map(|f| {
                                let mark = if f.exists { "On disk" } else { "Not on disk" };
                                let klass = if f.exists { "exist on" } else { "exist" };
                                let access = access_label(&f.access).to_string();
                                view! {
                                    <li class="file-item">
                                        <span class="name">{f.path}</span>
                                        <span class="path">{f.detail}</span>
                                        <span class="file-meta"><span class=klass>{mark}</span><span class="path">{access}</span></span>
                                    </li>
                                }
                            }).collect_view()}
                        </ul>
                    </section>
                })
            }).collect_view()}
            {(!doc.note.is_empty()).then(|| view! { <p class="hint">{doc.note}</p> })}
        </div>
    }
}

pub fn auth_sentence(auth: &str) -> &'static str {
    match auth {
        "file" => "The bearer token is the token file in the list. The secret is not shown.",
        "flag" => "A bearer token was passed to the process. It is not a file and it is not shown.",
        "off" => "This process is not checking a bearer token.",
        _ => "",
    }
}
