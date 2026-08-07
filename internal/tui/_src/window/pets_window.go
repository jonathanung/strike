package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

const petsWindowID = "pets"

// petsAnimInterval is the frame period for idle pet animation (~2 fps).
const petsAnimInterval = 500 * time.Millisecond

// petsTickMsg advances the active pet animation frame.
type petsTickMsg struct{}

// petSpec is one selectable ASCII pet with one or more animation frames.
// Each frame is a multi-line pure-ASCII drawing (no emoji / wide glyphs).
type petSpec struct {
	ID     string
	Name   string
	Frames []string
}

// petCatalog is the built-in pet roster. Keep drawings narrow so they fit the
// default 32-col right pane without horizontal overflow.
var petCatalog = []petSpec{
	{
		ID:   "cat",
		Name: "cat",
		Frames: []string{
			" /\\_/\\\n( o.o )\n > ^ <",
			" /\\_/\\\n( -.- )\n > ^ <",
			" /\\_/\\\n( o.o )\n > ^ <",
			" /\\_/\\\n( ^.^ )\n > ^ <",
		},
	},
	{
		ID:   "dog",
		Name: "dog",
		Frames: []string{
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/",
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/  *",
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/",
			"  __\no'') }___//\n `_/      )\n (_(_/-(_/",
		},
	},
	{
		ID:   "panda",
		Name: "panda",
		Frames: []string{
			" (\\_/)\n (o.o)\n /| |\\\n  ^ ^",
			" (\\_/)\n (-.-)\n /| |\\\n  ^ ^",
			" (\\_/)\n (o.o)\n /| |\\\n  ^ ^",
			" (\\_/)\n (^.^)\n /| |\\\n  ^ ^",
		},
	},
	{
		ID:   "fish",
		Name: "fish",
		Frames: []string{
			"><(((('>",
			" ><(((('>",
			"  ><(((('>",
			" ><(((('>",
		},
	},
	{
		ID:   "owl",
		Name: "owl",
		Frames: []string{
			" ,___,\n( o,o )\n/)   (\\\n \" \" \"",
			" ,___,\n( -,o )\n/)   (\\\n \" \" \"",
			" ,___,\n( o,o )\n/)   (\\\n \" \" \"",
			" ,___,\n( o,- )\n/)   (\\\n \" \" \"",
		},
	},
	{
		ID:   "rabbit",
		Name: "rabbit",
		Frames: []string{
			" (\\_/)\n (o.o)\n (\"|\")",
			" (\\_/)\n (-.-)\n (\"|\")",
			" (\\_/)\n (o.o)\n (\"|\")",
			" (\\_/)\n (^.^)\n (\"|\")",
		},
	},
	{
		ID:   "fox",
		Name: "fox",
		Frames: []string{
			"  /\\   /\\\n (  .V.  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  -V-  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  .V.  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  ^V^  )\n  \\  ^  /\n   |||||",
		},
	},
	{
		ID:   "bear",
		Name: "bear",
		Frames: []string{
			"  (\"\"\")\n ( o o )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( - - )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( o o )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( ^ ^ )\n  \\ ^ /\n  (' ')",
		},
	},
	{
		ID:   "bird",
		Name: "bird",
		Frames: []string{
			"  .--.\n ( o> )\n /)  )\n  \"\"",
			"  .--.\n ( -> )\n /)  )\n  \"\"",
			"  .--.\n ( o> )\n /)  )\n  \"\"",
			" .--. \n( o> )\n/)  ) \n \"\"  ",
		},
	},
	{
		ID:   "frog",
		Name: "frog",
		Frames: []string{
			"  (.)_(.)\n (  . .  )\n  (_____) ",
			"  (-)_(-)\n (  . .  )\n  (_____) ",
			"  (.)_(.)\n (  . .  )\n  (_____) ",
			"  (^)_(^)\n (  . .  )\n  (_____) ",
		},
	},
	{
		ID:   "turtle",
		Name: "turtle",
		Frames: []string{
			"  ___\n /._.\\\n \\_^_/\n  | |",
			"  ___\n /.-.\\\n \\_^_/\n  | |",
			"  ___\n /._.\\\n \\_^_/\n  | |",
			"  ___\n /.^_\\\n \\_^_/\n /   \\",
		},
	},
	{
		ID:   "mouse",
		Name: "mouse",
		Frames: []string{
			"(\\_/)\n(o.o)\n > < ~~",
			"(\\_/)\n(-.-)\n > < ~~",
			"(\\_/)\n(o.o)\n > <  ~",
			"(\\_/)\n(^.^)\n > <~~~",
		},
	},
	{
		ID:   "snail",
		Name: "snail",
		Frames: []string{
			"  .--.\n@/ o o\\\n \\_ v _/\n  '---'",
			"  .--.\n@/ - -\\\n \\_ v _/\n  '---'",
			" .--. \n@/ o o\\\n\\_ v _/\n '---'",
			"  .--.\n@/ ^ ^\\\n \\_ v _/\n  '---'",
		},
	},
	{
		ID:   "duck",
		Name: "duck",
		Frames: []string{
			"  __\n<(o )___\n (  ._> )\n  `--'  ",
			"  __\n<(- )___\n (  ._> )\n  `--'  ",
			"  __\n<(o )___\n (  ._> )\n  `--' .",
			"  __\n<(^ )___\n (  ._> )\n  `--'  ",
		},
	},
}

// petsWindow is a fun right-pane surface: pick an ASCII pet and watch it idle.
type petsWindow struct {
	selected int // index into petCatalog
	frame    int
	width    int
	height   int
}

