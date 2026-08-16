package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestWrapInputNil(t *testing.T) {
	if got := WrapInput(nil); got != nil {
		t.Errorf("WrapInput(nil) = %v, want nil", got)
	}
}

// TestWrapInputEnhancedRewrite pins byte-level rewrites under EnableEnhancedKeys.
//
// Model.Update tests that inject tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} (and similar) only
// validate routing after Bubble Tea has already decoded a key. These WrapInput
// tests cover the terminal wire format: Kitty CSI-u and xterm modifyOtherKeys
// sequences that must become legacy control bytes (or Alt+Enter) before the
// tea input parser sees them.
func TestWrapInputEnhancedRewrite(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// --- Shift+Enter (both protocols) → ESC+\r ---
		{
			name: "Kitty CSI-u Shift+Enter",
			in:   "\x1b[13;2u",
			want: "\x1b\r",
		},
		{
			name: "xterm modifyOtherKeys Shift+Enter",
			in:   "\x1b[27;2;13~",
			want: "\x1b\r",
		},
		// --- Alt+Enter (both protocols) → ESC+\r ---
		{
			name: "Kitty CSI-u Alt+Enter",
			in:   "\x1b[13;3u",
			want: "\x1b\r",
		},
		{
			name: "xterm modifyOtherKeys Alt+Enter",
			in:   "\x1b[27;3;13~",
			want: "\x1b\r",
		},
		// --- Ctrl+Enter (both protocols) → passthrough (not letter, not Shift/Alt+Enter) ---
		{
			name: "Ctrl+Enter Kitty CSI-u passthrough",
			in:   "\x1b[13;5u",
			want: "\x1b[13;5u",
		},
		{
			name: "Ctrl+Enter xterm modifyOtherKeys passthrough",
			in:   "\x1b[27;5;13~",
			want: "\x1b[27;5;13~",
		},
		// --- Invalid mods (<=0): passthrough, no underflow rewrite ---
		{
			name: "invalid mods 0 Kitty passthrough",
			in:   "\x1b[99;0u",
			want: "\x1b[99;0u",
		},
		// --- Ctrl+letter CSI → legacy control byte (code & 0x1f) ---
		// Wire-level Ctrl+C/P coverage: Update(KeyCtrlC) tests are routing-only.
		{
			name: "Ctrl+C Kitty CSI-u",
			in:   "\x1b[99;5u",
			want: "\x03",
		},
		{
			name: "Ctrl+C xterm modifyOtherKeys",
			in:   "\x1b[27;5;99~",
			want: "\x03",
		},
		{
			name: "Ctrl+C uppercase Kitty CSI-u",
			in:   "\x1b[67;5u",
			want: "\x03",
		},
		{
			name: "Ctrl+P Kitty CSI-u",
			in:   "\x1b[112;5u",
			want: "\x10",
		},
		{
			name: "Ctrl+P xterm modifyOtherKeys",
			in:   "\x1b[27;5;112~",
			want: "\x10",
		},
		// Sample app letters (at least one protocol each): h j k l d
		{
			name: "Ctrl+H Kitty",
			in:   "\x1b[104;5u",
			want: "\x08",
		},
		// Ctrl+J → Alt+j (not 0x0a); bare LF is also cycle via KeyCtrlJ (#324).
		{
			name: "Ctrl+J Kitty",
			in:   "\x1b[106;5u",
			want: "\x1bj",
		},
		{
			name: "Ctrl+J uppercase Kitty",
			in:   "\x1b[74;5u",
			want: "\x1bj",
		},
		{
			name: "Ctrl+J xterm modifyOtherKeys",
			in:   "\x1b[27;5;106~",
			want: "\x1bj",
		},
		{
			name: "Ctrl+K Kitty",
			in:   "\x1b[107;5u",
			want: "\x0b",
		},
		{
			name: "Ctrl+L Kitty",
			in:   "\x1b[108;5u",
			want: "\x0c",
		},
		{
			name: "Ctrl+D Kitty",
			in:   "\x1b[100;5u",
			want: "\x04",
		},
		{
			name: "Ctrl+H xterm",
			in:   "\x1b[27;5;104~",
			want: "\x08",
		},
		// --- Kitty event types: press (:1) / repeat (:2) rewrite; release (:3) drop ---
		{
			name: "Kitty Ctrl+C press event :1",
			in:   "\x1b[99;5:1u",
			want: "\x03",
		},
		{
			name: "Kitty Ctrl+C repeat event :2",
			in:   "\x1b[99;5:2u",
			want: "\x03",
		},
		{
			name: "Kitty Ctrl+C release event :3 dropped",
			in:   "\x1b[99;5:3u",
			want: "",
		},
		{
			name: "Kitty release in surrounding text",
			in:   "a\x1b[99;5:3ub",
			want: "ab",
		},
		// --- Legacy control bytes and unknown CSI passthrough ---
		{
			name: "legacy Ctrl+C passthrough",
			in:   "\x03",
			want: "\x03",
		},
		{
			name: "legacy Ctrl+P passthrough",
			in:   "\x10",
			want: "\x10",
		},
		{
			name: "unrelated CSI arrow passthrough",
			in:   "\x1b[A",
			want: "\x1b[A",
		},
		{
			name: "unrelated CSI with text",
			in:   "up\x1b[Adown",
			want: "up\x1b[Adown",
		},
		// --- Mixed / multi-sequence ---
		{
			name: "mixed text + Ctrl+C CSI + text",
			in:   "ab\x1b[99;5ucd",
			want: "ab\x03cd",
		},
		{
			name: "multi-sequence Shift+Enter + Ctrl+C + Ctrl+P",
			in:   "\x1b[13;2u\x1b[99;5u\x1b[112;5u",
			want: "\x1b\r\x03\x10",
		},
		// --- Existing plain / edge cases kept green ---
		{
			name: "plain CR unchanged",
			in:   "\r",
			want: "\r",
		},
		{
			name: "plain LF unchanged",
			in:   "\n",
			want: "\n",
		},
		{
			name: "mixed text and Kitty Shift+Enter",
			in:   "hello\x1b[13;2uworld",
			want: "hello\x1b\rworld",
		},
		{
			name: "mixed text and xterm Shift+Enter",
			in:   "a\x1b[27;2;13~b",
			want: "a\x1b\rb",
		},
		{
			name: "bare ESC passthrough",
			in:   "\x1b",
			want: "\x1b",
		},
		{
			name: "empty read",
			in:   "",
			want: "",
		},
		{
			name: "multi Shift+Enter sequences in one buffer",
			in:   "\x1b[13;2u\x1b[27;2;13~\x1b[13;2u",
			want: "\x1b\r\x1b\r\x1b\r",
		},
		{
			name: "multi Shift+Enter with text between",
			in:   "x\x1b[13;2uy\x1b[27;2;13~z",
			want: "x\x1b\ry\x1b\rz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(WrapInput(strings.NewReader(tt.in)))
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("rewrite = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestShiftEnterRewrite keeps the historical name as an alias entry point so
// -run TestShiftEnter still exercises the enhanced rewrite table.
func TestShiftEnterRewrite(t *testing.T) {
	TestWrapInputEnhancedRewrite(t)
}

// chunkReader yields successive chunks on each Read, then EOF.
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.i >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.i])
	c.i++
	return n, nil
}

