use std::env;
use std::path::PathBuf;
use std::process::ExitCode;
use std::sync::Arc;
use std::time::Instant;

use strike_desktop_core::{Desktop, GatewayError, OsKeyStore};

fn main() -> ExitCode {
    let started = Instant::now();
    let mut args = env::args().skip(1);
    let mut data_dir = default_dir();
    let mut link = None;
    while let Some(arg) = args.next() {
        match arg.as_str() {
            "--help" | "-h" => {
                println!(
                    "strike-desktop [--data-dir DIR] [strike://connect?...]\n\n\
                     Serves the gateway manager on 127.0.0.1 and proxies the\n\
                     selected daemon. Tokens are stored in the OS keyring."
                );
                return ExitCode::SUCCESS;
            }
            "--data-dir" => {
                let Some(dir) = args.next() else {
                    eprintln!("strike-desktop: --data-dir needs a path");
                    return ExitCode::from(2);
                };
                data_dir = PathBuf::from(dir);
            }
            other if other.starts_with('-') => {
                eprintln!("strike-desktop: unknown argument {other}");
                return ExitCode::from(2);
            }
            other => link = Some(other.to_string()),
        }
    }
    let runtime = match tokio::runtime::Runtime::new() {
        Ok(runtime) => runtime,
        Err(err) => {
            eprintln!("strike-desktop: {err}");
            return ExitCode::from(1);
        }
    };
    let desktop = match Desktop::open(data_dir, Arc::new(OsKeyStore), None) {
        Ok(desktop) => desktop,
        Err(err) => {
            eprintln!("strike-desktop: {}", err_line(&err));
            return ExitCode::from(1);
        }
    };
    let addr = match runtime.block_on(desktop.listen()) {
        Ok(addr) => addr,
        Err(err) => {
            eprintln!("strike-desktop: {}", err_line(&err));
            return ExitCode::from(1);
        }
    };
    if let Some(url) = link {
        let body = serde_json::json!({ "url": url }).to_string();
        if let Err(err) = runtime.block_on(desktop.deeplink_body(body.as_bytes())) {
            eprintln!("strike-desktop: {}", err_line(&err));
        }
    }
    println!("listening http://{addr}/_desktop/");
    println!("startup_ms {}", started.elapsed().as_millis());
    runtime.block_on(async {
        let _ = tokio::signal::ctrl_c().await;
        desktop.shutdown().await;
    });
    ExitCode::SUCCESS
}

fn default_dir() -> PathBuf {
    env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".strike")
        .join("desktop")
}

fn err_line(err: &GatewayError) -> String {
    let detail = err.detail();
    if detail.is_empty() {
        err.code().to_string()
    } else {
        format!("{}: {detail}", err.code())
    }
}