func newPetsWindow() petsWindow {
	return petsWindow{}
}

func (w petsWindow) id() string { return petsWindowID }

func (w petsWindow) title() string { return "pets" }

func (w petsWindow) init() tea.Cmd { return nil }

func (w petsWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case petsTickMsg:
		if p, ok := w.pet(); ok && len(p.Frames) > 0 {
			w.frame = (w.frame + 1) % len(p.Frames)
		}
		return w, nil
	case tea.KeyPressMsg:
		return w.handleKey(msg), nil
	}
	return w, nil
}

func (w petsWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w petsWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	p, ok := w.pet()
	if !ok {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no pets"),
		)
	}

	lines := make([]string, 0, 16)
	// Roster strip: highlight the selected pet name.
	roster := make([]string, 0, len(petCatalog))
	for i, spec := range petCatalog {
		label := sanitizeDisplayData(spec.Name)
		if i == w.selected {
			roster = append(roster, st.Accent.Render(label))
		} else {
			roster = append(roster, st.Muted.Render(label))
		}
	}
	sep := themedSpace(th.Spacing.SM)
	if sep == "" {
		sep = " "
	}
	lines = append(lines, wrapWindowText(strings.Join(roster, sep), w.width))
	lines = append(lines, "")

	art := p.Frames[w.frame%len(p.Frames)]
	for _, row := range strings.Split(art, "\n") {
		lines = append(lines, petsCenterLine(th, st.Text.Render(row), w.width))
	}

	// Muted hint when there is room.
	hintBudget := w.height - len(lines)
	if hintBudget > 1 {
		lines = append(lines, "")
		sep := th.Icons.DetailSeparator
		if strings.TrimSpace(sep) == "" {
			sep = "-"
		}
		hint := st.Muted.Render("j/k cycle " + sep + " /pets <name>")
		lines = append(lines, wrapWindowText(hint, w.width))
	}

	if w.height > 0 && len(lines) > w.height {
		lines = lines[:w.height]
	}
	return strings.Join(lines, "\n")
}

func (w petsWindow) handleKey(msg tea.KeyPressMsg) petsWindow {
	n := len(petCatalog)
	if n == 0 {
		return w
	}
	switch msg.String() {
	case "up", "k", "left", "h":
		w.selected = (w.selected - 1 + n) % n
		w.frame = 0
	case "down", "j", "right", "l":
		w.selected = (w.selected + 1) % n
		w.frame = 0
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < n {
			w.selected = idx
			w.frame = 0
		}
	}
	return w
}

func (w petsWindow) pet() (petSpec, bool) {
	if len(petCatalog) == 0 {
		return petSpec{}, false
	}
	if w.selected < 0 || w.selected >= len(petCatalog) {
		return petCatalog[0], true
	}
	return petCatalog[w.selected], true
}

// selectPet sets the active pet by id or display name (case-insensitive).
// ok is false when no catalog entry matches.
func (w petsWindow) selectPet(name string) (petsWindow, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return w, false
	}
	for i, p := range petCatalog {
		if strings.EqualFold(p.ID, name) || strings.EqualFold(p.Name, name) {
			w.selected = i
			w.frame = 0
			return w, true
		}
	}
	return w, false
}

// petsCenterLine centers a (possibly styled) row within width using spaces.
func petsCenterLine(th theme.Theme, row string, width int) string {
	if width <= 0 {
		return ""
	}
	rw := lipgloss.Width(row)
	if rw >= width {
		return wrapWindowText(truncateStyled(th, row, width), width)
	}
	pad := (width - rw) / 2
	if pad <= 0 {
		return wrapWindowText(row, width)
	}
	return wrapWindowText(strings.Repeat(" ", pad)+row, width)
}

// petsWindowActive reports whether the pets pane is in the active right-pane
// group (visible when the right column is shown).
func petsWindowActive(r windowRegistry) bool {
	for _, wi := range r.activeGroup().members {
		if wi < 0 || wi >= len(r.windows) {
			continue
		}
		if r.windows[wi].id() == petsWindowID {
			return true
		}
	}
	return false
}

// petsAnimCmd arms the next animation tick when the pets pane is active.
func petsAnimCmd(r windowRegistry) tea.Cmd {
	if !petsWindowActive(r) {
		return nil
	}
	return tea.Tick(petsAnimInterval, func(time.Time) tea.Msg {
		return petsTickMsg{}
	})
}

// applyPetsTick advances the pets window frame. Caller re-arms via petsAnimCmd
// only while the pane stays active.
func applyPetsTick(r windowRegistry, msg petsTickMsg) (windowRegistry, tea.Cmd) {
	for i, w := range r.windows {
		pw, ok := w.(petsWindow)
		if !ok {
			continue
		}
		next, cmd := pw.update(msg)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r, cmd
	}
	return r, nil
}

// selectPetsWindowPet sets the catalog selection on the pets window slot.
func selectPetsWindowPet(r windowRegistry, name string) (windowRegistry, bool) {
	for i, w := range r.windows {
		pw, ok := w.(petsWindow)
		if !ok {
			continue
		}
		next, found := pw.selectPet(name)
		if !found {
			return r, false
		}
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r, true
	}
	return r, false
}

// petCatalogNames returns the selectable pet ids for notices/help.
func petCatalogNames() string {
	names := make([]string, len(petCatalog))
	for i, p := range petCatalog {
		names[i] = p.ID
	}
	return strings.Join(names, ", ")
}
