package ui

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// defaultDiffMaxLines is used when DiffPreviewOpts.MaxLines is <= 0.
const defaultDiffMaxLines = 12

// minDiffLineNumberWidth is the minimum outer width that still fits a line
// number gutter (nn + guide + marker + a few content cells).
const minDiffLineNumberWidth = 16

// lcsLineBudget is the max product of old×new line counts for full LCS.
// Above this we fall back to prefix/suffix matching so huge files stay snappy.
const lcsLineBudget = 250_000

// DiffPreviewOpts configures DiffPreview.
type DiffPreviewOpts struct {
	Path      string
	Old       string
	New       string
	MaxLines  int // max hunk body lines; <=0 → defaultDiffMaxLines (12)
	Width     int // required for output; <=0 → return ""
	ShowStats bool
	// LinkBase resolves relative Path values for OSC 8 file:// hyperlinks.
	// Empty skips relative path links; absolute paths and empty Path are fine.
	LinkBase string
	// MoreHint, when non-empty and the body is truncated, is appended after the
	// more-lines count (for example "alt+enter to expand").
	MoreHint string
	// Offset skips the first N body lines of the full unified diff. Used for
	// scrollable large diffs (modals). Values < 0 are treated as 0. When
	// Offset is 0, truncation still prefers the changed region (historic).
	Offset int
	// NoLineNumbers disables the line-number gutter (auto-shown when width
	// allows). Word-diff highlights remain on.
	NoLineNumbers bool
	// NoWordDiff disables intra-line bold highlights on paired replace lines.
	NoWordDiff bool
}

// DiffBodyLen returns the number of unified hunk body lines for Old→New
// (equal + delete + insert rows from the line diff).
func DiffBodyLen(oldStr, newStr string) int {
	return len(lineDiff(oldStr, newStr))
}

// DiffExceeds reports whether the unified body has more than maxLines rows.
// maxLines <= 0 uses defaultDiffMaxLines.
func DiffExceeds(oldStr, newStr string, maxLines int) bool {
	if maxLines <= 0 {
		maxLines = defaultDiffMaxLines
	}
	return DiffBodyLen(oldStr, newStr) > maxLines
}

// DiffMaxOffset returns the largest Offset that still shows a full MaxLines
// window (or 0 when the body fits). Callers clamp scroll positions with this.
func DiffMaxOffset(oldStr, newStr string, maxLines int) int {
	if maxLines <= 0 {
		maxLines = defaultDiffMaxLines
	}
	n := DiffBodyLen(oldStr, newStr)
	if n <= maxLines {
		return 0
	}
	return n - maxLines
}

// DiffWindowStart returns the body offset used when Offset is 0 (change-
// preferring trim). Scroll handlers should snap to this before applying a
// delta so the first ↓ does not jump from the change region to line 0.
func DiffWindowStart(oldStr, newStr string, maxLines int) int {
	if maxLines <= 0 {
		maxLines = defaultDiffMaxLines
	}
	lines := lineDiff(oldStr, newStr)
	start, _ := preferredWindow(lines, maxLines)
	return start
}

type diffOp int

const (
	diffEqual diffOp = iota
	diffDelete
	diffInsert
)

type hlSpan struct {
	text    string
	changed bool
}

type diffLine struct {
	op    diffOp
	text  string
	oldLn int // 1-based; 0 when not applicable
	newLn int
	spans []hlSpan // optional word-diff segments; nil → plain text
}

