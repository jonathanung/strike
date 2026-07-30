package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const telemetryWindowID = "telemetry"

// telemetrySampleInterval is the default poll period (~1 Hz).
const telemetrySampleInterval = time.Second

// Pressure thresholds (used/total ratio). Documented constants; match
// internal/telemetry and ui.Meter bands.
const (
	telemetryWarnRatio = 0.70
	telemetryCritRatio = 0.90
)

// telemetryUnavailable is the explicit label for missing metrics.
const telemetryUnavailable = "unavailable"

// telemetryTickMsg schedules the next background sample.
type telemetryTickMsg struct {
	gen int
}

// telemetrySampleMsg delivers one non-blocking sample result to the model.
type telemetrySampleMsg struct {
	gen    int
	sample host.TelemetrySample
	err    error
}

// telemetryWindow is the right-pane system resource panel (CPU/RAM/disk).
// On by default (sampler ~1 Hz). Hide with /telemetry off; --telemetry keeps
// the pane on at launch.
type telemetryWindow struct {
	tel     host.Telemetry
	root    string
	sample  host.TelemetrySample
	has     bool // true after at least one sample attempt
	err     string
	gen     int // bumps on root change / rebind / disable to drop stale msgs
	width   int
	height  int
	enabled bool // false → hidden from stack/cycle and not sampling
	// sampling is true while a Collect cmd is in flight (no overlap).
	sampling bool
}

func newTelemetryWindow() telemetryWindow {
	return telemetryWindow{enabled: true}
}

func (w telemetryWindow) id() string { return telemetryWindowID }

func (w telemetryWindow) title() string { return "system" }

func (w telemetryWindow) init() tea.Cmd {
	if !w.enabled {
		return nil
	}
	return w.armTick(0)
}

func (w telemetryWindow) update(msg tea.Msg) (window, tea.Cmd) {
	if !w.enabled {
		return w, nil
	}
	switch msg := msg.(type) {
	case contextStateMsg:
		root := strings.TrimSpace(msg.WorkDir)
		if root == w.root {
			return w, nil
		}
		w.root = root
		w.gen++
		w.sampling = false
		// Drop stale disk from prior volume until the next sample lands.
		w.sample.DiskOK = false
		w.sample.DiskRoot = root
		return w.startSample()
	case telemetryTickMsg:
		if msg.gen != w.gen || w.sampling {
			return w, nil
		}
		return w.startSample()
	case telemetrySampleMsg:
		if msg.gen != w.gen {
			return w, nil
		}
		w.sampling = false
		w.has = true
		if msg.err != nil {
			w.err = msg.err.Error()
		} else {
			w.err = ""
			w.sample = msg.sample
		}
		return w, w.armTick(telemetrySampleInterval)
	}
	return w, nil
}

func (w telemetryWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w telemetryWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	if w.tel == nil {
		return wrapWindowText(st.Muted.Render(telemetryUnavailable), w.width)
	}
	if !w.has {
		return wrapWindowText(st.Muted.Render("sampling"+th.Icons.Ellipsis), w.width)
	}

	includeProc := w.width >= 36
	memR, memOK := telemetryMemRatio(w.sample)
	cpuR, cpuOK := telemetryCPURatio(w.sample)
	diskR, diskOK := telemetryDiskRatio(w.sample)
	lines := []string{
		telemetryMetricLine(th, w.width, "RAM", telemetryMemText(th, w.sample), memR, memOK),
	}
	// Cache is reclaimable — never pressure-colored (high cache is healthy).
	if cacheR, cacheOK := telemetryCacheRatio(w.sample); cacheOK {
		lines = append(lines, telemetryMetricLineNeutral(th, w.width, "Cache", telemetryCacheText(th, w.sample), cacheR, cacheOK))
	}
	// Hide swap row when the OS reports no swap configured (0/0).
	if w.sample.SwapOK && w.sample.SwapTotalBytes > 0 {
		swapR, swapOK := telemetrySwapRatio(w.sample)
		lines = append(lines, telemetryMetricLine(th, w.width, "Swap", telemetrySwapText(th, w.sample), swapR, swapOK))
	}
	lines = append(lines,
		telemetryMetricLine(th, w.width, "CPU", telemetryCPUText(th, w.sample, includeProc), cpuR, cpuOK),
		telemetryMetricLine(th, w.width, "Disk", telemetryDiskText(th, w.sample), diskR, diskOK),
	)
	if w.err != "" && w.width >= 12 {
		lines = append(lines, wrapWindowText(st.Muted.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)), w.width))
	}
	if w.height > 0 && len(lines) > w.height {
		lines = lines[:w.height]
	}
	return strings.Join(lines, "\n")
}

