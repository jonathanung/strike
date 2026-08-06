package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperProcess is the fake stdio language server used by tests.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("LSP_FAKE_MODE")
	runFakeLSP(mode)
	os.Exit(0)
}

func runFakeLSP(mode string) {
	r := bufio.NewReader(os.Stdin)
	var mu sync.Mutex
	write := func(v any) {
		data, _ := json.Marshal(v)
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}

	openURIs := map[string]int{}

	for {
		body, err := readFrame(r)
		if err != nil {
			return
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		if msg.ID == nil {
			// notification
			switch msg.Method {
			case "initialized":
				// ok
			case "textDocument/didOpen":
				var p didOpenParams
				_ = json.Unmarshal(msg.Params, &p)
				openURIs[p.TextDocument.URI] = p.TextDocument.Version
				if mode != "no-diagnostics" {
					// Publish a diagnostic when content contains "ERR"
					sev := 0
					message := "ok"
					if strings.Contains(p.TextDocument.Text, "ERR") {
						sev = SeverityError
						message = "found ERR"
					}
					diags := []Diagnostic{}
					if sev > 0 {
						diags = append(diags, Diagnostic{
							Range: Range{
								Start: Position{Line: 0, Character: 0},
								End:   Position{Line: 0, Character: 3},
							},
							Severity: sev,
							Source:   "fake-ls",
							Message:  message,
						})
					}
					write(map[string]any{
						"jsonrpc": "2.0",
						"method":  "textDocument/publishDiagnostics",
						"params": publishDiagnosticsParams{
							URI:         p.TextDocument.URI,
							Diagnostics: diags,
						},
					})
				}
			case "textDocument/didChange":
				var p didChangeParams
				_ = json.Unmarshal(msg.Params, &p)
				openURIs[p.TextDocument.URI] = p.TextDocument.Version
				text := ""
				if len(p.ContentChanges) > 0 {
					text = p.ContentChanges[0].Text
				}
				diags := []Diagnostic{}
				if strings.Contains(text, "ERR") {
					diags = append(diags, Diagnostic{
						Range: Range{
							Start: Position{Line: 0, Character: 0},
							End:   Position{Line: 0, Character: 3},
						},
						Severity: SeverityError,
						Source:   "fake-ls",
						Message:  "found ERR",
					})
				}
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "textDocument/publishDiagnostics",
					"params": publishDiagnosticsParams{
						URI:         p.TextDocument.URI,
						Diagnostics: diags,
					},
				})
			case "textDocument/didClose":
				var p didCloseParams
				_ = json.Unmarshal(msg.Params, &p)
				delete(openURIs, p.TextDocument.URI)
				write(map[string]any{
					"jsonrpc": "2.0",
					"method":  "textDocument/publishDiagnostics",
					"params": publishDiagnosticsParams{
						URI:         p.TextDocument.URI,
						Diagnostics: nil,
					},
				})
			case "exit":
				os.Exit(0)
			}
			continue
		}

		switch msg.Method {
		case "initialize":
			if mode == "fail-init" {
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      *msg.ID,
					"error":   map[string]any{"code": -32000, "message": "init refused"},
				})
				continue
			}
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"textDocumentSync": 1,
					},
					"serverInfo": map[string]string{"name": "fake-ls", "version": "0.0.1"},
				},
			})
			if mode == "server-request" {
				// Ask client for configuration after initialize response.
				go func() {
					time.Sleep(50 * time.Millisecond)
					write(map[string]any{
						"jsonrpc": "2.0",
						"id":      9001,
						"method":  "workspace/configuration",
						"params":  map[string]any{"items": []map[string]any{{"section": "gopls"}}},
					})
				}()
			}
		case "shutdown":
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result":  nil,
			})
			if mode == "crash-on-shutdown" {
				os.Exit(2)
			}
		case "textDocument/definition":
			if mode == "nav" || mode == "" || mode == "no-diagnostics" {
				var p textDocumentPositionParams
				_ = json.Unmarshal(msg.Params, &p)
				// Point at a synthetic location derived from the request URI.
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      *msg.ID,
					"result": Location{
						URI: p.TextDocument.URI,
						Range: Range{
							Start: Position{Line: p.Position.Line, Character: 0},
							End:   Position{Line: p.Position.Line, Character: 3},
						},
					},
				})
			} else {
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      *msg.ID,
					"result":  nil,
				})
			}
		case "textDocument/references":
			var p referenceParams
			_ = json.Unmarshal(msg.Params, &p)
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result": []Location{
					{
						URI: p.TextDocument.URI,
						Range: Range{
							Start: Position{Line: p.Position.Line, Character: 0},
							End:   Position{Line: p.Position.Line, Character: 3},
						},
					},
					{
						URI: p.TextDocument.URI,
						Range: Range{
							Start: Position{Line: p.Position.Line + 1, Character: 4},
							End:   Position{Line: p.Position.Line + 1, Character: 7},
						},
					},
				},
			})
		case "textDocument/documentSymbol":
			var p documentSymbolParams
			_ = json.Unmarshal(msg.Params, &p)
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result": []documentSymbol{
					{
						Name:           "Foo",
						Kind:           SymbolKindFunction,
						Range:          Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 2, Character: 1}},
						SelectionRange: Range{Start: Position{Line: 0, Character: 5}, End: Position{Line: 0, Character: 8}},
						Children: []documentSymbol{
							{
								Name:           "helper",
								Kind:           SymbolKindMethod,
								Range:          Range{Start: Position{Line: 1, Character: 1}, End: Position{Line: 1, Character: 10}},
								SelectionRange: Range{Start: Position{Line: 1, Character: 1}, End: Position{Line: 1, Character: 7}},
							},
						},
					},
				},
			})
		case "workspace/symbol":
			var p workspaceSymbolParams
			_ = json.Unmarshal(msg.Params, &p)
			uri := "file:///tmp/ws.go"
			if len(openURIs) > 0 {
				for u := range openURIs {
					uri = u
					break
				}
			}
			name := "Bar"
			if p.Query != "" {
				name = p.Query
			}
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result": []symbolInformation{
					{
						Name: name,
						Kind: SymbolKindStruct,
						Location: Location{
							URI:   uri,
							Range: Range{Start: Position{Line: 3, Character: 0}, End: Position{Line: 3, Character: 3}},
						},
						ContainerName: "pkg",
					},
				},
			})
		default:
			write(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
		if mode == "exit-after-init" && msg.Method == "initialize" {
			// Give client time to send initialized, then die.
			go func() {
				time.Sleep(100 * time.Millisecond)
				os.Exit(0)
			}()
		}
	}
}

