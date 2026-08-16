package protocol

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
	if !strings.Contains(src, "module github.com/jonathanung/strike-cli/pkg/protocol\n") {
		t.Fatalf("go.mod missing module path:\n%s", src)
	}
	if strings.Contains(src, "require ") {
		t.Fatalf("pkg/protocol must stay stdlib-only; unexpected require:\n%s", src)
	}
}
