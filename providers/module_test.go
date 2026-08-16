package providers

import (
	"os"
	"strings"
	"testing"
)

func TestStandaloneModule(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "module github.com/jonathanung/strike-cli/providers\n") {
		t.Fatalf("go.mod missing module path:\n%s", src)
	}
	if strings.Contains(src, "/internal/") {
		t.Fatalf("providers must not require internal packages:\n%s", src)
	}
	if !strings.Contains(src, "github.com/jonathanung/strike-cli/provider") {
		t.Fatalf("providers must require the public provider interface module:\n%s", src)
	}
}
