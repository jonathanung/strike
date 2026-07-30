package tui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

type scriptedTelemetry struct {
	sample host.TelemetrySample
	err    error
	roots  []string
	calls  atomic.Int32
	block  chan struct{} // when non-nil, Sample waits until closed
}

func (f *scriptedTelemetry) Sample(ctx context.Context, root string) (host.TelemetrySample, error) {
	f.calls.Add(1)
	f.roots = append(f.roots, root)
	if f.block != nil {
		select {
		case <-ctx.Done():
			return host.TelemetrySample{}, ctx.Err()
		case <-f.block:
		}
	}
	s := f.sample
	s.DiskRoot = root
	s.At = time.Now()
	return s, f.err
}

func TestTelemetryFormatHelpers(t *testing.T) {
	if got := telemetryFormatBytes(10*1024*1024*1024 + 100*1024*1024); got != "10.1 GB" {
		t.Errorf("bytes = %q", got)
	}
	if got := telemetryFormatPercent(42.3); got != "42.3%" {
		t.Errorf("pct = %q", got)
	}
	th := theme.Default()
	var empty host.TelemetrySample
	if got := telemetryMemText(th, empty); got != telemetryUnavailable {
		t.Errorf("mem empty = %q", got)
	}
	if got := telemetryCacheText(th, empty); got != telemetryUnavailable {
		t.Errorf("cache empty = %q", got)
	}
	if got := telemetrySwapText(th, empty); got != telemetryUnavailable {
		t.Errorf("swap empty = %q", got)
	}
	if got := telemetryCPUText(th, empty, true); got != telemetryUnavailable {
		t.Errorf("cpu empty = %q", got)
	}
	if got := telemetryDiskText(th, empty); got != telemetryUnavailable {
		t.Errorf("disk empty = %q", got)
	}
	withCache := host.TelemetrySample{
		MemOK: true, MemUsedBytes: 8 * 1024 * 1024 * 1024, MemTotalBytes: 24 * 1024 * 1024 * 1024,
		MemCachedOK: true, MemCachedBytes: 6 * 1024 * 1024 * 1024,
		SwapOK: true, SwapUsedBytes: 0, SwapTotalBytes: 0,
	}
	if got := telemetryCacheText(th, withCache); !strings.Contains(got, "cache") {
		t.Errorf("cache text = %q", got)
	}
	if got := telemetrySwapText(th, withCache); !strings.Contains(got, "0 B") {
		t.Errorf("empty swap text = %q", got)
	}
}

func TestTelemetryWindowRenderStates(t *testing.T) {
	th := theme.Default()
	normal := host.TelemetrySample{
		CPUHostOK: true, CPUHostPct: 42.3,
		MemOK: true, MemUsedBytes: 10 * 1024 * 1024 * 1024, MemTotalBytes: 32 * 1024 * 1024 * 1024,
		MemCachedOK: true, MemCachedBytes: 4 * 1024 * 1024 * 1024,
		SwapOK: true, SwapUsedBytes: 512 * 1024 * 1024, SwapTotalBytes: 2 * 1024 * 1024 * 1024,
		DiskOK: true, DiskUsedBytes: 287 * 1024 * 1024 * 1024, DiskTotalBytes: 494 * 1024 * 1024 * 1024,
		DiskFreeBytes: 207 * 1024 * 1024 * 1024,
	}
	warn := normal
	warn.MemUsedBytes = 24 * 1024 * 1024 * 1024 // 75%
	crit := normal
	crit.CPUHostPct = 95
	crit.DiskUsedBytes = 480 * 1024 * 1024 * 1024

	for _, tt := range []struct {
		name   string
		sample host.TelemetrySample
		width  int
		want   []string
		forbid []string
		noZero bool
	}{
		{"normal wide", normal, 48, []string{"RAM", "Cache", "Swap", "CPU", "Disk", "42.3%", "used"}, nil, false},
		{"warning", warn, 48, []string{"RAM", "used"}, nil, false},
		{"critical", crit, 48, []string{"CPU", "95.0%"}, nil, false},
		{"unavailable", host.TelemetrySample{}, 40, []string{"RAM", "CPU", "Disk", telemetryUnavailable}, nil, true},
		{"compact", normal, 20, []string{"RAM", "CPU"}, nil, false},
		{"tiny", normal, 10, []string{"RAM"}, nil, false},
		{"no swap configured", host.TelemetrySample{
			MemOK: true, MemUsedBytes: 1, MemTotalBytes: 2,
			MemCachedOK: true, MemCachedBytes: 1,
			SwapOK: true, SwapTotalBytes: 0,
			CPUHostOK: true, CPUHostPct: 1,
			DiskOK: true, DiskUsedBytes: 1, DiskTotalBytes: 2, DiskFreeBytes: 1,
		}, 48, []string{"RAM", "Cache", "CPU", "Disk"}, []string{"Swap"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := telemetryWindow{tel: &scriptedTelemetry{}, sample: tt.sample, has: true, width: tt.width, height: 8}
			view := w.view(th)
			plain := ansi.Strip(view)
			for _, want := range tt.want {
				if !strings.Contains(plain, want) {
					t.Errorf("missing %q in %q", want, plain)
				}
			}
			for _, bad := range tt.forbid {
				if strings.Contains(plain, bad) {
					t.Errorf("unexpected %q in %q", bad, plain)
				}
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > tt.width {
					t.Errorf("line width %d > %d: %q", got, tt.width, line)
				}
			}
			if tt.noZero {
				// Unavailable must not look like measured zeros.
				if strings.Contains(plain, "0.0%") && !strings.Contains(plain, telemetryUnavailable) {
					t.Errorf("silent zero percent: %q", plain)
				}
			}
		})
	}
}

