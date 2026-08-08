package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jonathanung/strike-cli/internal/audit"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/server"
	"github.com/jonathanung/strike-cli/internal/session"
)

const serveUsage = `Web agent workspace (live engine + durable session attach).

Usage:
  strike serve [options]

Options:
  --addr <host:port>     loopback bind address (default 127.0.0.1:8787)
  --auth                 require bearer authentication; auto-mints a token
  --token <token>        bearer token for /v1/*; requires --auth
  --session-dir <path>   sessions directory for --attach-only
                         (default ~/.strike/sessions; rejected in live mode)
  --provider <name>      live engine provider (default echo)
  --model <id>           model id for the live provider
  --attach-only          read-only JSONL attach (no live engine)
  --read-only            reject mutating protocol ops (POST /v1/ops + WS frames)
  --auto, --dangerously-skip-permissions
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
  GET  /v1/workflows                workflow catalog
  POST /v1/workflows/save           save document (never activates)
  POST /v1/workflows/{name}/start   start after confirm (rejects invalid)
  POST /v1/workflows/stop           stop active workflow
  POST /v1/workflow-drafts/review   draft review (checks + widening)
  POST /v1/workflow-drafts/save     save draft JSON with confirm

Auth: loopback is unauthenticated by default. Under --auth, use
Authorization: Bearer <token> or the strike_serve_token cookie (set by opening
/attach?token=… once; the server strips the query secret). Query tokens are
not accepted on /v1/* routes.

Bind is loopback-only (no cleartext LAN expose). For remote access use SSH:
  ssh -L 8787:127.0.0.1:8787 user@host
See docs/web.md.`

// errExposeRemoved is returned when legacy --expose / --allow-cidr are passed.
const errExposeRemoved = "--expose was removed (no cleartext LAN bind). Keep loopback and forward with: ssh -L 8787:127.0.0.1:8787 user@host (see docs/web.md)"

type serveOptions struct {
	addr                       string
	auth                       bool
	token                      string
	sessionDir                 string
	sessionDirSet              bool
	provider                   string
	model                      string
	attachOnly                 bool
	readOnly                   bool
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
	var expose bool
	var allowCIDR []string
	fs := flag.NewFlagSet("strike serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.addr, "addr", "127.0.0.1:8787", "")
	fs.BoolVar(&opts.auth, "auth", false, "")
	// Legacy flags: accepted only so we can print a migration error.
	fs.BoolVar(&expose, "expose", false, "")
	fs.Func("allow-cidr", "", func(v string) error {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				allowCIDR = append(allowCIDR, part)
			}
		}
		return nil
	})
	fs.StringVar(&opts.token, "token", "", "")
	fs.StringVar(&opts.sessionDir, "session-dir", "", "")
	fs.StringVar(&opts.provider, "provider", "echo", "")
	fs.StringVar(&opts.model, "model", "", "")
	fs.BoolVar(&opts.attachOnly, "attach-only", false, "")
	fs.BoolVar(&opts.readOnly, "read-only", false, "")
	fs.BoolVar(&opts.dangerouslySkipPermissions, "auto", false, "")
	fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
	if err := fs.Parse(args); err != nil {
		return serveOptions{}, err
	}
	if fs.NArg() != 0 {
		return serveOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "session-dir" {
			opts.sessionDirSet = true
		}
	})
	if expose || len(allowCIDR) > 0 {
		return serveOptions{}, errors.New(errExposeRemoved)
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
	if opts.token != "" && !opts.auth {
		return serveOptions{}, fmt.Errorf("--token requires --auth")
	}
	if opts.sessionDirSet && !opts.attachOnly {
		return serveOptions{}, fmt.Errorf("--session-dir requires --attach-only; live sessions use %s", session.DefaultDir())
	}
	return opts, nil
}

