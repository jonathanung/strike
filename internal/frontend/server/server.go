// Package server is the experimental HTTP/WebSocket attach surface for strike
// sessions: health/version, SSE JSONL tails, live ops bridge, and cockpit page.
package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/internal/daemon/trigger"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/server/terminal"
	"github.com/jonathanung/strike/internal/persist/attachment"
	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/persist/taskrun"
	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/product/version"
	"github.com/jonathanung/strike/internal/schedule"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
)

// Options configures the attach server.
type Options struct {
	// Addr is the bind address (host:port). Default "127.0.0.1:8787".
	Addr string
	// Auth enables bearer authentication for API routes.
	Auth bool
	// Token is required when Auth is true. It is the bearer until TokenFile
	// is set; a token file is re-read on every /v1 check so a rotated file
	// (strike daemon logout) replaces it without restarting the process.
	Token string
	// TokenFile, when set, is the source of truth for the bearer. Missing
	// or empty contents deny /v1. The process still needs Auth and an initial
	// Token so a server cannot start authenticated with no credential.
	TokenFile string
	// AdvertiseURL is an operator-supplied tunnel origin (http or https, no
	// query or fragment). Pairing QR codes prefer it over the bind address.
	// Strike does not open that tunnel. Empty keeps the bind URL.
	AdvertiseURL string
	// PairTTL is the pairing-code lifetime. Zero uses 5 minutes.
	PairTTL time.Duration
	// PairClock overrides time for pairing expiry and rate limits. Nil is time.Now.
	PairClock func() time.Time
	// PairRedeemLimit is redeemed attempts per client IP per minute. Zero uses 8.
	PairRedeemLimit int
	// PairRedeemGlobal is redeemed attempts across all IPs per minute. Zero uses 64.
	PairRedeemGlobal int
	// SessionDir is the JSONL sessions directory. Default session.DefaultDir().
	SessionDir string
	// Static is optional override for the attach page FS (tests). Nil uses embed.
	Static fs.FS
	// PollInterval is how often the SSE tail checks for new JSONL bytes.
	// Default 200ms. Tests may lower it.
	PollInterval time.Duration
	// Live is an optional engine bridge for composer/ops/status. Nil keeps
	// read-only attach (JSONL SSE only). When LiveHub is set, it takes
	// precedence and Live is ignored.
	Live *Live
	// LiveHub is the multi-root active-agent bridge. When set, ops, status,
	// events, and WebSocket are scoped to ?root=<id>. Nil means single-root
	// via Live (or attach-only when both are nil).
	LiveHub *LiveHub
	// AllowCIDRs, when non-empty, rejects requests whose client IP falls
	// outside the list (all routes including /health). Unused by strike serve
	// CLI (loopback-only bind); retained for programmatic hosts.
	AllowCIDRs []*net.IPNet
	// Workspaces is the durable workspace registry for /v1/workspaces.
	// Nil returns 503 from those routes.
	Workspaces *workspace.Store
	// SyncWorkDir points host file listings at the active root's directory.
	// Nil leaves the files service on the directory it was built with.
	SyncWorkDir func(dir string)
	// Schedules is the persisted clock (/v1/schedules). Nil returns 501.
	Schedules *schedule.Service
	// Services exposes optional frontend host capabilities.
	Services *host.Services
	// TaskRuns is the durable plan-run board (GET/POST /v1/plans/{id}/run).
	// Nil makes those routes report the taskRuns capability unavailable.
	TaskRuns *taskrun.Store
	// TaskSpawn, when set, replaces team.spawn while starting a plan run.
	// Production leaves it nil and spawns through the live engine.
	TaskSpawn func(ctx context.Context, ownerRoot, name, objective string) (childID string, err error)
	// Sandbox enables the sandbox capability and seeds live roots that do not
	// call Live.SetSandbox themselves. Nil keeps capabilities.sandbox false.
	Sandbox *SandboxSnapshot
	// ReadOnly rejects mutating protocol ops (POST /v1/ops and WS op frames).
	ReadOnly bool
	// Audit receives serve_op records (op type + source IP). Nil disables.
	Audit OpAuditor
	// UsageAuditDir is the hash-chained usage audit log. Empty leaves the
	// chain unconfigured; GET /v1/usage still reads SessionDir.
	UsageAuditDir string
	// UsagePricer overrides models.dev pricing for /v1/usage. Nil loads the catalog.
	UsagePricer func(provider, model string) (usageaudit.Rates, bool)
	// Triggers is the inbound trigger registry (webhooks, GitHub, channels).
	// Nil makes /v1/triggers and POST /hooks 503. The launcher inside the
	// service is NewRootLauncher (authored for #1282).
	Triggers *trigger.Service
	// Assembled is true when this process ran assemble (config load, project
	// stores, auth.json, attachments). Attach-only serve leaves it false.
	Assembled bool
	// OpsRate is tokens/sec per client IP for ops (default 10). <=0 uses default.
	OpsRate float64
	// OpsBurst is the per-IP burst (default 20). <=0 uses default.
	OpsBurst int
	// OpsClock overrides time.Now for rate-limit tests.
	OpsClock func() time.Time
	// Attachments stores composer uploads and replaces inline bytes on
	// user.input and steer with content-addressed refs. Nil leaves text-only
	// ops alone and rejects ops that carry images.
	Attachments *attachment.Store
	// SideChat answers questions about a session transcript. It must not
	// submit protocol ops. Nil makes POST /v1/sidechat/ask return 503.
	// POST /v1/sidechat/ops still refuses every op, including permission.reply.
	SideChat SideChatModel
	// TerminalRemote allows the terminal websocket on a non-loopback bind.
	// It requires Auth. Loopback serves a terminal without this flag.
	TerminalRemote bool
	// TerminalSandbox is the startup OS sandbox dial for interactive shells.
	// A live serve replaces it at create time with TerminalSandboxFor so plan
	// and phase rules are current and deny paths expand under the workspace.
	// Zero value is off. That is a real shell only when TerminalSandboxLoaded
	// is set; attach-only leaves the flag false and the terminal routes refuse
	// a shell.
	TerminalSandbox sandbox.Policy
	// TerminalSandboxLoaded reports that TerminalSandbox came from a live
	// serve, including an explicit sandbox off. Attach-only leaves it false.
	TerminalSandboxLoaded bool
	// TerminalSandboxFor compiles the policy for one new shell. sessionID
	// empty means the active root. The bool is false when no live engine can
	// compile it. Nil means no compiler was installed.
	TerminalSandboxFor func(sessionID, workDir string) (sandbox.Policy, bool)
	// WorkDir is the git workspace Apps reads (GitHub triage). The browser
	// cannot choose it. Empty uses the process working directory.
	WorkDir string
}

