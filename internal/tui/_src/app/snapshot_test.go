package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const snapshotFixtureWidth = 120
const snapshotFixtureHeight = 40
const snapshotFixtureMarker = "snapshot-fixture-hello"

func snapshotFixtureModel(t *testing.T) Model {
	t.Helper()
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: snapshotFixtureWidth, Height: snapshotFixtureHeight})
	m.cells = append(m.cells, &userCell{text: snapshotFixtureMarker})
	m.refreshViewport()
	_ = viewString(m)
	return m
}

func TestSnapshotFrameFixtureStable(t *testing.T) {
	m := snapshotFixtureModel(t)
	first, err := m.SnapshotFrame()
	if err != nil {
		t.Fatal(err)
	}
	if first.Width != snapshotFixtureWidth || first.Height != snapshotFixtureHeight {
		t.Fatalf("size = %dx%d, want %dx%d", first.Width, first.Height, snapshotFixtureWidth, snapshotFixtureHeight)
	}
	if first.Text == "" {
		t.Fatal("empty snapshot text")
	}
	if strings.Contains(first.Text, "\x1b") || ansi.Strip(first.Text) != first.Text {
		t.Fatalf("snapshot still has ANSI:\n%s", first.Text)
	}
	if !strings.Contains(first.Text, snapshotFixtureMarker) {
		t.Fatalf("fixture marker missing:\n%s", first.Text)
	}
	second, err := m.SnapshotFrame()
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != second.Text {
		t.Fatalf("snapshot not stable\n--- first ---\n%s\n--- second ---\n%s", first.Text, second.Text)
	}
	if first.Truncated != second.Truncated || first.Width != second.Width {
		t.Fatalf("metadata drifted: %+v vs %+v", first, second)
	}
}

func TestSnapshotFrameNoFrame(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	_, err := m.SnapshotFrame()
	if err == nil {
		t.Fatal("expected no-frame error")
	}
	if !strings.Contains(err.Error(), "no TUI frame available") {
		t.Fatalf("err = %v", err)
	}
}

func TestSnapshotFrameRedactsSecrets(t *testing.T) {
	secret := "sk-ant-" + strings.Repeat("a", 16)
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: snapshotFixtureWidth, Height: snapshotFixtureHeight})
	m.cells = append(m.cells, &userCell{text: "token " + secret})
	m.refreshViewport()
	_ = viewString(m)
	snap, err := m.SnapshotFrame()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap.Text, secret) {
		t.Fatalf("secret leaked:\n%s", snap.Text)
	}
	if !snap.Redacted {
		t.Fatal("expected Redacted=true")
	}
}

func TestSnapshotFramePublishesOnFrame(t *testing.T) {
	var got string
	var w, h int
	m, _ := newAppTestModelWithOptions(Options{
		OnFrame: func(frame string, width, height int) {
			got = frame
			w, h = width, height
		},
	})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: snapshotFixtureWidth, Height: snapshotFixtureHeight})
	m.cells = append(m.cells, &userCell{text: snapshotFixtureMarker})
	m.refreshViewport()
	_ = viewString(m)
	if !strings.Contains(got, snapshotFixtureMarker) {
		t.Fatalf("OnFrame missing marker: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("OnFrame should be ANSI-stripped: %q", got)
	}
	if w != snapshotFixtureWidth || h != snapshotFixtureHeight {
		t.Fatalf("OnFrame size = %dx%d", w, h)
	}
}

func TestBoundFrameText(t *testing.T) {
	lines := make([]string, maxFrameSnapshotLines+20)
	for i := range lines {
		lines[i] = strings.Repeat("x", 80)
	}
	got, truncated := boundFrameText(strings.Join(lines, "\n"))
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(got, "... (truncated)") {
		t.Fatalf("missing marker: %q", got)
	}
	if n := strings.Count(got, "\n"); n > maxFrameSnapshotLines+1 {
		t.Fatalf("lines after bound = %d", n)
	}
}
