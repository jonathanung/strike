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
	"sync"
	"syscall"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/frontend/server"
	"github.com/jonathanung/strike/internal/persist/attachment"
	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/persist/taskrun"
	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
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
  --terminal-remote      allow the side-panel shell on a non-loopback bind
                         (requires --auth; loopback serves a shell without it)
  --auto                 skip permission asks; network.allow still applies
  --dangerously-skip-permissions
                         skip permission asks and bypass network.allow
  -h, --help             show help

Endpoints:
  GET  /health                      JSON ok + version (no auth)
  GET  /  and  GET /attach          cockpit page
  GET  /v1/ws                       WebSocket: ops in, events out (live)
  GET  /v1/terminals               side-panel PTY shells (workspace cwd)
  GET  /v1/terminals/{id}/stream   terminal WebSocket (cookie, bearer, or one-time ticket)
  POST /v1/ops                      submit one op envelope (live)
  GET  /v1/sidechat                 list read-only side-chat turns
  POST /v1/sidechat/ask             ask about the session (no tools, no user.input)
  POST /v1/sidechat/ops             refused; cannot approve tools or post user.input
  GET  /v1/live/events              SSE of live engine events
  GET  /v1/status                   live status chrome
  GET  /v1/agents                   selectable agents
  GET  /v1/sessions                 session list (+ liveId)
  GET  /v1/sessions/{id}/events     SSE tail of a session JSONL log
  GET  /v1/workflows                workflow catalog
  GET  /v1/schedules                persisted schedules
  POST /v1/schedules                create a cron, interval, one-shot, or heartbeat
  POST /v1/schedules/{id}/run       start a root session now
  POST /v1/workflows/save           save document (never activates)
  POST /v1/workflows/{name}/start   start after confirm (rejects invalid)
  POST /v1/workflows/stop           stop active workflow
  POST /v1/workflow-drafts/review   draft review (checks + widening)
  POST /v1/workflow-drafts/save     save draft JSON with confirm

