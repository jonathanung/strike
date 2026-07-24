package tui

import (
	"strings"
	"unicode"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
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

// view renders the inline slash-command completion popup as a bordered panel of
// candidate rows above the composer. c.rows (set by reflow) bounds the visible
// window; keyboard handling stays in the app model.
func (c *completionState) view(width int, th theme.Theme) string {
	if c == nil || c.rows <= 0 || len(c.Candidates) == 0 || width <= 0 {
		return ""
	}
	popupWidth := min(width, completionMaxWidth)
	items := make([]ui.ListItem, len(c.Candidates))
	for i, candidate := range c.Candidates {
		name := candidate.Spec.Name
		if candidate.Spec.ArgsHint != "" {
			name += " " + candidate.Spec.ArgsHint
		}
		items[i] = ui.ListItem{Label: name, Detail: candidate.Spec.Description}
	}
	bodyWidth := max(1, ui.InnerWidth(popupWidth))
	if popupWidth < 4 {
		bodyWidth = max(1, popupWidth)
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  c.Selected,
		Width:   bodyWidth,
		Visible: min(c.rows, len(c.Candidates)),
	})
	if popupWidth < 4 {
		return body
	}
	return ui.Panel(th, ui.PanelOpts{Width: popupWidth, Focused: true}, body)
}