func helperCommand(t *testing.T, mode string) (command string, args []string, env map[string]string) {
	t.Helper()
	return os.Args[0], []string{"-test.run=TestHelperProcess", "--"}, map[string]string{
		"GO_WANT_HELPER_PROCESS": "1",
		"LSP_FAKE_MODE":          mode,
	}
}

func TestStartDidOpenDiagnostics(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()
	client, err := Start(ctx, ServerConfig{
		Name:    "fake",
		Command: cmd,
		Args:    args,
		Env:     env,
		RootDir: dir,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	path := filepath.Join(dir, "main.go")
	if err := client.DidOpenOrChange(ctx, path, "package main\nERR\n"); err != nil {
		t.Fatalf("DidOpenOrChange: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var diags []Diagnostic
	for time.Now().Before(deadline) {
		diags = client.Diagnostics(path)
		if len(diags) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %#v, want 1 error", diags)
	}
	if diags[0].Message != "found ERR" {
		t.Fatalf("message = %q", diags[0].Message)
	}
	if diags[0].Severity != SeverityError {
		t.Fatalf("severity = %d", diags[0].Severity)
	}

	// Change clears error.
	if err := client.DidOpenOrChange(ctx, path, "package main\n"); err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		diags = client.Diagnostics(path)
		if len(diags) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(diags) != 0 {
		t.Fatalf("after fix diagnostics = %#v", diags)
	}
}

func TestDidCloseClearsDiagnostics(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()
	client, err := Start(ctx, ServerConfig{
		Name: "fake", Command: cmd, Args: args, Env: env, RootDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	path := filepath.Join(dir, "x.go")
	_ = client.DidOpenOrChange(ctx, path, "ERR")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.Diagnostics(path)) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := client.DidClose(ctx, path); err != nil {
		t.Fatal(err)
	}
	// Local clear is immediate; server also publishes empty.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.Diagnostics(path)) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("diagnostics still present after close: %#v", client.Diagnostics(path))
}

func TestCrashIsolationDeadServerNoPanic(t *testing.T) {
	cmd, args, env := helperCommand(t, "exit-after-init")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()
	client, err := Start(ctx, ServerConfig{
		Name: "dying", Command: cmd, Args: args, Env: env, RootDir: dir,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	// Wait for process exit.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !client.Closed() {
		time.Sleep(20 * time.Millisecond)
	}
	if !client.Closed() {
		t.Fatal("expected client closed after server exit")
	}

	// Mutations must not panic or return hard errors to callers that ignore them.
	path := filepath.Join(dir, "a.go")
	if err := client.DidOpenOrChange(ctx, path, "x"); err != nil {
		// DidOpenOrChange returns nil when closed — if it returns error, still ok as long as no panic.
		t.Logf("DidOpenOrChange on dead client: %v", err)
	}
	_ = client.DidClose(ctx, path)
	if diags := client.Diagnostics(path); diags != nil {
		t.Fatalf("diags on dead = %#v", diags)
	}
}

func TestInitFailure(t *testing.T) {
	cmd, args, env := helperCommand(t, "fail-init")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Start(ctx, ServerConfig{
		Name: "bad", Command: cmd, Args: args, Env: env, RootDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected init error")
	}
	if !strings.Contains(err.Error(), "initialize") && !strings.Contains(err.Error(), "init refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerRegistryAndNotifyFile(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()

	m.StartAll(ctx, []ServerConfig{{
		Name:       "go",
		Command:    cmd,
		Args:       args,
		Env:        env,
		RootDir:    dir,
		Extensions: []string{".go"},
	}})

	sts := m.Statuses()
	if len(sts) != 1 || sts[0].State != "up" {
		t.Fatalf("statuses = %#v", sts)
	}
	if name, ok := m.ServerForExt(".go"); !ok || name != "go" {
		t.Fatalf("ServerForExt = %q %v", name, ok)
	}
	if _, ok := m.ServerForExt(".py"); ok {
		t.Fatal("unexpected py server")
	}

	path := filepath.Join(dir, "main.go")
	m.NotifyFile(ctx, path, "package main\nERR\n", false)

	deadline := time.Now().Add(3 * time.Second)
	var diags []Diagnostic
	for time.Now().Before(deadline) {
		diags = m.Diagnostics(path)
		if len(diags) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(diags) != 1 || diags[0].Message != "found ERR" {
		t.Fatalf("manager diags = %#v", diags)
	}

	// Unknown extension is a no-op.
	m.NotifyFile(ctx, filepath.Join(dir, "x.py"), "print(1)", false)

	// Delete closes.
	m.NotifyFile(ctx, path, "", true)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.Diagnostics(path)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestManagerCrashDoesNotKillSession(t *testing.T) {
	cmd, args, env := helperCommand(t, "exit-after-init")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()

	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env,
		RootDir: dir, Extensions: []string{".go"},
	}})

	// Wait until down.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sts := m.Statuses()
		if len(sts) == 1 && (sts[0].State == "down" || sts[0].State == "error") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Notify must not panic.
	m.NotifyFile(ctx, filepath.Join(dir, "a.go"), "x", false)
	if diags := m.Diagnostics(filepath.Join(dir, "a.go")); len(diags) != 0 {
		t.Fatalf("expected no diags, got %#v", diags)
	}
}

func TestManagerCrashClearsStaleDiagnostics(t *testing.T) {
	// Start a healthy server, collect a diagnostic, then kill it and ensure
	// Diagnostics returns empty (not stale cache).
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()

	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env,
		RootDir: dir, Extensions: []string{".go"},
	}})
	path := filepath.Join(dir, "bad.go")
	m.NotifyFile(ctx, path, "ERR", false)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.Diagnostics(path)) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.Diagnostics(path)) == 0 {
		t.Fatal("expected diagnostic before crash")
	}

	// Force-close the live client to simulate crash.
	m.mu.Lock()
	c := m.clients["go"]
	m.mu.Unlock()
	if c == nil {
		t.Fatal("no client")
	}
	_ = c.Close()

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.Diagnostics(path)) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stale diagnostics after crash: %#v", m.Diagnostics(path))
}

func TestManagerStartFailureIsolated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()

	cmd, args, env := helperCommand(t, "")
	m.StartAll(ctx, []ServerConfig{
		{Name: "missing", Command: "/nonexistent/lsp-binary-strike-test", Extensions: []string{".rs"}, RootDir: dir},
		{Name: "go", Command: cmd, Args: args, Env: env, Extensions: []string{".go"}, RootDir: dir},
	})

	sts := m.Statuses()
	if len(sts) != 2 {
		t.Fatalf("statuses len = %d", len(sts))
	}
	byName := map[string]Status{}
	for _, s := range sts {
		byName[s.Name] = s
	}
	if byName["missing"].State != "error" && byName["missing"].State != "down" {
		t.Fatalf("missing = %#v", byName["missing"])
	}
	if byName["go"].State != "up" {
		t.Fatalf("go = %#v", byName["go"])
	}
}

