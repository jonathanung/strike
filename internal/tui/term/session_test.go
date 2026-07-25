package term

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