func runServe(opts serveOptions, stdout, stderr io.Writer) error {
	bindAddr, err := server.ResolveBindAddr(opts.addr)
	if err != nil {
		return err
	}
	opts.addr = bindAddr

	token := opts.token
	minted := false
	if opts.auth && token == "" {
		t, err := server.MintToken()
		if err != nil {
			return fmt.Errorf("minting token: %w", err)
		}
		token = t
		minted = true
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var liveHub *server.LiveHub
	var services *host.Services
	var sandboxSnap *server.SandboxSnapshot
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

		agents := make([]server.AgentInfo, 0, len(a.services.Agents))
		for _, name := range a.services.Agents {
			agents = append(agents, server.AgentInfo{Name: name})
		}
		// Process-level sandbox chrome (same dial for every root from this serve).
		sandboxSnap = &server.SandboxSnapshot{
			Mode:         a.sandboxMode,
			Backend:      sandbox.BackendUnprobed,
			NetworkAllow: sandbox.CloneNetworkAllow(a.cfg.Network.Allow),
			Explain:      a.sandboxExplain,
		}
		seedLiveSandbox := func(live *server.Live) {
			if live == nil || sandboxSnap == nil {
				return
			}
			live.SetSandbox(sandboxSnap.Mode, sandboxSnap.Backend, sandboxSnap.Available, sandboxSnap.NetworkAllow, sandboxSnap.Explain)
		}

		// serveRoot tracks one live root engine for cleanup.
		type serveRoot struct {
			live    *server.Live
			bound   session.Bound
			wtClose func() error
			cancel  context.CancelFunc
			done    chan struct{}
		}
		var mu sync.Mutex
		roots := make(map[string]*serveRoot)

		// startServeRoot runs the engine and tees events to its Live bridge.
		startServeRoot := func(ctx context.Context, slot *rootSlot, live *server.Live) *serveRoot {
			rctx, rcancel := context.WithCancel(ctx)
			done := make(chan struct{})
			if slot.cancel == nil {
				slot.cancel = rcancel
			}
			if slot.done == nil {
				slot.done = done
			}
			sr := &serveRoot{
				live:    live,
				bound:   slot.bound,
				wtClose: slot.wtClose,
				cancel:  rcancel,
				done:    done,
			}
			go func() {
				defer close(done)
				runSession(rctx, slot.eng.Run, slot.eng.Events(), bindAudit(slot.bound, a.cfg), func(events <-chan protocol.Event) error {
					for {
						select {
						case <-rctx.Done():
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
			return sr
		}

		// Build LiveHub with spawn and resume callbacks.
		liveHub = server.NewLiveHub(
			func(ctx context.Context) (string, error) {
				slot, err := a.spawnRoot("")
				if err != nil {
					return "", err
				}
				live := server.NewLive(slot.id, slot.workDir, agents, slot.eng.Ops())
				seedLiveSandbox(live)
				sr := startServeRoot(ctx, slot, live)
				mu.Lock()
				roots[slot.id] = sr
				mu.Unlock()
				liveHub.Add(slot.id, live)
				if info, err := a.sessions.Get(slot.id); err == nil && info.Title != "" {
					liveHub.SetTitle(slot.id, info.Title)
				}
				return slot.id, nil
			},
			func(ctx context.Context, sessionID string) (string, bool, error) {
				// Validate: session must exist and be a root.
				sessionID = strings.TrimSpace(sessionID)
				if sessionID == "" {
					return "", false, fmt.Errorf("session id is empty")
				}
				info, err := a.sessions.Get(sessionID)
				if err != nil {
					return "", false, fmt.Errorf("session %q not found", sessionID)
				}
				if info.ParentSessionID != "" {
					return "", false, fmt.Errorf("cannot resume child session %q; resume a root session", sessionID)
				}
				// Already live: just activate.
				mu.Lock()
				_, exists := roots[sessionID]
				mu.Unlock()
				if exists {
					if err := liveHub.Activate(sessionID); err != nil {
						return "", false, err
					}
					return sessionID, true, nil
				}
				// Open the durable session.
				slot, err := a.spawnRoot(sessionID)
				if err != nil {
					return "", false, err
				}
				live := server.NewLive(slot.id, slot.workDir, agents, slot.eng.Ops())
				seedLiveSandbox(live)
				sr := startServeRoot(ctx, slot, live)
				mu.Lock()
				roots[slot.id] = sr
				mu.Unlock()
				liveHub.Add(slot.id, live)
				if info, err := a.sessions.Get(slot.id); err == nil && info.Title != "" {
					liveHub.SetTitle(slot.id, info.Title)
				}
				return slot.id, false, nil
			},
		)

		// Add initial root from the first assembly slot.
		initialSlot := a.firstSlot
		initialLive := server.NewLive(initialSlot.id, initialSlot.workDir, agents, initialSlot.eng.Ops())
		seedLiveSandbox(initialLive)
		liveHub.Add(initialSlot.id, initialLive)
		if info, err := a.sessions.Get(initialSlot.id); err == nil && info.Title != "" {
			liveHub.SetTitle(initialSlot.id, info.Title)
		}
		initialSR := startServeRoot(ctx, initialSlot, initialLive)
		roots[initialSlot.id] = initialSR

		services = &a.services

		cleanup = func() error {
			if liveHub != nil {
				liveHub.Close()
			}
			stop()
			mu.Lock()
			refs := make([]*serveRoot, 0, len(roots))
			for _, r := range roots {
				refs = append(refs, r)
			}
			mu.Unlock()
			var out error
			// Cancel all engines.
			for _, sr := range refs {
				if sr.cancel != nil {
					sr.cancel()
				}
			}
			// Wait for all engines to finish.
			for _, sr := range refs {
				<-sr.done
			}
			// Close all binds, worktrees, and shared services.
			for _, sr := range refs {
				if err := sr.bound.Close(); err != nil && out == nil {
					out = err
				}
				if sr.wtClose != nil {
					if err := sr.wtClose(); err != nil && out == nil {
						out = err
					}
				}
			}
			if a.mcpClose != nil {
				if err := a.mcpClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.lspClose != nil {
				if err := a.lspClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.harnessClose != nil {
				if err := a.harnessClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.schedulerClose != nil {
				a.schedulerClose()
			}
			if a.plansClose != nil {
				if err := a.plansClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.artifactsClose != nil {
				if err := a.artifactsClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.ledgerClose != nil {
				if err := a.ledgerClose(); err != nil && out == nil {
					out = err
				}
			}
			if a.goalsClose != nil {
				if err := a.goalsClose(); err != nil && out == nil {
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

	auditSink, err := audit.Open(audit.Options{})
	if err != nil {
		return fmt.Errorf("open audit sink: %w", err)
	}
	defer func() { _ = auditSink.Close() }()

	srv, err := server.New(server.Options{
		Addr:       opts.addr,
		Auth:       opts.auth,
		Token:      token,
		SessionDir: sessionDir,
		LiveHub:    liveHub,
		Services:   services,
		Sandbox:    sandboxSnap,
		ReadOnly:   opts.readOnly,
		Audit:      auditSink,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.addr, err)
	}

	actual := ln.Addr().String()

	printServeBanner(stdout, serveBanner{
		listenAddr: actual,
		token:      token,
		auth:       opts.auth,
		minted:     minted,
		liveHub:    liveHub,
		provider:   opts.provider,
		sessionDir: sessionDir,
		readOnly:   opts.readOnly,
	})

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

type serveBanner struct {
	listenAddr string
	token      string
	auth       bool
	minted     bool
	liveHub    *server.LiveHub
	provider   string
	sessionDir string
	readOnly   bool
}

func printServeBanner(w io.Writer, b serveBanner) {
	fmt.Fprintf(w, "strike serve listening on http://%s\n", b.listenAddr)
	fmt.Fprintf(w, "  health:  http://%s/health\n", b.listenAddr)
	if b.auth {
		fmt.Fprintf(w, "  cockpit: http://%s/attach?token=%s\n", b.listenAddr, url.QueryEscape(b.token))
	} else {
		fmt.Fprintf(w, "  cockpit: http://%s/attach\n", b.listenAddr)
	}
	if b.liveHub != nil {
		active := b.liveHub.Active()
		if active != nil {
			fmt.Fprintf(w, "  live:    session %s  provider %s\n", active.SessionID(), b.provider)
		}
		fmt.Fprintf(w, "  ws:      ws://%s/v1/ws  (cookie or Bearer)\n", b.listenAddr)
	} else {
		fmt.Fprintln(w, "  mode:    attach-only (read-only JSONL)")
	}
	if b.readOnly {
		fmt.Fprintln(w, "  ops:     read-only (mutating ops rejected)")
	}
	if !b.auth {
		fmt.Fprintln(w, "  auth:    off (loopback only)")
	} else if b.minted {
		fmt.Fprintf(w, "  token:   %s  (auto-minted; pass --token to set)\n", b.token)
	} else {
		fmt.Fprintln(w, "  token:   (from --token)")
	}
	fmt.Fprintf(w, "  sessions dir: %s\n", b.sessionDir)
	fmt.Fprintln(w, "  audit:   serve ops → ~/.strike/audit (family serve_op)")
	fmt.Fprintln(w, "  remote:  ssh -L 8787:127.0.0.1:8787 user@host  (loopback only; see docs/web.md)")
	fmt.Fprintln(w, "experimental web cockpit — TUI remains primary")
}
