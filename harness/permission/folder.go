package permission

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// FolderGrant is a session-scoped directory grant recorded by a
// DecisionFolder reply (write/edit asks only). Folder is the
// workspace-relative folder the grant covers; Patterns are the allow rules
// recorded for it, in evaluation order. The grant covers the folder
// subtree: a later write/edit ask whose target is under Folder evaluates
// to Allow while this grant is in force, and hard/managed denies still win
// because session grants sit before the modeLate/phase/managed layers in
// evaluation order.
type FolderGrant struct {
	// Permission is the ask permission name ("write" or "edit").
	Permission string
	// Folder is the workspace-relative folder, e.g. "docs" or "docs/api".
	Folder string
	// Patterns are the recorded allow-rule patterns (escaped doublestar
	// globs). Display code should use DisplayPattern, not these, so folder
	// names containing glob characters render as the folder itself.
	Patterns []string
}

// DisplayPattern is the human-readable pattern for the grant: the folder
// subtree glob, using the folder name verbatim.
func (g FolderGrant) DisplayPattern() string {
	return g.Folder + "/**"
}

// DeriveFolderGrant derives the folder grant for a write/edit ask from the
// ask's own patterns (and, for directory targets, its metadata),
// daemon-side. It returns ok=false — and the caller must record no grant —
// for anything it cannot express safely:
//
//   - permissions other than write/edit (a folder scope has no meaning for
//     shell/network calls, whose patterns are commands, not file paths);
//   - empty, absolute, glob-shaped, or escaping patterns ("*", absolute
//     paths, "..");
//   - patterns spanning more than one folder (a single click must never
//     mint grants for several folders at once);
//   - targets at the workspace root: a bare pattern is ambiguous between
//     "a file at the root" (folder = root) and "the folder itself" (a
//     directory delete), so v1 grants no folder scope there — Allow
//     session already covers the root, and a wrong guess would silently
//     widen a folder grant into a workspace-wide one.
//
// File targets grant their parent folder. A directory target (the delete
// tool marks its ask metadata with "directory": true) grants the named
// folder itself. Glob metacharacters in folder names are escaped in the
// recorded patterns so a folder literally named "a*b" cannot widen into a
// glob that matches other folders.
func DeriveFolderGrant(permission string, patterns []string, metadata json.RawMessage) (FolderGrant, bool) {
	if permission != "write" && permission != "edit" {
		return FolderGrant{}, false
	}
	if len(patterns) == 0 {
		return FolderGrant{}, false
	}
	cleaned := make([]string, 0, len(patterns))
	for _, pat := range patterns {
		if pat == "" || strings.HasPrefix(pat, "/") {
			return FolderGrant{}, false
		}
		clean := path.Clean(pat)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return FolderGrant{}, false
		}
		// A whole "*" or "**" segment means the pattern is a glob, not a
		// file path — refuse rather than guess its folder. A "*" inside a
		// segment is a literal file-name character and is fine: the grant
		// patterns are built from the escaped folder name.
		for _, seg := range strings.Split(clean, "/") {
			if seg == "*" || seg == "**" {
				return FolderGrant{}, false
			}
		}
		cleaned = append(cleaned, clean)
	}
	folder := ""
	if askTargetsDirectory(metadata) {
		// The pattern names the folder itself (directory delete). Only a
		// single-target ask is unambiguous.
		if len(cleaned) != 1 {
			return FolderGrant{}, false
		}
		folder = cleaned[0]
	} else {
		for _, clean := range cleaned {
			parent := path.Dir(clean)
			if parent == "." {
				return FolderGrant{}, false
			}
			if folder == "" {
				folder = parent
			} else if parent != folder {
				return FolderGrant{}, false
			}
		}
	}
	esc := escapeGlob(folder)
	pats := []string{esc, esc + "/*", esc + "/**"}
	for _, pat := range pats {
		if !doublestar.ValidatePattern(pat) {
			return FolderGrant{}, false
		}
	}
	return FolderGrant{Permission: permission, Folder: folder, Patterns: pats}, true
}

// askTargetsDirectory reports whether the ask metadata marks the target as
// a directory (the delete tool sets "directory": true). Unparseable or
// absent metadata means a file target.
func askTargetsDirectory(metadata json.RawMessage) bool {
	if len(metadata) == 0 {
		return false
	}
	var meta struct {
		Directory bool `json:"directory"`
	}
	return json.Unmarshal(metadata, &meta) == nil && meta.Directory
}

// escapeGlob escapes doublestar metacharacters so a literal path segment
// matches only itself. Backslash escapes the next character in doublestar.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