Auth: loopback is unauthenticated by default. Under --auth, use
Authorization: Bearer <token> or the strike_serve_token cookie. A one-time
pairing code (strike gateway url --qr, or the QR printed at startup) is how
a phone authenticates. The daemon token is not put in that URL. The legacy
/?token= handoff still sets the cookie and redirects; query tokens are not
accepted on /v1/* routes.

Bind is loopback unless you pass a private address with --allow-private-bind
and --auth. 0.0.0.0 is rejected. For remote access:
  strike connect user@host
  ssh -L 8787:127.0.0.1:8787 user@host
  strike serve --advertise-url https://your-tunnel.example
See docs/gateway.md.`

// errExposeRemoved is returned when legacy --expose / --allow-cidr are passed.
const errExposeRemoved = "--expose was removed (no cleartext LAN bind). Keep loopback and forward with: ssh -L 8787:127.0.0.1:8787 user@host (see docs/web.md)"

type serveOptions struct {
	addr                       string
	auth                       bool
	token                      string
	tokenFile                  string
	advertiseURL               string
	allowPrivateBind           bool
	sessionDir                 string
	sessionDirSet              bool
	provider                   string
	model                      string
	attachOnly                 bool
	readOnly                   bool
	auto                       bool
	dangerouslySkipPermissions bool
	terminalRemote             bool
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
	fs.StringVar(&opts.tokenFile, "token-file", "", "")
	fs.StringVar(&opts.advertiseURL, "advertise-url", "", "")
	fs.BoolVar(&opts.allowPrivateBind, "allow-private-bind", false, "")
	fs.StringVar(&opts.sessionDir, "session-dir", "", "")
	fs.StringVar(&opts.provider, "provider", "echo", "")
	fs.StringVar(&opts.model, "model", "", "")
	fs.BoolVar(&opts.attachOnly, "attach-only", false, "")
	fs.BoolVar(&opts.readOnly, "read-only", false, "")
	fs.BoolVar(&opts.auto, "auto", false, "")
	fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
	fs.BoolVar(&opts.terminalRemote, "terminal-remote", false, "")
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
	opts.tokenFile = strings.TrimSpace(opts.tokenFile)
	opts.advertiseURL = strings.TrimSpace(opts.advertiseURL)
	if opts.advertiseURL != "" {
		if _, err := server.ValidateAdvertiseURL(opts.advertiseURL); err != nil {
			return serveOptions{}, err
		}
	}
	if opts.token != "" && !opts.auth {
		return serveOptions{}, fmt.Errorf("--token requires --auth")
	}
	if opts.tokenFile != "" && !opts.auth {
		return serveOptions{}, fmt.Errorf("--token-file requires --auth")
	}
	if opts.token != "" && opts.tokenFile != "" {
		return serveOptions{}, fmt.Errorf("--token and --token-file are mutually exclusive")
	}
	if opts.allowPrivateBind && !opts.auth {
		return serveOptions{}, fmt.Errorf("--allow-private-bind requires --auth")
	}
	if opts.terminalRemote && !opts.auth {
		return serveOptions{}, fmt.Errorf("--terminal-remote requires --auth")
	}
	if opts.sessionDirSet && !opts.attachOnly {
		return serveOptions{}, fmt.Errorf("--session-dir requires --attach-only; live sessions use %s", session.DefaultDir())
	}
	return opts, nil
}

func runServe(opts serveOptions, stdout, stderr io.Writer) error {
	bindAddr, err := server.ResolveDaemonBindAddr(opts.addr, opts.allowPrivateBind)
	if err != nil {
		return err
	}
	opts.addr = bindAddr

	token := opts.token
	minted := false
	if opts.auth && token == "" && opts.tokenFile != "" {
		t, created, err := loadOrMintTokenFile(opts.tokenFile)
		if err != nil {
			return err
		}
		token, minted = t, created
	}
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

	usageDir := usageaudit.DefaultDir()
	if usageLog, err := usageaudit.Open(usageDir); err != nil {
		fmt.Fprintf(stderr, "strike: usage audit log unavailable: %v\n", err)
		usageDir = ""
	} else {
		defer usageLog.Close()
	}

	var liveHub *server.LiveHub
	var services *host.Services
	var sandboxSnap *server.SandboxSnapshot
	var sideChat server.SideChatModel
	var taskRuns *taskrun.Store
	var terminalSandbox sandbox.Policy
	var terminalSandboxFor func(sessionID, workDir string) (sandbox.Policy, bool)
	terminalSandboxLoaded := false
	var cleanup func() error
	var sharedAttachments *attachment.Store
	sessionDir := opts.sessionDir

	wsStore, err := workspace.Open("")
	if err != nil {
		return err
	}
	var browse []string
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		browse = append(browse, home)
	}
	if cwd, cerr := os.Getwd(); cerr == nil && cwd != "" {
		browse = append(browse, cwd)
	}
	if len(browse) > 0 {
		wsStore.ConfigureBrowse(browse)
	}

	if !opts.attachOnly {
		cliOpts := cliOptions{
			provider:                   opts.provider,
			providerSet:                true,
			model:                      opts.model,
			auto:                       opts.auto,
			dangerouslySkipPermissions: opts.dangerouslySkipPermissions,
			workspaces:                 wsStore,
		}
		a, err := assemble(cliOpts, true)
		if err != nil {
			return err
		}
		sharedAttachments = a.attachments
		sessionDir = a.sessions.Dir()
		sideChat = newSideChatModel(a.sideProvider, a.sideModel, a.sideSelect)

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
		engines := make(map[string]*engine.Engine)
		noteEngine := func(id string, eng *engine.Engine) {
			if id == "" || eng == nil {
				return
			}
			mu.Lock()
			engines[id] = eng
			mu.Unlock()
		}

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
		registerLive := func(ctx context.Context, slot *rootSlot) (string, error) {
			live := server.NewLive(slot.id, slot.workDir, agents, slot.eng.Ops())
			noteEngine(slot.id, slot.eng)
			a.attachMemoryMode(live, slot.id, slot.eng)
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
		}
		liveHub = server.NewLiveHub(
			// The hub passes the HTTP request context. The engine has to live
			// as long as the daemon, same as the initial root below.
			func(context.Context) (string, error) {
				slot, err := a.spawnRoot("")
				if err != nil {
					return "", err
				}
				return registerLive(ctx, slot)
			},
			func(_ context.Context, sessionID string) (string, bool, error) {
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
				a.attachMemoryMode(live, slot.id, slot.eng)
				seedLiveSandbox(live)
				sr := startServeRoot(ctx, slot, live)
				noteEngine(slot.id, slot.eng)
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
		liveHub.SetUnattendedSpawn(func(ctx context.Context) (string, error) {
			slot, err := a.spawnUnattended("")
			if err != nil {
				return "", err
			}
			return registerLive(ctx, slot)
		})

		// Add initial root from the first assembly slot.
		initialSlot := a.firstSlot
		initialLive := server.NewLive(initialSlot.id, initialSlot.workDir, agents, initialSlot.eng.Ops())
		a.attachMemoryMode(initialLive, initialSlot.id, initialSlot.eng)
		seedLiveSandbox(initialLive)
		noteEngine(initialSlot.id, initialSlot.eng)
		liveHub.Add(initialSlot.id, initialLive)
		if info, err := a.sessions.Get(initialSlot.id); err == nil && info.Title != "" {
			liveHub.SetTitle(initialSlot.id, info.Title)
		}
		initialSR := startServeRoot(ctx, initialSlot, initialLive)
		roots[initialSlot.id] = initialSR
		// Closing a root (DELETE /v1/roots) drops it from the hub; the hub
		// does not own the root's engine or session, so stop both here.
		// Without this, a root whose engine runs on the daemon lifetime
		// (the task-runner fix above) stays open in the session manager
		// after it is closed, and deleting its saved session then demands
		// force for a session that is not open anywhere.
		liveHub.SetOnRemove(func(id string) {
			mu.Lock()
			sr := roots[id]
			delete(roots, id)
			mu.Unlock()
			if sr != nil && sr.cancel != nil {
				sr.cancel()
			}
			_ = a.sessions.Close(id)
		})

		services = &a.services
		taskRuns = a.taskRuns
		terminalSandbox = a.sandboxPolicy
		terminalSandboxLoaded = true
		terminalSandboxFor = func(sessionID, workDir string) (sandbox.Policy, bool) {
			id := strings.TrimSpace(sessionID)
			if id == "" && liveHub != nil {
				id = liveHub.ActiveID()
			}
			mu.Lock()
			eng := engines[id]
			mu.Unlock()
			if eng == nil {
				return sandbox.Policy{}, false
			}
			return eng.BashSandboxPolicy(workDir), true
		}

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

		writePermissionsModeWarning(stderr, opts.auto, opts.dangerouslySkipPermissions)
	}

	auditSink, err := audit.Open(audit.Options{})
	if err != nil {
		return fmt.Errorf("open audit sink: %w", err)
	}
	defer func() { _ = auditSink.Close() }()

	if sharedAttachments == nil {
		st, err := attachment.Open(config.GlobalRoot())
		if err != nil {
			return fmt.Errorf("open attachments: %w", err)
		}
		defer func() { _ = st.Close() }()
		sharedAttachments = st
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	schedules, err := openScheduleService(workspace.DefaultDir(), liveHub, services, wsStore, cwd)
	if err != nil {
		return err
	}
	if liveHub != nil && !opts.readOnly {
		go schedules.Run(ctx)
	}

	srv, err := server.New(server.Options{
		Addr:         opts.addr,
		Auth:         opts.auth,
		Token:        token,
		TokenFile:    opts.tokenFile,
		AdvertiseURL: opts.advertiseURL,
		SessionDir:   sessionDir,
		Workspaces:   wsStore,
		Schedules:    schedules,
		LiveHub:      liveHub,
		Services:     services,
		SyncWorkDir: func(dir string) {
			if services != nil {
				local.SetFilesWorkDir(services.Files, dir)
			}
		},
		Sandbox:               sandboxSnap,
		ReadOnly:              opts.readOnly,
		Audit:                 auditSink,
		Attachments:           sharedAttachments,
		UsageAuditDir:         usageDir,
		SideChat:              sideChat,
		TaskRuns:              taskRuns,
		TerminalRemote:        opts.terminalRemote,
		TerminalSandbox:       terminalSandbox,
		TerminalSandboxLoaded: terminalSandboxLoaded,
		TerminalSandboxFor:    terminalSandboxFor,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.addr, err)
	}

	actual := ln.Addr().String()
	srv.NoteBind(actual)
	pairURL, pairQR, pairNote := startupPair(srv, opts.auth)

	printServeBanner(stdout, serveBanner{
		listenAddr: actual,
		token:      token,
		auth:       opts.auth,
		minted:     minted,
		tokenFile:  opts.tokenFile != "",
		liveHub:    liveHub,
		provider:   opts.provider,
		sessionDir: sessionDir,
		readOnly:   opts.readOnly,
		sideChat:   sideChat != nil,
		pairURL:    pairURL,
		pairQR:     pairQR,
		pairNote:   pairNote,
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
	tokenFile  bool
	liveHub    *server.LiveHub
	provider   string
	sessionDir string
	readOnly   bool
	sideChat   bool
	pairURL    string
	pairQR     string
	pairNote   string
}

func startupPair(srv *server.Server, auth bool) (url, blocks, note string) {
	info := srv.GatewayInfo()
	if auth {
		minted, err := srv.MintPair("serve")
		if err != nil {
			return "", "", info.OptIn
		}
		info = minted.Gateway
		url = minted.URL
	} else if len(info.URLs) > 0 {
		url = info.URLs[0].URL
	}
	if url != "" {
		blocks = server.QRBlocks(url)
	}
	var lines []string
	if info.OptIn != "" {
		lines = append(lines, info.OptIn)
	}
	if info.LanWarning != "" {
		lines = append(lines, info.LanWarning)
	}
	if info.AdvertiseWarning != "" {
		lines = append(lines, info.AdvertiseWarning)
	}
	return url, blocks, strings.Join(lines, "\n")
}

func printServeBanner(w io.Writer, b serveBanner) {
	fmt.Fprintf(w, "strike serve listening on http://%s\n", b.listenAddr)
	fmt.Fprintf(w, "  health:  http://%s/health\n", b.listenAddr)
	// The cockpit URL never carries the bearer. Paste it into the Gateways
	// field, or use strike connect, which injects a grant on loopback.
	fmt.Fprintf(w, "  cockpit: http://%s/attach\n", b.listenAddr)
	if b.pairURL != "" && (b.token == "" || !strings.Contains(b.pairURL, b.token)) && !strings.Contains(b.pairURL, "?token=") {
		fmt.Fprintf(w, "  pair:    %s\n", b.pairURL)
		if b.pairQR != "" {
			fmt.Fprintln(w, b.pairQR)
		}
	} else if b.pairURL != "" {
		fmt.Fprintln(w, "  pair:    refused (url contained the daemon token)")
	}
	if b.pairNote != "" {
		fmt.Fprintf(w, "  phone:   %s\n", strings.ReplaceAll(b.pairNote, "\n", "\n           "))
	}
	if b.auth {
		fmt.Fprintln(w, "  auth:    bearer or cookie; the token is not part of the URL")
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
	if b.sideChat {
		fmt.Fprintln(w, "  side chat: read-only (POST /v1/sidechat/ask; cannot approve tools)")
	}
	if !b.auth {
		fmt.Fprintln(w, "  auth:    off (loopback only)")
	} else if b.tokenFile {
		fmt.Fprintln(w, "  token:   (from --token-file; not printed. strike daemon token prints the file)")
	} else if b.minted {
		// In-memory mint has no other copy. This line is the operator's tty,
		// not an access log, and it is not a URL.
		fmt.Fprintf(w, "  token:   %s  (auto-minted in memory; pass --token-file to keep it on disk)\n", b.token)
	} else {
		fmt.Fprintln(w, "  token:   (from --token; not printed)")
	}
	fmt.Fprintf(w, "  sessions dir: %s\n", b.sessionDir)
	fmt.Fprintln(w, "  audit:   serve ops → ~/.strike/audit (family serve_op)")
	fmt.Fprintln(w, "  schedules: ~/.strike/schedules.json  (unattended runs use workspace mode, else plan)")
	fmt.Fprintln(w, "  remote:  strike connect user@host   or   ssh -L 8787:127.0.0.1:8787 user@host")
	fmt.Fprintln(w, "experimental web cockpit — TUI remains primary")
}
