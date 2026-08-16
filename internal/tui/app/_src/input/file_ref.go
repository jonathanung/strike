package tui

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// fileRef is a path:line citation found in transcript text.
type fileRef struct {
	Path string
	Line int // 1-indexed
}

// fileRefSpan is a fileRef located inside a plain (ANSI-stripped) line.
type fileRefSpan struct {
	fileRef
	Start int // byte offset in plain line
	End   int // exclusive byte offset covering path:line[:col]
}

// findFileRefSpans returns non-overlapping path:line (optional :col) spans in s.
func findFileRefSpans(s string) []fileRefSpan {
	if s == "" {
		return nil
	}
	var out []fileRefSpan
	i := 0
	for i < len(s) {
		colon := strings.IndexByte(s[i:], ':')
		if colon < 0 {
			break
		}
		colon += i
		// Line number must start with 1-9.
		numStart := colon + 1
		if numStart >= len(s) || s[numStart] < '1' || s[numStart] > '9' {
			i = colon + 1
			continue
		}
		numEnd := numStart
		for numEnd < len(s) && s[numEnd] >= '0' && s[numEnd] <= '9' {
			numEnd++
		}
		line, err := strconv.Atoi(s[numStart:numEnd])
		if err != nil || line < 1 {
			i = colon + 1
			continue
		}
		end := numEnd
		// Optional :column (ignored for open, included in highlighted span).
		if numEnd < len(s) && s[numEnd] == ':' {
			colStart := numEnd + 1
			if colStart < len(s) && s[colStart] >= '0' && s[colStart] <= '9' {
				colEnd := colStart
				for colEnd < len(s) && s[colEnd] >= '0' && s[colEnd] <= '9' {
					colEnd++
				}
				if colEnd > colStart {
					end = colEnd
				}
			}
		}
		pathStart := colon
		for pathStart > 0 {
			r, size := utf8.DecodeLastRuneInString(s[:pathStart])
			if size <= 0 || isFileRefBoundary(r) {
				break
			}
			pathStart -= size
		}
		path := s[pathStart:colon]
		if !looksLikeFilePath(path) {
			i = colon + 1
			continue
		}
		// Skip overlapping: if this starts inside a prior span, advance.
		if n := len(out); n > 0 && pathStart < out[n-1].End {
			i = colon + 1
			continue
		}
		out = append(out, fileRefSpan{
			fileRef: fileRef{Path: path, Line: line},
			Start:   pathStart,
			End:     end,
		})
		i = end
	}
	return out
}

func isFileRefBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '"', '\'', '`', '(', ')', '[', ']', '{', '}', '<', '>', ',', ';', '|', '!', '?', '*':
		return true
	default:
		return unicode.IsSpace(r)
	}
}

func looksLikeFilePath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || len(p) > 512 {
		return false
	}
	if strings.Contains(p, "://") {
		return false
	}
	// Reject bare scheme-like tokens (http:80 already excluded by extension rules).
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "http") || strings.HasPrefix(lower, "https") {
		return false
	}
	// Windows drive alone is not a file path.
	if len(p) == 2 && p[1] == ':' {
		return false
	}
	base := filepath.Base(filepath.Clean(p))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return false
	}
	ext := filepath.Ext(base)
	if ext == "" || ext == "." || len(ext) > 12 {
		// Allow extensionless names only when they look like nested paths.
		if !strings.ContainsAny(p, `/\`) {
			return false
		}
		for _, r := range base {
			if unicode.IsLetter(r) {
				return true
			}
		}
		return false
	}
	// Extension: leading dot + mostly alnum (".go", ".tsx", ".tar.gz" → ".gz").
	for i, r := range ext[1:] {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '_' {
			continue
		}
		if i > 0 && r == '.' {
			continue
		}
		return false
	}
	// Bare host:port (example.com:8080) is not a file citation.
	if !strings.ContainsAny(p, `/\`) && isCommonDNSExt(ext) {
		return false
	}
	hasName := false
	for _, r := range base[:len(base)-len(ext)] {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasName = true
			break
		}
	}
	return hasName
}

func isCommonDNSExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".com", ".org", ".net", ".io", ".dev", ".edu", ".gov", ".co", ".info",
		".biz", ".app", ".xyz", ".me", ".ai", ".cloud", ".local", ".test", ".example":
		return true
	default:
		return false
	}
}

// fileRefURI builds an OSC 8 target for terminal-native clicks.
func fileRefURI(workDir, path string, line int) string {
	abs := absPathInWorkDir(workDir, path)
	if abs == "" {
		abs = path
	}
	// url.PathEscape is too aggressive for file paths; use URL with Path set.
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if line > 0 {
		u.Fragment = "L" + strconv.Itoa(line)
	}
	return u.String()
}

// postLinkifyRendered scans each visual line of already-styled transcript
// content and, when the plain text contains path:line refs, redraws that line
// with accent hyperlinks (losing per-token styles on that line only).
func postLinkifyRendered(s string, th theme.Theme, workDir string) string {
	if s == "" {
		return s
	}
	th = th.Resolve()
	st := th.S()
	base := st.Text
	link := st.SelectedUnderline
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		plain := ansi.Strip(line)
		if len(findFileRefSpans(plain)) == 0 {
			continue
		}
		lines[i] = linkifyPlainLine(plain, base, link, workDir)
	}
	return strings.Join(lines, "\n")
}

func linkifyPlainLine(plain string, base, link lipgloss.Style, workDir string) string {
	spans := findFileRefSpans(plain)
	if len(spans) == 0 {
		return base.Render(plain)
	}
	var b strings.Builder
	cursor := 0
	for _, sp := range spans {
		if sp.Start > cursor {
			b.WriteString(base.Render(plain[cursor:sp.Start]))
		}
		token := plain[sp.Start:sp.End]
		b.WriteString(ansi.SetHyperlink(fileRefURI(workDir, sp.Path, sp.Line)))
		b.WriteString(link.Render(token))
		b.WriteString(ansi.ResetHyperlink())
		cursor = sp.End
	}
	if cursor < len(plain) {
		b.WriteString(base.Render(plain[cursor:]))
	}
	return b.String()
}

// fileRefAtColumn returns the file ref under visual column col (0-based) on a
// plain text line, if any.
func fileRefAtColumn(plain string, col int) (fileRef, bool) {
	if col < 0 || plain == "" {
		return fileRef{}, false
	}
	for _, sp := range findFileRefSpans(plain) {
		startCol := ansi.StringWidth(plain[:sp.Start])
		endCol := ansi.StringWidth(plain[:sp.End])
		if col >= startCol && col < endCol {
			return sp.fileRef, true
		}
	}
	return fileRef{}, false
}

// lastFileRef returns the last path:line citation in plain transcript lines.
func lastFileRef(plainLines []string) (fileRef, bool) {
	for i := len(plainLines) - 1; i >= 0; i-- {
		spans := findFileRefSpans(plainLines[i])
		if n := len(spans); n > 0 {
			return spans[n-1].fileRef, true
		}
	}
	return fileRef{}, false
}
