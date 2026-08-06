package plugin

import (
	"fmt"
	"strings"
)

// SourceType is how a plugin bundle was obtained.
type SourceType string

const (
	SourceLocal SourceType = "local"
	SourceGit   SourceType = "git"
	// SourceCatalog is reserved for #729.
	SourceCatalog SourceType = "catalog"
)

// SourceIdentity records provenance without credentials (docs/plugins.md §6).
type SourceIdentity struct {
	Type SourceType `json:"type"`

	// Local: absolute path the user supplied at install time (not the install root).
	Path string `json:"path,omitempty"`

	// Git
	URL    string `json:"url,omitempty"`
	Ref    string `json:"ref,omitempty"`    // branch/tag used at resolve time (optional)
	Commit string `json:"commit,omitempty"` // required full SHA after git install
	Subdir string `json:"subdir,omitempty"`
}

// Validate checks source identity shape for lockfile persistence.
func (s SourceIdentity) Validate() error {
	switch s.Type {
	case SourceLocal:
		if strings.TrimSpace(s.Path) == "" {
			return fmt.Errorf("local source requires path")
		}
		return nil
	case SourceGit:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("git source requires url")
		}
		if strings.TrimSpace(s.Commit) == "" {
			return fmt.Errorf("git source requires pinned commit")
		}
		if !isFullCommitSHA(s.Commit) {
			return fmt.Errorf("git commit must be a full 40-char hex SHA")
		}
		return nil
	case SourceCatalog:
		return fmt.Errorf("catalog source is not supported yet (#729)")
	case "":
		return fmt.Errorf("source type is required")
	default:
		return fmt.Errorf("unknown source type %q", s.Type)
	}
}

// String is a short human summary (no secrets).
func (s SourceIdentity) String() string {
	switch s.Type {
	case SourceLocal:
		return "local:" + s.Path
	case SourceGit:
		var b strings.Builder
		b.WriteString("git:")
		b.WriteString(s.URL)
		if s.Commit != "" {
			b.WriteString("@")
			if len(s.Commit) >= 12 {
				b.WriteString(s.Commit[:12])
			} else {
				b.WriteString(s.Commit)
			}
		}
		if s.Ref != "" {
			b.WriteString(" (ref=")
			b.WriteString(s.Ref)
			b.WriteString(")")
		}
		if s.Subdir != "" {
			b.WriteString(" subdir=")
			b.WriteString(s.Subdir)
		}
		return b.String()
	default:
		return string(s.Type)
	}
}

func isFullCommitSHA(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' {
			continue
		}
		return false
	}
	return true
}
