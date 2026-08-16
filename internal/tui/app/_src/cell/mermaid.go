package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	mermaidascii "github.com/AlexanderGrooff/mermaid-ascii/cmd"
	"github.com/AlexanderGrooff/mermaid-ascii/pkg/diagram"
)

// mermaidDiagramRender converts a mermaid source body to ASCII/Unicode art.
// Overridable in tests; nil means the mermaid-ascii library path.
var mermaidDiagramRender = renderMermaidDiagram

func renderMermaidDiagram(source string, width int) (string, error) {
	cfg := diagram.DefaultConfig()
	cfg.StyleType = "cli"
	// Tighten spacing in narrow panes so diagrams stay readable.
	if width > 0 && width < 60 {
		cfg.PaddingBetweenX = 2
		cfg.PaddingBetweenY = 2
		cfg.BoxBorderPadding = 0
	}
	out, err := mermaidascii.RenderDiagram(source, cfg)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// expandMermaidFences replaces closed ```mermaid / ~~~mermaid fences with
// plain fenced ASCII/Unicode diagrams. Unclosed fences and render failures
// leave the original markdown untouched.
func expandMermaidFences(source string, width int) string {
	if source == "" || !strings.Contains(strings.ToLower(source), "mermaid") {
		return source
	}
	lines := strings.Split(source, "\n")
	var b strings.Builder
	b.Grow(len(source))
	for i := 0; i < len(lines); {
		indent, marker, n, info, ok := parseFenceOpen(lines[i])
		if !ok || !isMermaidInfo(info) {
			b.WriteString(lines[i])
			if i < len(lines)-1 {
				b.WriteByte('\n')
			}
			i++
			continue
		}
		bodyStart := i + 1
		bodyEnd := bodyStart
		closed := false
		for bodyEnd < len(lines) {
			if isFenceClose(lines[bodyEnd], marker, n) {
				closed = true
				break
			}
			bodyEnd++
		}
		if !closed {
			// Incomplete fence: emit remainder unchanged.
			for j := i; j < len(lines); j++ {
				b.WriteString(lines[j])
				if j < len(lines)-1 {
					b.WriteByte('\n')
				}
			}
			return b.String()
		}
		body := strings.Join(lines[bodyStart:bodyEnd], "\n")
		fn := mermaidDiagramRender
		if fn == nil {
			fn = renderMermaidDiagram
		}
		ascii, err := fn(body, width)
		if err != nil || strings.TrimSpace(ascii) == "" {
			for j := i; j <= bodyEnd; j++ {
				b.WriteString(lines[j])
				if j < len(lines)-1 {
					b.WriteByte('\n')
				}
			}
			i = bodyEnd + 1
			continue
		}
		// Plain fence (no language) so glamour keeps monospaced art intact.
		fence := strings.Repeat(string(marker), n)
		b.WriteString(indent)
		b.WriteString(fence)
		b.WriteByte('\n')
		for _, line := range strings.Split(ascii, "\n") {
			b.WriteString(indent)
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteString(indent)
		b.WriteString(fence)
		if bodyEnd < len(lines)-1 {
			b.WriteByte('\n')
		}
		i = bodyEnd + 1
	}
	return b.String()
}

func parseFenceOpen(line string) (indent string, marker rune, n int, info string, ok bool) {
	i := 0
	for i < len(line) && i < 3 && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	indent = line[:i]
	rest := line[i:]
	if rest == "" {
		return "", 0, 0, "", false
	}
	marker, _ = utf8.DecodeRuneInString(rest)
	if marker != '`' && marker != '~' {
		return "", 0, 0, "", false
	}
	n = 0
	for _, r := range rest {
		if r != marker {
			break
		}
		n++
	}
	if n < 3 {
		return "", 0, 0, "", false
	}
	info = strings.TrimSpace(rest[n:])
	// Info must not contain the fence marker (CommonMark).
	if strings.ContainsRune(info, marker) {
		return "", 0, 0, "", false
	}
	return indent, marker, n, info, true
}

func isFenceClose(line string, marker rune, openN int) bool {
	i := 0
	for i < len(line) && i < 3 && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	rest := line[i:]
	n := 0
	for _, r := range rest {
		if r != marker {
			break
		}
		n++
	}
	if n < openN {
		return false
	}
	// Closing fence: only optional trailing whitespace after markers.
	for _, r := range rest[n:] {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func isMermaidInfo(info string) bool {
	info = strings.TrimSpace(info)
	if info == "" {
		return false
	}
	// First word is the language tag; allow trailing attributes.
	lang := info
	if i := strings.IndexFunc(info, unicode.IsSpace); i >= 0 {
		lang = info[:i]
	}
	return strings.EqualFold(lang, "mermaid")
}
