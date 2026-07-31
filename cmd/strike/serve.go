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

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/server"
	"github.com/jonathanung/strike-cli/internal/session"
)

const serveUsage = `Web agent workspace (live engine + durable session attach).

Usage:
  strike serve [options]

Options:
  --addr <host:port>     bind address (default 127.0.0.1:8787)
  --auth                 require bearer authentication; auto-mints a token
  --expose               bind on all interfaces (0.0.0.0) for LAN access;
                         requires --auth; prints LAN URLs + loud WARNING
  --allow-cidr <cidr>    with --expose, only accept clients in these CIDRs
                         (repeatable or comma-separated; optional)
  --token <token>        bearer token for /v1/*; requires --auth
  --session-dir <path>   sessions directory for --attach-only
                         (default ~/.strike/sessions; rejected in live mode)
  --provider <name>      live engine provider (default echo)
  --model <id>           model id for the live provider
  --attach-only          read-only JSONL attach (no live engine)
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

Auth: loopback is unauthenticated by default. Under --auth, use
Authorization: Bearer <token>, the strike_serve_token cookie (set by opening
/attach?token=…), or ?token= on /v1/* routes.

DANGER: --expose (or any non-loopback bind) puts session transcripts and the
live control plane on the network. There is no TLS. Prefer loopback + SSH -L
when possible. See docs/web.md.`

type serveOptions struct {
	addr                       string
	auth                       bool
	expose                     bool
	allowCIDR                  cidrFlag
	token                      string
	sessionDir                 string
	sessionDirSet              bool
	provider                   string
	model                      string
	attachOnly                 bool
	dangerouslySkipPermissions bool
}

// cidrFlag accumulates repeated --allow-cidr values (and comma-separated lists).
type cidrFlag []string

func (c *cidrFlag) String() string { return strings.Join(*c, ",") }

func (c *cidrFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*c = append(*c, part)
		}
	}
	return nil
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
	fs.BoolVar(&opts.auth, "auth", false, "")
	fs.BoolVar(&opts.expose, "expose", false, "")
	fs.Var(&opts.allowCIDR, "allow-cidr", "")
	fs.StringVar(&opts.token, "token", "", "")
	fs.StringVar(&opts.sessionDir, "session-dir", "", "")
	fs.StringVar(&opts.provider, "provider", "echo", "")
	fs.StringVar(&opts.model, "model", "", "")
	fs.BoolVar(&opts.attachOnly, "attach-only", false, "")
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
	if len(opts.allowCIDR) > 0 && !opts.expose {
		return serveOptions{}, fmt.Errorf("--allow-cidr requires --expose")
	}
	if opts.token != "" && !opts.auth {
		return serveOptions{}, fmt.Errorf("--token requires --auth")
	}
	if opts.expose && !opts.auth {
		return serveOptions{}, fmt.Errorf("--expose requires --auth")
	}
	if opts.sessionDirSet && !opts.attachOnly {
		return serveOptions{}, fmt.Errorf("--session-dir requires --attach-only; live sessions use %s", session.DefaultDir())
	}
	return opts, nil
}

