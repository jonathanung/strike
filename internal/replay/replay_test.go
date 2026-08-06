package replay_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/replay"
)

func TestExtractUserInputsSkipsChildLineage(t *testing.T) {
	events := []protocol.Event{
		protocol.UserMessage{Text: "root-a"},
		protocol.UserMessage{
			Correlation: protocol.Correlation{ParentSessionID: "root", Depth: 1},
			Text:        "child-only",
		},
		protocol.UserMessage{Text: "root-b"},
	}
	got := replay.ExtractUserInputs(events)
	want := []string{"root-a", "root-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractUserInputs = %#v, want %#v", got, want)
	}
}

func TestExtractToolCallsNormalizesArgs(t *testing.T) {
	events := []protocol.Event{
		protocol.ToolCallBegin{
			Name: "bash",
			Args: json.RawMessage(`{ "command" : "echo hi" }`),
		},
		protocol.ToolCallBegin{
			Correlation: protocol.Correlation{ParentSessionID: "p", Depth: 1},
			Name:        "bash",
			Args:        json.RawMessage(`{"command":"child"}`),
		},
		protocol.ToolCallBegin{
			Name: "read",
			Args: json.RawMessage(`{"path":"a.go"}`),
		},
	}
	got := replay.ExtractToolCalls(events)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "bash" {
		t.Errorf("got[0].Name = %q", got[0].Name)
	}
	if string(got[0].Args) != `{"command":"echo hi"}` {
		t.Errorf("got[0].Args = %s", got[0].Args)
	}
	if got[1].Name != "read" || string(got[1].Args) != `{"path":"a.go"}` {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestDiffToolCalls(t *testing.T) {
	a := []replay.ToolCall{{Name: "bash", Args: json.RawMessage(`{"command":"echo"}`)}}
	b := []replay.ToolCall{{Name: "bash", Args: json.RawMessage(`{ "command" : "echo" }`)}}
	if err := replay.DiffToolCalls(a, b); err != nil {
		t.Fatalf("equivalent args should match: %v", err)
	}
	if err := replay.DiffToolCalls(a, nil); err == nil {
		t.Fatal("expected count mismatch")
	}
	c := []replay.ToolCall{{Name: "read", Args: json.RawMessage(`{"path":"x"}`)}}
	if err := replay.DiffToolCalls(a, c); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want name mismatch, got %v", err)
	}
	d := []replay.ToolCall{{Name: "bash", Args: json.RawMessage(`{"command":"other"}`)}}
	if err := replay.DiffToolCalls(a, d); err == nil || !strings.Contains(err.Error(), "args") {
		t.Fatalf("want args mismatch, got %v", err)
	}
}

func TestRunPlainEchoNoTools(t *testing.T) {
	res, err := replay.Run(context.Background(), []string{"hello strike"}, replay.Options{
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d, want 1", res.Turns)
	}
	if len(res.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want none", res.ToolCalls)
	}
	if got := replay.ExtractUserInputs(res.Events); !reflect.DeepEqual(got, []string{"hello strike"}) {
		t.Fatalf("user inputs from events = %#v", got)
	}
}

func TestRunBashToolSequence(t *testing.T) {
	res, err := replay.Run(context.Background(), []string{"run echo hello-strike"}, replay.Options{
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 1 {
		t.Fatalf("turns = %d, want 1", res.Turns)
	}
	want := []replay.ToolCall{{
		Name: "bash",
		Args: json.RawMessage(`{"command":"echo hello-strike"}`),
	}}
	if err := replay.DiffToolCalls(want, res.ToolCalls); err != nil {
		t.Fatalf("tool sequence: %v (got %+v)", err, res.ToolCalls)
	}
}

func TestRunMultiTurnDeterministic(t *testing.T) {
	inputs := []string{"hello", "run echo second-pass"}
	opts := replay.Options{WorkDir: t.TempDir()}
	a, err := replay.Run(context.Background(), inputs, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.WorkDir = t.TempDir()
	b, err := replay.Run(context.Background(), inputs, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Turns != 2 || b.Turns != 2 {
		t.Fatalf("turns a=%d b=%d, want 2", a.Turns, b.Turns)
	}
	if err := replay.DiffToolCalls(a.ToolCalls, b.ToolCalls); err != nil {
		t.Fatalf("cross-run divergence: %v", err)
	}
	want := []replay.ToolCall{{
		Name: "bash",
		Args: json.RawMessage(`{"command":"echo second-pass"}`),
	}}
	if err := replay.DiffToolCalls(want, a.ToolCalls); err != nil {
		t.Fatalf("expected sequence: %v", err)
	}
}

func TestWriteLoadJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	events := []protocol.Event{
		protocol.UserMessage{Text: "hi"},
		protocol.ToolCallBegin{Name: "bash", Args: json.RawMessage(`{"command":"true"}`)},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	if err := replay.WriteJSONL(path, events); err != nil {
		t.Fatal(err)
	}
	got, err := replay.LoadJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("len = %d, want %d", len(got), len(events))
	}
	if um, ok := got[0].(protocol.UserMessage); !ok || um.Text != "hi" {
		t.Fatalf("first = %#v", got[0])
	}
	if tb, ok := got[1].(protocol.ToolCallBegin); !ok || tb.Name != "bash" {
		t.Fatalf("second = %#v", got[1])
	}
}

func TestRunRequiresWorkDir(t *testing.T) {
	_, err := replay.Run(context.Background(), []string{"x"}, replay.Options{})
	if err == nil || !strings.Contains(err.Error(), "WorkDir") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunEmptyInputs(t *testing.T) {
	res, err := replay.Run(context.Background(), nil, replay.Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 0 || res.Turns != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// seedScenarios define the committed golden corpus. UPDATE_GOLDEN=1 rewrites
// testdata/*.jsonl from a fresh echo run of each seed.
var seedScenarios = []struct {
	name   string
	inputs []string
}{
	{name: "plain-echo", inputs: []string{"hello strike"}},
	{name: "bash-run", inputs: []string{"run echo hello-strike"}},
	{name: "multi-turn", inputs: []string{"ping", "run echo second-pass"}},
}

func TestGoldenCorpus(t *testing.T) {
	testdata := filepath.Join("testdata")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(testdata, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, sc := range seedScenarios {
			res, err := replay.Run(context.Background(), sc.inputs, replay.Options{
				WorkDir: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("record %s: %v", sc.name, err)
			}
			path := filepath.Join(testdata, sc.name+".jsonl")
			if err := replay.WriteJSONL(path, res.Events); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			t.Logf("updated %s (%d events, %d tools)", path, len(res.Events), len(res.ToolCalls))
		}
	}

	entries, err := os.ReadDir(testdata)
	if err != nil {
		t.Fatalf("read testdata: %v (run UPDATE_GOLDEN=1 go test ./internal/replay/)", err)
	}
	var found int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		found++
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(testdata, name)
			golden, err := replay.LoadJSONL(path)
			if err != nil {
				t.Fatal(err)
			}
			inputs := replay.ExtractUserInputs(golden)
			if len(inputs) == 0 {
				t.Fatal("golden has no root user.message events")
			}
			wantTools := replay.ExtractToolCalls(golden)
			res, err := replay.Run(context.Background(), inputs, replay.Options{
				WorkDir: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := replay.DiffToolCalls(wantTools, res.ToolCalls); err != nil {
				t.Fatalf("tool-call sequence diverged: %v\nwant=%+v\ngot=%+v", err, wantTools, res.ToolCalls)
			}
			// Turn count must match golden TurnCompleted events (root only).
			wantTurns := 0
			for _, ev := range golden {
				if tc, ok := ev.(protocol.TurnCompleted); ok && tc.ParentSessionID == "" && tc.Depth == 0 {
					wantTurns++
				}
			}
			if res.Turns != wantTurns {
				t.Fatalf("turns = %d, want %d", res.Turns, wantTurns)
			}
		})
	}
	if found == 0 {
		t.Fatal("no golden JSONL files in testdata/ (run UPDATE_GOLDEN=1 go test ./internal/replay/)")
	}
}