// Server is an HTTP server for session attach and optional live cockpit.
type Server struct {
	opts   Options
	mux    *http.ServeMux
	http   *http.Server
	static fs.FS

	authFlowOnce   sync.Once
	authFlowsStore *authFlowStore

	// paneHost supervises process panes for the web cockpit (#732).
	paneHost *paneHost
	opsLimit *opLimiter
	// terms is the side-panel PTY manager.
	terms *terminal.Manager

	seenOnce sync.Once
	seen     *SeenStore

	// scriptCSPHashes allowlists inline <script> bodies from the embedded
	// index.html (Trunk emits an inline WASM loader) without unsafe-inline.
	scriptCSPHashes []string

	// gitMu serializes git reads and mutations for this daemon. Overlapping
	// status and reset calls on one repo corrupt the index.
	gitMu sync.Mutex

	// grants are short-lived bearers kept in memory only. grantNow overrides
	// the clock in tests. rootSum is the last host-token hash; a change
	// (strike daemon logout, or POST /v1/gateway/rotate) drops every grant.
	grantMu  sync.Mutex
	grants   []storedGrant
	grantNow func() time.Time
	rootSum  [32]byte
	rootSeen bool

	// notifyOnce lazily opens the redacted notification log under SessionDir.
	notifyOnce sync.Once
	notifyLog  *notifyLog

	// tokenHash is the sha256 of the daemon token file last observed.
	// A change appends a token-rotation audit entry. The token is not stored.
	tokenMu   sync.Mutex
	tokenHash string

	// sideChat stores read-only Q&A beside the session log, not in it.
	sideChat sideChatStore

	// pairs holds single-use phone pairing codes and revocable session tokens.
	pairs *pairStore

	// shutdownCtx is cancelled the moment Shutdown begins. Long-lived
	// streaming handlers (SSE tails, websockets, terminal streams) select
	// on it: http.Server.Shutdown does not cancel a request's context
	// while it waits for the connection to go idle, so without this a
	// single open cockpit stream made Shutdown block until its timeout
	// — and `strike serve` appear to hang on Ctrl-C (#1678).
	shutdownOnce   sync.Once
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

// New validates options and builds a Server. Does not listen.
func New(opts Options) (*Server, error) {
	opts.Token = strings.TrimSpace(opts.Token)
	adv, err := ValidateAdvertiseURL(opts.AdvertiseURL)
	if err != nil {
		return nil, err
	}
	opts.AdvertiseURL = adv
	if opts.Auth && opts.Token == "" {
		return nil, errors.New("server: token is required when auth is enabled")
	}
	if !opts.Auth && opts.Token != "" {
		return nil, errors.New("server: token requires auth")
	}
	if opts.TerminalRemote && !opts.Auth {
		return nil, errors.New("server: terminal on a non-loopback listener requires auth")
	}
	if strings.TrimSpace(opts.Addr) == "" {
		opts.Addr = "127.0.0.1:8787"
	}
	if strings.TrimSpace(opts.SessionDir) == "" {
		opts.SessionDir = session.DefaultDir()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	static := opts.Static
	if static == nil {
		static = staticFS
	}
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	s := &Server{
		opts:            opts,
		static:          static,
		mux:             http.NewServeMux(),
		opsLimit:        newOpLimiter(opts.OpsRate, opts.OpsBurst, opts.OpsClock),
		scriptCSPHashes: inlineScriptHashes(static),
		terms:           terminal.NewManager(terminal.ManagerConfig{Sandbox: opts.TerminalSandbox}),
		pairs:           newPairStore(opts),
		shutdownCtx:     shutdownCtx,
		shutdownCancel:  shutdownCancel,
	}
	s.terms.SetAgentProbe(func(sessionID string) terminal.AgentView {
		live := s.liveForTerminal(sessionID)
		if live == nil {
			return terminal.AgentView{}
		}
		st := live.Status()
		return terminal.AgentView{CWD: st.CWD, Busy: st.Busy, PermissionMode: st.PermissionMode}
	})
	s.pairs.observe(s.currentToken())
	s.routes()
	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.withMiddleware(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Addr returns the configured bind address.
func (s *Server) Addr() string { return s.opts.Addr }

// Token returns the configured auth token.
func (s *Server) Token() string { return s.opts.Token }

// Handler returns the root HTTP handler (for httptest).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// ListenAndServe binds and serves until the server is closed.
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Serve serves on ln until closed.
func (s *Server) Serve(ln net.Listener) error {
	s.http.Addr = ln.Addr().String()
	return s.http.Serve(ln)
}

// streamDone is closed when Shutdown begins. Streaming handlers select
// on it alongside the request context so they return promptly instead
// of pinning http.Server.Shutdown open. A nil channel (a Server built
// without New) never fires, matching the old behaviour.
func (s *Server) streamDone() <-chan struct{} {
	if s == nil || s.shutdownCtx == nil {
		return nil
	}
	return s.shutdownCtx.Done()
}

// beginShutdown signals every streaming handler to finish.
func (s *Server) beginShutdown() {
	if s == nil {
		return
	}
	s.shutdownOnce.Do(func() {
		if s.shutdownCancel != nil {
			s.shutdownCancel()
		}
	})
}

// Shutdown gracefully stops the server. Streaming handlers are signalled
// first so they release their connections; if the graceful wait still
// expires, the remaining connections are force-closed so Shutdown — and
// the process calling it, e.g. `strike serve` on Ctrl-C — cannot hang
// on a stream that ignored the signal (#1678).
func (s *Server) Shutdown(ctx context.Context) error {
	s.beginShutdown()
	if s.terms != nil {
		s.terms.Close()
	}
	if err := s.http.Shutdown(ctx); err != nil {
		_ = s.http.Close()
		return err
	}
	return nil
}

// MintToken returns a random 32-byte hex token suitable for --token.
func MintToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// IsLocalhostBind reports whether addr targets loopback only. Empty host
// (e.g. ":8787") binds all interfaces and is not localhost.
func IsLocalhostBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/gateway", s.handleGatewayInfo)
	s.mux.HandleFunc("POST /v1/gateway/grants", s.handleGatewayMint)
	s.mux.HandleFunc("DELETE /v1/gateway/grants", s.handleGatewayRevoke)
	s.mux.HandleFunc("POST /v1/gateway/rotate", s.handleGatewayRotate)
	s.mux.HandleFunc("POST /v1/gateway/logout", s.handleGatewayLogout)
	// Phone pairing (#1289). Redeem is unauthenticated; the code is the secret.
	s.mux.HandleFunc("POST /v1/pair", s.handlePairMint)
	s.mux.HandleFunc("POST /v1/pair/redeem", s.handlePairRedeem)
	s.mux.HandleFunc("GET /v1/pair/devices", s.handlePairDevices)
	s.mux.HandleFunc("DELETE /v1/pair/devices/{id}", s.handlePairRevoke)
	s.mux.HandleFunc("POST /v1/pair/revoke-all", s.handlePairRevokeAll)
	s.mux.HandleFunc("POST /v1/pair/revoke-self", s.handlePairRevokeSelf)
	s.mux.HandleFunc("GET /v1/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("GET /v1/team", s.handleTeam)
	s.mux.HandleFunc("GET /v1/artifacts", s.handleArtifactsList)
	s.mux.HandleFunc("GET /v1/artifacts/{id}", s.handleArtifactGet)
	s.mux.HandleFunc("GET /v1/ledger", s.handleLedgerList)
	s.mux.HandleFunc("GET /v1/ledger/{id}", s.handleLedgerGet)
	s.mux.HandleFunc("GET /v1/providers", s.handleProviders)
	s.mux.HandleFunc("POST /v1/auth/key", s.handleAuthKey)
	s.mux.HandleFunc("DELETE /v1/auth/{provider}", s.handleAuthLogout)
	s.mux.HandleFunc("POST /v1/auth/device", s.handleAuthDeviceStart)
	s.mux.HandleFunc("GET /v1/auth/device/{id}", s.handleAuthDeviceStatus)
	s.mux.HandleFunc("DELETE /v1/auth/device/{id}", s.handleAuthDeviceCancel)
	s.mux.HandleFunc("POST /v1/auth/oauth", s.handleAuthOAuthStart)
	s.mux.HandleFunc("GET /v1/auth/oauth/{id}", s.handleAuthOAuthStatus)
	s.mux.HandleFunc("POST /v1/auth/oauth/{id}/complete", s.handleAuthOAuthComplete)
	s.mux.HandleFunc("DELETE /v1/auth/oauth/{id}", s.handleAuthOAuthCancel)
	s.mux.HandleFunc("GET /v1/custom-providers", s.handleCustomProviders)
	s.mux.HandleFunc("POST /v1/custom-providers", s.handleCustomProviders)
	s.mux.HandleFunc("DELETE /v1/custom-providers/{name}", s.handleCustomProviderDelete)
	s.mux.HandleFunc("GET /v1/scheduler/presets", s.handleSchedulerPresets)
	s.mux.HandleFunc("POST /v1/scheduler/presets", s.handleSchedulerPresetsApply)
	s.mux.HandleFunc("GET /v1/config-sources", s.handleConfigSources)
	s.mux.HandleFunc("GET /v1/local-files", s.handleLocalFiles)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /v1/history", s.handleHistory)
	s.mux.HandleFunc("POST /v1/history", s.handleHistory)
	s.mux.HandleFunc("GET /v1/settings", s.handleSettings)
	s.mux.HandleFunc("GET /v1/themes", s.handleThemesList)
	s.mux.HandleFunc("GET /v1/themes/{id}", s.handleThemeGet)
	s.mux.HandleFunc("PATCH /v1/settings", s.handleSettings)
	s.mux.HandleFunc("GET /v1/sandbox", s.handleSandboxGet)
	s.mux.HandleFunc("PATCH /v1/sandbox", s.handleSandboxPatch)
	s.mux.HandleFunc("GET /v1/sessions/{id}/children", s.handleSessionChildren)
	s.mux.HandleFunc("GET /v1/session-lifecycle", s.handleSessionLifecycleCapabilities)
	s.mux.HandleFunc("POST /v1/sessions/{id}/fork", s.handleSessionFork)
	s.mux.HandleFunc("POST /v1/sessions/{id}/fork_at", s.handleSessionForkAt)
	s.mux.HandleFunc("GET /v1/sessions/{id}/rewind_points", s.handleSessionRewindPoints)
	s.mux.HandleFunc("PATCH /v1/sessions/{id}", s.handleSessionRename)
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleSessionDelete)
	// Session list organisation (pins, archive, folders, search). Not filesystem dirs.
	s.mux.HandleFunc("GET /v1/session-list/search", s.handleSessionListSearch)
	s.mux.HandleFunc("POST /v1/session-list/bulk", s.handleSessionListBulk)
	s.mux.HandleFunc("POST /v1/session-list/folders", s.handleSessionListFolderCreate)
	s.mux.HandleFunc("PATCH /v1/session-list/folders/{id}", s.handleSessionListFolderRename)
	s.mux.HandleFunc("DELETE /v1/session-list/folders/{id}", s.handleSessionListFolderDelete)
	s.mux.HandleFunc("PATCH /v1/session-list/sessions/{id}", s.handleSessionListSessionPatch)
	s.mux.HandleFunc("GET /v1/session-list", s.handleSessionList)
	s.mux.HandleFunc("GET /v1/files", s.handleFiles)
	s.mux.HandleFunc("GET /v1/files/search", s.handleFilesSearch)
	s.mux.HandleFunc("GET /v1/changed-files", s.handleChangedFiles)
	s.mux.HandleFunc("GET /v1/git/status", s.handleGitStatus)
	s.mux.HandleFunc("GET /v1/git/diff", s.handleGitDiff)
	s.mux.HandleFunc("GET /v1/git/branches", s.handleGitBranches)
	s.mux.HandleFunc("GET /v1/git/log", s.handleGitLog)
	s.mux.HandleFunc("POST /v1/git/stage", s.handleGitStage)
	s.mux.HandleFunc("POST /v1/git/unstage", s.handleGitUnstage)
	s.mux.HandleFunc("POST /v1/git/commit", s.handleGitCommit)
	s.mux.HandleFunc("POST /v1/git/checkout", s.handleGitCheckout)
	s.mux.HandleFunc("POST /v1/git/discard", s.handleGitDiscard)
	s.mux.HandleFunc("POST /v1/git/reset", s.handleGitReset)
	s.mux.HandleFunc("POST /v1/git/branch/delete", s.handleGitBranchDelete)
	s.mux.HandleFunc("GET /v1/git/worktrees", s.handleGitWorktrees)
	s.mux.HandleFunc("POST /v1/git/worktrees", s.handleGitWorktreeCreate)
	s.mux.HandleFunc("POST /v1/git/worktrees/remove", s.handleGitWorktreeRemove)
	s.mux.HandleFunc("POST /v1/git/worktrees/prune", s.handleGitWorktreePrune)
	s.mux.HandleFunc("GET /v1/file", s.handleFile)
	s.mux.HandleFunc("GET /v1/file/view", s.handleFileView)
	s.mux.HandleFunc("PUT /v1/file", s.handleFilePut)
	// Reviewed file mutations (WEBUI.9 / #1084) — typed apply only, no free-form write.
	s.mux.HandleFunc("POST /v1/files/apply-edit", s.handleFilesApplyEdit)
	s.mux.HandleFunc("POST /v1/files/apply-patch", s.handleFilesApplyPatch)
	s.mux.HandleFunc("GET /v1/memory", s.handleMemory)
	s.mux.HandleFunc("GET /v1/memory/export", s.handleMemoryExport)
	s.mux.HandleFunc("POST /v1/memory/import", s.handleMemoryImport)
	s.mux.HandleFunc("GET /v1/memory/mode", s.handleMemoryModeGet)
	s.mux.HandleFunc("PUT /v1/memory/mode", s.handleMemoryModePut)
	s.mux.HandleFunc("PUT /v1/memory/{key}", s.handleMemoryPut)
	s.mux.HandleFunc("DELETE /v1/memory/{key}", s.handleMemoryDelete)
	s.mux.HandleFunc("GET /v1/knowledge", s.handleKnowledgeList)
	s.mux.HandleFunc("PUT /v1/knowledge", s.handleKnowledgePut)
	s.mux.HandleFunc("DELETE /v1/knowledge", s.handleKnowledgeDelete)
	s.mux.HandleFunc("POST /v1/issues", s.handleIssueCreate)
	s.mux.HandleFunc("GET /v1/issues/export", s.handleIssuesExport)
	s.mux.HandleFunc("POST /v1/issues/import", s.handleIssuesImport)
	s.mux.HandleFunc("POST /v1/issues/{id}/close", s.handleIssueClose)
	s.mux.HandleFunc("GET /v1/issues", s.handleIssues)
	s.mux.HandleFunc("GET /v1/permissions/explain", s.handlePermissionExplain)
	s.mux.HandleFunc("GET /v1/permissions/diff", s.handlePermissionDiff)
	s.mux.HandleFunc("GET /v1/permissions/presets", s.handlePermissionPresets)
	s.mux.HandleFunc("GET /v1/permissions/rules", s.handlePermissionRules)
	s.mux.HandleFunc("POST /v1/permissions/rules", s.handlePermissionRules)
	s.mux.HandleFunc("POST /v1/permissions/rules/remove", s.handlePermissionRuleRemove)
	s.mux.HandleFunc("GET /v1/permissions/audit", s.handlePermissionAudit)
	s.mux.HandleFunc("GET /v1/plans", s.handlePlansList)
	s.mux.HandleFunc("POST /v1/plans", s.handlePlanCreate)
	s.mux.HandleFunc("GET /v1/plans/{id}", s.handlePlanGet)
	s.mux.HandleFunc("PATCH /v1/plans/{id}", s.handlePlanUpdateTitle)
	s.mux.HandleFunc("POST /v1/plans/{id}/sections", s.handlePlanAddSection)
	s.mux.HandleFunc("PATCH /v1/plans/{id}/sections/{sectionID}", s.handlePlanUpdateSection)
	s.mux.HandleFunc("POST /v1/plans/{id}/status", s.handlePlanSetStatus)
	s.mux.HandleFunc("POST /v1/plans/{id}/reopen", s.handlePlanReopen)
	s.mux.HandleFunc("GET /v1/plans/{id}/run", s.handleTaskRunGet)
	s.mux.HandleFunc("POST /v1/plans/{id}/run", s.handleTaskRunPost)
	s.mux.HandleFunc("GET /v1/goals", s.handleGoalsList)
	s.mux.HandleFunc("POST /v1/goals", s.handleGoalsSet)
	s.mux.HandleFunc("GET /v1/goals/{id}", s.handleGoalGet)
	s.mux.HandleFunc("POST /v1/goals/{id}/run", s.handleGoalRun)
	s.mux.HandleFunc("POST /v1/goals/{id}/pause", s.handleGoalPause)
	s.mux.HandleFunc("POST /v1/goals/{id}/resume", s.handleGoalResume)
	s.mux.HandleFunc("POST /v1/goals/{id}/abort", s.handleGoalAbort)
	s.mux.HandleFunc("GET /v1/goals/{id}/log", s.handleGoalLog)
	s.mux.HandleFunc("GET /v1/mcp", s.handleMCPList)
	s.mux.HandleFunc("POST /v1/mcp/retry", s.handleMCPRetry)
	s.mux.HandleFunc("POST /v1/mcp/disable", s.handleMCPDisable)
	// Plugin lifecycle + pane contributions (web parity #732).
	// Action paths are body-id based (POST /v1/plugins/disable) so static
	// segments never collide with plugin ids that contain dots/slashes.
	s.mux.HandleFunc("GET /v1/plugins/outdated", s.handlePluginOutdated)
	s.mux.HandleFunc("POST /v1/plugins/search", s.handlePluginSearch)
	s.mux.HandleFunc("POST /v1/plugins/install", s.handlePluginInstall)
	s.mux.HandleFunc("POST /v1/plugins/enable", s.handlePluginEnable)
	s.mux.HandleFunc("POST /v1/plugins/disable", s.handlePluginDisable)
	s.mux.HandleFunc("POST /v1/plugins/remove", s.handlePluginRemove)
	s.mux.HandleFunc("POST /v1/plugins/trust", s.handlePluginTrust)
	s.mux.HandleFunc("POST /v1/plugins/untrust", s.handlePluginUntrust)
	s.mux.HandleFunc("POST /v1/plugins/preview-update", s.handlePluginPreviewUpdate)
	s.mux.HandleFunc("POST /v1/plugins/update", s.handlePluginUpdate)
	s.mux.HandleFunc("GET /v1/plugins", s.handlePluginsList)
	s.mux.HandleFunc("GET /v1/plugins/{id}/trust-preview", s.handlePluginTrustPreview)
	s.mux.HandleFunc("GET /v1/plugins/{id}", s.handlePluginGet)
	s.mux.HandleFunc("GET /v1/panes", s.handlePanesList)
	s.mux.HandleFunc("GET /v1/panes/{id}/snapshot", s.handlePaneSnapshot)
	s.mux.HandleFunc("POST /v1/panes/{id}/mount", s.handlePaneMount)
	s.mux.HandleFunc("POST /v1/panes/{id}/unmount", s.handlePaneUnmount)
	s.mux.HandleFunc("POST /v1/panes/{id}/input", s.handlePaneInput)
	s.mux.HandleFunc("POST /v1/panes/{id}/resize", s.handlePaneResize)
	s.mux.HandleFunc("GET /v1/panes/{id}", s.handlePaneGet)
	s.mux.HandleFunc("GET /v1/lsp", s.handleLSP)
	s.mux.HandleFunc("POST /v1/lsp/retry", s.handleLSPRetry)
	s.mux.HandleFunc("POST /v1/lsp/{name}/disable", s.handleLSPDisable)
	s.mux.HandleFunc("GET /v1/diagnostics", s.handleDiagnostics)
	s.mux.HandleFunc("GET /v1/workflows", s.handleWorkflows)
	s.mux.HandleFunc("GET /v1/workflows/{name}", s.handleWorkflowGet)
	s.mux.HandleFunc("GET /v1/workflows/{name}/document", s.handleWorkflowDocument)
	s.mux.HandleFunc("POST /v1/workflows/{name}/start", s.handleWorkflowStart)
	s.mux.HandleFunc("POST /v1/workflows/stop", s.handleWorkflowStop)
	s.mux.HandleFunc("POST /v1/workflows/scaffold", s.handleWorkflowScaffold)
	s.mux.HandleFunc("POST /v1/workflows/validate", s.handleWorkflowValidate)
	s.mux.HandleFunc("POST /v1/workflows/format", s.handleWorkflowFormat)
	s.mux.HandleFunc("POST /v1/workflows/phase-grants", s.handleWorkflowPhaseGrants)
	s.mux.HandleFunc("POST /v1/workflows/save", s.handleWorkflowSave)
	s.mux.HandleFunc("POST /v1/workflow-drafts/review", s.handleWorkflowDraftReview)
	s.mux.HandleFunc("POST /v1/workflow-drafts/save", s.handleWorkflowDraftSave)
	s.mux.HandleFunc("GET /v1/sessions/{id}/events", s.handleSessionEvents)
	s.mux.HandleFunc("GET /v1/sessions/{id}/timeline", s.handleSessionTimeline)
	s.mux.HandleFunc("GET /v1/sessions/{id}/timeline/export", s.handleSessionTimelineExport)
	s.mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	s.mux.HandleFunc("GET /v1/usage", s.handleUsage)
	s.mux.HandleFunc("PUT /v1/usage/budget", s.handleUsageBudget)
	s.mux.HandleFunc("GET /v1/audit", s.handleAuditList)
	s.mux.HandleFunc("GET /v1/audit/verify", s.handleAuditVerify)
	s.mux.HandleFunc("GET /v1/audit/export", s.handleAuditExport)
	s.mux.HandleFunc("POST /v1/audit", s.handleAuditAppend)
	s.mux.HandleFunc("GET /v1/schedules", s.handleSchedulesList)
	s.mux.HandleFunc("POST /v1/schedules", s.handleScheduleCreate)
	s.mux.HandleFunc("GET /v1/schedules/{id}/history", s.handleScheduleHistory)
	s.mux.HandleFunc("POST /v1/schedules/{id}/pause", s.handleSchedulePause)
	s.mux.HandleFunc("POST /v1/schedules/{id}/resume", s.handleScheduleResume)
	s.mux.HandleFunc("POST /v1/schedules/{id}/run", s.handleScheduleRun)
	s.mux.HandleFunc("GET /v1/schedules/{id}", s.handleScheduleGet)
	s.mux.HandleFunc("PATCH /v1/schedules/{id}", s.handleScheduleUpdate)
	s.mux.HandleFunc("DELETE /v1/schedules/{id}", s.handleScheduleDelete)
	s.mux.HandleFunc("GET /v1/workspaces", s.handleWorkspacesList)
	s.mux.HandleFunc("GET /v1/workspaces/browse", s.handleWorkspaceBrowse)
	s.mux.HandleFunc("POST /v1/workspaces", s.handleWorkspaceAdd)
	s.mux.HandleFunc("POST /v1/workspaces/mkdir", s.handleWorkspaceMkdir)
	s.mux.HandleFunc("POST /v1/workspaces/{id}/activate", s.handleWorkspaceActivate)
	s.mux.HandleFunc("PATCH /v1/workspaces/{id}", s.handleWorkspacePolicy)
	s.mux.HandleFunc("DELETE /v1/workspaces/{id}", s.handleWorkspaceRemove)
	s.mux.HandleFunc("GET /v1/triggers", s.handleTriggersList)
	s.mux.HandleFunc("POST /v1/triggers", s.handleTriggerCreate)
	s.mux.HandleFunc("GET /v1/triggers/{id}/deliveries", s.handleTriggerDeliveries)
	s.mux.HandleFunc("POST /v1/triggers/{id}/test", s.handleTriggerTest)
	s.mux.HandleFunc("POST /v1/triggers/{id}/replay", s.handleTriggerReplay)
	s.mux.HandleFunc("PATCH /v1/triggers/{id}", s.handleTriggerUpdate)
	s.mux.HandleFunc("DELETE /v1/triggers/{id}", s.handleTriggerDelete)
	s.mux.HandleFunc("POST /hooks/{id}", s.handleHook)
	s.mux.HandleFunc("GET /v1/roots", s.handleRoots)
	s.mux.HandleFunc("POST /v1/roots", s.handleRootCreate)
	s.mux.HandleFunc("POST /v1/roots/{id}/activate", s.handleRootActivate)
	s.mux.HandleFunc("POST /v1/roots/{id}/resume", s.handleRootResume)
	s.mux.HandleFunc("DELETE /v1/roots/{id}", s.handleRootClose)
	s.mux.HandleFunc("GET /v1/apps", s.handleApps)
	s.mux.HandleFunc("GET /v1/apps/{id}/items", s.handleAppItems)
	s.mux.HandleFunc("GET /v1/apps/{id}/items/{itemID}", s.handleAppItem)
	s.mux.HandleFunc("POST /v1/apps/{id}/items/{itemID}/act", s.handleAppAct)
	s.mux.HandleFunc("POST /v1/apps/{id}/items/{itemID}/handoff", s.handleAppHandoff)
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /v1/status/sessions", s.handleSessionStatusBoard)
	s.mux.HandleFunc("GET /v1/status/events", s.handleSessionStatusEvents)
	s.mux.HandleFunc("POST /v1/sessions/{id}/seen", s.handleSessionSeen)
	s.mux.HandleFunc("DELETE /v1/sessions/{id}/seen", s.handleSessionUnseen)
	s.mux.HandleFunc("GET /v1/agents", s.handleAgents)
	s.mux.HandleFunc("GET /v1/diag", s.handleDiag)
	s.mux.HandleFunc("POST /v1/diag", s.handleDiag)
	s.mux.HandleFunc("POST /v1/attachments", s.handleAttachmentIngest)
	s.mux.HandleFunc("POST /v1/ops", s.handleOps)
	s.mux.HandleFunc("GET /v1/sidechat", s.handleSideChatList)
	s.mux.HandleFunc("POST /v1/sidechat/ask", s.handleSideChatAsk)
	s.mux.HandleFunc("POST /v1/sidechat/ops", s.handleSideChatOps)
	s.mux.HandleFunc("GET /v1/live/events", s.handleLiveEvents)
	s.mux.HandleFunc("GET /v1/notifications", s.handleNotifications)
	s.mux.HandleFunc("GET /v1/notifications/stream", s.handleNotificationStream)
	s.mux.HandleFunc("PUT /v1/notifications/prefs", s.handleNotificationPrefs)
	s.mux.HandleFunc("GET /v1/ws", s.handleWS)
	s.mux.HandleFunc("GET /v1/terminals", s.handleTerminalsList)
	s.mux.HandleFunc("POST /v1/terminals", s.handleTerminalCreate)
	s.mux.HandleFunc("POST /v1/terminals/{id}/resize", s.handleTerminalResize)
	s.mux.HandleFunc("POST /v1/terminals/{id}/ticket", s.handleTerminalTicket)
	s.mux.HandleFunc("GET /v1/terminals/{id}/stream", s.handleTerminalStream)
	s.mux.HandleFunc("DELETE /v1/terminals/{id}", s.handleTerminalDelete)
	s.mux.HandleFunc("GET /{$}", s.handleAttach)
	s.mux.HandleFunc("GET /attach", s.handleAttach)
	s.mux.HandleFunc("GET /assets/{path...}", s.handleAsset)
	s.mux.HandleFunc("GET /sw.js", s.handleStaticRoot("sw.js", "text/javascript; charset=utf-8"))
	s.mux.HandleFunc("GET /manifest.webmanifest", s.handleStaticRoot("manifest.webmanifest", "application/manifest+json"))
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.applySecurityHeaders(w)
		if len(s.opts.AllowCIDRs) > 0 {
			ip := ClientIP(r.RemoteAddr)
			if !IPAllowed(ip, s.opts.AllowCIDRs) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		s.applyCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if stateChanging(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !requestOriginAllowed(r) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
		}
		// Public: health + attach shell. Session event streams require a token.
		if s.opts.Auth && requiresToken(r.URL.Path) && !s.terminalAuthorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rootParam extracts ?root= from the request. Empty means use active.
func rootParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("root"))
}

// resolveLive returns the Live bridge for the request: LiveHub-scoped when hub is
// set, otherwise the single Live. Returns nil + writes error response when no
// live bridge is available.
func (s *Server) resolveLive(w http.ResponseWriter, r *http.Request) *Live {
	if s.opts.LiveHub != nil {
		rootID := rootParam(r)
		live := s.opts.LiveHub.LiveFor(rootID)
		if live != nil {
			s.opts.LiveHub.MarkActive(live.SessionID())
		}
		return live
	}
	return s.opts.Live
}

func (s *Server) hasLive() bool {
	if s.opts.LiveHub != nil {
		return s.opts.LiveHub.Active() != nil
	}
	return s.opts.Live != nil
}

func stateChanging(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete || method == http.MethodPut
}

// inlineScriptHashes returns CSP 'sha256-...' sources for every inline
// <script> body in the static index.html. Missing/unreadable index → nil.
func inlineScriptHashes(static fs.FS) []string {
	data, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return nil
	}
	var out []string
	rest := string(data)
	for {
		open := strings.Index(rest, "<script")
		if open < 0 {
			return out
		}
		rest = rest[open+len("<script"):]
		gt := strings.Index(rest, ">")
		if gt < 0 {
			return out
		}
		tag, body := rest[:gt], rest[gt+1:]
		end := strings.Index(body, "</script>")
		if end < 0 {
			return out
		}
		if !strings.Contains(tag, "src=") && end > 0 {
			sum := sha256.Sum256([]byte(body[:end]))
			out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		}
		rest = body[end+len("</script>"):]
	}
}

func (s *Server) applySecurityHeaders(w http.ResponseWriter) {
	scriptSrc := "script-src 'self' 'wasm-unsafe-eval'"
	for _, h := range s.scriptCSPHashes {
		scriptSrc += " " + h
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss: http: https:; img-src 'self' data: blob:; style-src 'self'; "+scriptSrc+"; font-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	// camera=(self) lets the cockpit scan a gateway QR. microphone=(self)
	// lets conversation mode use the browser speech API. Geolocation stays off.
	w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
}

const authCookieName = "strike_serve_token"

func requiresToken(path string) bool {
	// The phone has no daemon token yet. The single-use code in the POST body
	// is the credential. Query-string codes are rejected inside the handler.
	if path == "/v1/pair/redeem" {
		return false
	}
	return strings.HasPrefix(path, "/v1/")
}

func (s *Server) authorized(r *http.Request) bool {
	// Query secrets are rejected even when a cookie or grant is also present.
	// They leak into shell history, proxy logs, and Referer. Pairing codes
	// belong in the URL fragment and are redeemed with POST /v1/pair/redeem.
	if r.URL.Query().Get("token") != "" || r.URL.Query().Get("code") != "" || r.URL.Query().Get("pair") != "" {
		return false
	}
	// Bearer header or HttpOnly cookie. A short-lived grant and a paired
	// session are accepted the same way as the host token. Both headers are
	// checked so a wrong bearer does not hide a valid cookie, and the grant
	// scan always walks every slot.
	root := s.currentToken()
	if s.pairs != nil {
		s.pairs.observe(root)
	}
	bearer := bearerToken(r.Header.Get("Authorization"))
	cookie := cookieToken(r)
	rootOK := tokenEqual(bearer, root) || tokenEqual(cookie, root)
	grantOK := s.acceptAnyGrant(bearer, cookie)
	return rootOK || grantOK || s.sessionCredentialOK(r)
}

func (s *Server) sessionCredentialOK(r *http.Request) bool {
	if s.pairs == nil {
		return false
	}
	if s.pairs.sessionOK(bearerToken(r.Header.Get("Authorization"))) {
		return true
	}
	return s.pairs.sessionOK(cookieToken(r))
}

// rootAuthorized reports whether this request presented the host token
// (file or --token), not merely a short-lived grant. Mint, revoke-all, and
// rotate require it so a stolen grant cannot replace the host credential.
func (s *Server) rootAuthorized(r *http.Request) bool {
	root := s.currentToken()
	if root == "" {
		return false
	}
	return tokenEqual(bearerToken(r.Header.Get("Authorization")), root) || tokenEqual(cookieToken(r), root)
}

// currentToken is the host bearer checked on this request. A configured
// token file wins over the value captured at startup. A change in that
// value drops in-memory grants so strike daemon logout revokes them on the
// next /v1 request without a second call.
func (s *Server) currentToken() string {
	path := strings.TrimSpace(s.opts.TokenFile)
	tok := s.opts.Token
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			tok = ""
		} else {
			tok = strings.TrimSpace(string(data))
			// Record a token-file rotation for the audit log (main's
			// noteTokenFile); observeRoot below drops grants on change.
			s.noteTokenFile(tok)
		}
	}
	s.observeRoot(tok)
	return tok
}

// bearerToken extracts a Bearer credential. Scheme match is case-insensitive
// per RFC 7235.
func bearerToken(auth string) string {
	auth = strings.TrimSpace(auth)
	const prefix = "bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(authCookieName)
	if err != nil || c == nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

func tokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// maybeHandoffToken consumes ?token= on the attach shell: valid tokens become
// an HttpOnly SameSite=Strict cookie and the URL is redirected without the
// secret so subsequent same-origin fetch/EventSource/WebSocket calls
// authenticate via cookie. Invalid tokens leave the page unauthenticated.
func (s *Server) maybeHandoffToken(w http.ResponseWriter, r *http.Request) bool {
	if !s.opts.Auth {
		return false
	}
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	if tok == "" {
		return false
	}
	// A grant must be stored as itself. Copying the host token into the
	// cookie would turn a short-lived handoff into a long-lived one.
	current := s.currentToken()
	cookieVal := ""
	switch {
	case tokenEqual(tok, current):
		cookieVal = current
	case s.acceptAnyGrant(tok):
		cookieVal = tok
	default:
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    cookieVal,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Secure only when a loopback TLS proxy (tailscale serve) says the
		// browser is on HTTPS. A direct http://100.x bind must stay unset
		// or the phone will not store the cookie. See docs/mobile.md.
		Secure: cookieSecure(r),
	})
	q := r.URL.Query()
	q.Del("token")
	target := r.URL.Path
	if target == "" {
		target = "/"
	}
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && requestOriginAllowed(r) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	}
}

