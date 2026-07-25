package tui

import (
	"bytes"
	"io"
)

// Shift+Enter terminal sequences rewritten to ESC+\r (Alt+Enter), which Bubble
// Tea delivers as KeyEnter+Alt — matching the Newline binding.
var (
	// Kitty CSI-u Shift+Enter: ESC [ 13 ; 2 u
	shiftEnterKitty = []byte("\x1b[13;2u")
	// xterm modifyOtherKeys Shift+Enter: ESC [ 27 ; 2 ; 13 ~
	shiftEnterXterm = []byte("\x1b[27;2;13~")
	// Alt+Enter as delivered to Bubble Tea after rewrite.
	altEnter = []byte("\x1b\r")
)

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

// WrapInput returns a reader that rewrites known Shift+Enter CSI sequences to
// Alt+Enter. When r is file-like (Fd), the wrapper forwards Fd/Name/Close/Write
// so Bubble Tea still enables raw mode and cancelreader can interrupt reads.
// A nil reader is returned unchanged.
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
// progressive enhancement so Shift+Enter is distinguishable from Enter. The
// returned function restores the prior modes; it is safe to call multiple times.
// A nil writer is a no-op.
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
// rewrites only complete matches of the two Shift+Enter sequences above.
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
// buffer, leaving only an incomplete CSI prefix (if any) in s.buf.
func (s *shiftEnterReader) rewrite() []byte {
	if len(s.buf) == 0 {
		return nil
	}
	var out bytes.Buffer
	i := 0
	for i < len(s.buf) {
		if s.buf[i] != 0x1b {
			out.WriteByte(s.buf[i])
			i++
			continue
		}
		rest := s.buf[i:]
		if bytes.HasPrefix(rest, shiftEnterKitty) {
			out.Write(altEnter)
			i += len(shiftEnterKitty)
			continue
		}
		if bytes.HasPrefix(rest, shiftEnterXterm) {
			out.Write(altEnter)
			i += len(shiftEnterXterm)
			continue
		}
		// Hold back a partial prefix of either sequence so a split Read can
		// complete it. Anything else starting with ESC is passed through.
		if isPartialShiftEnter(rest) {
			break
		}
		out.WriteByte(s.buf[i])
		i++
	}
	s.buf = append([]byte(nil), s.buf[i:]...)
	return out.Bytes()
}

// isPartialShiftEnter reports whether b is a proper prefix of a known
// Shift+Enter CSI sequence. Bare ESC alone is not partial so Escape/interrupt
// is delivered immediately; holding starts only at ESC [ (CSI introducer).
func isPartialShiftEnter(b []byte) bool {
	if len(b) < 2 {
		return false
	}
	if b[0] != 0x1b || b[1] != '[' {
		return false
	}
	return bytes.HasPrefix(shiftEnterKitty, b) || bytes.HasPrefix(shiftEnterXterm, b)
}