func (w telemetryWindow) armTick(d time.Duration) tea.Cmd {
	gen := w.gen
	if d <= 0 {
		return func() tea.Msg { return telemetryTickMsg{gen: gen} }
	}
	return tea.Tick(d, func(time.Time) tea.Msg {
		return telemetryTickMsg{gen: gen}
	})
}

// startSample marks in-flight collection and returns a non-blocking sample cmd.
// The host collector honors ctx (disk Statfs is cached/off-thread); the timeout
// bounds worst-case wait so the UI tick loop cannot stall on a hung volume.
func (w telemetryWindow) startSample() (telemetryWindow, tea.Cmd) {
	if !w.enabled {
		w.sampling = false
		return w, nil
	}
	if w.tel == nil {
		w.has = true
		w.sampling = false
		return w, w.armTick(telemetrySampleInterval)
	}
	if w.sampling {
		return w, nil
	}
	w.sampling = true
	tel, root, gen := w.tel, w.root, w.gen
	return w, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s, err := tel.Sample(ctx, root)
		return telemetrySampleMsg{gen: gen, sample: s, err: err}
	}
}

// applyTelemetryMsg routes sample/tick msgs onto the telemetry window and
// returns any follow-up cmd. Used from Model.Update so sampling works even
// when another right-pane window is focused. No-op when telemetry is off.
func applyTelemetryMsg(r windowRegistry, msg tea.Msg) (windowRegistry, tea.Cmd) {
	for i, w := range r.windows {
		tw, ok := w.(telemetryWindow)
		if !ok {
			continue
		}
		if !tw.enabled {
			return r, nil
		}
		next, cmd := tw.update(msg)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r, cmd
	}
	return r, nil
}

// configureTelemetryWindow binds host.Telemetry + workDir onto the system pane.
// Does not change enabled; use setTelemetryEnabled to show/hide.
func configureTelemetryWindow(r windowRegistry, root string, tel host.Telemetry) windowRegistry {
	for i, w := range r.windows {
		tw, ok := w.(telemetryWindow)
		if !ok {
			continue
		}
		tw.tel = tel
		tw.root = strings.TrimSpace(root)
		tw.gen++
		tw.sampling = false
		windows := append([]window(nil), r.windows...)
		windows[i] = tw
		r.windows = windows
		return r
	}
	return r
}

// telemetryEnabled reports whether the system telemetry pane is shown.
func telemetryEnabled(r windowRegistry) bool {
	for _, w := range r.windows {
		if tw, ok := w.(telemetryWindow); ok {
			return tw.enabled
		}
	}
	return false
}

// setTelemetryEnabled shows or hides the system pane and starts/stops the
// sampler. Disabling bumps gen so in-flight sample msgs are dropped and no
// further ticks are armed. Enabling returns an init cmd that starts sampling.
func setTelemetryEnabled(r windowRegistry, enabled bool) (windowRegistry, tea.Cmd) {
	for i, w := range r.windows {
		tw, ok := w.(telemetryWindow)
		if !ok {
			continue
		}
		if tw.enabled == enabled {
			return r, nil
		}
		tw.enabled = enabled
		tw.gen++
		tw.sampling = false
		if !enabled {
			tw.has = false
			tw.err = ""
			tw.sample = host.TelemetrySample{}
		}
		windows := append([]window(nil), r.windows...)
		windows[i] = tw
		r.windows = windows
		r.groups = defaultWindowGroups(r.windows)
		if !enabled && len(r.windows) > 0 {
			cur := r.windows[r.index%len(r.windows)]
			if cur.id() == telemetryWindowID {
				r, _ = r.activate("context")
			}
		}
		var cmd tea.Cmd
		if enabled {
			cmd = tw.init()
		}
		return r, cmd
	}
	return r, nil
}