// DiffPreview renders a unified +/-/context diff of Old→New using theme
// DiffAdded/DiffRemoved styles. It is width-safe and height-bounded.
//
// Visual upgrades over a bare unified dump:
//   - LCS line matching (multi-hunk aware; prefix/suffix fallback on huge inputs)
//   - optional line-number gutter with theme guide glyph
//   - SurfaceMuted wash on add/remove rows
//   - bold word-level highlights on paired replace lines
//   - Offset window for scrollable large diffs
func DiffPreview(th theme.Theme, opts DiffPreviewOpts) string {
	if opts.Width <= 0 {
		return ""
	}
	if opts.Old == "" && opts.New == "" && opts.Path == "" && !opts.ShowStats {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	ic := resolveIcons(th)
	space := strings.Repeat(" ", th.Spacing.XS)

	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = defaultDiffMaxLines
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	lines := lineDiff(opts.Old, opts.New)
	if !opts.NoWordDiff {
		applyWordDiff(lines)
	}
	added, removed := 0, 0
	for _, dl := range lines {
		switch dl.op {
		case diffInsert:
			added++
		case diffDelete:
			removed++
		}
	}

	var parts []string

	// Header does not consume MaxLines.
	if opts.Path != "" || opts.ShowStats {
		var headerBits []string
		if opts.Path != "" {
			pathStyled := st.Muted.Render(opts.Path)
			if uri := fileLinkURI(opts.Path, opts.LinkBase); uri != "" {
				pathStyled = ansi.SetHyperlink(uri) + pathStyled + ansi.ResetHyperlink()
			}
			headerBits = append(headerBits, pathStyled)
		}
		if opts.ShowStats {
			headerBits = append(headerBits,
				st.DiffAdded.Render("+"+strconv.Itoa(added)),
				st.DiffRemoved.Render("-"+strconv.Itoa(removed)),
			)
		}
		header := strings.Join(headerBits, space)
		if h := truncate(th, header, opts.Width); h != "" {
			parts = append(parts, h)
		}
	}

	// Empty old+new with only a stats/path header still renders the header.
	if opts.Old == "" && opts.New == "" && len(lines) == 0 {
		return strings.Join(parts, "\n")
	}

	body, overflowBefore, overflowAfter := windowDiffBody(lines, maxLines, offset)
	showNums := !opts.NoLineNumbers && opts.Width >= minDiffLineNumberWidth
	lnWidth := 0
	if showNums {
		lnWidth = lineNumberWidth(body)
	}
	for _, dl := range body {
		parts = append(parts, formatDiffRow(th, st, ic, dl, opts.Width, lnWidth, showNums))
	}
	if overflowBefore > 0 || overflowAfter > 0 {
		overflow := overflowBefore + overflowAfter
		more := ic.Ellipsis + space + "(" + strconv.Itoa(overflow) + space + "more lines"
		if hint := strings.TrimSpace(opts.MoreHint); hint != "" {
			more += space + ic.Dot + space + hint
		}
		more += ")"
		parts = append(parts, truncate(th, st.Muted.Render(more), opts.Width))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func formatDiffRow(th theme.Theme, st theme.Styles, ic theme.Icons, dl diffLine, width, lnWidth int, showNums bool) string {
	var base, strong lipgloss.Style
	var marker string
	switch dl.op {
	case diffDelete:
		base, strong = st.DiffRemovedLine, st.DiffRemovedStrong
		marker = "-"
	case diffInsert:
		base, strong = st.DiffAddedLine, st.DiffAddedStrong
		marker = "+"
	default:
		base, strong = st.Muted, st.MutedStrong
		marker = " "
	}

	gutter := ""
	if showNums && lnWidth > 0 {
		n := 0
		switch dl.op {
		case diffDelete:
			n = dl.oldLn
		case diffInsert:
			n = dl.newLn
		default:
			if dl.oldLn > 0 {
				n = dl.oldLn
			} else {
				n = dl.newLn
			}
		}
		num := strings.Repeat(" ", lnWidth)
		if n > 0 {
			num = padLeftNum(n, lnWidth)
		}
		guide := ic.ToolGuide
		if guide == "" {
			guide = theme.DefaultIcons().ToolGuide
		}
		gutter = st.Muted.Render(num) + st.BorderMuted.Render(guide)
	}

	markerStyled := base.Render(marker)
	used := lipgloss.Width(gutter) + 1 // marker is one cell
	contentWidth := width - used
	if contentWidth < 1 {
		return truncate(th, gutter+markerStyled, width)
	}

	content := renderDiffContent(th, base, strong, dl, contentWidth)
	row := gutter + markerStyled + content
	if gap := width - lipgloss.Width(row); gap > 0 && (dl.op == diffDelete || dl.op == diffInsert) {
		row += base.Render(strings.Repeat(" ", gap))
	}
	return truncate(th, row, width)
}

func renderDiffContent(th theme.Theme, base, strong lipgloss.Style, dl diffLine, width int) string {
	if width <= 0 {
		return ""
	}
	spans := dl.spans
	if len(spans) == 0 {
		spans = []hlSpan{{text: dl.text, changed: false}}
	}

	// Build plain first so we can truncate by display width, then restyle.
	var plain strings.Builder
	for _, sp := range spans {
		plain.WriteString(sp.text)
	}
	full := plain.String()
	if lipgloss.Width(full) > width {
		// Truncate plain, then map back onto spans.
		trunc := ansi.Truncate(full, width, resolveIcons(th).Ellipsis)
		return paintTruncatedSpans(base, strong, spans, trunc, dl.op)
	}

	var b strings.Builder
	for _, sp := range spans {
		style := base
		if sp.changed {
			style = strong
		}
		b.WriteString(style.Render(sp.text))
	}
	out := b.String()
	if gap := width - lipgloss.Width(out); gap > 0 && (dl.op == diffDelete || dl.op == diffInsert) {
		out += base.Render(strings.Repeat(" ", gap))
	}
	return out
}

// paintTruncatedSpans renders spans clipped to the already-truncated plain
// string (which may end in an ellipsis glyph).
func paintTruncatedSpans(base, strong lipgloss.Style, spans []hlSpan, trunc string, _ diffOp) string {
	var b strings.Builder
	remain := trunc
	for _, sp := range spans {
		if remain == "" {
			break
		}
		style := base
		if sp.changed {
			style = strong
		}
		if strings.HasPrefix(remain, sp.text) {
			b.WriteString(style.Render(sp.text))
			remain = remain[len(sp.text):]
			continue
		}
		// Partial span or ellipsis tail — paint rest with current style and stop.
		b.WriteString(style.Render(remain))
		remain = ""
		break
	}
	if remain != "" {
		b.WriteString(base.Render(remain))
	}
	if b.Len() == 0 {
		return base.Render(trunc)
	}
	return b.String()
}

func padLeftNum(n, width int) string {
	s := strconv.Itoa(n)
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}

func lineNumberWidth(body []diffLine) int {
	maxN := 0
	for _, dl := range body {
		if dl.oldLn > maxN {
			maxN = dl.oldLn
		}
		if dl.newLn > maxN {
			maxN = dl.newLn
		}
	}
	if maxN <= 0 {
		return 0
	}
	w := len(strconv.Itoa(maxN))
	if w < 1 {
		return 1
	}
	return w
}

// windowDiffBody selects a MaxLines window. Offset 0 keeps the historic
// change-preferring trim; Offset > 0 scrolls a plain window over the full body.
func windowDiffBody(lines []diffLine, maxLines, offset int) (body []diffLine, overflowBefore, overflowAfter int) {
	if maxLines <= 0 {
		return lines, 0, 0
	}
	if len(lines) <= maxLines && offset <= 0 {
		return lines, 0, 0
	}

	if offset > 0 {
		if offset > len(lines) {
			offset = len(lines)
		}
		end := offset + maxLines
		if end > len(lines) {
			end = len(lines)
		}
		return lines[offset:end], offset, len(lines) - end
	}

	// Offset 0: prefer the changed region (drop leading/trailing equal first).
	start, end := preferredWindow(lines, maxLines)
	return lines[start:end], start, len(lines) - end
}

// preferredWindow returns the [start,end) body range for change-preferring trim.
func preferredWindow(lines []diffLine, maxLines int) (start, end int) {
	if maxLines <= 0 || len(lines) <= maxLines {
		return 0, len(lines)
	}
	start = 0
	for len(lines)-start > maxLines && start < len(lines) && lines[start].op == diffEqual {
		start++
	}
	end = len(lines)
	for end-start > maxLines && end > start && lines[end-1].op == diffEqual {
		end--
	}
	if end-start > maxLines {
		end = start + maxLines
	}
	return start, end
}

// trimDiffBody prefers the changed region when truncating to maxLines.
// Kept for tests and internal callers that want pre-Offset helper semantics.
func trimDiffBody(lines []diffLine, maxLines int) (body []diffLine, overflow int) {
	body, before, after := windowDiffBody(lines, maxLines, 0)
	return body, before + after
}

// splitLines splits s on \n. An empty string yields no lines so a pure
// insertion/deletion does not invent a blank counterpart line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// fileLinkURI builds a file:// OSC 8 target for path. Relative paths need
// linkBase; absolute paths work alone. Returns "" when the path should not
// be linked.
func fileLinkURI(path, linkBase string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, " \t\n\r") || strings.Contains(path, "://") {
		return ""
	}
	if !filepath.IsAbs(path) {
		if linkBase == "" {
			return ""
		}
		path = filepath.Join(linkBase, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(abs)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// lineDiff splits old and new on \n and returns an LCS line diff with
// 1-based line numbers. Huge inputs fall back to prefix/suffix matching.
func lineDiff(oldStr, newStr string) []diffLine {
	a := splitLines(oldStr)
	b := splitLines(newStr)
	if len(a)+len(b) == 0 {
		return nil
	}
	if len(a) > 0 && len(b) > 0 && len(a)*len(b) > lcsLineBudget {
		return lineDiffPrefixSuffix(a, b)
	}
	return lcsLineDiff(a, b)
}

// lineDiffPrefixSuffix finds the longest common prefix and suffix of equal
// lines, then treats the middle as all-old deleted followed by all-new inserted.
func lineDiffPrefixSuffix(a, b []string) []diffLine {
	pref := 0
	for pref < len(a) && pref < len(b) && a[pref] == b[pref] {
		pref++
	}
	suff := 0
	for pref+suff < len(a) && pref+suff < len(b) && a[len(a)-1-suff] == b[len(b)-1-suff] {
		suff++
	}

	out := make([]diffLine, 0, len(a)+len(b))
	oldLn, newLn := 1, 1
	for i := 0; i < pref; i++ {
		out = append(out, diffLine{op: diffEqual, text: a[i], oldLn: oldLn, newLn: newLn})
		oldLn++
		newLn++
	}
	for i := pref; i < len(a)-suff; i++ {
		out = append(out, diffLine{op: diffDelete, text: a[i], oldLn: oldLn})
		oldLn++
	}
	for i := pref; i < len(b)-suff; i++ {
		out = append(out, diffLine{op: diffInsert, text: b[i], newLn: newLn})
		newLn++
	}
	for i := len(a) - suff; i < len(a); i++ {
		out = append(out, diffLine{op: diffEqual, text: a[i], oldLn: oldLn, newLn: newLn})
		oldLn++
		newLn++
	}
	return out
}

// lcsLineDiff is a classic DP LCS backtrack over lines. Shared lines stay as
// context; everything else is delete then insert — multi-hunk aware.
func lcsLineDiff(a, b []string) []diffLine {
	n, m := len(a), len(b)
	// dp[i][j] = LCS length of a[i:] and b[j:]
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	out := make([]diffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			out = append(out, diffLine{op: diffEqual, text: a[i], oldLn: i + 1, newLn: j + 1})
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			out = append(out, diffLine{op: diffDelete, text: a[i], oldLn: i + 1})
			i++
		} else {
			out = append(out, diffLine{op: diffInsert, text: b[j], newLn: j + 1})
			j++
		}
	}
	for i < n {
		out = append(out, diffLine{op: diffDelete, text: a[i], oldLn: i + 1})
		i++
	}
	for j < m {
		out = append(out, diffLine{op: diffInsert, text: b[j], newLn: j + 1})
		j++
	}
	return out
}

// applyWordDiff annotates paired delete/insert runs with intra-line spans.
func applyWordDiff(lines []diffLine) {
	i := 0
	for i < len(lines) {
		if lines[i].op != diffDelete {
			i++
			continue
		}
		delStart := i
		for i < len(lines) && lines[i].op == diffDelete {
			i++
		}
		insStart := i
		for i < len(lines) && lines[i].op == diffInsert {
			i++
		}
		delEnd, insEnd := insStart, i
		nd, ni := delEnd-delStart, insEnd-insStart
		if nd == 0 || ni == 0 {
			continue
		}
		n := nd
		if ni < n {
			n = ni
		}
		for k := 0; k < n; k++ {
			oldSpans, newSpans := wordDiffSpans(lines[delStart+k].text, lines[insStart+k].text)
			if spansUseful(oldSpans) {
				lines[delStart+k].spans = oldSpans
			}
			if spansUseful(newSpans) {
				lines[insStart+k].spans = newSpans
			}
		}
	}
}

func spansUseful(spans []hlSpan) bool {
	if len(spans) <= 1 {
		return false
	}
	for _, sp := range spans {
		if sp.changed && sp.text != "" {
			return true
		}
	}
	return false
}

// wordDiffSpans tokenizes old/new and marks non-LCS tokens as changed.
func wordDiffSpans(oldS, newS string) (oldSpans, newSpans []hlSpan) {
	if oldS == newS {
		return nil, nil
	}
	// Rune-level for short lines (tighter highlights); word tokens otherwise.
	if utf8.RuneCountInString(oldS) <= 120 && utf8.RuneCountInString(newS) <= 120 {
		return sequenceDiffSpans(splitRunes(oldS), splitRunes(newS))
	}
	return sequenceDiffSpans(tokenizeDiff(oldS), tokenizeDiff(newS))
}

func splitRunes(s string) []string {
	if s == "" {
		return nil
	}
	rs := []rune(s)
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}

func tokenizeDiff(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			flush()
			out = append(out, string(r))
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return out
}

// sequenceDiffSpans LCS-diffs two token sequences and collapses consecutive
// equal/changed runs into hlSpan values for each side.
func sequenceDiffSpans(ot, nt []string) (oldSpans, newSpans []hlSpan) {
	n, m := len(ot), len(nt)
	// Cap DP for pathological long token lists.
	if n > 400 {
		ot = ot[:400]
		n = 400
	}
	if m > 400 {
		nt = nt[:400]
		m = 400
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ot[i] == nt[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var oldParts, newParts []hlSpan
	var ob, nb strings.Builder
	och, nch := false, false
	flushOld := func() {
		if ob.Len() == 0 {
			return
		}
		oldParts = append(oldParts, hlSpan{text: ob.String(), changed: och})
		ob.Reset()
	}
	flushNew := func() {
		if nb.Len() == 0 {
			return
		}
		newParts = append(newParts, hlSpan{text: nb.String(), changed: nch})
		nb.Reset()
	}
	pushOld := func(s string, ch bool) {
		if och != ch && ob.Len() > 0 {
			flushOld()
		}
		och = ch
		ob.WriteString(s)
	}
	pushNew := func(s string, ch bool) {
		if nch != ch && nb.Len() > 0 {
			flushNew()
		}
		nch = ch
		nb.WriteString(s)
	}

	i, j := 0, 0
	for i < n && j < m {
		if ot[i] == nt[j] {
			pushOld(ot[i], false)
			pushNew(nt[j], false)
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			pushOld(ot[i], true)
			i++
		} else {
			pushNew(nt[j], true)
			j++
		}
	}
	for i < n {
		pushOld(ot[i], true)
		i++
	}
	for j < m {
		pushNew(nt[j], true)
		j++
	}
	flushOld()
	flushNew()
	return oldParts, newParts
}
