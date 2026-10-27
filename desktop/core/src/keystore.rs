use std::collections::HashMap;
use std::sync::Mutex;

use zeroize::Zeroizing;

/// Where a gateway token lives. The JSON store never implements this.
pub trait KeyStore: Send + Sync {
    fn get(&self, id: &str) -> Result<Option<Zeroizing<String>>, String>;
    fn set(&self, id: &str, secret: &str) -> Result<(), String>;
    fn delete(&self, id: &str) -> Result<(), String>;
}

#[derive(Default)]
pub struct MemoryKeyStore {
    inner: Mutex<HashMap<String, Zeroizing<String>>>,
}

impl KeyStore for MemoryKeyStore {
    fn get(&self, id: &str) -> Result<Option<Zeroizing<String>>, String> {
        let guard = self.inner.lock().map_err(|e| e.to_string())?;
        Ok(guard.get(id).cloned())
    }

    fn set(&self, id: &str, secret: &str) -> Result<(), String> {
        if secret.is_empty() || secret.len() > 512 || secret.chars().any(|c| c.is_control()) {
            return Err("token is empty or not a single line".into());
        }
        let mut guard = self.inner.lock().map_err(|e| e.to_string())?;
        guard.insert(id.to_string(), Zeroizing::new(secret.to_string()));
        Ok(())
    }

    fn delete(&self, id: &str) -> Result<(), String> {
        let mut guard = self.inner.lock().map_err(|e| e.to_string())?;
        guard.remove(id);
        Ok(())
    }
}

/// OS keychain via the `keyring` crate (Secret Service + keyutils on Linux,
/// Keychain on macOS, Credential Manager on Windows). There is no file fallback.
pub struct OsKeyStore;

const SERVICE: &str = "strike-desktop";

impl KeyStore for OsKeyStore {
    fn get(&self, id: &str) -> Result<Option<Zeroizing<String>>, String> {
        let entry = keyring::Entry::new(SERVICE, id).map_err(|e| e.to_string())?;
        match entry.get_password() {
            Ok(secret) => Ok(Some(Zeroizing::new(secret))),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(err) => Err(err.to_string()),
        }
    }

    fn set(&self, id: &str, secret: &str) -> Result<(), String> {
        if secret.is_empty() || secret.len() > 512 || secret.chars().any(|c| c.is_control()) {
            return Err("token is empty or not a single line".into());
        }
        let entry = keyring::Entry::new(SERVICE, id).map_err(|e| e.to_string())?;
        entry.set_password(secret).map_err(|e| e.to_string())
    }

    fn delete(&self, id: &str) -> Result<(), String> {
        let entry = keyring::Entry::new(SERVICE, id).map_err(|e| e.to_string())?;
        match entry.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(err) => Err(err.to_string()),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn memory_round_trip_and_reject_empty() {
        let store = MemoryKeyStore::default();
        assert!(store.set("a", "").is_err());
        assert!(store.set("a", "line\nbreak").is_err());
        store.set("a", "tok").unwrap();
        assert_eq!(store.get("a").unwrap().unwrap().as_str(), "tok");
        store.delete("a").unwrap();
        assert!(store.get("a").unwrap().is_none());
    }
}
