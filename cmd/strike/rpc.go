package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/rpc"
	"github.com/jonathanung/strike-cli/internal/server"
)

const rpcUsage = `Speak the Op/Event protocol over stdio as newline-delimited JSON-RPC 2.0.

Usage:
  strike rpc [options]

Options are the same as strike ( --provider, --model, --effort, --sandbox,
--i-know, --auto / --dangerously-skip-permissions, --session, --continue,
--worktree ). Stdout is pure JSON-RPC; diagnostics go to stderr.

Wire (one JSON object per line):

  → {"jsonrpc":"2.0","id":1,"method":"initialize"}
  ← {"jsonrpc":"2.0","method":"rpc.ready","params":{"protocolVersion":"…","sessionId":"…"}}
  ← {"jsonrpc":"2.0","id":1,"result":{…}}

  → {"jsonrpc":"2.0","id":2,"method":"user.input","params":{"text":"hi"}}
  ← {"jsonrpc":"2.0","id":2,"result":{"ok":true}}
  ← {"jsonrpc":"2.0","method":"event","params":{"type":"text.delta","v":"…","data":{…}}}

  → {"jsonrpc":"2.0","id":3,"method":"op","params":{"type":"interrupt"}}
  → {"jsonrpc":"2.0","id":4,"method":"shutdown"}

Ops may be sent as method=<op type> with params=op data, or method=op with
params=OpEnvelope (same shape as POST /v1/ops and the WebSocket). Events are
always method=event with params=Event envelope (session JSONL shape).

Permission and question asks are emitted as events; reply with
permission.reply / question.reply (or pass --auto to skip configured asks).`

// runRPCCLI parses `strike rpc` args and runs the stdio JSON-RPC bridge.
func runRPCCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseRPCArgs(args)
	if err != nil {
		if errors.Is(err, errRPCHelp) {
			fmt.Fprintln(stdout, rpcUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, rpcUsage)
		return 2
	}
	if err := runRPC(opts, stdin, stdout, stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

var errRPCHelp = errors.New("rpc help")

func parseRPCArgs(args []string) (cliOptions, error) {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return cliOptions{}, errRPCHelp
		}
	}
	opts, err := parseCLIOptions(args)
	if err != nil {
		return cliOptions{}, err
	}
	// rpc is a long-lived bridge; --upgrade/--version belong to the root CLI.
	if opts.upgrade {
		return cliOptions{}, fmt.Errorf("unknown flag --upgrade (use: strike upgrade)")
	}
	if opts.version {
		return cliOptions{}, fmt.Errorf("unknown flag --version (use: strike version)")
	}
	return opts, nil
}

// runRPC is the composition root for `strike rpc`: same engine + session log
// as serve/exec, with a stdio JSON-RPC frontend instead of TUI/HTTP.
func runRPC(opts cliOptions, stdin io.Reader, stdout, stderr io.Writer) (runErr error) {
	a, err := assemble(opts, true)
	if err != nil {
		return err
	}
	if a.worktreeNotice != "" {
		fmt.Fprintln(stderr, a.worktreeNotice)
	}
	defer func() {
		if a.mcpClose != nil {
			if err := a.mcpClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing mcp servers: %w", err)
			}
		}
		if a.schedulerClose != nil {
			a.schedulerClose()
		}
		if a.worktreeClose != nil {
			if err := a.worktreeClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("removing session worktree: %w", err)
			}
		}
		if a.goalsClose != nil {
			if err := a.goalsClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project goals: %w", err)
			}
		}
		if err := a.issuesClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project issues: %w", err)
		}
		if err := a.memoryClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project memory: %w", err)
		}
		if err := a.historyClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing prompt history: %w", err)
		}
	}()

	storeOwned := false
	defer func() {
		if !storeOwned {
			if cerr := a.store.Close(); cerr != nil && runErr == nil {
				runErr = fmt.Errorf("closing session store: %w", cerr)
			}
		}
	}()

	writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)

	// Live bridge tracks status chrome the same way serve's WebSocket path does.
	agents := make([]server.AgentInfo, 0, len(a.services.Agents))
	for _, name := range a.services.Agents {
		agents = append(agents, server.AgentInfo{Name: name})
	}
	sessionID := a.sessionID
	if sessionID == "" {
		sessionID = a.store.ID()
	}
	live := server.NewLive(sessionID, a.workDir, agents, a.eng.Ops())
	defer live.Close()

	storeOwned = true
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runSession(ctx, a.eng.Run, a.eng.Events(), a.store, func(events <-chan protocol.Event) error {
		// Tee engine events into Live for status, then into the RPC writer.
		bridged := make(chan protocol.Event, 256)
		go func() {
			defer close(bridged)
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-events:
					if !ok {
						return
					}
					live.Publish(ev)
					select {
					case bridged <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}()

		submit := func(submitCtx context.Context, op protocol.Op) error {
			return live.Submit(submitCtx, op)
		}
		srv := rpc.New(stdin, stdout, submit, rpc.Options{
			SessionID: sessionID,
			Status: func() map[string]any {
				st := live.Status()
				return map[string]any{
					"sessionId":      st.SessionID,
					"provider":       st.Provider,
					"model":          st.Model,
					"agent":          st.Agent,
					"effort":         st.Effort,
					"autonomy":       st.Autonomy,
					"permissionMode": st.PermissionMode,
					"phase":          st.Phase,
					"workflow":       st.Workflow,
					"cwd":            st.CWD,
					"busy":           st.Busy,
					"contextUsed":    st.ContextUsed,
					"contextLimit":   st.ContextLimit,
				}
			},
			SubmitTimeout: 5 * time.Second,
		})
		return srv.Run(ctx, bridged)
	})
}
