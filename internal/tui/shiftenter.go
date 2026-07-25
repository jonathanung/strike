package tui

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// altEnter is Alt+Enter as delivered to Bubble Tea after rewrite (KeyEnter+Alt).
var altEnter = []byte("\x1b\r")

// altSemicolon is Alt+; — the wire form for ToggleOrientation after ctrl+;
// CSI is rewritten (Bubble Tea has no native "ctrl+;" KeyType).
var altSemicolon = []byte("\x1b;")

// Enhanced-keyboard enable/disable sequences written at program start/exit:
//
//	enable  modifyOtherKeys level 2: ESC [ > 4 ; 2 m
//	disable modifyOtherKeys:         ESC [ > 4 ; 0 m
//	enable  Kitty keyboard (flags=1, disambiguate): ESC [ > 1 u
//	disable Kitty keyboard:          ESC [ < u
var (
	enableModifyOtherKeys  = []byte("\x1b[>4;2m")
	disableModifyOtherKeys = []byte("\x1b[>4;0m")
	enableKittyKeyboard    = []byte("\x1b[>1u")
	disableKittyKeyboard   = []byte("\x1b[<u")
)

// fileReader is the subset of *os.File needed so Bubble Tea can enter raw mode
// (term.File: ReadWriteCloser + Fd) and cancelreader can epoll the fd
// (cancelreader.File also wants Name).
type fileReader interface {
	io.Reader
	Fd() uintptr
}

// WrapInput returns a reader that normalizes enhanced-keyboard CSI sequences:
// Shift/Alt+Enter → Alt+Enter (ESC+\r), and Ctrl+letter (Kitty CSI-u /
// xterm modifyOtherKeys) → legacy control bytes. When r is file-like (Fd), the
// wrapper forwards Fd/Name/Close/Write so Bubble Tea still enables raw mode and
// cancelreader can interrupt reads. A nil reader is returned unchanged.
func WrapInput(r io.Reader) io.Reader {
	if r == nil {
		return nil
	}
	if f, ok := r.(fileReader); ok {
		return &shiftEnterFile{
			shiftEnterReader: shiftEnterReader{r: r},
			file:             f,
		}
	}
	return &shiftEnterReader{r: r}
}

// shiftEnterFile embeds rewrite logic and preserves TTY file semantics for
// Bubble Tea (term.File) and muesli/cancelreader (File).
type shiftEnterFile struct {
	shiftEnterReader
	file fileReader
}

func (s *shiftEnterFile) Fd() uintptr { return s.file.Fd() }

func (s *shiftEnterFile) Close() error {
	if c, ok := s.file.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (s *shiftEnterFile) Write(p []byte) (int, error) {
	if w, ok := s.file.(io.Writer); ok {
		return w.Write(p)
	}
	return 0, io.ErrClosedPipe
}

func (s *shiftEnterFile) Name() string {
	if n, ok := s.file.(interface{ Name() string }); ok {
		return n.Name()
	}
	return ""
}

// EnableEnhancedKeys asks the terminal for modifyOtherKeys level 2 and Kitty
// progressive enhancement so Shift+Enter and Ctrl+letter are distinguishable
// from bare keys. The returned function restores the prior modes; it is safe to
// call multiple times. A nil writer is a no-op.
//
// Note: these sequences are typically written before the alt screen is entered.
// Bubble Tea has no clean post-altscreen hook; terminals still accept the modes.
func EnableEnhancedKeys(w io.Writer) (restore func()) {
	noop := func() {}
	if w == nil {
		return noop
	}
	_, _ = w.Write(enableModifyOtherKeys)
	_, _ = w.Write(enableKittyKeyboard)
	var done bool
	return func() {
		if done {
			return
		}
		done = true
		_, _ = w.Write(disableModifyOtherKeys)
		_, _ = w.Write(disableKittyKeyboard)
	}
}

// shiftEnterReader buffers incomplete CSI prefixes across Read calls and
// normalizes complete enhanced-key sequences (Shift/Alt+Enter, Ctrl+letter).
type shiftEnterReader struct {
	r   io.Reader
	buf []byte // unconsumed input, possibly a partial CSI prefix
	out []byte // pending rewritten/passthrough bytes for the next Read
}

func (s *shiftEnterReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.out) > 0 {
		n := copy(p, s.out)
		s.out = s.out[n:]
		return n, nil
	}

	tmp := make([]byte, len(p))
	n, err := s.r.Read(tmp)
	if n > 0 {
		s.buf = append(s.buf, tmp[:n]...)
	}
	s.out = s.rewrite()
	if len(s.out) == 0 && err != nil && len(s.buf) > 0 {
		// Incomplete CSI prefix at EOF/error: pass through rather than drop.
		s.out = s.buf
		s.buf = nil
	}
	if len(s.out) == 0 {
		return 0, err
	}
	m := copy(p, s.out)
	s.out = s.out[m:]
	if len(s.out) > 0 {
		// Deliver remaining rewritten bytes on the next Read; suppress err
		// until the buffer is drained so callers see all data first.
		return m, nil
	}
	return m, err
}

