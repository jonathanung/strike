#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use strike_desktop_core::{Desktop, OsKeyStore};
use tauri::menu::{Menu, MenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{Manager, WebviewUrl, WebviewWindowBuilder};

fn main() {
    let port = match start_server() {
        Ok(port) => port,
        Err(err) => {
            eprintln!("strike-desktop: {err}");
            std::process::exit(1);
        }
    };
    let manager = format!("http://127.0.0.1:{port}/_desktop/");
    if let Err(err) = tauri::Builder::default()
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_deep_link::init())
        .setup(move |app| {
            WebviewWindowBuilder::new(
                app,
                "main",
                WebviewUrl::External(manager.parse().expect("manager url")),
            )
            .title("Strike")
            .inner_size(1280.0, 800.0)
            .build()?;
            if let Err(err) = install_tray(app.handle(), port) {
                eprintln!("strike-desktop: tray unavailable: {err}");
            }
            let handle = app.handle().clone();
            std::thread::spawn(move || poll_notices(handle, port));
            Ok(())
        })
        .run(tauri::generate_context!())
    {
        eprintln!("strike-desktop: {err}");
        std::process::exit(1);
    }
}

fn start_server() -> Result<u16, String> {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(|err| err.to_string())?;
    let dir = std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".strike")
        .join("desktop");
    let desktop = runtime
        .block_on(async { Desktop::open(dir, Arc::new(OsKeyStore), None) })
        .map_err(|err| err.detail())?;
    let addr = runtime
        .block_on(desktop.listen())
        .map_err(|err| err.detail())?;
    let port = addr.port();
    std::thread::spawn(move || {
        runtime.block_on(desktop.cancelled());
    });
    Ok(port)
}

fn install_tray(app: &tauri::AppHandle, port: u16) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "show", "Open Strike", true, None::<&str>)?;
    let gateways = MenuItem::with_id(app, "gateways", "Gateways", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &gateways, &quit])?;
    let icon = tauri::image::Image::from_bytes(include_bytes!("../icons/32x32.png"))?;
    let manager = format!("http://127.0.0.1:{port}/_desktop/");
    TrayIconBuilder::with_id("strike")
        .icon(icon)
        .menu(&menu)
        .tooltip("Strike")
        .on_menu_event(move |app, event| {
            let id = event.id.as_ref();
            if id == "quit" {
                app.exit(0);
                return;
            }
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.set_focus();
                if id == "gateways" || id == "show" {
                    if let Ok(url) = manager.parse() {
                        let _ = window.navigate(url);
                    }
                }
            }
        })
        .build(app)?;
    Ok(())
}

fn poll_notices(app: tauri::AppHandle, port: u16) {
    use tauri_plugin_notification::NotificationExt;
    let mut seen = 0u64;
    loop {
        std::thread::sleep(Duration::from_secs(2));
        let Some(body) = http_get(port, "/_desktop/api/notices") else {
            continue;
        };
        let Ok(value) = serde_json::from_str::<serde_json::Value>(&body) else {
            continue;
        };
        let Some(notices) = value.get("notices").and_then(|v| v.as_array()) else {
            continue;
        };
        for notice in notices {
            let id = notice.get("id").and_then(|v| v.as_u64()).unwrap_or(0);
            if id <= seen {
                continue;
            }
            seen = id;
            let title = notice
                .get("title")
                .and_then(|v| v.as_str())
                .unwrap_or("Strike");
            let body = notice.get("body").and_then(|v| v.as_str()).unwrap_or("");
            let _ = app.notification().builder().title(title).body(body).show();
        }
    }
}

fn http_get(port: u16, path: &str) -> Option<String> {
    use std::io::{Read, Write};
    let mut stream = std::net::TcpStream::connect(("127.0.0.1", port)).ok()?;
    stream.set_read_timeout(Some(Duration::from_secs(2))).ok()?;
    let req = format!("GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n\r\n");
    stream.write_all(req.as_bytes()).ok()?;
    let mut buf = String::new();
    stream.read_to_string(&mut buf).ok()?;
    Some(buf.split_once("\r\n\r\n")?.1.to_string())
}
