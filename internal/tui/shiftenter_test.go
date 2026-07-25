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

func TestShiftEnterRewrite(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
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
			name: "mixed text and Kitty sequence",
			in:   "hello\x1b[13;2uworld",
			want: "hello\x1b\rworld",
		},
		{
			name: "mixed text and xterm sequence",
			in:   "a\x1b[27;2;13~b",
			want: "a\x1b\rb",
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
			name: "multi-sequence in one buffer",
			in:   "\x1b[13;2u\x1b[27;2;13~\x1b[13;2u",
			want: "\x1b\r\x1b\r\x1b\r",
		},
		{
			name: "multi-sequence with text between",
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
			name: "Kitty split mid-sequence",
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
			name: "xterm split mid-sequence",
			chunks: [][]byte{
				[]byte("\x1b[27;2"),
				[]byte(";13~"),
			},
			want: "\x1b\r",
		},
		{
			name: "text then partial then completion",
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

// TestDivergentCSINotStuck ensures up-arrow (and similar non-Shift+Enter CSI)
// is delivered on the first Read and never held as a Shift+Enter prefix.
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

func TestShiftEnterPartialPrefixAtEOFPassthrough(t *testing.T) {
	// Incomplete CSI that is a prefix of a Shift+Enter sequence must not be
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
