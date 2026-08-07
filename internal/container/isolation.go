package container

import (
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// IsolationEnvValue returns the STRIKE_ISOLATION value for a managed container
// created from cfg (container vs container+no-network).
func IsolationEnvValue(cfg Config) string {
	mode := strings.ToLower(strings.TrimSpace(cfg.Network.Mode))
	if mode == "none" || mode == "off" {
		return protocol.IsolationContainerNoNet
	}
	return protocol.IsolationContainer
}

// InsideContainerNoNetwork reports whether STRIKE_ISOLATION indicates an
// offline container (for status UIs). Prefer env over /.dockerenv.
func InsideContainerNoNetwork() bool {
	p, ok := protocol.ParseIsolationEnv(strings.TrimSpace(os.Getenv(protocol.IsolationEnvKey)))
	return ok && p == protocol.IsolationContainerNoNet
}
