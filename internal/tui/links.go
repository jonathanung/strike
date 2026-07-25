package tui

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// withHyperlink wraps styled display text in an OSC 8 hyperlink when uri is set.
// Terminals that support OSC 8 make the span clickable; unsupported terminals
// ignore the sequences and still show the styled text.
func withHyperlink(uri, styled string) string {
	if uri == "" || styled == "" {
		return styled
	}
	return ansi.SetHyperlink(uri) + styled + ansi.ResetHyperlink()
}

// displayURI returns an OSC 8 target for text that is a URL or file path.
// Relative paths resolve against linkBase when non-empty.
func displayURI(text, linkBase string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\n\r\t") {
		return ""
	}
	// Bare URLs (and URL-ish titles from webfetch).
	if strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "http://") {
		if u, err := url.Parse(text); err == nil && u.Host != "" {
			return u.String()
		}
		return ""
	}
	// webfetch titles: "https://example.com/x (text/html)"
	if i := strings.Index(text, " ("); i > 0 {
		if uri := displayURI(text[:i], linkBase); uri != "" {
			return uri
		}
	}
	if !looksLikePath(text) {
		return ""
	}
	path := text
	if strings.HasPrefix(path, "~"+string(filepath.Separator)) || path == "~" {
		// Leave home-relative paths alone; terminals rarely resolve ~ in file://.
		return ""
	}
	if !filepath.IsAbs(path) {
		if linkBase == "" {
			return ""
		}
		path = filepath.Join(linkBase, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return pathToFileURI(abs)
}

func pathToFileURI(abs string) string {
	abs = filepath.Clean(abs)
	// url.URL handles escaping; Path must be slash-separated.
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	// On Unix Abs paths are /foo → file:///foo via Path.
	return u.String()
}

// looksLikePath reports whether s is plausibly a filesystem path worth linking
// (not a shell command or free-form title).
func looksLikePath(s string) bool {
	if s == "" || strings.Contains(s, "://") {
		return false
	}
	// Reject obvious commands / multi-word titles.
	if strings.ContainsAny(s, " \t") {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	if filepath.IsAbs(s) {
		return true
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}
	// Relative project paths: must contain a separator or a file extension.
	if strings.ContainsRune(s, '/') || strings.ContainsRune(s, '\\') {
		return !strings.HasPrefix(s, "-")
	}
	// Single segment with a common source extension (e.g. main.go).
	ext := strings.ToLower(filepath.Ext(s))
	switch ext {
	case ".go", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".rs",
		".ts", ".tsx", ".js", ".jsx", ".py", ".rb", ".c", ".h", ".cpp",
		".css", ".html", ".sh", ".mod", ".sum":
		return len(s) > len(ext) && !strings.ContainsFunc(s[:len(s)-len(ext)], func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.')
		})
	default:
		return false
	}
}

// osc8URIAtCell walks a single rendered line (ANSI included) and returns the
// active OSC 8 URI at visual column col (0-based), or "".
func osc8URIAtCell(line string, col int) string {
	if col < 0 || line == "" {
		return ""
	}
	var (
		uri    string
		visual int
		i      int
	)
	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == ']' {
			// OSC: ESC ] … BEL or ESC ]
			if strings.HasPrefix(line[i:], "\x1b]8;") {
				bodyStart := i + len("\x1b]8;")
				end, term := findOSCTerminator(line, bodyStart)
				if end < 0 {
					break
				}
				payload := line[bodyStart:end] // params;uri
				if semi := strings.LastIndexByte(payload, ';'); semi >= 0 {
					uri = payload[semi+1:]
				} else {
					uri = ""
				}
				i = end + term
				continue
			}
			if j := skipANSIEscape(line, i); j > i {
				i = j
				continue
			}
		}
		if line[i] == '\x1b' {
			if j := skipANSIEscape(line, i); j > i {
				i = j
				continue
			}
		}
		// C1 OSC 8: \x9d 8 ; params ; uri \x9c
		if line[i] == '\x9d' && i+1 < len(line) && line[i+1] == '8' {
			bodyStart := i + len("\x9d8;")
			end := strings.IndexByte(line[bodyStart:], '\x9c')
			if end < 0 {
				break
			}
			payload := line[bodyStart : bodyStart+end]
			if semi := strings.LastIndexByte(payload, ';'); semi >= 0 {
				uri = payload[semi+1:]
			} else {
				uri = ""
			}
			i = bodyStart + end + 1
			continue
		}
		if line[i] == '\n' || line[i] == '\r' {
			break
		}
		// Advance one visual cell (rune width via ansi.StringWidth of one rune).
		_, size := decodeRuneAt(line, i)
		if size <= 0 {
			break
		}
		w := ansi.StringWidth(line[i : i+size])
		if w <= 0 {
			w = 1
		}
		if visual+w > col && visual <= col {
			return uri
		}
		visual += w
		i += size
		if visual > col {
			return ""
		}
	}
	return ""
}

func decodeRuneAt(s string, i int) (r rune, size int) {
	if i >= len(s) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(s[i:])
}

// findOSCTerminator returns the index of BEL or ST after start, and the
// terminator byte length (1 for BEL, 2 for ESC\).
func findOSCTerminator(s string, start int) (end, term int) {
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '\x07':
			return j, 1
		case '\x1b':
			if j+1 < len(s) && s[j+1] == '\\' {
				return j, 2
			}
		}
	}
	return -1, 0
}

func skipANSIEscape(s string, i int) int {
	if i >= len(s) || s[i] != '\x1b' {
		return i
	}
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[': // CSI
		j := i + 2
		for j < len(s) {
			c := s[j]
			j++
			if c >= 0x40 && c <= 0x7e {
				return j
			}
		}
		return j
	case ']': // OSC ... BEL or ST
		j := i + 2
		for j < len(s) {
			if s[j] == '\x07' {
				return j + 1
			}
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return j
	case 'P', 'X', '^', '_': // DCS/SOS/PM/APC ... ST
		j := i + 2
		for j < len(s) {
			if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return j
	default:
		// Two-byte escapes (e.g. ESC 7).
		return i + 2
	}
}
