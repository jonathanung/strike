package swebench

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// evalIsolationConfig is merged into every eval instance's project config
// unless the caller already set network.allow. Non-empty network.allow turns
// on bash/webfetch egress preflight so the agent cannot fetch GitHub PRs or
// other public URLs. 127.0.0.1 is a dummy entry (webfetch still SSRF-blocks
// loopback). eval-test / docker exec stay local.
var evalIsolationConfig = []byte(`{
  "network": {"allow": ["127.0.0.1"]},
  "permissions": [
    {"permission": "webfetch", "pattern": "*", "action": "deny"},
    {"permission": "websearch", "pattern": "*", "action": "deny"}
  ]
}
`)

// MergeEvalIsolation returns project config JSON with eval egress isolation
// applied. Existing network.allow from the caller wins (sweeps can override).
func MergeEvalIsolation(user []byte) ([]byte, error) {
	user = bytes.TrimSpace(user)
	if len(user) == 0 {
		return append([]byte(nil), evalIsolationConfig...), nil
	}
	var overlay map[string]any
	if err := json.Unmarshal(user, &overlay); err != nil {
		return nil, fmt.Errorf("swebench: project config json: %w", err)
	}
	if overlay == nil {
		overlay = map[string]any{}
	}
	if _, hasNet := overlay["network"]; !hasNet {
		var iso map[string]any
		if err := json.Unmarshal(evalIsolationConfig, &iso); err != nil {
			return nil, err
		}
		overlay["network"] = iso["network"]
		if _, hasPerm := overlay["permissions"]; !hasPerm {
			overlay["permissions"] = iso["permissions"]
		}
	}
	out, err := json.Marshal(overlay)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
