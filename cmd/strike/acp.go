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

	"github.com/jonathanung/strike-cli/internal/acp"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/server"
	"github.com/jonathanung/strike-cli/internal/version"
)

const acpUsage = `Speak the Agent Client Protocol (ACP) over stdio as newline-delimited JSON-RPC 2.0.

Usage:
  strike acp [options]

Options are the same as strike ( --provider, --model, --effort, --sandbox,
--i-know, --auto / --dangerously-skip-permissions, --session, --continue,
--worktree ). Stdout is pure ACP JSON-RPC; diagnostics go to stderr.

Embed strike in ACP clients (Zed, Devin Desktop, …) by configuring the agent
command to ` + "`strike acp`" + `.

Wire (one JSON object per line):

  → {"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}
  ← {"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{…},"agentInfo":{…}}}

  → {"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/abs/path","mcpServers":[]}}
  ← {"jsonrpc":"2.0","id":1,"result":{"sessionId":"…"}}

  → {"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"…","prompt":[{"type":"text","text":"hi"}]}}
  ← {"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"…","update":{"sessionUpdate":"agent_message_chunk",…}}}
  ← {"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}

  → {"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"…"}}

Maps ACP session/prompt onto strike user.input, streams TextDelta/ToolCall*
events as session/update, and forwards PermissionAsked as
session/request_permission (reply with allow/reject options). Use --auto to
skip configured permission asks.`

// runACPCLI parses `strike acp` args and runs the ACP agent adapter.
func runACPCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseACPArgs(args)
	if err != nil {
		if errors.Is(err, errACPHelp) {
			fmt.Fprintln(stdout, acpUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, acpUsage)
		return 2
	}
	if err := runACP(opts, stdin, stdout, stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

var errACPHelp = errors.New("acp help")

func parseACPArgs(args []string) (cliOptions, error) {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return cliOptions{}, errACPHelp
		}
	}
	opts, err := parseCLIOptions(args)
	if err != nil {
		return cliOptions{}, err
	}
	if opts.upgrade {
		return cliOptions{}, fmt.Errorf("unknown flag --upgrade (use: strike upgrade)")
	}
	if opts.version {
		return cliOptions{}, fmt.Errorf("unknown flag --version (use: strike version)")
	}
	return opts, nil
}

// runACP is the composition root for `strike acp`: same engine + session log
// as rpc/serve/exec, with an ACP agent frontend instead of TUI/HTTP/raw rpc.
func runACP(opts cliOptions, stdin io.Reader, stdout, stderr io.Writer) (runErr error) {
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
		if a.harnessClose != nil {
			if err := a.harnessClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing harness workers: %w", err)
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
		if a.plansClose != nil {
			if err := a.plansClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project plans: %w", err)
			}
		}
		if a.artifactsClose != nil {
			if err := a.artifactsClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project artifacts: %w", err)
			}
		}
		if a.ledgerClose != nil {
			if err := a.ledgerClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project ledger: %w", err)
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

	ver := version.Version
	if ver == "" {
		ver = "dev"
	}

	return runSession(ctx, a.eng.Run, a.eng.Events(), bindAudit(a.store, a.cfg), func(events <-chan protocol.Event) error {
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
		srv := acp.New(stdin, stdout, submit, acp.Options{
			SessionID:     sessionID,
			CWD:           a.workDir,
			AgentName:     "strike",
			AgentTitle:    "Strike",
			AgentVersion:  ver,
			SubmitTimeout: 5 * time.Second,
		})
		return srv.Run(ctx, bridged)
	})
}