func TestConfigsFromMap(t *testing.T) {
	got := ConfigsFromMap(map[string]ServerConfigFields{
		"gopls": {Command: "gopls", Extensions: []string{"go", ".go", ".GO"}},
		"":      {Command: "x", Extensions: []string{".x"}},
		"bad":   {Command: "", Extensions: []string{".c"}},
		"noext": {Command: "ccls"},
		"py":    {Command: "pylsp", Args: []string{"--tcp"}, Extensions: []string{".py"}, Env: map[string]string{"A": "1"}},
	}, "/work")
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	// sorted by name: gopls, py
	if got[0].Name != "gopls" || len(got[0].Extensions) != 1 || got[0].Extensions[0] != ".go" {
		t.Fatalf("gopls = %#v", got[0])
	}
	if got[0].RootDir != "/work" || got[0].WorkDir != "/work" {
		t.Fatalf("paths = %#v", got[0])
	}
	if got[1].Name != "py" || got[1].Env["A"] != "1" {
		t.Fatalf("py = %#v", got[1])
	}
}

func TestPathURIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.go")
	uri := PathToURI(path)
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("uri = %q", uri)
	}
	back := URIToPath(uri)
	if back != path {
		// Allow cleaned equality
		if filepath.Clean(back) != filepath.Clean(path) {
			t.Fatalf("roundtrip %q → %q → %q", path, uri, back)
		}
	}
}

