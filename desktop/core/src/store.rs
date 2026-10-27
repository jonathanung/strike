use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use crate::error::GatewayError;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Mode {
    Sidecar,
    Remote,
    Ssh,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct Gateway {
    pub id: String,
    pub name: String,
    pub mode: Mode,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub ssh_target: String,
    #[serde(default)]
    pub remote_port: u16,
    #[serde(default)]
    pub cleartext: bool,
    /// SHA-256 hex of the trusted end-entity certificate. Not a bearer token.
    #[serde(default)]
    pub pin: String,
    #[serde(default)]
    pub provider: String,
    #[serde(skip)]
    pub status: String,
    #[serde(skip)]
    pub detail: String,
}

impl Gateway {
    pub fn local_default() -> Self {
        Self {
            id: "local".into(),
            name: "This computer".into(),
            mode: Mode::Sidecar,
            url: String::new(),
            ssh_target: String::new(),
            remote_port: 0,
            cleartext: false,
            pin: String::new(),
            provider: String::new(),
            status: "idle".into(),
            detail: String::new(),
        }
    }

    pub fn public_json(&self, active: bool) -> serde_json::Value {
        serde_json::json!({
            "id": self.id,
            "name": self.name,
            "mode": self.mode,
            "url": safe_display_url(self),
            "sshTarget": self.ssh_target,
            "remotePort": self.remote_port,
            "cleartext": self.cleartext,
            "pinned": !self.pin.is_empty(),
            "provider": self.provider,
            "status": if self.status.is_empty() { "idle" } else { &self.status },
            "detail": self.detail,
            "active": active,
        })
    }
}

fn safe_display_url(gw: &Gateway) -> String {
    if gw.mode == Mode::Ssh {
        return format!("ssh {} → 127.0.0.1:{}", gw.ssh_target, gw.remote_port);
    }
    strip_query(&gw.url)
}

pub fn strip_query(url: &str) -> String {
    match url.split_once(['?', '#']) {
        Some((left, _)) => left.to_string(),
        None => url.to_string(),
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct File {
    version: u32,
    #[serde(default)]
    active: Option<String>,
    #[serde(default)]
    gateways: Vec<Gateway>,
}

pub struct Store {
    path: PathBuf,
    pub active: Option<String>,
    pub gateways: Vec<Gateway>,
}

impl Store {
    pub fn open(dir: &Path) -> Result<Self, GatewayError> {
        fs::create_dir_all(dir).map_err(|e| GatewayError::Store {
            detail: e.to_string(),
        })?;
        tighten_dir(dir)?;
        let path = dir.join("gateways.json");
        if !path.exists() {
            let store = Self {
                path,
                active: None,
                gateways: vec![Gateway::local_default()],
            };
            store.save()?;
            return Ok(store);
        }
        let raw = fs::read_to_string(&path).map_err(|e| GatewayError::Store {
            detail: e.to_string(),
        })?;
        let file: File = serde_json::from_str(&raw).map_err(|e| GatewayError::Store {
            detail: format!("gateways.json is not readable: {e}"),
        })?;
        if file.version != 1 {
            return Err(GatewayError::Store {
                detail: format!("gateways.json version {} is not supported", file.version),
            });
        }
        Ok(Self {
            path,
            active: file.active,
            gateways: file.gateways,
        })
    }

    pub fn save(&self) -> Result<(), GatewayError> {
        let file = File {
            version: 1,
            active: self.active.clone(),
            gateways: self.gateways.clone(),
        };
        let body = serde_json::to_vec_pretty(&file).map_err(|e| GatewayError::Store {
            detail: e.to_string(),
        })?;
        write_private(&self.path, &body).map_err(|e| GatewayError::Store {
            detail: e.to_string(),
        })
    }

    #[cfg(test)]
    pub fn path(&self) -> &Path {
        &self.path
    }
}

fn tighten_dir(dir: &Path) -> Result<(), GatewayError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(dir, fs::Permissions::from_mode(0o700)).map_err(|e| {
            GatewayError::Store {
                detail: e.to_string(),
            }
        })?;
    }
    let _ = dir;
    Ok(())
}

fn write_private(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    let tmp = path.with_extension("json.tmp");
    {
        let mut opts = OpenOptions::new();
        opts.create(true).write(true).truncate(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            opts.mode(0o600);
        }
        let mut file = opts.open(&tmp)?;
        file.write_all(bytes)?;
        file.sync_all()?;
    }
    fs::rename(&tmp, path)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o600))?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn file_drops_a_hand_edited_token_and_is_private() {
        let dir = std::env::temp_dir().join(format!("strike-desktop-store-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let mut store = Store::open(&dir).unwrap();
        let path = store.path().to_path_buf();
        let raw = fs::read_to_string(&path).unwrap();
        assert!(!raw.contains("token"), "{raw}");
        fs::write(
            &path,
            r#"{"version":1,"active":null,"gateways":[{"id":"g","name":"N","mode":"remote","url":"http://127.0.0.1:9","token":"s3cret"}],"token":"s3cret"}"#,
        )
        .unwrap();
        let loaded = Store::open(&dir).unwrap();
        loaded.save().unwrap();
        let raw = fs::read_to_string(&path).unwrap();
        assert!(!raw.contains("s3cret"), "{raw}");
        assert!(!raw.contains("\"token\""), "{raw}");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
            let dmode = fs::metadata(&dir).unwrap().permissions().mode() & 0o777;
            assert_eq!(dmode, 0o700);
        }
        store.gateways.clear();
        let _ = store;
        let _ = fs::remove_dir_all(&dir);
    }
}
