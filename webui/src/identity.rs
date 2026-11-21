//! Per-agent identity (#1379): a deterministic tone and initial so two
//! children (or two sessions) are told apart at a glance.
//!
//! The tone is a stable hash of an identity key mapped onto a small set of
//! hues derived from the Strike purple (see `webui/identity.css`); the
//! initial is the first letter or digit of the agent's name. Identity is
//! separate from status: the disc never replaces the status dot or badge,
//! and it is decorative (`aria-hidden`) — the name text stays the label.
//! The lead/root keeps the plain Strike presentation; only children and
//! peer sessions get a disc. No settings, no persistence: the same key
//! always yields the same disc.

use leptos::prelude::*;

/// Number of derived tones (`data-tone="0".."5"` in identity.css).
/// Collisions past this are acceptable: the initial differs too.
pub const TONES: u32 = 6;

/// FNV-1a over the normalised key (trimmed, ASCII-lowercased), so the
/// same agent hashes the same on every reload and in every surface.
fn fnv1a(key: &str) -> u32 {
    let mut h: u32 = 0x811c_9dc5;
    for b in key.trim().to_ascii_lowercase().bytes() {
        h ^= u32::from(b);
        h = h.wrapping_mul(0x0100_0193);
    }
    h
}

/// Tone index for an identity key.
pub fn tone(key: &str) -> u32 {
    fnv1a(key) % TONES
}

/// First letter or digit of `name`, uppercased; "?" when there is none.
pub fn initial(name: &str) -> String {
    name.chars()
        .find(|c| c.is_alphanumeric())
        .map(|c| c.to_uppercase().collect())
        .unwrap_or_else(|| "?".to_string())
}

/// Identity key for a child: its stable teammate alias when it has one
/// (the key the transcript's child cards already use, so a child's row
/// and its card share a disc), else its session id.
pub fn child_key(name: &str, session_id: &str) -> String {
    let name = name.trim();
    if name.is_empty() {
        session_id.trim().to_string()
    } else {
        name.to_string()
    }
}

/// Small initial disc. `ident` picks the tone; `name` gives the letter.
#[component]
pub fn IdentityDisc(ident: String, name: String) -> impl IntoView {
    let tone = tone(&ident).to_string();
    let letter = initial(&name);
    view! {
        <span class="identity-disc" data-tone=tone aria-hidden="true">{letter}</span>
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tone_is_stable_and_normalised() {
        for key in ["scout", "c1", "01JD5A9X", "child-b"] {
            assert_eq!(tone(key), tone(key));
            assert!(tone(key) < TONES);
        }
        assert_eq!(tone("Scout"), tone("scout"));
        assert_eq!(tone(" scout "), tone("scout"));
    }

    #[test]
    fn neighbouring_ids_get_distinct_tones() {
        let fixtures: [&[&str]; 4] = [
            &["c1", "c2", "c3"],
            &["child-a", "child-b", "child-c"],
            &["01JD5A", "01JD5B", "01JD5C"],
            &["alpha", "bravo", "charlie"],
        ];
        for ids in fixtures {
            let mut tones: Vec<u32> = ids.iter().map(|id| tone(id)).collect();
            tones.sort_unstable();
            tones.dedup();
            assert_eq!(tones.len(), ids.len(), "{ids:?}");
        }
    }

    #[test]
    fn initials() {
        let cases = [
            ("scout", "S"),
            ("  fixer", "F"),
            ("#2 builder", "2"),
            ("élan", "É"),
            ("", "?"),
            ("--", "?"),
        ];
        for (name, want) in cases {
            assert_eq!(initial(name), want, "{name:?}");
        }
    }

    #[test]
    fn child_key_prefers_alias() {
        assert_eq!(child_key("scout", "sess-1"), "scout");
        assert_eq!(child_key("  ", "sess-1"), "sess-1");
        // Row and card for the same child share a tone.
        assert_eq!(tone(&child_key("Scout", "sess-1")), tone("scout"));
    }
}