func TestTelemetryWindowNilService(t *testing.T) {
	w := telemetryWindow{has: true, width: 24, height: 3}
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, telemetryUnavailable) {
		t.Errorf("nil tel = %q", plain)
	}
}

func asTelemetryWindow(t *testing.T, w window) telemetryWindow {
	t.Helper()
	tw, ok := w.(telemetryWindow)
	if !ok {
		t.Fatalf("window = %T, want telemetryWindow", w)
	}
	return tw
}

func TestTelemetrySampleAndRootSwitch(t *testing.T) {
	ft := &scriptedTelemetry{sample: host.TelemetrySample{
		CPUHostOK: true, CPUHostPct: 11,
		MemOK: true, MemUsedBytes: 1, MemTotalBytes: 2,
		DiskOK: true, DiskUsedBytes: 1, DiskTotalBytes: 4, DiskFreeBytes: 3,
	}}
	w := telemetryWindow{tel: ft, root: "/a", enabled: true}
	next, cmd := w.update(telemetryTickMsg{gen: w.gen})
	w = asTelemetryWindow(t, next)
	if cmd == nil {
		t.Fatal("expected sample cmd")
	}
	if !w.sampling {
		t.Fatal("expected sampling")
	}
	// Overlapping tick while sampling is ignored.
	_, cmd2 := w.update(telemetryTickMsg{gen: w.gen})
	if cmd2 != nil {
		t.Fatal("overlap tick should not start another sample")
	}

	msg := cmd().(telemetrySampleMsg)
	next, cmd = w.update(msg)
	w = asTelemetryWindow(t, next)
	if !w.has || w.sample.CPUHostPct != 11 {
		t.Fatalf("sample not applied: %+v", w.sample)
	}
	if cmd == nil {
		t.Fatal("expected re-arm tick")
	}
	if ft.calls.Load() != 1 {
		t.Errorf("calls = %d", ft.calls.Load())
	}

	// Root change cancels in-flight gen and resamples.
	next, cmd = w.update(contextStateMsg{WorkDir: "/b"})
	w = asTelemetryWindow(t, next)
	if w.root != "/b" || cmd == nil {
		t.Fatalf("root switch: root=%q cmd=%v", w.root, cmd != nil)
	}
	msg = cmd().(telemetrySampleMsg)
	if msg.sample.DiskRoot != "/b" {
		t.Errorf("disk root = %q", msg.sample.DiskRoot)
	}
	// Stale sample from old gen is dropped.
	stale := telemetrySampleMsg{gen: msg.gen - 1, sample: host.TelemetrySample{CPUHostOK: true, CPUHostPct: 99}}
	next, _ = w.update(stale)
	w = asTelemetryWindow(t, next)
	if w.sample.CPUHostPct == 99 {
		t.Error("stale sample applied")
	}
}

func TestTelemetrySampleError(t *testing.T) {
	ft := &scriptedTelemetry{err: errors.New("collect failed")}
	w := telemetryWindow{tel: ft, enabled: true}
	w, cmd := w.startSample()
	msg := cmd().(telemetrySampleMsg)
	next, _ := w.update(msg)
	w = asTelemetryWindow(t, next)
	if !w.has || w.err == "" {
		t.Fatalf("err not recorded: %+v", w)
	}
	plain := ansi.Strip(w.resize(40, 5).view(theme.Default()))
	// Metrics still show unavailable when sample empty + err note.
	if !strings.Contains(plain, telemetryUnavailable) {
		t.Errorf("view = %q", plain)
	}
}

func TestApplyTelemetryMsgRoutesToWindow(t *testing.T) {
	ft := &scriptedTelemetry{sample: host.TelemetrySample{CPUHostOK: true, CPUHostPct: 5}}
	r := newWindowRegistry()
	r = configureTelemetryWindow(r, "/proj", ft)
	r, _ = setTelemetryEnabled(r, true)
	var gen int
	for _, w := range r.windows {
		if tw, ok := w.(telemetryWindow); ok {
			gen = tw.gen
			break
		}
	}
	r, cmd := applyTelemetryMsg(r, telemetryTickMsg{gen: gen})
	if cmd == nil {
		t.Fatal("no sample cmd")
	}
	msg := cmd()
	r, _ = applyTelemetryMsg(r, msg)
	for _, w := range r.windows {
		if tw, ok := w.(telemetryWindow); ok {
			if !tw.has || tw.sample.CPUHostPct != 5 {
				t.Fatalf("window state = %+v", tw)
			}
			return
		}
	}
	t.Fatal("telemetry window missing")
}