func runServe(opts serveOptions, stdout, stderr io.Writer) error {
	bindAddr, err := server.ResolveBindAddr(opts.addr, opts.expose)
	if err != nil {
		return err
	}
	opts.addr = bindAddr
	if !server.IsLocalhostBind(opts.addr) && !opts.auth {
		return errors.New("non-loopback --addr requires --auth (and --expose for network binds)")
	}

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

	var allowCIDRs []*net.IPNet
	if len(opts.allowCIDR) > 0 {
		allowCIDRs, err = server.ParseCIDRs([]string(opts.allowCIDR))
		if err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var liveHub *server.LiveHub
	var services *host.Services
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
				runSession(rctx, slot.eng.Run, slot.eng.Events(), slot.bound, func(events <-chan protocol.Event) error {
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

	srv, err := server.New(server.Options{
		Addr:       opts.addr,
		Auth:       opts.auth,
		Token:      token,
		SessionDir: sessionDir,
		LiveHub:    liveHub,
		Expose:     opts.expose || !server.IsLocalhostBind(opts.addr),
		AllowCIDRs: allowCIDRs,
		Services:   services,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.addr, err)
	}

	actual := ln.Addr().String()
	_, port, _ := net.SplitHostPort(actual)
	if port == "" {
		port = "8787"
	}

	exposed := opts.expose || !server.IsLocalhostBind(opts.addr)
	if exposed {
		writeExposeWarning(stderr)
	}

	printServeBanner(stdout, serveBanner{
		listenAddr: actual,
		port:       port,
		token:      token,
		auth:       opts.auth,
		minted:     minted,
		exposed:    exposed,
		liveHub:    liveHub,
		provider:   opts.provider,
		sessionDir: sessionDir,
		allowCIDR:  []string(opts.allowCIDR),
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

func writeExposeWarning(stderr io.Writer) {
	fmt.Fprintln(stderr, "")
	fmt.Fprintln(stderr, "╔══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(stderr, "║  WARNING: strike serve --expose is bound off localhost       ║")
	fmt.Fprintln(stderr, "║                                                              ║")
	fmt.Fprintln(stderr, "║  Anyone on the network who has the token can:                ║")
	fmt.Fprintln(stderr, "║    • read session transcripts                                ║")
	fmt.Fprintln(stderr, "║    • submit ops / run tools via the live engine              ║")
	fmt.Fprintln(stderr, "║  There is NO TLS. Treat the token like a password.           ║")
	fmt.Fprintln(stderr, "║  Prefer loopback + SSH -L when possible. See docs/web.md.    ║")
	fmt.Fprintln(stderr, "╚══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(stderr, "")
}

type serveBanner struct {
	listenAddr string
	port       string
	token      string
	auth       bool
	minted     bool
	exposed    bool
	liveHub    *server.LiveHub
	provider   string
	sessionDir string
	allowCIDR  []string
}

func printServeBanner(w io.Writer, b serveBanner) {
	fmt.Fprintf(w, "strike serve listening on http://%s\n", b.listenAddr)
	if b.exposed {
		fmt.Fprintln(w, "  mode:    EXPOSE (LAN)")
		if ips := server.LANIPs(); len(ips) > 0 {
			fmt.Fprintln(w, "  LAN IPs:")
			for _, ip := range ips {
				hostport := net.JoinHostPort(ip, b.port)
				fmt.Fprintf(w, "    http://%s/health\n", hostport)
			}
		} else {
			fmt.Fprintln(w, "  LAN IPs: (none detected)")
		}
		// Print full cockpit URL with token once (stdout) for phone open.
		if ips := server.LANIPs(); len(ips) > 0 {
			u := cockpitURL(ips[0], b.port, b.token)
			fmt.Fprintf(w, "  cockpit: %s\n", u)
			fmt.Fprintln(w, "           (full URL with token printed once — do not share)")
		} else {
			fmt.Fprintf(w, "  cockpit: http://<lan-ip>:%s/attach?token=%s\n", b.port, url.QueryEscape(b.token))
		}
	} else {
		fmt.Fprintf(w, "  health:  http://%s/health\n", b.listenAddr)
		if b.auth {
			fmt.Fprintf(w, "  cockpit: http://%s/attach?token=%s\n", b.listenAddr, url.QueryEscape(b.token))
		} else {
			fmt.Fprintf(w, "  cockpit: http://%s/attach\n", b.listenAddr)
		}
	}
	if b.liveHub != nil {
		active := b.liveHub.Active()
		if active != nil {
			fmt.Fprintf(w, "  live:    session %s  provider %s\n", active.SessionID(), b.provider)
		}
		if b.exposed {
			if ips := server.LANIPs(); len(ips) > 0 {
				fmt.Fprintf(w, "  ws:      ws://%s/v1/ws?token=<token>\n", net.JoinHostPort(ips[0], b.port))
			} else {
				fmt.Fprintf(w, "  ws:      ws://<lan-ip>:%s/v1/ws?token=<token>\n", b.port)
			}
		} else {
			fmt.Fprintf(w, "  ws:      ws://%s/v1/ws?token=<token>\n", b.listenAddr)
		}
	} else {
		fmt.Fprintln(w, "  mode:    attach-only (read-only JSONL)")
	}
	if !b.auth {
		fmt.Fprintln(w, "  auth:    off (loopback only)")
	} else if b.minted {
		fmt.Fprintf(w, "  token:   %s  (auto-minted; pass --token to set)\n", b.token)
	} else {
		fmt.Fprintln(w, "  token:   (from --token)")
	}
	if len(b.allowCIDR) > 0 {
		fmt.Fprintf(w, "  allow:   %s\n", strings.Join(b.allowCIDR, ", "))
	}
	fmt.Fprintf(w, "  sessions dir: %s\n", b.sessionDir)
	fmt.Fprintln(w, "experimental web cockpit — TUI remains primary")
}

func cockpitURL(host, port, token string) string {
	u := url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort(host, port),
		Path:     "/attach",
		RawQuery: "token=" + url.QueryEscape(token),
	}
	return u.String()
}
