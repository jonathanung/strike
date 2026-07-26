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
