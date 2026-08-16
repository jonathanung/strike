package actionfacts

import (
	"strings"
	"testing"
)

func TestAnalyzeSimpleCommand(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: "ls -la src"})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if !f.EnforcementEligible() {
		t.Fatal("expected enforcement eligible")
	}
	if len(f.Commands) != 1 || f.Commands[0].Program != "ls" {
		t.Fatalf("commands=%+v", f.Commands)
	}
}

func TestAnalyzePipeAndRedirect(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: "cat notes.txt | grep todo > out.txt"})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v cmds=%+v", f.Parse.Status, f.Parse.Issues, f.Commands)
	}
	if len(f.Commands) != 2 {
		t.Fatalf("want 2 commands, got %+v", f.Commands)
	}
	progs := map[string]bool{}
	for _, c := range f.Commands {
		progs[c.Program] = true
	}
	if !progs["cat"] || !progs["grep"] {
		t.Fatalf("progs=%v", progs)
	}
	var sawRead, sawWrite bool
	for _, p := range f.Paths {
		if p.Value == "notes.txt" && p.Access == PathRead {
			sawRead = true
		}
		if p.Value == "out.txt" && (p.Access == PathWrite || p.Access == PathAppend) {
			sawWrite = true
		}
	}
	if !sawRead || !sawWrite {
		t.Fatalf("paths=%+v read=%v write=%v", f.Paths, sawRead, sawWrite)
	}
}

func TestAnalyzeCurlNetwork(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: "curl -s https://example.com/a -o /tmp/x"})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if len(f.Network) == 0 || f.Network[0].Host != "example.com" {
		t.Fatalf("network=%+v", f.Network)
	}
	var sawOut bool
	for _, p := range f.Paths {
		if p.Value == "/tmp/x" && p.Access == PathWrite {
			sawOut = true
		}
	}
	if !sawOut {
		t.Fatalf("paths=%+v", f.Paths)
	}
}

func TestAnalyzeWget(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: "wget -O out.bin http://files.example.org/pkg"})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if len(f.Network) == 0 || f.Network[0].Host != "files.example.org" {
		t.Fatalf("network=%+v", f.Network)
	}
}

func TestAnalyzeNestedShell(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: `bash -c 'rm -rf /tmp/x'`})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v cmds=%+v", f.Parse.Status, f.Parse.Issues, f.Commands)
	}
	var sawRM bool
	for _, c := range f.Commands {
		if c.Program == "rm" {
			sawRM = true
		}
	}
	if !sawRM {
		t.Fatalf("expected nested rm, cmds=%+v", f.Commands)
	}
	keys := MatchKeys(f)
	joined := strings.Join(keys, " | ")
	if !strings.Contains(joined, "rm") {
		t.Fatalf("match keys missing rm: %v", keys)
	}
}

func TestAnalyzeAndOrChain(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: "git status && git diff"})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if len(f.Commands) != 2 {
		t.Fatalf("cmds=%+v", f.Commands)
	}
}

func TestAnalyzeQuotedArgs(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: `echo "hello world" 'single'`})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if got := f.Commands[0].Argv; len(got) != 3 || got[1] != "hello world" || got[2] != "single" {
		t.Fatalf("argv=%v", got)
	}
}

func TestAnalyzeArgvInput(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Argv: []string{"curl", "https://h.example/x"}})
	if !f.Authoritative() {
		t.Fatalf("status=%s", f.Parse.Status)
	}
	if len(f.Network) == 0 || f.Network[0].Host != "h.example" {
		t.Fatalf("network=%+v", f.Network)
	}
}

func TestAnalyzeWebfetchTool(t *testing.T) {
	f := Analyze(Input{Tool: "webfetch", Argv: []string{"https://api.example.com/v1"}})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if len(f.Network) != 1 || f.Network[0].Host != "api.example.com" {
		t.Fatalf("network=%+v", f.Network)
	}
}

func TestAnalyzeBrowserTool(t *testing.T) {
	f := Analyze(Input{Tool: "browser", Argv: []string{"https://app.example.com/ui"}})
	if !f.Authoritative() {
		t.Fatalf("status=%s issues=%v", f.Parse.Status, f.Parse.Issues)
	}
	if len(f.Network) != 1 || f.Network[0].Host != "app.example.com" {
		t.Fatalf("network=%+v", f.Network)
	}
}

func TestAnalyzeReadTool(t *testing.T) {
	f := Analyze(Input{Tool: "read", Argv: []string{"internal/foo.go"}})
	if !f.Authoritative() {
		t.Fatalf("status=%s", f.Parse.Status)
	}
	if len(f.Paths) != 1 || f.Paths[0].Value != "internal/foo.go" {
		t.Fatalf("paths=%+v", f.Paths)
	}
}

func TestSummaryAndMatchKeys(t *testing.T) {
	f := Analyze(Input{Tool: "bash", Command: "curl https://example.com"})
	sum := Summary(f)
	if !strings.Contains(sum, "authoritative") || !strings.Contains(sum, "curl") {
		t.Fatalf("summary=%q", sum)
	}
	keys := MatchKeys(f)
	if len(keys) == 0 {
		t.Fatal("expected keys")
	}
	// Non-eligible → no keys
	partial := Analyze(Input{Tool: "bash", Command: "echo $HOME"})
	if partial.EnforcementEligible() {
		t.Fatal("dynamic should not be enforcement eligible")
	}
	if keys := MatchKeys(partial); len(keys) != 0 {
		t.Fatalf("keys=%v", keys)
	}
}

func TestEmptyAndInvalid(t *testing.T) {
	if f := Analyze(Input{Tool: "bash", Command: "   "}); f.Parse.Status != StatusInvalid {
		t.Fatalf("empty status=%s", f.Parse.Status)
	}
	if f := Analyze(Input{Tool: "bash", Command: "echo \x00 hi"}); f.Parse.Status != StatusInvalid {
		t.Fatalf("nul status=%s", f.Parse.Status)
	}
}
