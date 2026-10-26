package engine

import (
	"strings"
	"testing"
)

type staticMemory struct{ text string }

func (s staticMemory) AutoLoad() (string, int, error) { return s.text, 0, nil }

func TestMemoryModeSkipsTemporaryLayer(t *testing.T) {
	eng := New(Options{SessionID: "s", Memory: staticMemory{text: "REMEMBER THIS"}})
	if !strings.Contains(eng.system(), "REMEMBER THIS") {
		t.Fatal("normal mode should load memory")
	}
	if err := eng.SetMemoryMode("temporary"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(eng.system(), "REMEMBER THIS") {
		t.Fatal("temporary mode leaked memory into the prompt")
	}
	if err := eng.SetMemoryMode("incognito"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(eng.system(), "REMEMBER THIS") {
		t.Fatal("incognito should still read memory")
	}
	if err := eng.SetMemoryMode("nope"); err == nil {
		t.Fatal("expected unknown mode to fail")
	}
	if eng.MemoryMode() != "incognito" {
		t.Fatalf("mode changed on rejected set: %s", eng.MemoryMode())
	}
}

func TestInitialMemoryMode(t *testing.T) {
	eng := New(Options{SessionID: "s", Memory: staticMemory{text: "SECRET FACT"}, InitialMemoryMode: "temporary"})
	if eng.MemoryMode() != "temporary" {
		t.Fatalf("mode = %s", eng.MemoryMode())
	}
	if strings.Contains(eng.system(), "SECRET FACT") {
		t.Fatal("initial temporary mode loaded memory")
	}
}