func TestLanguageID(t *testing.T) {
	if got := languageID("x.go"); got != "go" {
		t.Fatalf("go = %q", got)
	}
	if got := languageID("a.TsX"); got != "typescriptreact" {
		t.Fatalf("tsx = %q", got)
	}
	if got := languageID("noext"); got != "plaintext" {
		t.Fatalf("noext = %q", got)
	}
}

func TestReadFrame(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":{}}`
	raw := fmt.Sprintf("Content-Length: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n%s", len(body), body)
	r := bufio.NewReader(strings.NewReader(raw))
	got, err := readFrame(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("got %q", got)
	}
	// EOF
	if _, err := readFrame(r); err != io.EOF && err != nil {
		// ReadString on empty returns EOF
		if err != io.EOF {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestFormatStatuses(t *testing.T) {
	if !strings.Contains(FormatStatuses(nil), "no language servers") {
		t.Fatal("empty")
	}
	s := FormatStatuses([]Status{{Name: "go", State: "up", Command: "gopls", Extensions: []string{".go"}}})
	if !strings.Contains(s, "gopls") || !strings.Contains(s, ".go") {
		t.Fatalf("s = %q", s)
	}
	if !strings.Contains(s, "/lsp retry") || !strings.Contains(s, "/diagnostics") {
		t.Fatalf("missing hints: %q", s)
	}
}

func TestDisableAndRetry(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()

	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env, RootDir: dir, Extensions: []string{".go"},
	}})
	if err := m.Disable("go"); err != nil {
		t.Fatal(err)
	}
	sts := m.Statuses()
	if sts[0].State != "disabled" {
		t.Fatalf("state = %s", sts[0].State)
	}
	// Notify while disabled is no-op.
	m.NotifyFile(ctx, filepath.Join(dir, "a.go"), "ERR", false)
	if len(m.Diagnostics(filepath.Join(dir, "a.go"))) != 0 {
		t.Fatal("expected no diags while disabled")
	}
	if err := m.Retry(ctx, "go"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	sts = m.Statuses()
	if sts[0].State != "up" {
		t.Fatalf("after retry = %#v", sts[0])
	}
}

func TestServerRequestConfiguration(t *testing.T) {
	cmd, args, env := helperCommand(t, "server-request")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Start(ctx, ServerConfig{
		Name: "fake", Command: cmd, Args: args, Env: env, RootDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Give server time to send workspace/configuration; client should reply without hanging.
	time.Sleep(200 * time.Millisecond)
	if client.Closed() {
		t.Fatal("client closed unexpectedly")
	}
}
