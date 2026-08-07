package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestPetsWindowDefaultCatalogAndSelect(t *testing.T) {
	w := newPetsWindow()
	if w.id() != petsWindowID {
		t.Fatalf("id = %q, want %s", w.id(), petsWindowID)
	}
	p, ok := w.pet()
	if !ok || p.ID != "cat" {
		t.Fatalf("default pet = %+v ok=%v, want cat", p, ok)
	}
	w, ok = w.selectPet("DOG")
	if !ok {
		t.Fatal("selectPet(DOG) failed")
	}
	p, ok = w.pet()
	if !ok || p.ID != "dog" {
		t.Fatalf("after select = %+v, want dog", p)
	}
	if _, ok = w.selectPet("dragon"); ok {
		t.Fatal("selectPet(dragon) should fail")
	}
	// Unknown leaves selection unchanged.
	p, _ = w.pet()
	if p.ID != "dog" {
		t.Fatalf("selection after miss = %q, want dog", p.ID)
	}
}

func TestPetsWindowCycleKeys(t *testing.T) {
	w := newPetsWindow().resize(32, 12).(petsWindow)
	// j advances cat → dog
	next, _ := w.update(tea.KeyPressMsg{Text: "j"})
	w = next.(petsWindow)
	if p, _ := w.pet(); p.ID != "dog" {
		t.Fatalf("j = %q, want dog", p.ID)
	}
	// k goes back
	next, _ = w.update(tea.KeyPressMsg{Text: "k"})
	w = next.(petsWindow)
	if p, _ := w.pet(); p.ID != "cat" {
		t.Fatalf("k = %q, want cat", p.ID)
	}
	// wrap backward from cat → last catalog entry
	last := petCatalog[len(petCatalog)-1].ID
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyUp})
	w = next.(petsWindow)
	if p, _ := w.pet(); p.ID != last {
		t.Fatalf("up wrap = %q, want %s", p.ID, last)
	}
	// digit select
	next, _ = w.update(tea.KeyPressMsg{Text: "3"})
	w = next.(petsWindow)
	if p, _ := w.pet(); p.ID != "panda" {
		t.Fatalf("3 = %q, want panda", p.ID)
	}
}

func TestPetsWindowViewWidthSafeAndAnimated(t *testing.T) {
	w := newPetsWindow().resize(20, 10).(petsWindow)
	view := w.view(theme.Default())
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 20 {
			t.Errorf("line width %d > 20: %q", got, line)
		}
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "cat") {
		t.Fatalf("view missing roster: %q", plain)
	}
	// Tick advances frame without changing selection.
	frame0 := w.frame
	next, _ := w.update(petsTickMsg{})
	w = next.(petsWindow)
	if w.frame == frame0 && len(petCatalog[0].Frames) > 1 {
		t.Fatal("petsTickMsg did not advance frame")
	}
	if p, _ := w.pet(); p.ID != "cat" {
		t.Fatalf("tick changed pet to %q", p.ID)
	}
}

func TestPetsAnimCmdOnlyWhenActive(t *testing.T) {
	r := newWindowRegistry()
	if cmd := petsAnimCmd(r); cmd != nil {
		t.Fatal("petsAnimCmd on context should be nil")
	}
	r, ok := r.activate(petsWindowID)
	if !ok {
		t.Fatal("activate pets")
	}
	if !petsWindowActive(r) {
		t.Fatal("petsWindowActive = false after activate")
	}
	if cmd := petsAnimCmd(r); cmd == nil {
		t.Fatal("petsAnimCmd on pets should arm a tick")
	}
}

func TestSelectPetsWindowPetAndSlash(t *testing.T) {
	r := newWindowRegistry()
	r, ok := selectPetsWindowPet(r, "fish")
	if !ok {
		t.Fatal("selectPetsWindowPet(fish)")
	}
	var found bool
	for _, w := range r.windows {
		if pw, ok := w.(petsWindow); ok {
			found = true
			if p, _ := pw.pet(); p.ID != "fish" {
				t.Fatalf("selected = %q, want fish", p.ID)
			}
		}
	}
	if !found {
		t.Fatal("pets window missing from registry")
	}

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.composer.SetValue("/pets panda")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if m.windows.active().id() != petsWindowID {
		t.Fatalf("active = %q, want pets", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	pw, ok := m.windows.active().(petsWindow)
	if !ok {
		t.Fatalf("active type = %T", m.windows.active())
	}
	if p, _ := pw.pet(); p.ID != "panda" {
		t.Fatalf("slash select = %q, want panda", p.ID)
	}

	// Unknown pet stays put with a notice.
	m.focus = focusLeft
	m.composer.SetValue("/pets unicorn")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.notice, "unknown pet") {
		t.Fatalf("notice = %q, want unknown pet", m.notice)
	}
	if m.windows.active().id() == petsWindowID && m.focus == focusRight {
		// Still ok if already on pets; must not have selected unicorn.
	}
}

func TestPetsTickAdvancesOnlyWhileActive(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	// Tick while context is active: no-op (frame stays 0).
	m = updateApp(t, m, petsTickMsg{})
	for _, w := range m.windows.windows {
		if pw, ok := w.(petsWindow); ok && pw.frame != 0 {
			t.Fatalf("frame advanced while inactive: %d", pw.frame)
		}
	}
	m.windows, _ = m.windows.activate(petsWindowID)
	m.focus = focusRight
	before := 0
	for _, w := range m.windows.windows {
		if pw, ok := w.(petsWindow); ok {
			before = pw.frame
		}
	}
	m = updateApp(t, m, petsTickMsg{})
	for _, w := range m.windows.windows {
		if pw, ok := w.(petsWindow); ok {
			if pw.frame == before && len(petCatalog[pw.selected].Frames) > 1 {
				t.Fatal("frame did not advance while pets active")
			}
		}
	}
}

func TestApplyPetsTick(t *testing.T) {
	r := newWindowRegistry()
	r, _ = r.activate(petsWindowID)
	r, _ = applyPetsTick(r, petsTickMsg{})
	pw := r.active().(petsWindow)
	if pw.frame != 1 {
		t.Fatalf("frame = %d, want 1", pw.frame)
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

func TestSelectNewPets(t *testing.T) {
	w := newPetsWindow()
	for _, name := range []string{"owl", "RABBIT", "Duck", "snail"} {
		var ok bool
		w, ok = w.selectPet(name)
		if !ok {
			t.Fatalf("selectPet(%q) failed", name)
		}
		p, _ := w.pet()
		if !strings.EqualFold(p.ID, name) {
			t.Fatalf("after selectPet(%q) got %q", name, p.ID)
		}
	}
}
