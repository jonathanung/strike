package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func allowAll(workDir string) *tool.Context {
	return &tool.Context{
		WorkDir:     workDir,
		SandboxMode: "off",
		Ask:         func(context.Context, tool.AskRequest) error { return nil },
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func schemaNameSet(schemas []provider.ToolSchema) map[string]bool {
	out := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		out[s.Name] = true
	}
	return out
}
