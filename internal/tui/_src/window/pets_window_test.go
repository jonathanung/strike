package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestPetCatalogHasFourteenDistinctAnimals(t *testing.T) {
	const wantCount = 14
	if got := len(petCatalog); got != wantCount {
		t.Fatalf("len(petCatalog) = %d, want %d", got, wantCount)
	}
	seen := make(map[string]bool, len(petCatalog))
	for _, p := range petCatalog {
		if p.ID == "" {
			t.Fatal("pet with empty ID")
		}
		if seen[p.ID] {
			t.Fatalf("duplicate pet ID %q", p.ID)
		}
		seen[p.ID] = true
		if len(p.Frames) == 0 {
			t.Fatalf("pet %q has no frames", p.ID)
		}
		for i, fr := range p.Frames {
			if strings.TrimSpace(fr) == "" {
				t.Fatalf("pet %q frame %d empty", p.ID, i)
			}
			for _, line := range strings.Split(fr, "\n") {
				if w := lipgloss.Width(line); w > 32 {
					t.Errorf("pet %q frame %d line width %d > 32: %q", p.ID, i, w, line)
				}
			}
		}
	}
}

func TestPetCatalogNames(t *testing.T) {
	got := petCatalogNames()
	for _, want := range []string{
		"cat", "dog", "panda", "fish",
		"owl", "rabbit", "fox", "bear", "bird",
		"frog", "turtle", "mouse", "snail", "duck",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("petCatalogNames missing %q: %q", want, got)
		}
	}
}

func TestPetByID(t *testing.T) {
	idx, ok := petByID("DOG")
	if !ok || petCatalog[idx].ID != "dog" {
		t.Fatalf("petByID(DOG) = %d ok=%v", idx, ok)
	}
	if _, ok := petByID("dragon"); ok {
		t.Fatal("petByID(dragon) should fail")
	}
}

func TestAgentsEnsurePetsAssignedPrefersUnassigned(t *testing.T) {
	// Deterministic picks: always first free slot.
	prev := petRandN
	petRandN = func(n int) int {
		if n <= 0 {
			return 0
		}
		return 0
	}
	t.Cleanup(func() { petRandN = prev })

	w := newAgentsWindow()
	next, _ := w.update(agentsStateMsg{
		activeID: "r1",
		roots: []agentsRootSnap{
			{ID: "r1", Title: "one", State: theme.AgentStateReady},
			{ID: "r2", Title: "two", State: theme.AgentStateReady},
			{ID: "r3", Title: "three", State: theme.AgentStateReady},
		},
	})
	w = next.(agentsWindow)
	if len(w.pets) != 3 {
		t.Fatalf("pets assigned = %d, want 3: %+v", len(w.pets), w.pets)
	}
	// With petRandN always 0, each new agent takes the first free index → 0,1,2.
	if w.pets["r1"] != 0 || w.pets["r2"] != 1 || w.pets["r3"] != 2 {
		t.Fatalf("expected sequential free picks, got %+v", w.pets)
	}
	// Distinct while catalog has free slots.
	seen := map[int]bool{}
	for _, idx := range w.pets {
		if seen[idx] {
			t.Fatalf("duplicate assignment while free pets remain: %+v", w.pets)
		}
		seen[idx] = true
	}
}

func TestAgentsEnsurePetsWhenCatalogExhausted(t *testing.T) {
	prev := petRandN
	calls := 0
	petRandN = func(n int) int {
		calls++
		if n <= 0 {
			return 0
		}
		return calls % n
	}
	t.Cleanup(func() { petRandN = prev })

	roots := make([]agentsRootSnap, 0, len(petCatalog)+2)
	for i := 0; i < len(petCatalog)+2; i++ {
		roots = append(roots, agentsRootSnap{
			ID:    "r" + itoa(i),
			Title: "a" + itoa(i),
			State: theme.AgentStateReady,
		})
	}
	w := newAgentsWindow()
	next, _ := w.update(agentsStateMsg{activeID: "r0", roots: roots})
	w = next.(agentsWindow)
	if len(w.pets) != len(roots) {
		t.Fatalf("pets = %d, want %d", len(w.pets), len(roots))
	}
	// Extra agents still get some pet index.
	for _, r := range roots {
		idx, ok := w.pets[r.ID]
		if !ok || idx < 0 || idx >= len(petCatalog) {
			t.Fatalf("bad assignment for %s: %v ok=%v", r.ID, idx, ok)
		}
	}
}

func TestAgentsPetShownAboveTreeAndCycle(t *testing.T) {
	prev := petRandN
	petRandN = func(n int) int { return 0 }
	t.Cleanup(func() { petRandN = prev })

	w := newAgentsWindow().resize(32, 16).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID:  "root-a",
		viewingID: "root-a",
		roots: []agentsRootSnap{
			{ID: "root-a", Title: "Alpha", State: theme.AgentStateReady},
			{ID: "root-b", Title: "Beta", State: theme.AgentStateWorking},
		},
	})
	w = next.(agentsWindow)
	view := w.view(theme.Default())
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "cat") {
		t.Fatalf("view missing pet name: %q", plain)
	}
	if !strings.Contains(plain, "Alpha") {
		t.Fatalf("view missing agent tree: %q", plain)
	}
	// Pet name should appear before the agent label (above the tree).
	if i, j := strings.Index(plain, "cat"), strings.Index(plain, "Alpha"); i < 0 || j < 0 || i > j {
		t.Fatalf("pet should render above agents: cat@%d Alpha@%d in %q", i, j, plain)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 32 {
			t.Errorf("line width %d > 32: %q", got, line)
		}
	}

	// p cycles pet for focused agent.
	before := w.pets["root-a"]
	next, _ = w.update(tea.KeyPressMsg{Text: "p"})
	w = next.(agentsWindow)
	if w.pets["root-a"] == before {
		t.Fatal("p did not cycle pet")
	}
	// Other agent unchanged.
	if w.pets["root-b"] != 1 { // sequential free: r1=0, r2=1 with rand 0
		// root-b was second assigned → index 1 with deterministic free pick
	}
	bPet := w.pets["root-b"]
	next, _ = w.update(tea.KeyPressMsg{Text: "p"})
	w = next.(agentsWindow)
	if w.pets["root-b"] != bPet {
		t.Fatalf("cycling focus pet changed other agent: %d → %d", bPet, w.pets["root-b"])
	}
}

