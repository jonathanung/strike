package term

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hinshun/vt10x"
)

func TestSessionEchoBytesRoundTrip(t *testing.T) {
	// A tiny script that prints a marker, reads one line, echoes it, exits.
	script := `#!/bin/sh
printf 'READY\n'
IFS= read -r line
printf 'GOT:%s\n' "$line"
`
	dir := t.TempDir()
	path := dir + "/echo.sh"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	s, err := Start(cmd, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for READY")
		case <-s.Notify():
			// String locks VT state itself — do not call Session.Lock around it.
			if strings.Contains(s.Terminal().String(), "READY") {
				goto ready
			}
		case <-s.Done():
			t.Fatalf("process exited early: %v", s.WaitErr())
		}
	}
ready:
	if _, err := s.Write([]byte("hello-pty\r")); err != nil {
		t.Fatal(err)
	}
	deadline = time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for echo; screen=%q", s.Terminal().String())
		case <-s.Notify():
			if strings.Contains(s.Terminal().String(), "GOT:hello-pty") {
				return
			}
		case <-s.Done():
			text := s.Terminal().String()
			if strings.Contains(text, "GOT:hello-pty") {
				return
			}
			t.Fatalf("exited without echo: %v screen=%q", s.WaitErr(), text)
		}
	}
}

func TestSessionRespondsToBackgroundColorQuery(t *testing.T) {
	cmd := exec.Command("bash", "-c", `
		stty raw -echo
		printf '\033]11;?\007'
		IFS= read -r -d $'\a' response
		case "$response" in
			$'\033]11;rgb:'*) printf '\033[2J\033[HRESPONSE:OK' ;;
			*) printf '\033[2J\033[HRESPONSE:BAD' ;;
		esac
	`)
	s, err := Start(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for background color response; screen=%q", s.Terminal().String())
		case <-s.Notify():
			if text := s.Terminal().String(); strings.Contains(text, "RESPONSE:OK") {
				return
			} else if strings.Contains(text, "RESPONSE:BAD") {
				t.Fatalf("child received an invalid background color response: screen=%q", text)
			}
		case <-s.Done():
			text := s.Terminal().String()
			if strings.Contains(text, "RESPONSE:OK") {
				return
			}
			t.Fatalf("child exited without background color response: %v screen=%q", s.WaitErr(), text)
		}
	}
}

func TestPTYEnvForcesXterm256ColorEnvironment(t *testing.T) {
	parent := []string{
		"HOME=/home/alice",
		"TERM=screen-256color",
		"COLORTERM=24bit",
		"NVIM_APPNAME=embedded-test",
		"TERM=vt100",
		"COLORTERM=truecolor",
	}
	want := []string{
		"HOME=/home/alice",
		"NVIM_APPNAME=embedded-test",
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	}

	got := ptyEnv(parent)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("ptyEnv(%q) = %q, want %q", parent, got, want)
	}
}

func TestStartPassesNormalizedPTYEnvToChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'TERM=%s COLORTERM=%s NVIM_APPNAME=%s\\n' \"$TERM\" \"$COLORTERM\" \"$NVIM_APPNAME\"")
	cmd.Env = []string{
		"TERM=screen-256color",
		"COLORTERM=truecolor",
		"NVIM_APPNAME=embedded-test",
	}
	s, err := Start(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for normalized environment; screen=%q", s.Terminal().String())
		case <-s.Notify():
			if text := s.Terminal().String(); strings.Contains(text, "TERM=xterm-256color COLORTERM=truecolor NVIM_APPNAME=embedded-test") {
				return
			}
		case <-s.Done():
			text := s.Terminal().String()
			if strings.Contains(text, "TERM=xterm-256color COLORTERM=truecolor NVIM_APPNAME=embedded-test") {
				return
			}
			t.Fatalf("child exited before printing normalized environment: %v screen=%q", s.WaitErr(), text)
		}
	}
}

func TestSessionNvimRendersTruecolorBufferText(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
	dir := t.TempDir()
	path := dir + "/sample.go"
	if err := os.WriteFile(path, []byte("package marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		"nvim", "--clean", "-u", "NONE", "-n",
		"--cmd", `if $COLORTERM !=# 'truecolor' | cquit | endif`,
		"-c", "set termguicolors",
		"-c", "highlight EmbeddedKeyword guifg=#12ab34",
		"-c", `call matchadd('EmbeddedKeyword', '^package')`,
		"-c", "redraw!",
		path,
	)
	s, err := Start(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("nvim did not render truecolor buffer text; screen=%q", s.Terminal().String())
		case <-s.Notify():
			if nvimTokenHasForeground(s, "package", vt10x.Color(0x12ab34)) {
				_, _ = s.Write([]byte(":q!\r"))
				return
			}
		case <-s.Done():
			t.Fatalf("nvim exited before rendering truecolor buffer text: %v screen=%q", s.WaitErr(), s.Terminal().String())
		}
	}
}

func nvimTokenHasForeground(s *Session, token string, want vt10x.Color) bool {
	s.Lock()
	defer s.Unlock()
	cols, rows := s.Terminal().Size()
	for y := 0; y < rows; y++ {
		line := make([]rune, cols)
		for x := 0; x < cols; x++ {
			line[x] = s.Terminal().Cell(x, y).Char
		}
		start := strings.Index(string(line), token)
		if start < 0 {
			continue
		}
		for x := start; x < start+len([]rune(token)); x++ {
			if s.Terminal().Cell(x, y).FG != want {
				return false
			}
		}
		return true
	}
	return false
}

func TestSessionResizeAndCleanShutdown(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	s, err := Start(cmd, 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(60, 12); err != nil {
		t.Fatal(err)
	}
	cols, rows := s.Size()
	if cols != 60 || rows != 12 {
		t.Fatalf("size = %dx%d, want 60x12", cols, rows)
	}
	// Close must not hang.
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung")
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Close")
	}
}

func TestSessionNvimRendersInGrid(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
	dir := t.TempDir()
	path := dir + "/sample.txt"
	content := "embedded-vim-marker-line"
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("nvim", "--clean", "-u", "NONE", "-n", path)
	s, err := Start(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("nvim did not render file contents; screen=%q", s.Terminal().String())
		case <-s.Notify():
			text := s.Terminal().String()
			if strings.Contains(text, "embedded-vim-marker-line") {
				s.Lock()
				cur := s.Terminal().Cursor()
				s.Unlock()
				if cur.X < 0 || cur.Y < 0 {
					t.Fatalf("cursor invalid: %+v", cur)
				}
				// Quit nvim cleanly.
				_, _ = s.Write([]byte(":q!\r"))
				select {
				case <-s.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("nvim did not exit after :q!")
				}
				return
			}
		case <-s.Done():
			t.Fatalf("nvim exited early: %v", s.WaitErr())
		}
	}
}
