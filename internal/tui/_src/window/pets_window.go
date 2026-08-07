package tui

import (
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// petsWindowID remains reserved so plugins cannot claim the former pane id.
const petsWindowID = "pets"

// petsAnimInterval is the frame period for pet animation (~2 fps).
const petsAnimInterval = 500 * time.Millisecond

// petsTickMsg advances agent-pet animation frames while the agents pane is active.
type petsTickMsg struct{}

// petSpec is one selectable ASCII pet with one or more animation frames.
// Each frame is a multi-line pure-ASCII drawing (no emoji / wide glyphs).
type petSpec struct {
	ID     string
	Name   string
	Frames []string
}

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

// petRandN picks a random index in [0, n). Tests may override for determinism.
var petRandN = func(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.IntN(n)
}

// petByID returns the catalog index for id or name (case-insensitive).
func petByID(name string) (int, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return 0, false
	}
	for i, p := range petCatalog {
		if strings.EqualFold(p.ID, name) || strings.EqualFold(p.Name, name) {
			return i, true
		}
	}
	return 0, false
}

// petAt returns the catalog entry at idx, falling back to the first entry.
func petAt(idx int) (petSpec, bool) {
	if len(petCatalog) == 0 {
		return petSpec{}, false
	}
	if idx < 0 || idx >= len(petCatalog) {
		return petCatalog[0], true
	}
	return petCatalog[idx], true
}

// petCatalogNames returns the selectable pet ids for notices/help.
func petCatalogNames() string {
	names := make([]string, len(petCatalog))
	for i, p := range petCatalog {
		names[i] = p.ID
	}
	return strings.Join(names, ", ")
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

// agentsWindowActive reports whether the agents pane is in the active right-pane
// group (pets animate while agents are visible).
func agentsWindowActive(r windowRegistry) bool {
	for _, wi := range r.activeGroup().members {
		if wi < 0 || wi >= len(r.windows) {
			continue
		}
		if r.windows[wi].id() == agentsWindowID {
			return true
		}
	}
	return false
}

// petsAnimCmd arms the next animation tick when the agents pane is active.
func petsAnimCmd(r windowRegistry) tea.Cmd {
	if !agentsWindowActive(r) {
		return nil
	}
	return tea.Tick(petsAnimInterval, func(time.Time) tea.Msg {
		return petsTickMsg{}
	})
}

// applyPetsTick advances the agents-window pet animation frame.
func applyPetsTick(r windowRegistry, msg petsTickMsg) (windowRegistry, tea.Cmd) {
	for i, w := range r.windows {
		aw, ok := w.(agentsWindow)
		if !ok {
			continue
		}
		next, cmd := aw.update(msg)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r, cmd
	}
	return r, nil
}

// selectAgentPet sets the pet for the agents-pane focus session by catalog name.
// When sessionID is empty, uses the window's current focus pet target.
func selectAgentPet(r windowRegistry, name, sessionID string) (windowRegistry, bool) {
	idx, found := petByID(name)
	if !found {
		return r, false
	}
	for i, w := range r.windows {
		aw, ok := w.(agentsWindow)
		if !ok {
			continue
		}
		id := strings.TrimSpace(sessionID)
		if id == "" {
			id = aw.focusPetSessionID()
		}
		if id == "" {
			// No agent yet — stash as pending default on empty key once roots arrive
			// by assigning when ensure runs; still record against activeID if set.
			id = strings.TrimSpace(aw.activeID)
		}
		if id == "" {
			return r, false
		}
		if aw.pets == nil {
			aw.pets = map[string]int{}
		}
		aw.pets[id] = idx
		aw.petFrame = 0
		windows := append([]window(nil), r.windows...)
		windows[i] = aw
		r.windows = windows
		return r, true
	}
	return r, false
}
