//! Inline strokes. No icon font. Shared with the rest of the client so
//! the shell and the session rail draw the same marks.

pub fn icon(name: &str) -> &'static str {
    match name {
        "sessions" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h7"/></svg>"#
        }
        "activity" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1.5 8h2.5l1.8-3.5L9 12l1.7-4H14.5"/></svg>"#
        }
        "files" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2.5h5l3 3V13.5H4z"/><path d="M9 2.5V5.5h3"/></svg>"#
        }
        "changes" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5h10M3 8h6M3 11.5h8"/></svg>"#
        }
        "git" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="4" cy="4" r="1.6"/><circle cx="4" cy="12" r="1.6"/><circle cx="12" cy="6" r="1.6"/><path d="M4 5.6v4.8M4 4h5.2a2.2 2.2 0 0 1 0 4.4H4"/></svg>"#
        }
        "worktrees" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 13.5V3.5M3 8h6.5a2 2 0 0 0 0-4H8M9.5 8H13"/></svg>"#
        }
        "memory" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2.5h8v11l-4-2.2L4 13.5z"/></svg>"#
        }
        "knowledge" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3.5h4.2c.7 0 1.3.4 1.8 1 .5-.6 1.1-1 1.8-1H14.5v9h-3.7c-.7 0-1.3.4-1.8 1-.5-.6-1.1-1-1.8-1H3z"/></svg>"#
        }
        "terminal" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5l3.5 3.5L3 11.5M8 11.5h5"/></svg>"#
        }
        "artifacts" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 5.5 8 2.5l5.5 3L8 8.5z"/><path d="M2.5 5.5V10.5L8 13.5l5.5-3V5.5"/></svg>"#
        }
        "command" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 3.5h11v9h-11z"/><path d="M5 7l2 1.5L5 10M8.5 10.5h2.5"/></svg>"#
        }
        "status" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="8" cy="8" r="2.5"/></svg>"#
        }
        "back" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 3.5 5 8l4.5 4.5"/></svg>"#
        }
        "forward" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6.5 3.5 11 8l-4.5 4.5"/></svg>"#
        }
        "focus" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6V3.5h2.5M10.5 3.5H13V6M13 10v2.5h-2.5M5.5 12.5H3V10"/></svg>"#
        }
        "more" => {
            r#"<svg class="icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M4 8h.01M8 8h.01M12 8h.01"/></svg>"#
        }
        _ => "",
    }
}
