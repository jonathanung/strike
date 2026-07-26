package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/server"
	"github.com/jonathanung/strike-cli/internal/session"
)

const serveUsage = `Experimental web cockpit server (composer + live ops + RO attach).

Usage:
  strike serve [options]

Options:
  --addr <host:port>     bind address (default 127.0.0.1:8787)
  --token <token>        bearer token for /v1/* (required; auto-minted if omitted)
  --session-dir <path>   sessions directory (default ~/.strike/sessions)
  --provider <name>      live engine provider (default echo)
  --model <id>           model id for the live provider
  --attach-only          read-only JSONL attach (no live engine)
  --dangerously-skip-permissions
                         auto-allow permission asks in the live engine
  -h, --help             show help

Endpoints:
  GET  /health                      JSON ok + version (no auth)
  GET  /  and  GET /attach          cockpit page
  GET  /v1/ws                       WebSocket: ops in, events out (live)
  POST /v1/ops                      submit one op envelope (live)
  GET  /v1/live/events              SSE of live engine events
  GET  /v1/status                   live status chrome
  GET  /v1/agents                   selectable agents
  GET  /v1/sessions                 session list (+ liveId)
  GET  /v1/sessions/{id}/events     SSE tail of a session JSONL log

Auth: Authorization: Bearer <token> or ?token= on /v1/* routes.

DANGER: binding outside localhost exposes session transcripts and the live
engine control plane to the network. Keep --addr on loopback unless you
understand the risk. There is no TLS in this experimental server.
See docs/web.md. LAN expose flag is tracked separately (--expose).`

type serveOptions struct {
	addr                       string
	token                      string
	sessionDir                 string
	provider                   string
	model                      string
	attachOnly                 bool
	dangerouslySkipPermissions bool
}

func runServeCLI(args []string, stdout, stderr io.Writer) int {
	opts, err := parseServeArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, serveUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, serveUsage)
		return 2
	}
	if err := runServe(opts, stdout, stderr); err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

func parseServeArgs(args []string) (serveOptions, error) {
	var opts serveOptions
	fs := flag.NewFlagSet("strike serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.addr, "addr", "127.0.0.1:8787", "")
	fs.StringVar(&opts.token, "token", "", "")
	fs.StringVar(&opts.sessionDir, "session-dir", "", "")
	fs.StringVar(&opts.provider, "provider", "echo", "")
	fs.StringVar(&opts.model, "model", "", "")
	fs.BoolVar(&opts.attachOnly, "attach-only", false, "")
	fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
	if err := fs.Parse(args); err != nil {
		return serveOptions{}, err
	}
	if fs.NArg() != 0 {
		return serveOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.addr = strings.TrimSpace(opts.addr)
	opts.token = strings.TrimSpace(opts.token)
	opts.sessionDir = strings.TrimSpace(opts.sessionDir)
	opts.provider = strings.TrimSpace(opts.provider)
	opts.model = strings.TrimSpace(opts.model)
	if opts.addr == "" {
		opts.addr = "127.0.0.1:8787"
	}
	if opts.sessionDir == "" {
		opts.sessionDir = session.DefaultDir()
	}
	if opts.provider == "" {
		opts.provider = "echo"
	}
	return opts, nil
}

func runServe(opts serveOptions, stdout, stderr io.Writer) error {
	token := opts.token
	minted := false
	if token == "" {
		t, err := server.MintToken()
		if err != nil {
			return fmt.Errorf("minting token: %w", err)
		}
		token = t
		minted = true
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var live *server.Live
	var cleanup func() error
	sessionDir := opts.sessionDir

	if !opts.attachOnly {
		cliOpts := cliOptions{
			provider:                   opts.provider,
			providerSet:                true,
			model:                      opts.model,
			dangerouslySkipPermissions: opts.dangerouslySkipPermissions,
		}
		a, err := assemble(cliOpts, true)
		if err != nil {
			return err
		}
		sessionDir = a.sessions.Dir()
		if opts.sessionDir != "" && opts.sessionDir != session.DefaultDir() {
			// assemble always uses DefaultDir; honor explicit --session-dir only
			// for RO listing when attach-only. Live sessions stay in default dir.
			_ = opts.sessionDir
		}

		agents := make([]server.AgentInfo, 0, len(a.services.Agents))
		for _, name := range a.services.Agents {
			agents = append(agents, server.AgentInfo{Name: name})
		}
		live = server.NewLive(a.sessionID, a.workDir, agents, a.eng.Ops())

		engineDone := make(chan error, 1)
		go func() {
			engineDone <- runSession(ctx, a.eng.Run, a.eng.Events(), a.store, func(events <-chan protocol.Event) error {
				for {
					select {
					case <-ctx.Done():
						return nil
					case ev, ok := <-events:
						if !ok {
							return nil
						}
						live.Publish(ev)
					}
				}
			})
		}()

		cleanup = func() error {
			live.Close()
			stop()
			var out error
			select {
			case err := <-engineDone:
				out = err
			case <-time.After(8 * time.Second):
				out = errors.New("engine shutdown timeout")
			}
			if a.mcpClose != nil {
				if err := a.mcpClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.worktreeClose != nil {
				if err := a.worktreeClose(); err != nil && out == nil {
					out = err
				}
			}
			if err := a.issuesClose(); err != nil && out == nil {
				out = err
			}
			if err := a.memoryClose(); err != nil && out == nil {
				out = err
			}
			if err := a.historyClose(); err != nil && out == nil {
				out = err
			}
			if a.sessions != nil {
				_ = a.sessions.CloseAll()
			}
			return out
		}
		defer func() { _ = cleanup() }()

		writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)
	}

	srv, err := server.New(server.Options{
		Addr:       opts.addr,
		Token:      token,
		SessionDir: sessionDir,
		Live:       live,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.addr, err)
	}

	if !server.IsLocalhostBind(opts.addr) {
		fmt.Fprintln(stderr, "WARNING: strike serve is bound outside localhost.")
		fmt.Fprintln(stderr, "Session streams and live ops are reachable with the token; there is no TLS.")
		fmt.Fprintln(stderr, "Prefer --addr 127.0.0.1:8787 for local attach only. See docs/web.md.")
	}

	actual := ln.Addr().String()
	fmt.Fprintf(stdout, "strike serve listening on http://%s\n", actual)
	fmt.Fprintf(stdout, "  health:  http://%s/health\n", actual)
	fmt.Fprintf(stdout, "  cockpit: http://%s/attach?token=<token>\n", actual)
	if live != nil {
		fmt.Fprintf(stdout, "  live:    session %s  provider %s\n", live.SessionID(), opts.provider)
		fmt.Fprintf(stdout, "  ws:      ws://%s/v1/ws?token=<token>\n", actual)
	} else {
		fmt.Fprintln(stdout, "  mode:    attach-only (read-only JSONL)")
	}
	if minted {
		fmt.Fprintf(stdout, "  token:   %s  (auto-minted; pass --token to set)\n", token)
	} else {
		fmt.Fprintln(stdout, "  token:   (from --token)")
	}
	fmt.Fprintf(stdout, "  sessions dir: %s\n", sessionDir)
	fmt.Fprintln(stdout, "experimental web cockpit — TUI remains primary")

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		err := <-errCh
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-errCh:
		return err
	}
}