// rewrite consumes complete sequences from s.buf into a passthrough/rewrite
// buffer, leaving only an incomplete CSI/OSC prefix (if any) in s.buf.
func (s *shiftEnterReader) rewrite() []byte {
	if len(s.buf) == 0 {
		return nil
	}
	var out bytes.Buffer
	i := 0
	for i < len(s.buf) {
		if s.buf[i] != 0x1b {
			// Drop leaked OSC payloads whose leading ESC was consumed elsewhere
			// (e.g. "]11;rgb:0000/0000/0000\").
			if n := scanLeakedOSC(s.buf[i:]); n > 0 {
				i += n
				continue
			}
			out.WriteByte(s.buf[i])
			i++
			continue
		}
		// OSC (ESC ] … BEL/ST): drop terminal replies (bg color, title, …).
		if end, osc, incomplete := scanOSC(s.buf[i:]); osc || incomplete {
			if incomplete {
				break
			}
			// Drop complete OSC — never inject into the composer.
			i += end
			continue
		}
		end, csi, incomplete := scanCSI(s.buf[i:])
		if incomplete {
			// Hold incomplete CSI so a split Read can complete it.
			break
		}
		if !csi {
			// Bare ESC or ESC not followed by '[': emit immediately.
			out.WriteByte(s.buf[i])
			i++
			continue
		}
		seq := s.buf[i : i+end]
		rewritten, drop, handled := classifyEnhanced(seq)
		if handled && drop {
			// Consume release events with no output.
		} else if handled {
			out.Write(rewritten)
		} else {
			out.Write(seq)
		}
		i += end
	}
	s.buf = append([]byte(nil), s.buf[i:]...)
	return out.Bytes()
}

// scanCSI examines b starting at ESC. It returns:
//
//	end         — bytes consumed when a complete CSI is found
//	csi         — true if this is a CSI sequence (ESC [ … final)
//	incomplete  — true if the stream ends mid-CSI (hold for more input)
//
// Bare ESC alone is not incomplete CSI (caller emits immediately). ESC not
// followed by '[' is not CSI.
func scanCSI(b []byte) (end int, csi bool, incomplete bool) {
	if len(b) == 0 || b[0] != 0x1b {
		return 0, false, false
	}
	if len(b) == 1 {
		// Bare ESC: deliver immediately (Escape/interrupt), do not hold.
		return 1, false, false
	}
	if b[1] != '[' {
		return 1, false, false
	}
	// After ESC [, consume parameter/intermediate bytes 0x20-0x3F; final is 0x40-0x7E.
	for i := 2; i < len(b); i++ {
		c := b[i]
		if c >= 0x40 && c <= 0x7e {
			return i + 1, true, false
		}
		if c < 0x20 || c > 0x3f {
			// Invalid CSI byte before final: not a well-formed CSI; caller
			// will emit ESC and rescan (treat as non-CSI from this ESC).
			return 1, false, false
		}
	}
	// Stream ended mid-params: hold.
	return 0, true, true
}

// scanOSC examines b starting at ESC. It returns:
//
//	end         — bytes consumed when a complete OSC is found
//	osc         — true if this is an OSC sequence (ESC ] … BEL or ST)
//	incomplete  — true if the stream ends mid-OSC (hold for more input)
//
// OSC replies (e.g. bg color "ESC ] 11 ; rgb:… BEL") must not reach the
// composer. ST is ESC \ or the C1 0x9c byte.
func scanOSC(b []byte) (end int, osc bool, incomplete bool) {
	if len(b) == 0 || b[0] != 0x1b {
		return 0, false, false
	}
	if len(b) == 1 {
		return 1, false, false
	}
	if b[1] != ']' {
		return 0, false, false
	}
	for i := 2; i < len(b); i++ {
		switch b[i] {
		case 0x07: // BEL
			return i + 1, true, false
		case 0x9c: // C1 ST
			return i + 1, true, false
		case 0x1b: // ESC \ (ST) or interrupted
			if i+1 < len(b) && b[i+1] == '\\' {
				return i + 2, true, false
			}
			// ESC without '\': treat payload up to (not including) ESC as OSC end
			// so a following CSI can be rescanned. Drop what we have.
			if i+1 >= len(b) {
				return 0, true, true
			}
			return i, true, false
		}
	}
	return 0, true, true
}

