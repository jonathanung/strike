package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestShortenHomePath(t *testing.T) {
	t.Setenv("HOME", "/home/dev")
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"/home/dev", "~"},
		{"/home/dev/", "~"},
		{"/home/dev/Projects/strike-cli", "~/Projects/strike-cli"},
		{"/tmp/other", "/tmp/other"},
		{"relative/path", "relative/path"},
	}
	for _, tt := range tests {
		if got := shortenHomePath(tt.in); got != tt.want {
			t.Errorf("shortenHomePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHeaderShowsWorkDirAt80Cols(t *testing.T) {
	t.Setenv("HOME", "/home/dev")
	m, _ := newAppTestModelWithOptions(Options{
		WorkDir: "/home/dev/Projects/strike-cli",
	})
	m.providerName = "echo"
	m.modelName = "echo-1"

	header := m.headerView(80)
	plain := ansi.Strip(header)
	if !strings.Contains(plain, "strike-cli") {
		t.Errorf("80-col header missing workdir leaf:\n%s", plain)
	}
	if !strings.Contains(plain, "~/Projects/strike-cli") {
		t.Errorf("80-col header missing home-shortened path:\n%s", plain)
	}
	if got := lipgloss.Width(header); got != 80 {
		t.Errorf("header width = %d, want 80\n%s", got, plain)
	}
}

func TestHeaderWorkDirWidthSafe(t *testing.T) {
	t.Setenv("HOME", "/home/dev")
	m, _ := newAppTestModelWithOptions(Options{
		WorkDir: "/home/dev/very/long/path/to/some/deeply/nested/project/name",
	})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.agentName = "build"
	m.applyEvent(protocol.TurnStarted{})

	for _, width := range []int{40, 56, 80, 120} {
		header := m.headerView(width)
		if got := lipgloss.Width(header); got != width {
			t.Errorf("width %d: header measured %d\n%s", width, got, ansi.Strip(header))
		}
	}
}

func TestHeaderWorkDirUpdatesWithBinding(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: "/tmp/alpha-repo"})
	m.providerName = "echo"
	m.modelName = "echo-1"

	plain := ansi.Strip(m.headerView(100))
	if !strings.Contains(plain, "alpha-repo") {
		t.Fatalf("header missing initial workdir:\n%s", plain)
	}

	m.workDir = "/tmp/beta-repo"
	plain = ansi.Strip(m.headerView(100))
	if !strings.Contains(plain, "beta-repo") {
		t.Errorf("header did not update after workdir change:\n%s", plain)
	}
	if strings.Contains(plain, "alpha-repo") {
		t.Errorf("header retained previous workdir:\n%s", plain)
	}
}

func TestHeaderWorkDirOmittedWhenEmptyOrTight(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: ""})
	m.providerName = "echo"
	m.modelName = "echo-1"
	if got := m.headerWorkDirLabel(m.th, 40); got != "" {
		t.Errorf("empty workdir label = %q, want empty", got)
	}
	m.workDir = "/tmp/proj"
	if got := m.headerWorkDirLabel(m.th, 5); got != "" {
		t.Errorf("tight budget still rendered path: %q", ansi.Strip(got))
	}
}
