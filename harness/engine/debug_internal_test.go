package engine

import (
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike/harness/debugmode"
	"github.com/jonathanung/strike/harness/provider"
)

// TestChildInheritsParentDebugGate: a delegated child must not bypass the
// parent's hypothesis-phase gate, including a session started after spawn.
func TestChildInheritsParentDebugGate(t *testing.T) {
	parent := New(Options{SessionID: "parent"})
	child := New(Options{SessionID: "child", debugFrom: parent})
	grandchild := New(Options{SessionID: "grandchild", debugFrom: child})
	args, _ := json.Marshal(map[string]string{"oldString": "a", "newString": "b"})
	call := provider.ToolCall{ID: "c1", Name: "edit", Args: args}

	if ge := grandchild.checkDebugGate(call); ge != nil {
		t.Fatalf("no debug session: gate = %v", ge)
	}
	parent.debug.Store(debugmode.New())
	for name, e := range map[string]*Engine{"parent": parent, "child": child, "grandchild": grandchild} {
		if ge := e.checkDebugGate(call); ge == nil || ge.Tool != "edit" {
			t.Fatalf("%s: gate = %v, want edit denial", name, ge)
		}
	}
	read := provider.ToolCall{ID: "c2", Name: "read", Args: json.RawMessage(`{"filePath":"a.go"}`)}
	if ge := child.checkDebugGate(read); ge != nil {
		t.Fatalf("read must pass the gate: %v", ge)
	}
	parent.debug.Store(nil)
	if ge := child.checkDebugGate(call); ge != nil {
		t.Fatalf("after parent session ends: gate = %v", ge)
	}
}