func telemetryMemRatio(s host.TelemetrySample) (float64, bool) {
	if !s.MemOK || s.MemTotalBytes == 0 {
		return 0, false
	}
	r := float64(s.MemUsedBytes) / float64(s.MemTotalBytes)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r, true
}

func telemetryCacheRatio(s host.TelemetrySample) (float64, bool) {
	if !s.MemCachedOK || !s.MemOK || s.MemTotalBytes == 0 {
		return 0, false
	}
	r := float64(s.MemCachedBytes) / float64(s.MemTotalBytes)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r, true
}

func telemetrySwapRatio(s host.TelemetrySample) (float64, bool) {
	if !s.SwapOK || s.SwapTotalBytes == 0 {
		return 0, false
	}
	r := float64(s.SwapUsedBytes) / float64(s.SwapTotalBytes)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r, true
}

func telemetryCPURatio(s host.TelemetrySample) (float64, bool) {
	if !s.CPUHostOK {
		return 0, false
	}
	r := s.CPUHostPct / 100
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r, true
}

func telemetryDiskRatio(s host.TelemetrySample) (float64, bool) {
	if !s.DiskOK || s.DiskTotalBytes == 0 {
		return 0, false
	}
	r := float64(s.DiskUsedBytes) / float64(s.DiskTotalBytes)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r, true
}

func telemetryMemText(th theme.Theme, s host.TelemetrySample) string {
	if !s.MemOK {
		return telemetryUnavailable
	}
	r, ok := telemetryMemRatio(s)
	if !ok {
		return telemetryUnavailable
	}
	sep := th.Resolve().Icons.DetailSeparator
	return telemetryFormatBytes(s.MemUsedBytes) + " / " + telemetryFormatBytes(s.MemTotalBytes) +
		" used " + sep + " " + telemetryFormatPercent(r*100)
}

func telemetryCacheText(th theme.Theme, s host.TelemetrySample) string {
	if !s.MemCachedOK || !s.MemOK {
		return telemetryUnavailable
	}
	sep := th.Resolve().Icons.DetailSeparator
	line := telemetryFormatBytes(s.MemCachedBytes) + " cache"
	if r, ok := telemetryCacheRatio(s); ok {
		line += " " + sep + " " + telemetryFormatPercent(r*100)
	}
	return line
}

func telemetrySwapText(th theme.Theme, s host.TelemetrySample) string {
	if !s.SwapOK {
		return telemetryUnavailable
	}
	sep := th.Resolve().Icons.DetailSeparator
	if s.SwapTotalBytes == 0 {
		return telemetryFormatBytes(0) + " / " + telemetryFormatBytes(0) + " used"
	}
	r, ok := telemetrySwapRatio(s)
	if !ok {
		return telemetryUnavailable
	}
	return telemetryFormatBytes(s.SwapUsedBytes) + " / " + telemetryFormatBytes(s.SwapTotalBytes) +
		" used " + sep + " " + telemetryFormatPercent(r*100)
}

func telemetryCPUText(th theme.Theme, s host.TelemetrySample, includeProc bool) string {
	if !s.CPUHostOK {
		return telemetryUnavailable
	}
	line := telemetryFormatPercent(s.CPUHostPct)
	if includeProc && s.CPUProcOK {
		sep := th.Resolve().Icons.DetailSeparator
		line += " " + sep + " proc " + telemetryFormatPercent(s.CPUProcPct)
	}
	return line
}

