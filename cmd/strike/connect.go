package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jonathanung/strike/internal/frontend/server"
	"github.com/jonathanung/strike/internal/persist/workspace"
)

const connectUsage = `Open a loopback SSH forward to a strike daemon and keep the grant local.

Usage:
  strike connect [--remote 127.0.0.1:8787] [--local 127.0.0.1:0] [--ttl 15m] [--save name] [--open] [--ssh-port N] user@host
  strike connect [--open] <profile>
  strike connect --list
  strike connect --forget <profile>

The daemon stays on the remote loopback. This process binds a second
loopback port, forwards it over SSH, and injects a short-lived grant.
The grant is not printed, not put in the URL, and not written to the
profile file. Ctrl+C closes the forward.

Profiles are saved in ~/.strike/gateway-profiles.json (0600) as name,
ssh destination, and ports. They do not contain a token.

Named tunnels (Tailscale Serve, a tunnel you run yourself) are not
started by this command. See docs/gateway.md.`

// destPattern is an ssh destination, not a URL and not a shell snippet.
var destPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+@[A-Za-z0-9._:-]+$`)

// profileNamePattern keeps names distinct from destinations and flags.
var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type connectOptions struct {
	Dest    string
	Remote  string
	Local   string
	TTL     time.Duration
	Save    string
	Forget  string
	List    bool
	Open    bool
	SSHPort string
}

type gatewayProfile struct {
	Name   string `json:"name"`
	SSH    string `json:"ssh"`
	Remote string `json:"remote"`
	Local  string `json:"local,omitempty"`
	TTL    string `json:"ttl,omitempty"`
}

type profileFile struct {
	Version  int              `json:"version"`
	Profiles []gatewayProfile `json:"profiles"`
}

type forwardSpec struct {
	Dest   string
	Remote string
	Local  string
}

type runningForward struct {
	Local string
	Close func()
}

type sshClient interface {
	LocalForward(ctx context.Context, spec forwardSpec) (runningForward, error)
	Output(ctx context.Context, dest, remoteCommand string) (stdout, stderr []byte, err error)
}

type connectDeps struct {
	ssh      sshClient
	profiles string
	listen   func(addr string) (net.Listener, error)
	open     func(rawURL string) error
}

func runConnectCLI(args []string, stdout, stderr io.Writer) int {
	opt, err := parseConnectArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, connectUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", redactSecrets(err.Error()))
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps := connectDeps{
		ssh:      execSSH{bin: "ssh", port: opt.SSHPort},
		profiles: gatewayProfilePath(),
		listen: func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		},
		open: func(rawURL string) error {
			return exec.Command("xdg-open", rawURL).Start()
		},
	}
	publicURL, err := runConnect(ctx, opt, deps, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", redactSecrets(err.Error()))
		return 1
	}
	if publicURL == "" {
		return 0
	}
	fmt.Fprintln(stdout, "Ctrl+C closes the forward.")
	<-ctx.Done()
	return 0
}

func parseConnectArgs(args []string) (connectOptions, error) {
	var opt connectOptions
	fs := flag.NewFlagSet("strike connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	local := fs.String("local", "", "")
	remote := fs.String("remote", "127.0.0.1:8787", "")
	ttl := fs.String("ttl", "15m", "")
	save := fs.String("save", "", "")
	forget := fs.String("forget", "", "")
	list := fs.Bool("list", false, "")
	open := fs.Bool("open", false, "")
	sshPort := fs.String("ssh-port", "", "")
	if err := fs.Parse(args); err != nil {
		return connectOptions{}, err
	}
	d, err := parseGrantTTL(*ttl)
	if err != nil {
		return connectOptions{}, err
	}
	opt = connectOptions{
		Remote:  strings.TrimSpace(*remote),
		Local:   strings.TrimSpace(*local),
		TTL:     d,
		Save:    strings.TrimSpace(*save),
		Forget:  strings.TrimSpace(*forget),
		List:    *list,
		Open:    *open,
		SSHPort: strings.TrimSpace(*sshPort),
	}
	if opt.List || opt.Forget != "" {
		if fs.NArg() != 0 {
			return connectOptions{}, errors.New("unexpected argument")
		}
		if opt.Forget != "" && !profileNamePattern.MatchString(opt.Forget) {
			return connectOptions{}, fmt.Errorf("invalid profile name %q", opt.Forget)
		}
		return opt, nil
	}
	if fs.NArg() != 1 {
		return connectOptions{}, errors.New("expected user@host or a saved profile name")
	}
	arg := strings.TrimSpace(fs.Arg(0))
	if arg == "" {
		return connectOptions{}, errors.New("expected user@host or a saved profile name")
	}
	if strings.Contains(strings.ToLower(arg), "token=") || strings.Contains(arg, "Bearer ") {
		return connectOptions{}, errors.New("refusing destination that contains a token")
	}
	if strings.Contains(arg, "@") {
		if !destPattern.MatchString(arg) {
			return connectOptions{}, fmt.Errorf("invalid ssh destination %q", arg)
		}
	} else if !profileNamePattern.MatchString(arg) {
		return connectOptions{}, fmt.Errorf("invalid profile name %q", arg)
	}
	opt.Dest = arg
	if opt.Save != "" && !profileNamePattern.MatchString(opt.Save) {
		return connectOptions{}, fmt.Errorf("invalid profile name %q", opt.Save)
	}
	if opt.SSHPort != "" && !validPort(opt.SSHPort) {
		return connectOptions{}, fmt.Errorf("invalid --ssh-port %q", opt.SSHPort)
	}
	if err := validateForwardTarget(opt.Remote); err != nil {
		return connectOptions{}, err
	}
	if opt.Local == "" {
		opt.Local = "127.0.0.1:0"
	}
	if err := validateLocalBind(opt.Local); err != nil {
		return connectOptions{}, err
	}
	return opt, nil
}

func validPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n > 0 && n <= 65535
}

func validateForwardTarget(addr string) error {
	if !server.IsLocalhostBind(addr) {
		return fmt.Errorf("SSH forward target %s is not loopback; strike connect only forwards to the remote machine's loopback daemon", addr)
	}
	return nil
}

func validateLocalBind(addr string) error {
	if !server.IsLocalhostBind(addr) {
		return fmt.Errorf("local listen %s is not loopback", addr)
	}
	return nil
}

func gatewayProfilePath() string {
	return filepath.Join(workspace.DefaultDir(), "gateway-profiles.json")
}

func loadProfiles(path string) (profileFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return profileFile{Version: 1}, nil
		}
		return profileFile{}, err
	}
	if strings.Contains(string(data), `"token"`) || strings.Contains(string(data), "token=") {
		return profileFile{}, errors.New("profile file contains a token field; refusing to load it")
	}
	var pf profileFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return profileFile{}, err
	}
	if pf.Version == 0 {
		pf.Version = 1
	}
	return pf, nil
}

func saveProfiles(path string, pf profileFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	pf.Version = 1
	raw, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func upsertProfile(path string, p gatewayProfile) error {
	if strings.Contains(p.SSH, "token=") || strings.Contains(p.Remote, "token=") || strings.Contains(p.Local, "token=") {
		return errors.New("refusing to store a token in a gateway profile")
	}
	pf, err := loadProfiles(path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range pf.Profiles {
		if pf.Profiles[i].Name == p.Name {
			pf.Profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		pf.Profiles = append(pf.Profiles, p)
	}
	return saveProfiles(path, pf)
}

// grantRemoteCommand is the remote argv, shell-quoted. It never carries a token.
func grantRemoteCommand(ttl time.Duration, remote string) string {
	base := "http://" + remote
	return "strike daemon grant --ttl " + shellQuote(ttl.String()) + " --base " + shellQuote(base)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runConnect starts the forward and the injecting proxy. The returned URL
// has no token. An empty URL means the command was --list or --forget.
// The proxy runs until ctx is cancelled.
func runConnect(ctx context.Context, opt connectOptions, deps connectDeps, stdout, stderr io.Writer) (string, error) {
	if deps.profiles == "" {
		deps.profiles = gatewayProfilePath()
	}
	if opt.List {
		pf, err := loadProfiles(deps.profiles)
		if err != nil {
			return "", err
		}
		if len(pf.Profiles) == 0 {
			fmt.Fprintln(stdout, "No saved gateway profiles.")
			return "", nil
		}
		for _, p := range pf.Profiles {
			fmt.Fprintf(stdout, "%s\t%s\tremote %s\n", p.Name, p.SSH, p.Remote)
		}
		return "", nil
	}
	if opt.Forget != "" {
		pf, err := loadProfiles(deps.profiles)
		if err != nil {
			return "", err
		}
		next := make([]gatewayProfile, 0, len(pf.Profiles))
		found := false
		for _, p := range pf.Profiles {
			if p.Name == opt.Forget {
				found = true
				continue
			}
			next = append(next, p)
		}
		if !found {
			return "", fmt.Errorf("no gateway profile %q", opt.Forget)
		}
		pf.Profiles = next
		if err := saveProfiles(deps.profiles, pf); err != nil {
			return "", err
		}
		fmt.Fprintf(stdout, "Forgot gateway profile %s\n", opt.Forget)
		return "", nil
	}

	if !strings.Contains(opt.Dest, "@") {
		pf, err := loadProfiles(deps.profiles)
		if err != nil {
			return "", err
		}
		var match *gatewayProfile
		for i := range pf.Profiles {
			if pf.Profiles[i].Name == opt.Dest {
				match = &pf.Profiles[i]
				break
			}
		}
		if match == nil {
			return "", fmt.Errorf("no gateway profile %q", opt.Dest)
		}
		if !profileNamePattern.MatchString(opt.Dest) {
			return "", fmt.Errorf("invalid profile name %q", opt.Dest)
		}
		opt.Dest = match.SSH
		if opt.Remote == "127.0.0.1:8787" && match.Remote != "" {
			opt.Remote = match.Remote
		}
		if opt.Local == "127.0.0.1:0" && match.Local != "" {
			opt.Local = match.Local
		}
		if match.TTL != "" && opt.TTL == 15*time.Minute {
			d, err := parseGrantTTL(match.TTL)
			if err != nil {
				return "", err
			}
			opt.TTL = d
		}
	}
	if !destPattern.MatchString(opt.Dest) {
		return "", fmt.Errorf("invalid ssh destination %q", opt.Dest)
	}
	if err := validateForwardTarget(opt.Remote); err != nil {
		return "", err
	}
	if err := validateLocalBind(opt.Local); err != nil {
		return "", err
	}
	if opt.Save != "" {
		localSaved := opt.Local
		if strings.HasSuffix(localSaved, ":0") {
			localSaved = ""
		}
		if err := upsertProfile(deps.profiles, gatewayProfile{
			Name:   opt.Save,
			SSH:    opt.Dest,
			Remote: opt.Remote,
			Local:  localSaved,
			TTL:    opt.TTL.String(),
		}); err != nil {
			return "", err
		}
		fmt.Fprintf(stderr, "Saved gateway profile %s\n", opt.Save)
	}

	if deps.ssh == nil {
		return "", errors.New("ssh client is not configured")
	}
	fwd, err := deps.ssh.LocalForward(ctx, forwardSpec{Dest: opt.Dest, Remote: opt.Remote, Local: "127.0.0.1:0"})
	if err != nil {
		return "", err
	}
	healthCtx, cancelHealth := context.WithTimeout(ctx, 8*time.Second)
	err = waitHealthy(healthCtx, "http://"+fwd.Local+"/health")
	cancelHealth()
	if err != nil {
		fwd.Close()
		return "", err
	}
	cmd := grantRemoteCommand(opt.TTL, opt.Remote)
	out, errOut, err := deps.ssh.Output(ctx, opt.Dest, cmd)
	grant := strings.TrimSpace(string(out))
	if err != nil || !validDaemonToken(grant) {
		fwd.Close()
		return "", fmt.Errorf("mint grant over ssh: %s", redactSecrets(string(errOut), grant))
	}
	upstream, err := url.Parse("http://" + fwd.Local)
	if err != nil {
		fwd.Close()
		return "", err
	}
	if deps.listen == nil {
		deps.listen = func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		}
	}
	ln, err := deps.listen(opt.Local)
	if err != nil {
		fwd.Close()
		return "", err
	}
	errLog := &secretLog{secret: grant}
	proxy := newGrantProxy(grant, upstream, errLog)
	httpSrv := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(errLog, "", 0),
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		fwd.Close()
		_ = ln.Close()
	}()
	go func() { _ = httpSrv.Serve(ln) }()

	publicURL := "http://" + ln.Addr().String() + "/"
	if strings.Contains(publicURL, grant) {
		fwd.Close()
		return "", errors.New("refusing to publish a URL that contains the grant")
	}
	fmt.Fprintf(stdout, "Forwarding %s\nOpen %s\nThe grant stays in this process. It is not in the URL or this output.\n", opt.Dest, publicURL)
	if opt.Open && deps.open != nil {
		if err := deps.open(publicURL); err != nil {
			fmt.Fprintf(stderr, "strike: could not open the browser (%s)\n", redactSecrets(err.Error(), grant))
		}
	}
	return publicURL, nil
}

func waitHealthy(ctx context.Context, rawURL string) error {
	var last error
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %d", res.StatusCode)
		} else if ctx.Err() == nil {
			last = err
		}
		if ctx.Err() != nil {
			if last == nil {
				return ctx.Err()
			}
			return fmt.Errorf("gateway health: %v", last)
		}
		select {
		case <-ctx.Done():
			if last == nil {
				return ctx.Err()
			}
			return fmt.Errorf("gateway health: %v", last)
		case <-tick.C:
		}
	}
}

func newGrantProxy(grant string, upstream *url.URL, errLog io.Writer) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		q := req.URL.Query()
		if q.Has("token") {
			q.Del("token")
			req.URL.RawQuery = q.Encode()
		}
		if bearerToken(req.Header.Get("Authorization")) == "" {
			req.Header.Set("Authorization", "Bearer "+grant)
		}
	}
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(errLog, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errLog != nil {
			fmt.Fprintln(errLog, redactSecrets(err.Error(), grant))
		}
		http.Error(w, "gateway unreachable", http.StatusBadGateway)
	}
	return proxy
}

// bearerToken matches the server helper: empty when the scheme is missing
// or the credential is blank, so the proxy can fill it in.
func bearerToken(auth string) string {
	auth = strings.TrimSpace(auth)
	const prefix = "bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

type secretLog struct {
	secret string
}

func (s *secretLog) Write(p []byte) (int, error) {
	// Discard. The proxy must not grow a log that could hold the grant.
	_ = redactSecrets(string(p), s.secret)
	return len(p), nil
}

type execSSH struct {
	bin  string
	port string
}

func (c execSSH) binary() string {
	if c.bin != "" {
		return c.bin
	}
	return "ssh"
}

func (c execSSH) baseArgs() []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ForwardAgent=no",
		"-o", "ExitOnForwardFailure=yes",
	}
	if c.port != "" {
		args = append(args, "-p", c.port)
	}
	return args
}

func (c execSSH) LocalForward(ctx context.Context, spec forwardSpec) (runningForward, error) {
	local := spec.Local
	if local == "" || strings.HasSuffix(local, ":0") {
		reserved, err := reserveLoopback()
		if err != nil {
			return runningForward{}, err
		}
		local = reserved
	}
	if !server.IsLocalhostBind(local) || !server.IsLocalhostBind(spec.Remote) {
		return runningForward{}, errors.New("refusing a non-loopback SSH forward")
	}
	args := append(c.baseArgs(), "-N", "-L", local+":"+spec.Remote, spec.Dest)
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return runningForward{}, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		conn, err := dialer.DialContext(ctx, "tcp", local)
		if err == nil {
			conn.Close()
			return runningForward{Local: local, Close: func() { _ = cmd.Process.Kill(); <-done }}, nil
		}
		select {
		case <-done:
			return runningForward{}, fmt.Errorf("ssh forward exited: %s", redactSecrets(stderr.String()))
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return runningForward{}, ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Kill()
			return runningForward{}, fmt.Errorf("ssh forward did not listen: %s", redactSecrets(stderr.String()))
		case <-tick.C:
		}
	}
}

func (c execSSH) Output(ctx context.Context, dest, remoteCommand string) ([]byte, []byte, error) {
	args := append(c.baseArgs(), dest, remoteCommand)
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func reserveLoopback() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}
