package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/host"
)

// fileMentionSpan is one @path token in composer/submit text.
type fileMentionSpan struct {
	Path  string // project-relative, slash-separated, no leading @
	Start int    // byte offset in original text
	End   int    // exclusive byte offset covering @path
}

// resolveCommandPathArg normalizes a slash-command file path argument.
// Plain paths are returned unchanged. A leading @ (composer-style mention)
// is stripped; bare "@", empty after strip, or path-escape forms error as
// unresolved mentions.
func resolveCommandPathArg(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, "@") {
		return raw, nil
	}
	path := strings.TrimPrefix(raw, "@")
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimPrefix(path, "./")
	if path == "" || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return "", fmt.Errorf("unresolved file mention %q", raw)
	}
	return path, nil
}

// findFileMentions returns non-overlapping @path tokens. A mention starts at
// '@' that is at the beginning of the string or after whitespace/newline, and
// continues through path runes (letters, digits, / \ . - _ + ~).
func findFileMentions(s string) []fileMentionSpan {
	if s == "" {
		return nil
	}
	var out []fileMentionSpan
	i := 0
	for i < len(s) {
		at := strings.IndexByte(s[i:], '@')
		if at < 0 {
			break
		}
		at += i
		if at > 0 {
			prev, _ := utf8.DecodeLastRuneInString(s[:at])
			if prev == utf8.RuneError || !unicode.IsSpace(prev) {
				i = at + 1
				continue
			}
		}
		pathStart := at + 1
		if pathStart >= len(s) {
			break
		}
		pathEnd := pathStart
		for pathEnd < len(s) {
			r, size := utf8.DecodeRuneInString(s[pathEnd:])
			if size <= 0 || r == utf8.RuneError && size == 1 || !isFileMentionPathRune(r) {
				break
			}
			pathEnd += size
		}
		if pathEnd == pathStart {
			i = at + 1
			continue
		}
		path := strings.ReplaceAll(s[pathStart:pathEnd], "\\", "/")
		path = strings.TrimPrefix(path, "./")
		if path == "" || path == "." || path == ".." || strings.HasPrefix(path, "../") {
			i = pathEnd
			continue
		}
		out = append(out, fileMentionSpan{Path: path, Start: at, End: pathEnd})
		i = pathEnd
	}
	return out
}

// expandFileMentions resolves @path tokens via host.Files.ReadScoped and
// appends attachment blocks to the model-visible text. The caller keeps the
// original text for history/display. notices are skip/truncate messages.
func expandFileMentions(text string, files host.Files) (expanded string, notices []string) {
	spans := findFileMentions(text)
	if len(spans) == 0 || files == nil {
		return text, nil
	}
	seen := make(map[string]struct{}, len(spans))
	var blocks strings.Builder
	for _, sp := range spans {
		if _, ok := seen[sp.Path]; ok {
			continue
		}
		seen[sp.Path] = struct{}{}
		fc, err := files.ReadScoped(sp.Path)
		if err != nil {
			notices = append(notices, fmt.Sprintf("@%s: %v", sp.Path, err))
			continue
		}
		displayPath := fc.Path
		if displayPath == "" {
			displayPath = sp.Path
		}
		if fc.Skip {
			msg := fc.Notice
			if msg == "" {
				msg = "skipped"
			}
			notices = append(notices, fmt.Sprintf("@%s: %s", displayPath, msg))
			continue
		}
		if fc.Notice != "" {
			notices = append(notices, fmt.Sprintf("@%s: %s", displayPath, fc.Notice))
		}
		kind := "file"
		if strings.HasSuffix(displayPath, "/") {
			kind = "folder"
		}
		fmt.Fprintf(&blocks, "\n\n--- %s: %s ---\n%s", kind, displayPath, fc.Content)
		if !strings.HasSuffix(fc.Content, "\n") {
			blocks.WriteByte('\n')
		}
		fmt.Fprintf(&blocks, "--- end %s: %s ---", kind, displayPath)
	}
	if blocks.Len() == 0 {
		return text, notices
	}
	return text + blocks.String(), notices
}

// stripFileMentionAttachments removes model-facing @file/@folder attachment
// fences that expandFileMentions appends. Used so the transcript keeps the
// human prompt (@path tokens) without dumping file bodies into chat.
func stripFileMentionAttachments(text string) string {
	if text == "" || !strings.Contains(text, "--- ") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	rest := text
	for rest != "" {
		start, prefixLen := indexFileMentionAttachmentStart(rest)
		if start < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		chunk := rest[start+prefixLen:] // begins at "--- "
		end := endFileMentionAttachment(chunk)
		if end < 0 {
			// Not a well-formed attachment fence; keep the rest verbatim.
			b.WriteString(rest[start:])
			break
		}
		rest = strings.TrimLeft(chunk[end:], "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// indexFileMentionAttachmentStart finds the next expandFileMentions fence.
// Returns byte index of the run of leading newlines before "--- ", and how
// many bytes to skip to land on "--- ". (-1, 0) when none.
func indexFileMentionAttachmentStart(s string) (idx, prefixLen int) {
	// expandFileMentions writes "\n\n--- kind: path ---"; after stripping one
	// block a single "\n--- " may remain between siblings.
	for _, nl := range []string{"\n\n--- ", "\n--- "} {
		i := 0
		for {
			at := strings.Index(s[i:], nl)
			if at < 0 {
				break
			}
			at += i
			chunk := s[at+len(nl)-len("--- "):] // "--- …"
			if endFileMentionAttachment(chunk) >= 0 {
				return at, len(nl) - len("--- ")
			}
			i = at + 1
		}
	}
	if strings.HasPrefix(s, "--- ") && endFileMentionAttachment(s) >= 0 {
		return 0, 0
	}
	return -1, 0
}

// endFileMentionAttachment reports the byte end (exclusive) of a leading
// "--- file|folder: path ---…--- end file|folder: path ---" block, or -1.
func endFileMentionAttachment(s string) int {
	if !strings.HasPrefix(s, "--- ") {
		return -1
	}
	headerEnd := strings.Index(s, " ---\n")
	if headerEnd < 0 {
		// Allow header ending at EOF without body newline (degenerate).
		headerEnd = strings.Index(s, " ---")
		if headerEnd < 0 || headerEnd+4 > len(s) {
			return -1
		}
		// "--- kind: path ---" only
		header := s[len("--- "):headerEnd]
		kind, _, ok := strings.Cut(header, ": ")
		if !ok || (kind != "file" && kind != "folder") {
			return -1
		}
		return headerEnd + 4
	}
	header := s[len("--- "):headerEnd]
	kind, path, ok := strings.Cut(header, ": ")
	if !ok || (kind != "file" && kind != "folder") || path == "" {
		return -1
	}
	bodyStart := headerEnd + len(" ---\n")
	closeMark := "\n--- end " + kind + ": " + path + " ---"
	// Body may omit the leading newline when content already ended with \n
	// before the close mark was written without an extra blank line — match
	// either "\n--- end …" or a close mark flush against bodyStart.
	if rel := strings.Index(s[bodyStart:], closeMark); rel >= 0 {
		return bodyStart + rel + len(closeMark)
	}
	alt := "--- end " + kind + ": " + path + " ---"
	if strings.HasPrefix(s[bodyStart:], alt) {
		return bodyStart + len(alt)
	}
	if rel := strings.Index(s[bodyStart:], "\n"+alt); rel >= 0 {
		return bodyStart + rel + 1 + len(alt)
	}
	return -1
}
