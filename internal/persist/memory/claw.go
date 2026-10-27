package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// rewriteProjectionLocked writes the inspectable markdown file from the
// current entries. The JSON file remains the indexed source of truth; this
// projection is what a person opens. It is rewritten on every successful
// mutation and when the store is opened, so a restart shows the same text
// the agent will read (tagged entries, via AutoLoad).
func (s *Store) rewriteProjectionLocked() error {
	entries := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, cloneEntry(e))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	var b strings.Builder
	fmt.Fprintf(&b, "# Strike memory (%s)\n\n", s.Scope())
	b.WriteString("This file is the plain-text projection of the memory store.\n")
	b.WriteString("The agent reads entries tagged instruction, preference, or project-convention at every turn, including the start of a session.\n")
	b.WriteString("Edits made in this file are replaced on the next write. Change memory through the memory tools or PUT /v1/memory.\n")
	fmt.Fprintf(&b, "Audit log: `%s`\n\n", s.AuditPath())
	if len(entries) == 0 {
		b.WriteString("_Nothing stored._\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "## %s\n\n", e.Key)
		if e.Author != "" {
			fmt.Fprintf(&b, "- author: %s\n", e.Author)
		}
		if !e.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, "- updated: %s\n", e.UpdatedAt.UTC().Format(time.RFC3339))
		}
		if len(e.Tags) > 0 {
			fmt.Fprintf(&b, "- tags: %s\n", strings.Join(e.Tags, ", "))
		}
		b.WriteString("\n")
		b.WriteString(e.Value)
		if !strings.HasSuffix(e.Value, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return writeFileAtomic(s.MarkdownPath(), []byte(b.String()))
}

type auditLine struct {
	Time   string   `json:"ts"`
	Op     string   `json:"op"`
	Scope  string   `json:"scope"`
	Key    string   `json:"key,omitempty"`
	Value  string   `json:"value,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Author string   `json:"author,omitempty"`
}

func (s *Store) appendAuditLocked(op string, e Entry) error {
	line := auditLine{
		Time:   time.Now().UTC().Format(time.RFC3339Nano),
		Op:     op,
		Scope:  s.Scope(),
		Key:    e.Key,
		Value:  e.Value,
		Tags:   e.Tags,
		Author: e.Author,
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("encode memory audit: %w", err)
	}
	raw = append(raw, '\n')
	path := s.AuditPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read memory audit: %w", err)
	}
	next := append(existing, raw...)
	if len(next) > maxAuditBytes {
		next = next[len(next)-maxAuditBytes:]
		if i := strings.IndexByte(string(next), '\n'); i >= 0 && i < len(next)-1 {
			next = next[i+1:]
		}
	}
	return writeFileAtomic(path, next)
}