func TestTelemetryDefaultOnSamplerAndOptOut(t *testing.T) {
	ft := &scriptedTelemetry{sample: host.TelemetrySample{CPUHostOK: true, CPUHostPct: 1}}
	r := newWindowRegistry()
	r = configureTelemetryWindow(r, "/proj", ft)
	if !telemetryEnabled(r) {
		t.Fatal("telemetry disabled by default")
	}
	// Session stack includes system pane when on.
	found := false
	for _, g := range r.groups {
		if g.id != "session" {
			continue
		}
		for _, mi := range g.members {
			if r.windows[mi].id() == telemetryWindowID {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("session group missing telemetry when on")
	}
	// Disable stops sampling and drops in-flight gen.
	r, _ = setTelemetryEnabled(r, false)
	if telemetryEnabled(r) {
		t.Fatal("disable failed")
	}
	for _, g := range r.groups {
		if g.id != "session" {
			continue
		}
		for _, mi := range g.members {
			if r.windows[mi].id() == telemetryWindowID {
				t.Fatal("session group includes telemetry when off")
			}
		}
	}
	r, cmd := applyTelemetryMsg(r, telemetryTickMsg{gen: 0})
	if cmd != nil {
		t.Fatal("disabled telemetry armed a sample cmd")
	}
	if ft.calls.Load() != 0 {
		t.Fatalf("Sample called %d times while off", ft.calls.Load())
	}
	// Re-enable starts sampling.
	r, cmd = setTelemetryEnabled(r, true)
	if !telemetryEnabled(r) {
		t.Fatal("enable failed")
	}
	if cmd == nil {
		t.Fatal("enable should return init tick")
	}
	tick := cmd().(telemetryTickMsg)
	r, cmd = applyTelemetryMsg(r, tick)
	if cmd == nil {
		t.Fatal("expected sample after enable")
	}
	_ = cmd() // run sample
	r, _ = setTelemetryEnabled(r, false)
	r, cmd = applyTelemetryMsg(r, telemetryTickMsg{gen: tick.gen})
	if cmd != nil {
		t.Fatal("stale tick after disable should not sample")
	}
}

func TestTelemetryInSessionStack(t *testing.T) {
	ft := &scriptedTelemetry{sample: host.TelemetrySample{
		CPUHostOK: true, CPUHostPct: 12.5,
		MemOK: true, MemUsedBytes: 4 * 1024 * 1024 * 1024, MemTotalBytes: 16 * 1024 * 1024 * 1024,
		DiskOK: true, DiskUsedBytes: 100 * 1024 * 1024 * 1024, DiskTotalBytes: 500 * 1024 * 1024 * 1024,
		DiskFreeBytes: 400 * 1024 * 1024 * 1024,
	}}
	m, _ := newAppTestModel(nil, nil)
	m.services.Telemetry = ft
	m.workDir = "/workspace"
	m.windows = configureTelemetryWindow(m.windows, m.workDir, ft)
	m.windows, _ = setTelemetryEnabled(m.windows, true)
	// Inject a completed sample.
	var tw telemetryWindow
	for _, w := range m.windows.windows {
		if x, ok := w.(telemetryWindow); ok {
			tw = x
			break
		}
	}
	tw.sample = ft.sample
	tw.has = true
	tw.enabled = true
	m.windows, _ = m.windows.replace(telemetryWindowID, tw, false)
	// replace does not rebuild groups; ensure session stack still has telemetry.
	m.windows.groups = defaultWindowGroups(m.windows.windows)

	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 48})
	m.focus = focusRight
	m.windows, _ = m.windows.activate("context")
	m.reflow()
	plain := ansi.Strip(viewString(m))
	for _, want := range []string{"context", "activity", "system", "RAM", "CPU", "Disk"} {
		if !strings.Contains(plain, want) {
			t.Errorf("stack missing %q:\n%s", want, plain)
		}
	}
	// Off removes the pane.
	m.windows, _ = setTelemetryEnabled(m.windows, false)
	m.reflow()
	plain = ansi.Strip(viewString(m))
	if strings.Contains(plain, "system") && strings.Contains(plain, "RAM") {
		t.Errorf("system pane still visible after off:\n%s", plain)
	}
}

func TestTelemetryThresholdConstantsDocumented(t *testing.T) {
	if telemetryWarnRatio != 0.70 || telemetryCritRatio != 0.90 {
		t.Fatalf("thresholds drifted: warn=%v crit=%v", telemetryWarnRatio, telemetryCritRatio)
	}
}
