package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

const (
	completionMaxRows  = 6
	completionMaxWidth = 72
)

type runeRange struct {
	Start int
	End   int
}

type completionCandidate struct {
	Spec   commandSpec
	Source commandSource
}

type completionState struct {
	Candidates []completionCandidate
	Selected   int
	Source     commandSource
	Replace    runeRange
	rows       int
}

func commandMatches(catalog []commandSpec, query string) []completionCandidate {
	query = strings.ToLower(strings.TrimPrefix(query, "/"))
	buckets := [3][]completionCandidate{}
	for _, spec := range catalog {
		name := strings.ToLower(strings.TrimPrefix(spec.Name, "/"))
		rank := -1
		switch {
		case name == query:
			rank = 0
		case strings.HasPrefix(name, query):
			rank = 1
		case orderedSubsequence(name, query):
			rank = 2
		}
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], completionCandidate{Spec: spec, Source: spec.Source})
		}
	}
	matches := make([]completionCandidate, 0, len(catalog))
	for _, bucket := range buckets {
		matches = append(matches, bucket...)
	}
	return matches
}

func orderedSubsequence(value, query string) bool {
	if query == "" {
		return true
	}
	needle := []rune(query)
	matched := 0
	for _, r := range value {
		if r == needle[matched] {
			matched++
			if matched == len(needle) {
				return true
			}
		}
	}
	return false
}

func leadingSlashCompletion(value string, cursorRow, cursorCol int, catalog []commandSpec) *completionState {
	if cursorRow != 0 || cursorCol <= 0 {
		return nil
	}
	lines := strings.Split(value, "\n")
	first := []rune(lines[0])
	if len(first) == 0 || first[0] != '/' {
		return nil
	}
	end := 0
	for end < len(first) && !unicode.IsSpace(first[end]) {
		end++
	}
	if cursorCol > end {
		return nil
	}
	queryEnd := min(cursorCol, end)
	matches := commandMatches(catalog, string(first[1:queryEnd]))
	if len(matches) == 0 {
		return nil
	}
	return &completionState{
		Candidates: matches,
		Source:     matches[0].Source,
		Replace:    runeRange{Start: 0, End: end},
	}
}

func (c *completionState) move(delta int) {
	if len(c.Candidates) == 0 {
		return
	}
	c.Selected = (c.Selected + delta + len(c.Candidates)) % len(c.Candidates)
	c.Source = c.Candidates[c.Selected].Source
}

func (c *completionState) view(width int, th theme.Theme) string {
	if c == nil || c.rows <= 0 || len(c.Candidates) == 0 || width <= 0 {
		return ""
	}
	rows := min(c.rows, len(c.Candidates))
	start := max(0, min(c.Selected-rows/2, len(c.Candidates)-rows))
	end := min(len(c.Candidates), start+rows)
	popupWidth := min(width, completionMaxWidth)
	innerWidth := max(1, popupWidth-2)

	muted := lipgloss.NewStyle().Foreground(th.TextMuted)
	selected := lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
	normal := lipgloss.NewStyle().Foreground(th.Text)
	lines := make([]string, 0, rows)
	for i := start; i < end; i++ {
		candidate := c.Candidates[i]
		marker := "  "
		nameStyle := normal
		if i == c.Selected {
			marker = "▸ "
			nameStyle = selected
		}
		name := candidate.Spec.Name
		if candidate.Spec.ArgsHint != "" {
			name += " " + candidate.Spec.ArgsHint
		}
		description := candidate.Spec.Description
		plain := marker + name
		if description != "" {
			plain += " — " + description
		}
		plain = truncateDisplay(plain, innerWidth)

		styledName := marker + nameStyle.Render(name)
		if description != "" {
			styledName += muted.Render(" — " + description)
		}
		if lipgloss.Width(styledName) > innerWidth {
			styledName = normal.Render(plain)
		}
		lines = append(lines, styledName)
	}

	body := strings.Join(lines, "\n")
	if popupWidth < 4 {
		return body
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.BorderFocus).
		Width(innerWidth).
		Render(body)
}

func truncateDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var out strings.Builder
	used := 0
	for _, r := range value {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > width-1 {
			break
		}
		out.WriteRune(r)
		used += runeWidth
	}
	return out.String() + "…"
}
