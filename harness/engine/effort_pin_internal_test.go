package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestResolveTaskEffortPin(t *testing.T) {
	t.Run("empty inherits", func(t *testing.T) {
		pin, err := resolveTaskEffortPin("")
		if err != nil {
			t.Fatal(err)
		}
		if pin.lock || pin.level != protocol.EffortDefault {
			t.Fatalf("pin = %#v, want unlocked default", pin)
		}
	})
	t.Run("blank inherits", func(t *testing.T) {
		pin, err := resolveTaskEffortPin("  ")
		if err != nil {
			t.Fatal(err)
		}
		if pin.lock {
			t.Fatal("blank should not lock")
		}
	})
	for _, level := range protocol.Efforts() {
		t.Run(string(level), func(t *testing.T) {
			pin, err := resolveTaskEffortPin(string(level))
			if err != nil {
				t.Fatal(err)
			}
			if !pin.lock || pin.level != level {
				t.Fatalf("pin = %#v, want locked %q", pin, level)
			}
		})
	}
	t.Run("case insensitive", func(t *testing.T) {
		pin, err := resolveTaskEffortPin("XHigh")
		if err != nil {
			t.Fatal(err)
		}
		if pin.level != protocol.EffortXHigh {
			t.Fatalf("level = %q", pin.level)
		}
	})
	t.Run("unknown rejected", func(t *testing.T) {
		_, err := resolveTaskEffortPin("turbo")
		if err == nil || !strings.Contains(err.Error(), "unknown effort") {
			t.Fatalf("err = %v, want unknown effort", err)
		}
	})
}
