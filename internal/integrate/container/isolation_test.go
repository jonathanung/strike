package container

import (
	"testing"

	"github.com/jonathanung/strike/pkg/protocol"
)

func TestIsolationEnvValue(t *testing.T) {
	cfg := DefaultConfig()
	if got := IsolationEnvValue(cfg); got != protocol.IsolationContainer {
		t.Fatalf("%q", got)
	}
	cfg.Network.Mode = "none"
	if got := IsolationEnvValue(cfg); got != protocol.IsolationContainerNoNet {
		t.Fatalf("%q", got)
	}
}