// scanLeakedOSC matches an OSC payload whose leading ESC was already consumed
// (common when KeyEsc ate the introducer): "]11;rgb:…\" or "]11;rgb:…BEL".
// Returns bytes to drop, or 0 if b does not start a leaked OSC reply.
func scanLeakedOSC(b []byte) int {
	if len(b) < 4 || b[0] != ']' {
		return 0
	}
	// Require "]<digits>;" so normal ']' typing is unaffected.
	i := 1
	if i >= len(b) || b[i] < '0' || b[i] > '9' {
		return 0
	}
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i >= len(b) || b[i] != ';' {
		return 0
	}
	i++
	for i < len(b) {
		switch b[i] {
		case 0x07, 0x9c:
			return i + 1
		case '\\':
			return i + 1
		case 0x1b:
			if i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
			return i
		case '\n', '\r':
			return i
		}
		// Bound runaway matches (OSC payloads are short).
		if i > 128 {
			return 0
		}
		i++
	}
	// Incomplete leaked OSC: hold nothing at this layer (caller emits bytes);
	// complete forms are what appear after submit. Drop nothing if unterminated
	// so we don't eat user "]11;foo" mid-type forever — only terminated forms.
	return 0
}

// classifyEnhanced maps known enhanced-keyboard CSI sequences to legacy bytes.
// handled=false means passthrough the original sequence unchanged.
// drop=true (with handled) means consume with no output (e.g. key-release).
func classifyEnhanced(seq []byte) (out []byte, drop bool, handled bool) {
	if len(seq) < 3 || seq[0] != 0x1b || seq[1] != '[' {
		return nil, false, false
	}
	final := seq[len(seq)-1]
	params := string(seq[2 : len(seq)-1])

	var code, mods, event int
	var hasEvent bool
	switch final {
	case 'u':
		// Kitty CSI-u: code | code;mods | code;mods:event
		fields := strings.Split(params, ";")
		if len(fields) < 1 || fields[0] == "" {
			return nil, false, false
		}
		var ok bool
		code, ok = parsePrimary(fields[0])
		if !ok {
			return nil, false, false
		}
		mods = 1
		if len(fields) >= 2 {
			mods, event, hasEvent, ok = parseModsEvent(fields[1])
			if !ok {
				return nil, false, false
			}
		}
	case '~':
		// xterm modifyOtherKeys: 27;mods;code
		fields := strings.Split(params, ";")
		if len(fields) != 3 {
			return nil, false, false
		}
		lead, err1 := strconv.Atoi(fields[0])
		m, err2 := strconv.Atoi(fields[1])
		c, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil || lead != 27 {
			return nil, false, false
		}
		mods, code = m, c
	default:
		return nil, false, false
	}

	// event==3 (release) → drop; missing/1/2 continue; other → passthrough
	if hasEvent {
		switch event {
		case 3:
			return nil, true, true
		case 1, 2:
			// press / repeat
		default:
			return nil, false, false
		}
	}

	// Kitty/xterm mods are 1-based (1 = none). mods<=0 is invalid; avoid
	// underflow on bits := mods-1 and passthrough the original sequence.
	if mods <= 0 {
		return nil, false, false
	}

	bits := mods - 1
	shift := bits&1 != 0
	alt := bits&2 != 0
	ctrl := bits&4 != 0

	// Bare Escape (CSI-u code 27, no shift/alt/ctrl) → 0x1b so KeyEsc matches.
	// mods==1 is "none" in the 1-based Kitty/xterm scheme.
	if code == 27 && !shift && !alt && !ctrl {
		return []byte{0x1b}, false, true
	}
	// Shift/Alt+Enter (no ctrl) → Alt+Enter for Newline binding.
	// Ctrl+Enter (code 13 with ctrl) is intentionally not rewritten.
	if code == 13 && (shift || alt) && !ctrl {
		return altEnter, false, true
	}
	// Ctrl+; → Alt+; for ToggleOrientation (no native ctrl+; KeyType).
	if ctrl && !shift && !alt && code == int(';') {
		return altSemicolon, false, true
	}
	// Ctrl+letter → legacy control byte (code & 0x1f).
	if ctrl && isLetterCode(code) {
		return []byte{byte(code & 0x1f)}, false, true
	}
	return nil, false, false
}

// parsePrimary parses the first numeric field of a Kitty CSI-u parameter
// (before any ':' sub-params).
func parsePrimary(field string) (int, bool) {
	primary, _, _ := strings.Cut(field, ":")
	if primary == "" {
		return 0, false
	}
	n, err := strconv.Atoi(primary)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseModsEvent parses a Kitty mods field, optionally with :event
// (e.g. "5", "5:1", "5:3").
func parseModsEvent(field string) (mods, event int, hasEvent, ok bool) {
	primary, rest, found := strings.Cut(field, ":")
	if primary == "" {
		return 0, 0, false, false
	}
	m, err := strconv.Atoi(primary)
	if err != nil {
		return 0, 0, false, false
	}
	if !found || rest == "" {
		return m, 0, false, true
	}
	// Only the first sub-param after ':' is the event type.
	evStr, _, _ := strings.Cut(rest, ":")
	ev, err := strconv.Atoi(evStr)
	if err != nil {
		return 0, 0, false, false
	}
	return m, ev, true, true
}

func isLetterCode(code int) bool {
	return (code >= 'A' && code <= 'Z') || (code >= 'a' && code <= 'z')
}