func telemetryDiskText(th theme.Theme, s host.TelemetrySample) string {
	if !s.DiskOK {
		return telemetryUnavailable
	}
	r, ok := telemetryDiskRatio(s)
	if !ok {
		return telemetryUnavailable
	}
	sep := th.Resolve().Icons.DetailSeparator
	return telemetryFormatBytes(s.DiskUsedBytes) + " / " + telemetryFormatBytes(s.DiskTotalBytes) +
		" used " + sep + " " + telemetryFormatBytes(s.DiskFreeBytes) + " free " + sep + " " + telemetryFormatPercent(r*100)
}

func telemetryFormatBytes(n uint64) string {
	const (
		kiB = 1024
		miB = 1024 * kiB
		giB = 1024 * miB
		tiB = 1024 * giB
	)
	switch {
	case n >= tiB:
		return telemetryFormatFrac(float64(n)/float64(tiB), "TB")
	case n >= giB:
		return telemetryFormatFrac(float64(n)/float64(giB), "GB")
	case n >= miB:
		return telemetryFormatFrac(float64(n)/float64(miB), "MB")
	case n >= kiB:
		return telemetryFormatFrac(float64(n)/float64(kiB), "KB")
	default:
		return strconv.FormatUint(n, 10) + " B"
	}
}

func telemetryFormatFrac(v float64, unit string) string {
	if v >= 100 {
		return strconv.FormatInt(int64(v+0.5), 10) + " " + unit
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + unit
}

func telemetryFormatPercent(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// telemetryMetricLine renders "RAM  text [bar]" with pressure-colored bar.
// At tiny widths the bar is dropped; unavailable never shows as zero.
func telemetryMetricLine(th theme.Theme, width int, label, text string, ratio float64, ok bool) string {
	return telemetryMetricLineStyled(th, width, label, text, ratio, ok, true)
}

// telemetryMetricLineNeutral is like telemetryMetricLine but never applies
// warn/crit pressure colors (for reclaimable cache — high is not bad).
func telemetryMetricLineNeutral(th theme.Theme, width int, label, text string, ratio float64, ok bool) string {
	return telemetryMetricLineStyled(th, width, label, text, ratio, ok, false)
}

func telemetryMetricLineStyled(th theme.Theme, width int, label, text string, ratio float64, ok, pressure bool) string {
	th = th.Resolve()
	st := th.S()
	if width <= 0 {
		return ""
	}
	gap := themedSpace(th.Spacing.SM)
	labelPart := st.Muted.Render(label)
	labelW := ansi.StringWidth(ansi.Strip(labelPart)) + ansi.StringWidth(gap)

	// Bar sizing: prefer trailing bar when there is room.
	barW := 0
	const minBar = 4
	const maxBar = 12
	if width >= 28 {
		barW = min(maxBar, max(minBar, width/4))
	}
	if width < 22 {
		barW = 0
	}

	textStyle := st.Text
	if text == telemetryUnavailable {
		textStyle = st.Muted
	} else if ok && pressure {
		switch {
		case ratio > telemetryCritRatio:
			textStyle = st.Error
		case ratio >= telemetryWarnRatio:
			textStyle = st.Warning
		default:
			textStyle = st.Success
		}
	} else if ok {
		textStyle = st.Muted
	}

	bar := ""
	barPad := 0
	if barW > 0 {
		meterRatio := -1.0
		if ok {
			meterRatio = ratio
		}
		bar = themedSpace(th.Spacing.XS) + ui.Meter(th, barW, meterRatio)
		barPad = ansi.StringWidth(ansi.Strip(bar))
	}

	budget := max(0, width-labelW-barPad)
	body := textStyle.Render(welcomeTruncate(text, budget, th.Icons.Ellipsis))
	line := labelPart + gap + body + bar
	if pad := width - lipgloss.Width(line); pad > 0 {
		line += themedSpace(pad)
	}
	if lipgloss.Width(line) > width {
		return ansi.Truncate(line, width, th.Icons.Ellipsis)
	}
	return line
}