// originAllowed permits loopback and private-network browser origins
// (localhost, RFC1918, CGNAT/tailnet), the only binds strike serves.
func originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	o := strings.ToLower(origin)
	if !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
		return false
	}
	host := originHost(o)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return IsPrivateBind(host)
}

func originHost(origin string) string {
	rest := origin
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	host := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		host = rest[:i]
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return strings.Trim(h, "[]")
}

type healthResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// Now is unix milliseconds. Clients treat a gap longer than
	// protocol.HeartbeatStaleMs as a dead gateway.
	Now int64 `json:"now"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		OK:      true,
		Version: version.Version,
		Commit:  version.Commit,
		Now:     time.Now().UnixMilli(),
	})
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	if s.maybeHandoffToken(w, r) {
		return
	}
	data, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.Error(w, "attach page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	path := "assets/" + strings.TrimPrefix(r.PathValue("path"), "/")
	data, err := fs.ReadFile(s.static, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(path) {
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".wasm":
		w.Header().Set("Content-Type", "application/wasm")
	case ".webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

// handleStaticRoot serves one file from the static root (PWA service worker
// and manifest must live at the origin root, not under /assets/).
func (s *Server) handleStaticRoot(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(s.static, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := session.LogPath(s.opts.SessionDir, id)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	offset := eventCursor(r)
	if st, err := os.Stat(path); err == nil {
		offset = clampCursor(offset, st.Size())
	}
	// Backlog after Last-Event-ID / ?after=, then tail.
	n, err := s.writeEventsFrom(ctx, w, flusher, path, offset)
	if err != nil {
		return
	}
	offset = n

	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.streamDone():
			return
		case <-ticker.C:
			n, err := s.writeEventsFrom(ctx, w, flusher, path, offset)
			if err != nil {
				return
			}
			if n > offset {
				offset = n
			}
		}
	}
}

// writeEventsFrom reads JSONL from path starting at byte offset and writes each
// complete line as an SSE data event. Returns the new file offset.
func (s *Server) writeEventsFrom(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return offset, err
	}
	if st.Size() < offset {
		// Truncated/rewound log — restart from beginning.
		offset = 0
	}
	return s.writeEventsRangeFile(ctx, w, flusher, f, offset, st.Size())
}

func (s *Server) writeEventsRange(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, path string, offset, boundary int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	return s.writeEventsRangeFile(ctx, w, flusher, f, offset, boundary)
}

func (s *Server) writeEventsRangeFile(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, f *os.File, offset, boundary int64) (int64, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}

	reader := bufio.NewReaderSize(io.LimitReader(f, boundary-offset), 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return offset, err
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// Only emit complete lines (trailing \n).
			if line[len(line)-1] != '\n' {
				// Incomplete trailing line; wait for more bytes.
				break
			}
			offset += int64(len(line))
			payload := bytes.TrimSpace(line)
			if len(payload) == 0 {
				continue
			}
			if !json.Valid(payload) {
				// Skip malformed lines rather than killing the stream.
				continue
			}
			// Skip session log schema header (#803); clients expect protocol events.
			if isSessionLogHeader(payload) {
				continue
			}
			// id is the byte offset after this line. EventSource sends it
			// back as Last-Event-ID on reconnect.
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", offset, payload); err != nil {
				return offset, err
			}
			flusher.Flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return offset, nil
			}
			return offset, err
		}
	}
	return offset, nil
}

// isSessionLogHeader reports whether payload is the optional first-line
// session.header schema marker written by internal/persist/session (#803).
func isSessionLogHeader(payload []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return probe.Type == "session.header"
}

func validateSessionID(id string) error {
	if id == "" {
		return errors.New("session id is empty")
	}
	if strings.Contains(id, string(filepath.Separator)) || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return errors.New("invalid session id")
	}
	if strings.Contains(id, "..") {
		return errors.New("invalid session id")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
