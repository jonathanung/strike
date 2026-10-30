//! Fuzzy ranking, recent-command order, and unique name resolution.
//! These stay free of the DOM so `cargo test` can pin them.

/// Subsequence score. Empty query matches everything at score 0.
/// Consecutive hits and word starts rank higher than a sparse match.
pub fn fuzzy_score(query: &str, text: &str) -> Option<i32> {
    let q: Vec<char> = query
        .trim()
        .chars()
        .flat_map(|c| c.to_lowercase())
        .collect();
    if q.is_empty() {
        return Some(0);
    }
    let t: Vec<char> = text.chars().flat_map(|c| c.to_lowercase()).collect();
    let mut qi = 0usize;
    let mut score = 0i32;
    let mut prev = false;
    let mut first = true;
    for (ti, ch) in t.iter().copied().enumerate() {
        if qi < q.len() && ch == q[qi] {
            let mut add = 1;
            if prev {
                add += 6;
            }
            if ti == 0 || !t[ti - 1].is_alphanumeric() {
                add += 4;
            }
            if first {
                add += 2;
                first = false;
            }
            score += add;
            prev = true;
            qi += 1;
        } else {
            prev = false;
        }
    }
    if qi == q.len() {
        Some(score)
    } else {
        None
    }
}

/// Best weighted score across fields. `None` when nothing matches.
pub fn best_score(query: &str, fields: &[(&str, i32)]) -> Option<i32> {
    let query = query.trim();
    if query.is_empty() {
        return Some(0);
    }
    let mut best: Option<i32> = None;
    for (text, weight) in fields {
        if text.is_empty() {
            continue;
        }
        if let Some(score) = fuzzy_score(query, text) {
            let score = score.saturating_mul(*weight);
            best = Some(best.map_or(score, |cur| cur.max(score)));
        }
    }
    best
}

pub fn command_score(
    query: &str,
    label: &str,
    detail: &str,
    shortcut: &str,
    keywords: &[&str],
) -> Option<i32> {
    let mut best = best_score(query, &[(label, 3), (detail, 1), (shortcut, 2)]);
    if query.trim().is_empty() {
        return best;
    }
    // An exact label ("Usage") must beat a longer label that merely contains
    // the same word ("View context usage"). Those two otherwise tie, and the
    // lower order number wins.
    if label.eq_ignore_ascii_case(query.trim()) {
        let bonus = (query.trim().chars().count() as i32).saturating_mul(4);
        best = Some(best.unwrap_or(0).saturating_add(bonus));
    }
    for word in keywords {
        if let Some(score) = fuzzy_score(query, word) {
            let score = score.saturating_mul(2);
            best = Some(best.map_or(score, |cur| cur.max(score)));
        }
    }
    // A scattered subsequence through a long detail ("nord" inside
    // "session … or id") outranks nothing useful. Require a score that
    // is mostly consecutive or on a word start.
    let floor = (query.chars().count() as i32).saturating_mul(8);
    best.filter(|score| *score >= floor)
}

/// Most recent id first. Drops duplicates and keeps at most `cap` entries.
pub fn push_recent(list: &mut Vec<String>, id: &str, cap: usize) {
    if id.is_empty() {
        return;
    }
    list.retain(|item| item != id);
    list.insert(0, id.to_string());
    if cap > 0 && list.len() > cap {
        list.truncate(cap);
    }
}

/// One id whose id or label contains `query`. Errors are sentences the
/// palette can show without inventing a result.
pub fn unique_match(items: &[(String, String)], query: &str) -> Result<String, String> {
    let q = query.trim().to_ascii_lowercase();
    if q.is_empty() {
        return Err("Type part of a name.".into());
    }
    let mut hits: Vec<&(String, String)> = items
        .iter()
        .filter(|(id, name)| {
            id.to_ascii_lowercase().contains(&q) || name.to_ascii_lowercase().contains(&q)
        })
        .collect();
    hits.sort_by(|a, b| {
        a.1.to_ascii_lowercase()
            .cmp(&b.1.to_ascii_lowercase())
            .then(a.0.cmp(&b.0))
    });
    match hits.len() {
        0 => Err("Nothing matches that.".into()),
        1 => Ok(hits[0].0.clone()),
        n => Err(format!("{n} matches. Type more of the name.")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fuzzy_prefers_consecutive_and_rejects_misses() {
        assert_eq!(fuzzy_score("", "New session"), Some(0));
        assert!(fuzzy_score("zzzz", "New session").is_none());
        let tight = fuzzy_score("new", "New session").unwrap();
        let sparse = fuzzy_score("nsn", "New session").unwrap();
        assert!(tight > sparse);
        assert!(fuzzy_score("nord", "Theme: Nord").is_some());
        assert!(fuzzy_score("theme nord", "Theme: Nord").is_some());
    }

    #[test]
    fn exact_label_beats_a_longer_label_with_the_same_word() {
        let exact = command_score("Usage", "Usage", "Tokens and cost", "", &[]).unwrap();
        let longer = command_score(
            "Usage",
            "View context usage",
            "Show how full the context window is",
            "",
            &["context", "tokens"],
        )
        .unwrap();
        assert!(exact > longer);
    }

    #[test]
    fn sparse_detail_loses_to_a_real_label_match() {
        assert!(command_score(
            "nord",
            "Open session…",
            "Jump to a live or saved session by title or id",
            "",
            &["switch", "resume", "goto"],
        )
        .is_none());
        assert!(command_score("nord", "Theme: Nord", "Bundled theme", "", &[]).is_some());
        assert!(
            command_score("ns", "New session", "Open another live root", "", &["root"]).is_some()
        );
        assert!(command_score(
            "resume",
            "Open session…",
            "Jump to a session",
            "",
            &["resume"],
        )
        .is_some());
    }

    #[test]
    fn keywords_match_when_the_label_does_not() {
        let score = command_score(
            "resume",
            "Open session…",
            "Jump to a session",
            "",
            &["switch"],
        );
        assert!(score.is_none());
        let score = command_score(
            "resume",
            "Open session…",
            "Jump to a session",
            "",
            &["resume"],
        );
        assert!(score.is_some());
    }

    #[test]
    fn recents_move_to_front_and_cap() {
        let mut list = vec!["a".into(), "b".into()];
        push_recent(&mut list, "b", 2);
        assert_eq!(list, vec!["b".to_string(), "a".to_string()]);
        push_recent(&mut list, "c", 2);
        assert_eq!(list, vec!["c".to_string(), "b".to_string()]);
        push_recent(&mut list, "", 2);
        assert_eq!(list.len(), 2);
    }

    #[test]
    fn unique_match_reports_zero_and_many() {
        let items = vec![
            ("s1".into(), "Alpha".into()),
            ("s2".into(), "Alpine".into()),
            ("s3".into(), "Beta".into()),
        ];
        assert_eq!(unique_match(&items, "beta").unwrap(), "s3");
        assert_eq!(unique_match(&items, "s1").unwrap(), "s1");
        assert!(unique_match(&items, "  ").is_err());
        assert!(unique_match(&items, "nope")
            .unwrap_err()
            .contains("Nothing matches"));
        assert!(unique_match(&items, "alp")
            .unwrap_err()
            .contains("2 matches"));
    }
}
