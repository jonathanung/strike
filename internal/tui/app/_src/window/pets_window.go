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

// petSpec is one selectable ASCII pet with status-specific animation frames.
// Each frame is a multi-line pure-ASCII drawing (no emoji / wide glyphs).
// Ready is required; Working/Attention/Error fall back to Ready when empty.
type petSpec struct {
	ID        string
	Name      string
	Ready     []string
	Working   []string
	Attention []string
	Error     []string
}

// petCatalog is the built-in pet roster. Keep drawings narrow so they fit the
// default 32-col right pane without horizontal overflow.
var petCatalog = []petSpec{
	{
		ID:   "cat",
		Name: "cat",
		Ready: []string{
			" /\\_/\\\n( o.o )\n > ^ <",
			" /\\_/\\\n( -.- )\n > ^ <",
			" /\\_/\\\n( o.o )\n > ^ <",
			" /\\_/\\\n( ^.^ )\n > ^ <",
		},
		Working: []string{
			" /\\_/\\\n( *.o )\n > ^ <",
			" /\\_/\\\n( o.* )\n > ^ < ~",
			" /\\_/\\\n( *.* )\n > ^ <",
			" /\\_/\\\n( o.o )\n > ^ <  z",
		},
		Attention: []string{
			" /\\_/\\\n( O.O )\n > ! <",
			" /\\_/\\\n( O.O )\n > ! < !",
			" /\\_/\\\n( O,O )\n > ! <",
			" /\\_/\\\n( O.O )\n > ! <",
		},
		Error: []string{
			" /\\_/\\\n( x.x )\n > _ <",
			" /\\_/\\\n( x_x )\n > _ <",
			" /\\_/\\\n( x.x )\n > _ <",
			" /\\_/\\\n( >.< )\n > _ <",
		},
	},
	{
		ID:   "dog",
		Name: "dog",
		Ready: []string{
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/",
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/  *",
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/",
			"  __\no'') }___//\n `_/      )\n (_(_/-(_/",
		},
		Working: []string{
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/ ~",
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/  *",
			"  __\no'')}____//\n `_/      )\n (_(_/-(_/~~",
			"  __\no'') }___//\n `_/      )\n (_(_/-(_/ .",
		},
		Attention: []string{
			"  __\no'')}____//\n `_/  !   )\n (_(_/-(_/",
			"  __\no'')}____//\n `_/ !!   )\n (_(_/-(_/",
			"  __\no'')}____//\n `_/  !   )\n (_(_/-(_/ !",
			"  __\no'') }___//\n `_/  !   )\n (_(_/-(_/",
		},
		Error: []string{
			"  __\no'')}____//\n `_/  x   )\n (_(_/-(_/",
			"  __\no'')}____//\n `_/ xxx  )\n (_(_/-(_/",
			"  __\no'')}____//\n `_/  x   )\n (_(_/-(_/",
			"  __\no'') }___//\n `_/  x   )\n (_(_/-(_/",
		},
	},
	{
		ID:   "panda",
		Name: "panda",
		Ready: []string{
			" (\\_/)\n (o.o)\n /| |\\\n  ^ ^",
			" (\\_/)\n (-.-)\n /| |\\\n  ^ ^",
			" (\\_/)\n (o.o)\n /| |\\\n  ^ ^",
			" (\\_/)\n (^.^)\n /| |\\\n  ^ ^",
		},
		Working: []string{
			" (\\_/)\n (*.*)\n /| |\\\n  ^ ^",
			"  (\\_/)\n  (-.-)\n  /| |\\\n   ^ ^ .",
			" (\\_/)\n (*.*)\n /| |\\\n  ^ ^",
			" (\\_/)\n (^.^)\n /| |\\\n  ^ ^ .",
		},
		Attention: []string{
			" (\\_/)\n (O.O)\n /| |\\\n  ^ ^",
			" (\\_/)\n (O.O)\n /| |\\\n  ^ ^",
			" (\\_/)\n (O.O)\n /| |\\\n  ^ ^",
			" (\\_/)!\n (^.^)\n /| |\\\n  ^ ^",
		},
		Error: []string{
			" (\\_/)\n (x.x)\n /| |\\\n  ^ ^",
			" (\\_/)\n (-.-)\n /| |\\\n  ^ ^ x",
			" (\\_/)\n (x.x)\n /| |\\\n  ^ ^",
			" (\\_/)\n (x.x)\n /| |\\\n  ^ ^",
		},
	},
	{
		ID:   "fish",
		Name: "fish",
		Ready: []string{
			"><(((('>",
			" ><(((('>",
			"  ><(((('>",
			" ><(((('>",
		},
		Working: []string{
			"~><(((('>",
			"  ~><(((('>",
			"  ~><(((('>",
			" ~><(((('>",
		},
		Attention: []string{
			"><(((('>?",
			" ><(((('>?",
			"  ><(((('>?",
			" ><(((('>?",
		},
		Error: []string{
			"><(((('>X",
			" ><(((('>X",
			"  ><(((('>X",
			" ><(((('>X",
		},
	},
	{
		ID:   "owl",
		Name: "owl",
		Ready: []string{
			" ,___,\n( o,o )\n/)   (\\\n \" \" \"",
			" ,___,\n( -,o )\n/)   (\\\n \" \" \"",
			" ,___,\n( o,o )\n/)   (\\\n \" \" \"",
			" ,___,\n( o,- )\n/)   (\\\n \" \" \"",
		},
		Working: []string{
			" ,___,\n( *,* )\n/)   (\\\n \" \" \"",
			"  ,___,\n ( -,* )\n /)   (\\\n  \" \" \"",
			" ,___,\n( *,* )\n/)   (\\\n \" \" \"",
			" ,___,\n( o,- )\n/)   (\\\n \" \" \" .",
		},
		Attention: []string{
			" ,___,\n( O,O )\n/)   (\\\n \" \" \"",
			" ,___,\n( -,O )\n/)   (\\\n \" \" \"",
			" ,___,\n( O,O )\n/)   (\\\n \" \" \"",
			" ,___,!\n( o,- )\n/)   (\\\n \" \" \"",
		},
		Error: []string{
			" ,___,\n( x,x )\n/)   (\\\n \" \" \"",
			" ,___,\n( -,x )\n/)   (\\\n \" \" \"",
			" ,___,\n( x,x )\n/)   (\\\n \" \" \"",
			" ,___,\n( o,- )\n/)   (\\\n \" \" \" x",
		},
	},
	{
		ID:   "rabbit",
		Name: "rabbit",
		Ready: []string{
			" (\\_/)\n (o.o)\n (\"|\")",
			" (\\_/)\n (-.-)\n (\"|\")",
			" (\\_/)\n (o.o)\n (\"|\")",
			" (\\_/)\n (^.^)\n (\"|\")",
		},
		Working: []string{
			" (\\_/)\n (*.*)\n (\"|\")",
			"  (\\_/)\n  (-.-)\n  (\"|\") .",
			" (\\_/)\n (*.*)\n (\"|\")",
			" (\\_/)\n (^.^)\n (\"|\") .",
		},
		Attention: []string{
			" (\\_/)\n (O.O)\n (\"|\")",
			" (\\_/)\n (O.O)\n (\"|\")",
			" (\\_/)\n (O.O)\n (\"|\")",
			" (\\_/)!\n (^.^)\n (\"|\")",
		},
		Error: []string{
			" (\\_/)\n (x.x)\n (\"|\")",
			" (\\_/)\n (-.-)\n (\"|\") x",
			" (\\_/)\n (x.x)\n (\"|\")",
			" (\\_/)\n (x.x)\n (\"|\")",
		},
	},
	{
		ID:   "fox",
		Name: "fox",
		Ready: []string{
			"  /\\   /\\\n (  .V.  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  -V-  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  .V.  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  ^V^  )\n  \\  ^  /\n   |||||",
		},
		Working: []string{
			"  /\\   /\\\n (  *V*  )\n  \\  ^  /\n   |||||",
			"   /\\   /\\\n  (  -V-  )\n   \\  ^  /\n    ||||| .",
			"  /\\   /\\\n (  *V*  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  ^V^  )\n  \\  ^  /\n   ||||| .",
		},
		Attention: []string{
			"  /\\   /\\\n (  !V!  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\!\n (  -V-  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  !V!  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\!\n (  ^V^  )\n  \\  ^  /\n   |||||",
		},
		Error: []string{
			"  /\\   /\\\n (  xVx  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  -V-  )\n  \\  ^  /\n   ||||| x",
			"  /\\   /\\\n (  xVx  )\n  \\  ^  /\n   |||||",
			"  /\\   /\\\n (  ^V^  )\n  \\  ^  /\n   ||||| x",
		},
	},
	{
		ID:   "bear",
		Name: "bear",
		Ready: []string{
			"  (\"\"\")\n ( o o )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( - - )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( o o )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( ^ ^ )\n  \\ ^ /\n  (' ')",
		},
		Working: []string{
			"  (\"\"\")\n ( * * )\n  \\ ^ /\n  (' ')",
			"   (\"\"\")\n  ( - - )\n   \\ ^ /\n   (' ') .",
			"  (\"\"\")\n ( * * )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( ^ ^ )\n  \\ ^ /\n  (' ') .",
		},
		Attention: []string{
			"  (\"\"\")\n ( O O )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")!\n ( - - )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( O O )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")!\n ( ^ ^ )\n  \\ ^ /\n  (' ')",
		},
		Error: []string{
			"  (\"\"\")\n ( x x )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( - - )\n  \\ ^ /\n  (' ') x",
			"  (\"\"\")\n ( x x )\n  \\ ^ /\n  (' ')",
			"  (\"\"\")\n ( ^ ^ )\n  \\ ^ /\n  (' ') x",
		},
	},
	{
		ID:   "bird",
		Name: "bird",
		Ready: []string{
			"  .--.\n ( o> )\n /)  )\n  \"\"",
			"  .--.\n ( -> )\n /)  )\n  \"\"",
			"  .--.\n ( o> )\n /)  )\n  \"\"",
			" .--. \n( o> )\n/)  ) \n \"\"  ",
		},
		Working: []string{
			"  .--.\n ( *> )\n /)  )\n  \"\"",
			"   .--.\n  ( -> )\n  /)  )\n   \"\" .",
			"  .--.\n ( *> )\n /)  )\n  \"\"",
			" .--. \n( *> )\n/)  ) \n \"\"  ",
		},
		Attention: []string{
			"  .--.\n ( O> )\n /)  )\n  \"\"",
			"  .--.!\n ( -> )\n /)  )\n  \"\"",
			"  .--.\n ( O> )\n /)  )\n  \"\"",
			" .--. \n( O> )\n/)  ) \n \"\"  ",
		},
		Error: []string{
			"  .--.\n ( x> )\n /)  )\n  \"\"",
			"  .--.\n ( -> )\n /)  )\n  \"\" x",
			"  .--.\n ( x> )\n /)  )\n  \"\"",
			" .--. \n( x> )\n/)  ) \n \"\"  ",
		},
	},
	{
		ID:   "frog",
		Name: "frog",
		Ready: []string{
			"  (.)_(.)\n (  . .  )\n  (_____) ",
			"  (-)_(-)\n (  . .  )\n  (_____) ",
			"  (.)_(.)\n (  . .  )\n  (_____) ",
			"  (^)_(^)\n (  . .  )\n  (_____) ",
		},
		Working: []string{
			"  (.)_(.)\n (  . .  )\n  (_____)  .",
			"   (-)_(-)\n  (  . .  )\n   (_____)  .",
			"  (.)_(.)\n (  . .  )\n  (_____)  .",
			"  (^)_(^)\n (  . .  )\n  (_____)  .",
		},
		Attention: []string{
			"  (.)_(.)!\n (  . .  )\n  (_____) ",
			"  (-)_(-)!\n (  . .  )\n  (_____) ",
			"  (.)_(.)!\n (  . .  )\n  (_____) ",
			"  (^)_(^)!\n (  . .  )\n  (_____) ",
		},
		Error: []string{
			"  (.)_(.)\n (  . .  )\n  (_____)  x",
			"  (-)_(-)\n (  . .  )\n  (_____)  x",
			"  (.)_(.)\n (  . .  )\n  (_____)  x",
			"  (^)_(^)\n (  . .  )\n  (_____)  x",
		},
	},
	{
		ID:   "turtle",
		Name: "turtle",
		Ready: []string{
			"  ___\n /._.\\\n \\_^_/\n  | |",
			"  ___\n /.-.\\\n \\_^_/\n  | |",
			"  ___\n /._.\\\n \\_^_/\n  | |",
			"  ___\n /.^_\\\n \\_^_/\n /   \\",
		},
		Working: []string{
			"  ___\n /._.\\\n \\_^_/\n  | | .",
			"   ___\n  /.-.\\\n  \\_^_/\n   | | .",
			"  ___\n /._.\\\n \\_^_/\n  | | .",
			"  ___\n /.^_\\\n \\_^_/\n /   \\ .",
		},
		Attention: []string{
			"  ___!\n /._.\\\n \\_^_/\n  | |",
			"  ___!\n /.-.\\\n \\_^_/\n  | |",
			"  ___!\n /._.\\\n \\_^_/\n  | |",
			"  ___!\n /.^_\\\n \\_^_/\n /   \\",
		},
		Error: []string{
			"  ___\n /._.\\\n \\_^_/\n  | | x",
			"  ___\n /.-.\\\n \\_^_/\n  | | x",
			"  ___\n /._.\\\n \\_^_/\n  | | x",
			"  ___\n /.^_\\\n \\_^_/\n /   \\ x",
		},
	},
	{
		ID:   "mouse",
		Name: "mouse",
		Ready: []string{
			"(\\_/)\n(o.o)\n > < ~~",
			"(\\_/)\n(-.-)\n > < ~~",
			"(\\_/)\n(o.o)\n > <  ~",
			"(\\_/)\n(^.^)\n > <~~~",
		},
		Working: []string{
			"(\\_/)\n(*.*)\n > < ~~",
			" (\\_/)\n (-.-)\n  > < ~~ .",
			"(\\_/)\n(*.*)\n > <  ~",
			"(\\_/)\n(^.^)\n > <~~~ .",
		},
		Attention: []string{
			"(\\_/)\n(O.O)\n > < ~~",
			"(\\_/)\n(O.O)\n > < ~~",
			"(\\_/)\n(O.O)\n > <  ~",
			"(\\_/)!\n(^.^)\n > <~~~",
		},
		Error: []string{
			"(\\_/)\n(x.x)\n > < ~~",
			"(\\_/)\n(-.-)\n > < ~~ x",
			"(\\_/)\n(x.x)\n > <  ~",
			"(\\_/)\n(x.x)\n > <~~~",
		},
	},
	{
		ID:   "snail",
		Name: "snail",
		Ready: []string{
			"  .--.\n@/ o o\\\n \\_ v _/\n  '---'",
			"  .--.\n@/ - -\\\n \\_ v _/\n  '---'",
			" .--. \n@/ o o\\\n\\_ v _/\n '---'",
			"  .--.\n@/ ^ ^\\\n \\_ v _/\n  '---'",
		},
		Working: []string{
			"  .--.\n@/ * *\\\n \\_ v _/\n  '---'",
			"   .--.\n @/ - -\\\n  \\_ v _/\n   '---' .",
			" .--. \n@/ * *\\\n\\_ v _/\n '---'",
			"  .--.\n@/ ^ ^\\\n \\_ v _/\n  '---' .",
		},
		Attention: []string{
			"  .--.\n@/ O O\\\n \\_ v _/\n  '---'",
			"  .--.!\n@/ - -\\\n \\_ v _/\n  '---'",
			" .--. \n@/ O O\\\n\\_ v _/\n '---'",
			"  .--.!\n@/ ^ ^\\\n \\_ v _/\n  '---'",
		},
		Error: []string{
			"  .--.\n@/ x x\\\n \\_ v _/\n  '---'",
			"  .--.\n@/ - -\\\n \\_ v _/\n  '---' x",
			" .--. \n@/ x x\\\n\\_ v _/\n '---'",
			"  .--.\n@/ ^ ^\\\n \\_ v _/\n  '---' x",
		},
	},
	{
		ID:   "duck",
		Name: "duck",
		Ready: []string{
			"  __\n<(o )___\n (  ._> )\n  `--'  ",
			"  __\n<(- )___\n (  ._> )\n  `--'  ",
			"  __\n<(o )___\n (  ._> )\n  `--' .",
			"  __\n<(^ )___\n (  ._> )\n  `--'  ",
		},
		Working: []string{
			"  __\n<(* )___\n (  ._> )\n  `--'  ",
			"   __\n <(- )___\n  (  ._> )\n   `--'   .",
			"  __\n<(* )___\n (  ._> )\n  `--' .",
			"  __\n<(^ )___\n (  ._> )\n  `--'   .",
		},
		Attention: []string{
			"  __\n<(O )___\n (  ._> )\n  `--'  ",
			"  __!\n<(- )___\n (  ._> )\n  `--'  ",
			"  __\n<(O )___\n (  ._> )\n  `--' .",
			"  __!\n<(^ )___\n (  ._> )\n  `--'  ",
		},
		Error: []string{
			"  __\n<(x )___\n (  ._> )\n  `--'  ",
			"  __\n<(- )___\n (  ._> )\n  `--'   x",
			"  __\n<(x )___\n (  ._> )\n  `--' .",
			"  __\n<(^ )___\n (  ._> )\n  `--'   x",
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

// framesFor returns the animation frames for a runtime agent state.
// Dead uses a single muted Ready frame; unknown/empty falls back to Ready.
func (p petSpec) framesFor(state theme.AgentState) []string {
	switch state {
	case theme.AgentStateWorking:
		if len(p.Working) > 0 {
			return p.Working
		}
	case theme.AgentStateAttention:
		if len(p.Attention) > 0 {
			return p.Attention
		}
	case theme.AgentStateError:
		if len(p.Error) > 0 {
			return p.Error
		}
	case theme.AgentStateDead:
		if len(p.Ready) > 0 {
			// Static final pose — no idle blink loop for dead sessions.
			return p.Ready[:1]
		}
	}
	if len(p.Ready) > 0 {
		return p.Ready
	}
	// Legacy safety: treat any leftover single slice as ready.
	return nil
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
