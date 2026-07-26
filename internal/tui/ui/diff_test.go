package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestDiffPreview(t *testing.T) {
	th := theme.Default()

	t.Run("width <= 0 returns empty", func(t *testing.T) {
		for _, width := range []int{0, -1, -10} {
			got := DiffPreview(th, DiffPreviewOpts{
				Path: "file.go", Old: "a", New: "b", Width: width, ShowStats: true,
			})
			if got != "" {
				t.Errorf("Width=%d: got %q, want empty", width, got)
			}
		}
	})

	t.Run("empty old+new without path or stats returns empty", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{Width: 40})
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("pure insert lines start with +", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Old: "", New: "alpha\nbeta", Width: 40,
		})
		plain := ansi.Strip(got)
		for i, line := range nonEmptyLines(plain) {
			if !strings.HasPrefix(line, "+") {
				t.Errorf("line %d = %q, want + prefix", i, line)
			}
		}
		if !strings.Contains(plain, "+alpha") || !strings.Contains(plain, "+beta") {
			t.Errorf("missing insert content: %q", plain)
		}
		if strings.Contains(plain, "-") {
			// bare "-" marker for deletes should not appear on pure insert
			for _, line := range nonEmptyLines(plain) {
				if strings.HasPrefix(line, "-") {
					t.Errorf("unexpected delete line on pure insert: %q", plain)
				}
			}
		}
	})

	t.Run("pure delete lines start with -", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Old: "alpha\nbeta", New: "", Width: 40,
		})
		plain := ansi.Strip(got)
		for i, line := range nonEmptyLines(plain) {
			if !strings.HasPrefix(line, "-") {
				t.Errorf("line %d = %q, want - prefix", i, line)
			}
		}
		if !strings.Contains(plain, "-alpha") || !strings.Contains(plain, "-beta") {
			t.Errorf("missing delete content: %q", plain)
		}
		for _, line := range nonEmptyLines(plain) {
			if strings.HasPrefix(line, "+") {
				t.Errorf("unexpected insert line on pure delete: %q", plain)
			}
		}
	})

	t.Run("shared prefix and suffix with middle replace", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Old:   "head\nold mid\ntail",
			New:   "head\nnew mid\ntail",
			Width: 40,
		})
		plain := ansi.Strip(got)
		lines := nonEmptyLines(plain)
		if len(lines) != 4 {
			t.Fatalf("lines = %v (%d), want 4 (context, -, +, context)", lines, len(lines))
		}
		if !strings.HasPrefix(lines[0], " ") || !strings.Contains(lines[0], "head") {
			t.Errorf("prefix context = %q, want leading-space head", lines[0])
		}
		if !strings.HasPrefix(lines[1], "-") || !strings.Contains(lines[1], "old mid") {
			t.Errorf("delete = %q, want -old mid", lines[1])
		}
		if !strings.HasPrefix(lines[2], "+") || !strings.Contains(lines[2], "new mid") {
			t.Errorf("insert = %q, want +new mid", lines[2])
		}
		if !strings.HasPrefix(lines[3], " ") || !strings.Contains(lines[3], "tail") {
			t.Errorf("suffix context = %q, want leading-space tail", lines[3])
		}
	})

	t.Run("ShowStats includes +N and -M matching counts", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Old:       "a\nb\nc",
			New:       "a\nx\ny\nc",
			Width:     40,
			ShowStats: true,
		})
		plain := ansi.Strip(got)
		// middle: -b, +x, +y → +2 -1
		if !strings.Contains(plain, "+2") {
			t.Errorf("missing +2 added stats in %q", plain)
		}
		if !strings.Contains(plain, "-1") {
			t.Errorf("missing -1 removed stats in %q", plain)
		}
	})

	t.Run("path header present when Path set", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Path:  "internal/foo.go",
			Old:   "a",
			New:   "b",
			Width: 40,
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "internal/foo.go") {
			t.Errorf("missing path header: %q", plain)
		}
		// path is header-only; body still has diff markers
		if !strings.Contains(plain, "-a") || !strings.Contains(plain, "+b") {
			t.Errorf("missing body markers: %q", plain)
		}
	})

	t.Run("path and stats header without body when old and new empty", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Path: "empty.go", Width: 40, ShowStats: true,
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "empty.go") {
			t.Errorf("missing path: %q", plain)
		}
		if !strings.Contains(plain, "+0") || !strings.Contains(plain, "-0") {
			t.Errorf("missing zero stats: %q", plain)
		}
	})

	t.Run("MaxLines truncation keeps first change lines and reports overflow", func(t *testing.T) {
		// Pure replace (no shared context): change block alone exceeds MaxLines.
		// After dropping no equal context, first MaxLines of the remainder are kept
		// (deletes first in the prefix/suffix algorithm).
		var oldB, newB strings.Builder
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&oldB, "old-%d\n", i)
			fmt.Fprintf(&newB, "new-%d\n", i)
		}
		const maxLines = 4
		got := DiffPreview(th, DiffPreviewOpts{
			Old:      strings.TrimSuffix(oldB.String(), "\n"),
			New:      strings.TrimSuffix(newB.String(), "\n"),
			MaxLines: maxLines,
			Width:    60,
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "more lines") {
			t.Errorf("missing overflow notice: %q", plain)
		}
		if !strings.Contains(plain, th.Resolve().Icons.Ellipsis) {
			t.Errorf("missing ellipsis in overflow: %q", plain)
		}
		// 10 deletes + 10 inserts = 20 body lines; overflow = 20-4 = 16
		if !strings.Contains(plain, "16") {
			t.Errorf("expected overflow count 16 in %q", plain)
		}
		if bodyLines := countDiffBodyLines(plain); bodyLines != maxLines {
			t.Errorf("body hunk lines = %d, want %d; plain=%q", bodyLines, maxLines, plain)
		}
		if !strings.Contains(plain, "-old-0") {
			t.Errorf("first change line not kept: %q", plain)
		}
		if strings.Contains(plain, "-old-9") {
			t.Errorf("truncated delete should not appear: %q", plain)
		}
		if strings.Contains(plain, "+new-0") {
			t.Errorf("inserts should be past MaxLines window: %q", plain)
		}
	})

	t.Run("MaxLines prefers changed region over long shared prefix", func(t *testing.T) {
		// 9 unique equal prefix lines + one-line replace would hide the edit
		// under head-only truncation at MaxLines=8. Prefer dropping leading
		// context so -OLD/+NEW remain visible.
		var ctx strings.Builder
		for i := 0; i < 9; i++ {
			fmt.Fprintf(&ctx, "context-%d\n", i)
		}
		prefix := ctx.String()
		const maxLines = 8
		got := DiffPreview(th, DiffPreviewOpts{
			Old: prefix + "OLD", New: prefix + "NEW",
			MaxLines: maxLines, Width: 60,
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "-OLD") {
			t.Errorf("missing delete of changed line: %q", plain)
		}
		if !strings.Contains(plain, "+NEW") {
			t.Errorf("missing insert of changed line: %q", plain)
		}
		// 9 equal + 1 del + 1 ins = 11; drop 3 leading equal → overflow 3
		if !strings.Contains(plain, "more lines") {
			t.Errorf("expected overflow for dropped leading context: %q", plain)
		}
		if !strings.Contains(plain, "3") {
			t.Errorf("expected overflow count 3 in %q", plain)
		}
		if bodyLines := countDiffBodyLines(plain); bodyLines != maxLines {
			t.Errorf("body hunk lines = %d, want %d; plain=%q", bodyLines, maxLines, plain)
		}
		// earliest context should have been dropped first
		if strings.Contains(plain, "context-0") {
			t.Errorf("leading context-0 should be dropped before the change: %q", plain)
		}
	})

	t.Run("MaxLines drops trailing context after leading context", func(t *testing.T) {
		// Short change flanked by long equal prefix and suffix. Leading equal
		// lines drop first, then trailing equal, so the change stays visible.
		var pre, suf strings.Builder
		for i := 0; i < 6; i++ {
			fmt.Fprintf(&pre, "pre-%d\n", i)
			fmt.Fprintf(&suf, "suf-%d\n", i)
		}
		old := pre.String() + "OLD\n" + strings.TrimSuffix(suf.String(), "\n")
		newS := pre.String() + "NEW\n" + strings.TrimSuffix(suf.String(), "\n")
		const maxLines = 4
		got := DiffPreview(th, DiffPreviewOpts{
			Old: old, New: newS, MaxLines: maxLines, Width: 60,
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "-OLD") || !strings.Contains(plain, "+NEW") {
			t.Errorf("change must remain visible: %q", plain)
		}
		if !strings.Contains(plain, "more lines") {
			t.Errorf("expected overflow notice: %q", plain)
		}
		// 6 pre + del + ins + 6 suf = 14; keep 4 → overflow 10
		if !strings.Contains(plain, "10") {
			t.Errorf("expected overflow count 10 in %q", plain)
		}
		if strings.Contains(plain, "pre-0") {
			t.Errorf("leading context should be dropped first: %q", plain)
		}
		if strings.Contains(plain, "suf-5") {
			t.Errorf("trailing context should be dropped before shrinking the change: %q", plain)
		}
		if bodyLines := countDiffBodyLines(plain); bodyLines != maxLines {
			t.Errorf("body hunk lines = %d, want %d; plain=%q", bodyLines, maxLines, plain)
		}
	})

	t.Run("MaxLines when change alone exceeds limit shows first MaxLines of change", func(t *testing.T) {
		// Shared one-line prefix/suffix with a large middle replace that alone
		// exceeds MaxLines. Equal context is dropped entirely; first MaxLines
		// of the change remainder are kept.
		var midOld, midNew strings.Builder
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&midOld, "old-mid-%d\n", i)
			fmt.Fprintf(&midNew, "new-mid-%d\n", i)
		}
		old := "HEAD\n" + midOld.String() + "TAIL"
		newS := "HEAD\n" + midNew.String() + "TAIL"
		const maxLines = 5
		got := DiffPreview(th, DiffPreviewOpts{
			Old: old, New: newS, MaxLines: maxLines, Width: 60,
		})
		plain := ansi.Strip(got)
		if strings.Contains(plain, "HEAD") || strings.Contains(plain, "TAIL") {
			t.Errorf("equal context should be dropped when change alone exceeds MaxLines: %q", plain)
		}
		if !strings.Contains(plain, "-old-mid-0") {
			t.Errorf("first change line should be kept: %q", plain)
		}
		if strings.Contains(plain, "-old-mid-9") {
			t.Errorf("change lines past MaxLines should be truncated: %q", plain)
		}
		if !strings.Contains(plain, "more lines") {
			t.Errorf("expected overflow notice: %q", plain)
		}
		// 1 head + 10 del + 10 ins + 1 tail = 22; keep 5 → overflow 17
		if !strings.Contains(plain, "17") {
			t.Errorf("expected overflow count 17 in %q", plain)
		}
		if bodyLines := countDiffBodyLines(plain); bodyLines != maxLines {
			t.Errorf("body hunk lines = %d, want %d; plain=%q", bodyLines, maxLines, plain)
		}
	})

	t.Run("MaxLines default when 0 is 12", func(t *testing.T) {
		var oldB, newB strings.Builder
		for i := 0; i < 20; i++ {
			fmt.Fprintf(&oldB, "o%d\n", i)
			fmt.Fprintf(&newB, "n%d\n", i)
		}
		got := DiffPreview(th, DiffPreviewOpts{
			Old:      strings.TrimSuffix(oldB.String(), "\n"),
			New:      strings.TrimSuffix(newB.String(), "\n"),
			MaxLines: 0,
			Width:    40,
		})
		plain := ansi.Strip(got)
		// 20 del + 20 ins = 40; default max 12 → overflow 28
		if !strings.Contains(plain, "more lines") {
			t.Errorf("default MaxLines should truncate: %q", plain)
		}
		if !strings.Contains(plain, "28") {
			t.Errorf("want overflow 28 with default max 12: %q", plain)
		}
		if bodyLines := countDiffBodyLines(plain); bodyLines != 12 {
			t.Errorf("default MaxLines body count = %d, want 12; plain=%q", bodyLines, plain)
		}
	})

	t.Run("width safety every line within Width", func(t *testing.T) {
		longOld := strings.Repeat("old-content-", 20) + "\n" + strings.Repeat("x", 100)
		longNew := strings.Repeat("new-content-", 20) + "\n" + strings.Repeat("y", 100)
		for _, width := range []int{4, 20, 40, 80} {
			got := DiffPreview(th, DiffPreviewOpts{
				Path: "very/long/path/to/some/file.go",
				Old:  longOld, New: longNew,
				MaxLines: 8, Width: width, ShowStats: true,
			})
			if got == "" && width > 0 {
				// still may be non-empty; empty only if truncate wiped everything
			}
			for i, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Errorf("width %d: line %d width = %d > %d: %q", width, i, w, width, ansi.Strip(line))
				}
			}
		}
	})

	t.Run("single-line replace", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Old: "foo", New: "bar", Width: 40,
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "-foo") || !strings.Contains(plain, "+bar") {
			t.Errorf("single-line replace = %q", plain)
		}
	})

	t.Run("MoreHint appears only when truncated", func(t *testing.T) {
		var oldB, newB strings.Builder
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&oldB, "old-%d\n", i)
			fmt.Fprintf(&newB, "new-%d\n", i)
		}
		oldS := strings.TrimSuffix(oldB.String(), "\n")
		newS := strings.TrimSuffix(newB.String(), "\n")
		got := DiffPreview(th, DiffPreviewOpts{
			Old: oldS, New: newS, MaxLines: 4, Width: 60, MoreHint: "enter to expand",
		})
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "more lines") || !strings.Contains(plain, "enter to expand") {
			t.Errorf("truncated missing hint: %q", plain)
		}
		// Full body: no overflow → no hint.
		full := DiffPreview(th, DiffPreviewOpts{
			Old: oldS, New: newS, MaxLines: DiffBodyLen(oldS, newS), Width: 60, MoreHint: "enter to expand",
		})
		fullPlain := ansi.Strip(full)
		if strings.Contains(fullPlain, "more lines") || strings.Contains(fullPlain, "enter to expand") {
			t.Errorf("full body should not show hint: %q", fullPlain)
		}
		// Short replace with MaxLines room: no hint.
		short := DiffPreview(th, DiffPreviewOpts{
			Old: "a", New: "b", MaxLines: 8, Width: 40, MoreHint: "enter to expand",
		})
		if strings.Contains(ansi.Strip(short), "enter to expand") {
			t.Errorf("short diff should not show hint: %q", short)
		}
	})

	t.Run("DiffBodyLen and DiffExceeds", func(t *testing.T) {
		if got := DiffBodyLen("a", "b"); got != 2 {
			t.Errorf("single replace body len = %d, want 2", got)
		}
		if DiffExceeds("a", "b", 8) {
			t.Error("short replace should not exceed 8")
		}
		var oldB, newB strings.Builder
		for i := 0; i < 10; i++ {
			fmt.Fprintf(&oldB, "o%d\n", i)
			fmt.Fprintf(&newB, "n%d\n", i)
		}
		oldS := strings.TrimSuffix(oldB.String(), "\n")
		newS := strings.TrimSuffix(newB.String(), "\n")
		// 10 deletes + 10 inserts
		if got := DiffBodyLen(oldS, newS); got != 20 {
			t.Errorf("body len = %d, want 20", got)
		}
		if !DiffExceeds(oldS, newS, 8) {
			t.Error("20-line body should exceed 8")
		}
		if DiffExceeds(oldS, newS, 20) {
			t.Error("exact MaxLines should not exceed")
		}
		if !DiffExceeds(oldS, newS, 0) {
			// default max is 12
			t.Error("20-line body should exceed default max")
		}
	})

	t.Run("multi-line replace with common prefix", func(t *testing.T) {
		got := DiffPreview(th, DiffPreviewOpts{
			Old:   "package main\n\nfunc a() {}\nfunc old() {}",
			New:   "package main\n\nfunc a() {}\nfunc new() {}",
			Width: 60,
		})
		plain := ansi.Strip(got)
		// shared prefix lines should appear as context
		if !strings.Contains(plain, "package main") {
			t.Errorf("missing shared prefix: %q", plain)
		}
		if !strings.Contains(plain, "-func old() {}") {
			t.Errorf("missing delete: %q", plain)
		}
		if !strings.Contains(plain, "+func new() {}") {
			t.Errorf("missing insert: %q", plain)
		}
		// context marker on package line
		foundContext := false
		for _, line := range nonEmptyLines(plain) {
			if strings.Contains(line, "package main") && strings.HasPrefix(line, " ") {
				foundContext = true
			}
		}
		if !foundContext {
			t.Errorf("package main should be context (leading space): %q", plain)
		}
	})
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// countDiffBodyLines counts unified-diff body rows (+/-/context), excluding
// the muted overflow footer.
func countDiffBodyLines(plain string) int {
	n := 0
	for _, line := range nonEmptyLines(plain) {
		if strings.Contains(line, "more lines") {
			continue
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
			n++
		}
	}
	return n
}