func TestShiftEnterPartialCSIAcrossReads(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{
			name: "Kitty Shift+Enter split mid-sequence",
			chunks: [][]byte{
				[]byte("\x1b[13"),
				[]byte(";2u"),
			},
			want: "\x1b\r",
		},
		{
			name: "Kitty CSI held then completed one byte at a time",
			// Bare ESC alone is not held (Escape must pass through). Holding
			// starts at ESC[; split from the CSI introducer onward.
			chunks: func() [][]byte {
				seq := []byte("\x1b[13;2u")
				out := [][]byte{seq[:2]} // ESC [
				for _, b := range seq[2:] {
					out = append(out, []byte{b})
				}
				return out
			}(),
			want: "\x1b\r",
		},
		{
			name: "xterm Shift+Enter split mid-sequence",
			chunks: [][]byte{
				[]byte("\x1b[27;2"),
				[]byte(";13~"),
			},
			want: "\x1b\r",
		},
		{
			name: "text then partial Shift+Enter then completion",
			chunks: [][]byte{
				[]byte("hi"),
				[]byte("\x1b[1"),
				[]byte("3;2u"),
				[]byte("!"),
			},
			want: "hi\x1b\r!",
		},
		{
			name: "partial CSI held then diverge passthrough",
			chunks: [][]byte{
				[]byte("\x1b["),
				[]byte("A"),
			},
			want: "\x1b[A",
		},
		// Enhanced: Ctrl+letter CSI held across split reads.
		{
			name: "Kitty Ctrl+C split mid-sequence",
			chunks: [][]byte{
				[]byte("\x1b[99"),
				[]byte(";5u"),
			},
			want: "\x03",
		},
		{
			name: "xterm Ctrl+P split mid-sequence",
			chunks: [][]byte{
				[]byte("\x1b[27;5"),
				[]byte(";112~"),
			},
			want: "\x10",
		},
		{
			name: "Kitty Ctrl+C one byte at a time from CSI introducer",
			chunks: func() [][]byte {
				seq := []byte("\x1b[99;5u")
				out := [][]byte{seq[:2]} // ESC [
				for _, b := range seq[2:] {
					out = append(out, []byte{b})
				}
				return out
			}(),
			want: "\x03",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(WrapInput(&chunkReader{chunks: tt.chunks}))
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("split rewrite = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBareESCDeliveredImmediately(t *testing.T) {
	// Bare ESC must not be held waiting for more bytes (Escape/interrupt).
	r := WrapInput(&chunkReader{chunks: [][]byte{[]byte("\x1b")}})
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if n != 1 || buf[0] != 0x1b {
		t.Fatalf("first Read = %d %q err=%v, want 1 byte ESC", n, buf[:n], err)
	}
	// No more data until EOF on next read.
	n, err = r.Read(buf)
	if n != 0 || err != io.EOF {
		t.Fatalf("second Read = %d %q err=%v, want 0, EOF", n, buf[:n], err)
	}
}

// TestWrapInputLeakedSGRMouseRePrefixed covers #484: when ESC is delivered as a
// bare KeyEsc, the following "[<64;col;rowM" body must be re-prefixed so Bubble
// Tea decodes MouseMsg (scroll) instead of typing the body into the composer.
func TestWrapInputLeakedSGRMouseRePrefixed(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "wheel up body",
			in:   "[<64;56;36M",
			want: "\x1b[<64;56;36M",
		},
		{
			name: "wheel down body",
			in:   "[<65;62;26M",
			want: "\x1b[<65;62;26M",
		},
		{
			name: "release m terminator",
			in:   "[<0;10;12m",
			want: "\x1b[<0;10;12m",
		},
		{
			name: "full sequence passthrough",
			in:   "\x1b[<64;56;36M",
			want: "\x1b[<64;56;36M",
		},
		{
			name: "leak then text",
			in:   "[<64;1;1Mhi",
			want: "\x1b[<64;1;1Mhi",
		},
		{
			name: "normal bracket typing",
			in:   "[notes]",
			want: "[notes]",
		},
		{
			name: "bracket less-than without digits",
			in:   "[<foo",
			want: "[<foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(WrapInput(strings.NewReader(tt.in)))
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("WrapInput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWrapInputLeakedSGRMouseSplitAcrossReads(t *testing.T) {
	// ESC alone, then leaked body chunks — body must re-gain ESC, not type.
	cr := &chunkReader{chunks: [][]byte{
		[]byte("\x1b"),
		[]byte("[<64;"),
		[]byte("56;36M"),
		[]byte("x"),
	}}
	got, err := io.ReadAll(WrapInput(cr))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	want := "\x1b\x1b[<64;56;36Mx"
	if string(got) != want {
		t.Errorf("split mouse leak = %q, want %q", got, want)
	}
}

func TestPartialLeakedMouseHeldAcrossReads(t *testing.T) {
	cr := &chunkReader{chunks: [][]byte{
		[]byte("[<64"),
		[]byte(";1;2M"),
	}}
	r := WrapInput(cr)
	buf := make([]byte, 32)
	n, err := r.Read(buf)
	if n != 0 || err != nil {
		t.Fatalf("partial leaked mouse Read = %d %q err=%v, want 0, nil", n, buf[:n], err)
	}
	n, err = r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("complete Read err: %v", err)
	}
	if string(buf[:n]) != "\x1b[<64;1;2M" {
		t.Fatalf("complete Read = %q, want re-prefixed mouse CSI", buf[:n])
	}
}

func TestStripComposerMouseLeak(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"[<64;56;36M", ""},
		{"[<65;62;26M", ""},
		{"hello[<64;1;1Mworld", "helloworld"},
		{"\x1b[<64;56;36M", ""},
		{"[notes]", "[notes]"},
		{"[<foo", "[<foo"},
		{"pre[<0;1;2mok", "preok"},
	}
	for _, tt := range tests {
		if got := stripComposerMouseLeak(tt.in); got != tt.want {
			t.Errorf("stripComposerMouseLeak(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestDivergentCSINotStuck ensures up-arrow (and similar non-rewritten CSI)
// is delivered on the first Read and never held as a rewrite prefix.
func TestDivergentCSINotStuck(t *testing.T) {
	const seq = "\x1b[A"
	r := WrapInput(strings.NewReader(seq))
	buf := make([]byte, 16)
	n, err := r.Read(buf)
	if n == 0 && err == nil {
		t.Fatal("Read returned 0, nil — divergent CSI held/stuck")
	}
	if err != nil && err != io.EOF {
		t.Fatalf("Read error: %v", err)
	}
	got := string(buf[:n])
	if got != seq {
		rest, rerr := io.ReadAll(r)
		if rerr != nil {
			t.Fatalf("ReadAll: %v", rerr)
		}
		got += string(rest)
	}
	if got != seq {
		t.Errorf("divergent CSI = %q, want %q", got, seq)
	}
}

func TestPartialCSIHeldAcrossReads(t *testing.T) {
	// Requirement: first Read gets "\x1b[13", second gets ";2u" → Alt+Enter.
	// The partial prefix must be held (0, nil), not leaked as raw CSI.
	cr := &chunkReader{chunks: [][]byte{
		[]byte("\x1b[13"),
		[]byte(";2u"),
	}}
	r := WrapInput(cr)
	buf := make([]byte, 16)

	n, err := r.Read(buf)
	if n != 0 || err != nil {
		// First chunk is only a partial CSI; nothing to deliver yet (and no EOF).
		t.Fatalf("partial CSI Read = %d %q err=%v, want 0, nil", n, buf[:n], err)
	}

	n, err = r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("complete Read err: %v", err)
	}
	if string(buf[:n]) != string(altEnter) {
		t.Fatalf("complete Read = %q, want Alt+Enter %q", buf[:n], altEnter)
	}
}

// TestPartialCtrlCHeldAcrossReads asserts wire-level hold semantics for Kitty
// Ctrl+C CSI split across reads. First partial Read returns 0, nil; completion
// yields legacy \x03. (Update KeyCtrlC tests cover routing only.)
func TestPartialCtrlCHeldAcrossReads(t *testing.T) {
	cr := &chunkReader{chunks: [][]byte{
		[]byte("\x1b[99"),
		[]byte(";5u"),
	}}
	r := WrapInput(cr)
	buf := make([]byte, 16)

	n, err := r.Read(buf)
	if n != 0 || err != nil {
		t.Fatalf("partial Ctrl+C CSI Read = %d %q err=%v, want 0, nil", n, buf[:n], err)
	}

	n, err = r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("complete Read err: %v", err)
	}
	if string(buf[:n]) != "\x03" {
		t.Fatalf("complete Read = %q, want Ctrl+C \\x03", buf[:n])
	}
}

// TestPartialCtrlPXtermHeldAcrossReads asserts hold semantics for xterm
// modifyOtherKeys Ctrl+P split across reads.
func TestPartialCtrlPXtermHeldAcrossReads(t *testing.T) {
	cr := &chunkReader{chunks: [][]byte{
		[]byte("\x1b[27;5"),
		[]byte(";112~"),
	}}
	r := WrapInput(cr)
	buf := make([]byte, 16)

	n, err := r.Read(buf)
	if n != 0 || err != nil {
		t.Fatalf("partial Ctrl+P CSI Read = %d %q err=%v, want 0, nil", n, buf[:n], err)
	}

	n, err = r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("complete Read err: %v", err)
	}
	if string(buf[:n]) != "\x10" {
		t.Fatalf("complete Read = %q, want Ctrl+P \\x10", buf[:n])
	}
}

func TestShiftEnterPartialPrefixAtEOFPassthrough(t *testing.T) {
	// Incomplete CSI that is a prefix of a rewritten sequence must not be
	// dropped when the underlying reader hits EOF.
	partial := "\x1b[13"
	got, err := io.ReadAll(WrapInput(strings.NewReader(partial)))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != partial {
		t.Errorf("partial at EOF = %q, want passthrough %q", got, partial)
	}
}

func TestPartialCtrlCPrefixAtEOFPassthrough(t *testing.T) {
	partial := "\x1b[99"
	got, err := io.ReadAll(WrapInput(strings.NewReader(partial)))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != partial {
		t.Errorf("partial Ctrl+C at EOF = %q, want passthrough %q", got, partial)
	}
}

// fakeFile implements the file-like surface cancelreader/term need.
type fakeFile struct {
	*bytes.Reader
	fd   uintptr
	name string
}

func (f *fakeFile) Fd() uintptr  { return f.fd }
func (f *fakeFile) Name() string { return f.name }
func (f *fakeFile) Close() error { return nil }
func (f *fakeFile) Write(p []byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestWrapInputPreservesFileSemantics(t *testing.T) {
	// Type-assert path used by Bubble Tea (term.File) and cancelreader.File.
	type termFile interface {
		io.ReadWriteCloser
		Fd() uintptr
	}
	type cancelFile interface {
		io.ReadWriteCloser
		Fd() uintptr
		Name() string
	}

	f := &fakeFile{Reader: bytes.NewReader([]byte("\x1b[13;2u")), fd: 42, name: "fake-stdin"}
	wrapped := WrapInput(f)

	tf, ok := wrapped.(termFile)
	if !ok {
		t.Fatal("WrapInput(file) does not implement term.File (ReadWriteCloser+Fd)")
	}
	if tf.Fd() != 42 {
		t.Errorf("Fd() = %d, want 42", tf.Fd())
	}

	cf, ok := wrapped.(cancelFile)
	if !ok {
		t.Fatal("WrapInput(file) does not implement cancelreader.File (ReadWriteCloser+Fd+Name)")
	}
	if cf.Name() != "fake-stdin" {
		t.Errorf("Name() = %q, want fake-stdin", cf.Name())
	}

	got, err := io.ReadAll(wrapped)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "\x1b\r" {
		t.Errorf("rewrite through file wrapper = %q, want Alt+Enter", got)
	}
}

func TestWrapInputOSPipePreservesFd(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	wrapped := WrapInput(r)
	type fdHolder interface {
		Fd() uintptr
	}
	fh, ok := wrapped.(fdHolder)
	if !ok {
		t.Fatal("WrapInput(*os.File) missing Fd()")
	}
	if fh.Fd() != r.Fd() {
		t.Errorf("Fd() = %d, want underlying %d", fh.Fd(), r.Fd())
	}
	if namer, ok := wrapped.(interface{ Name() string }); ok {
		if namer.Name() != r.Name() {
			t.Errorf("Name() = %q, want %q", namer.Name(), r.Name())
		}
	} else {
		t.Error("WrapInput(*os.File) missing Name()")
	}
}

func TestWrapInputPlainReaderHasNoFd(t *testing.T) {
	wrapped := WrapInput(strings.NewReader("x"))
	if _, ok := wrapped.(interface{ Fd() uintptr }); ok {
		t.Error("WrapInput(plain reader) unexpectedly has Fd()")
	}
}

func TestEnableEnhancedKeys(t *testing.T) {
	var buf bytes.Buffer
	restore := EnableEnhancedKeys(&buf)
	written := buf.String()
	if !strings.Contains(written, string(enableModifyOtherKeys)) {
		t.Errorf("enable missing modifyOtherKeys: %q", written)
	}
	if !strings.Contains(written, string(enableKittyKeyboard)) {
		t.Errorf("enable missing Kitty keyboard: %q", written)
	}
	if !strings.Contains(written, string(enableKittyKeyboardSet)) {
		t.Errorf("enable missing Kitty set-mode: %q", written)
	}

	buf.Reset()
	restore()
	disabled := buf.String()
	if !strings.Contains(disabled, string(disableModifyOtherKeys)) {
		t.Errorf("restore missing disable modifyOtherKeys: %q", disabled)
	}
	if !strings.Contains(disabled, string(disableKittyKeyboard)) {
		t.Errorf("restore missing disable Kitty keyboard: %q", disabled)
	}

	// Second restore is a no-op.
	buf.Reset()
	restore()
	if buf.Len() != 0 {
		t.Errorf("second restore wrote %q, want empty", buf.Bytes())
	}

	// Nil writer is safe and returns a callable restore.
	restoreNil := EnableEnhancedKeys(nil)
	if restoreNil == nil {
		t.Fatal("EnableEnhancedKeys(nil) returned nil restore")
	}
	restoreNil() // must not panic
	restoreNil() // second call still safe
}