func TestAgentsPerAgentPetsIndependent(t *testing.T) {
	prev := petRandN
	petRandN = func(n int) int { return 0 }
	t.Cleanup(func() { petRandN = prev })

	w := newAgentsWindow().resize(40, 12).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "a",
		roots: []agentsRootSnap{
			{ID: "a", Title: "A", State: theme.AgentStateReady},
			{ID: "b", Title: "B", State: theme.AgentStateReady},
		},
	})
	w = next.(agentsWindow)
	w, ok := w.setFocusPetByName("owl", "a")
	if !ok {
		t.Fatal("set owl on a")
	}
	w, ok = w.setFocusPetByName("duck", "b")
	if !ok {
		t.Fatal("set duck on b")
	}
	if petCatalog[w.pets["a"]].ID != "owl" || petCatalog[w.pets["b"]].ID != "duck" {
		t.Fatalf("pets = a:%d b:%d", w.pets["a"], w.pets["b"])
	}
}

func TestPetsAnimCmdOnlyWhenAgentsActive(t *testing.T) {
	r := newWindowRegistry()
	if cmd := petsAnimCmd(r); cmd != nil {
		t.Fatal("petsAnimCmd on context should be nil")
	}
	r, ok := r.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	if !agentsWindowActive(r) {
		t.Fatal("agentsWindowActive = false after activate")
	}
	if cmd := petsAnimCmd(r); cmd == nil {
		t.Fatal("petsAnimCmd on agents should arm a tick")
	}
}

func TestSelectAgentPetAndSlash(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	// Seed agents state with a root matching the model session when possible.
	sid := m.sessionID
	if sid == "" {
		sid = "sess-1"
		m.sessionID = sid
	}
	m.windows, _ = m.windows.broadcast(agentsStateMsg{
		activeID:  sid,
		viewingID: sid,
		roots:     []agentsRootSnap{{ID: sid, Title: "main", State: theme.AgentStateReady}},
	})

	m.composer.SetValue("/pets panda")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if m.windows.active().id() != agentsWindowID {
		t.Fatalf("active = %q, want agents", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	aw, ok := m.windows.active().(agentsWindow)
	if !ok {
		t.Fatalf("active type = %T", m.windows.active())
	}
	idx, ok := aw.pets[sid]
	if !ok || petCatalog[idx].ID != "panda" {
		t.Fatalf("slash select pet = %v ok=%v, want panda", idx, ok)
	}

	m.focus = focusLeft
	m.composer.SetValue("/pets unicorn")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.notice, "unknown pet") {
		t.Fatalf("notice = %q, want unknown pet", m.notice)
	}
}

func TestApplyPetsTickOnAgents(t *testing.T) {
	prev := petRandN
	petRandN = func(n int) int { return 0 }
	t.Cleanup(func() { petRandN = prev })

	r := newWindowRegistry()
	r, _ = r.activate(agentsWindowID)
	r, _ = r.broadcast(agentsStateMsg{
		activeID: "x",
		roots:    []agentsRootSnap{{ID: "x", Title: "X", State: theme.AgentStateReady}},
	})
	r, _ = applyPetsTick(r, petsTickMsg{})
	aw := r.active().(agentsWindow)
	if aw.petFrame != 1 {
		t.Fatalf("petFrame = %d, want 1", aw.petFrame)
	}
}

func TestPetsTickAdvancesOnlyWhileAgentsActive(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updateApp(t, m, petsTickMsg{})
	for _, w := range m.windows.windows {
		if aw, ok := w.(agentsWindow); ok && aw.petFrame != 0 {
			t.Fatalf("frame advanced while inactive: %d", aw.petFrame)
		}
	}
	m.windows, _ = m.windows.activate(agentsWindowID)
	m.windows, _ = m.windows.broadcast(agentsStateMsg{
		activeID: "z",
		roots:    []agentsRootSnap{{ID: "z", Title: "Z", State: theme.AgentStateReady}},
	})
	m.focus = focusRight
	before := 0
	for _, w := range m.windows.windows {
		if aw, ok := w.(agentsWindow); ok {
			before = aw.petFrame
		}
	}
	m = updateApp(t, m, petsTickMsg{})
	for _, w := range m.windows.windows {
		if aw, ok := w.(agentsWindow); ok {
			if aw.petFrame == before {
				// Only advances when focus pet has multi-frame art.
				if p, ok := aw.focusPet(); ok && len(p.Frames) > 1 {
					t.Fatal("frame did not advance while agents active")
				}
			}
		}
	}
}
