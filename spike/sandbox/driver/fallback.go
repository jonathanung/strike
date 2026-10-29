package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// fallbackProbe is the isolation script for the bubblewrap stand-in.
// It writes the same keys as the NixOS guest probe. awk is mawk because
// /usr/bin/awk points at /etc/alternatives, which is not mounted.
const fallbackProbe = `#!/bin/bash
set +e
out=/workspace/probe.txt
{
  echo probe_format=1
  echo "hostname=$(cat /proc/sys/kernel/hostname)"
  ifaces=$(/usr/bin/mawk 'NR>2 {gsub(/:/,"",$1); if ($1 != "lo") printf "%s ", $1}' /proc/net/dev | /usr/bin/sed 's/ *$//')
  echo "non_lo_ifaces=$ifaces"
  if /usr/bin/mawk 'NR>1 && $2=="00000000" {found=1} END {exit found?0:1}' /proc/net/route; then
    echo default_route=present
  else
    echo default_route=absent
  fi
  ping_err=$(/usr/bin/ping -c 1 -W 2 1.1.1.1 2>&1)
  if [ $? -eq 0 ]; then echo net_ping=reachable; else echo net_ping=blocked; fi
  echo "net_ping_detail=$(printf '%s' "$ping_err" | /usr/bin/tr '\n' ' ' | /usr/bin/cut -c1-180)"
  tcp_err=$(/usr/bin/timeout 3 /bin/bash -c 'echo >/dev/tcp/1.1.1.1/443' 2>&1)
  if [ $? -eq 0 ]; then echo net_tcp=reachable; else echo net_tcp=blocked; fi
  echo "net_tcp_detail=$(printf '%s' "$tcp_err" | /usr/bin/tr '\n' ' ' | /usr/bin/cut -c1-180)"
  meta_err=$(/usr/bin/timeout 3 /bin/bash -c 'echo >/dev/tcp/169.254.169.254/80' 2>&1)
  if [ $? -eq 0 ]; then echo net_metadata=reachable; else echo net_metadata=blocked; fi
  echo "net_metadata_detail=$(printf '%s' "$meta_err" | /usr/bin/tr '\n' ' ' | /usr/bin/cut -c1-180)"
  if /usr/bin/timeout 3 /usr/bin/getent hosts example.com >/dev/null 2>&1; then
    echo net_dns=reachable
  else
    echo net_dns=blocked
  fi
  if [ -e /var/tmp/strike-phase0/host-canary ]; then echo host_canary=present; else echo host_canary=absent; fi
  if [ -e /var/tmp/strike-phase0/secrets/auth.json ]; then echo host_auth=present; else echo host_auth=absent; fi
  if [ -d /home/ubuntu ]; then echo host_home=present; else echo host_home=absent; fi
  if [ -e /workspace/../host-canary ] || [ -e /host-canary ]; then
    echo workspace_parent_escape=present
  else
    echo workspace_parent_escape=absent
  fi
  if [ -f /workspace/marker.txt ]; then echo workspace_marker=present; else echo workspace_marker=absent; fi
  if [ -e /root/.strike/auth.json ] || [ -e /tmp/strike-home/.strike/auth.json ]; then
    echo strike_auth=present
  else
    echo strike_auth=absent
  fi
  if [ -d /run/secrets ] || [ -d /run/credentials ]; then
    echo run_secrets=present
  else
    echo run_secrets=absent
  fi
  echo env_names_begin
  env | /usr/bin/mawk -F= '
    {
      n=tolower($1)
      if (n ~ /key/ || n ~ /token/ || n ~ /secret/ || n ~ /password/ || n ~ /api/)
        print "env_name " $1
    }'
  echo env_names_end
  echo workspace_entries_begin
  /bin/ls -A /workspace 2>/dev/null | /usr/bin/sed 's/^/entry /'
  echo workspace_entries_end
  /usr/bin/mawk '/^MemTotal:/ {print "mem_total_kb="$2} /^MemAvailable:/ {print "mem_available_kb="$2}' /proc/meminfo
  /usr/bin/findmnt -n -T /workspace -o TARGET,FSTYPE,SOURCE | /usr/bin/sed 's/^/mount /'
  /usr/bin/findmnt -n -o TARGET,FSTYPE,SOURCE | /usr/bin/sed 's/^/mount_tree /'
  echo probe_end=ok
} > "$out" 2>&1
exit 0
`

// bwrapArgs builds the bubblewrap command line. The only writable bind is
// the workspace directory. There is no --share-net.
func bwrapArgs(state, strikeBin, probeScript string, rpc bool) ([]string, error) {
	ws := filepath.Join(state, workspaceName)
	args := []string{
		"--unshare-all",
		"--die-with-parent",
		"--hostname", "strike-phase0",
		"--clearenv",
		"--setenv", "PATH", "/usr/sbin:/usr/bin:/sbin:/bin",
		"--setenv", "HOME", "/tmp/strike-home",
		"--setenv", "USER", "strike",
		"--setenv", "LOGNAME", "strike",
		"--setenv", "LANG", "C",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
	}
	if st, err := os.Stat("/lib64"); err == nil && st.IsDir() {
		args = append(args, "--ro-bind", "/lib64", "/lib64")
	}
	if st, err := os.Stat("/sbin"); err == nil && st.IsDir() {
		args = append(args, "--ro-bind", "/sbin", "/sbin")
	}
	args = append(args,
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--dev", "/dev",
		"--bind", ws, "/workspace",
		"--chdir", "/workspace",
	)
	if rpc {
		if strikeBin == "" {
			return nil, fmt.Errorf("strike binary is required")
		}
		args = append(args,
			"--ro-bind", strikeBin, "/strike",
			"--",
			"/bin/bash", "-c", "mkdir -p /tmp/strike-home && exec /strike rpc --provider echo --auto",
		)
	} else {
		if probeScript == "" {
			return nil, fmt.Errorf("probe script is required")
		}
		args = append(args,
			"--ro-bind", probeScript, "/probe.sh",
			"--",
			"/bin/bash", "/probe.sh",
		)
	}
	if err := auditBwrapArgs(state, strikeBin, probeScript, args); err != nil {
		return nil, err
	}
	return args, nil
}

func auditBwrapArgs(state, strikeBin, probeScript string, args []string) error {
	ws := filepath.Join(state, workspaceName)
	allowedRO := map[string]bool{
		"/usr": true, "/bin": true, "/lib": true, "/lib64": true, "/sbin": true,
	}
	if probeScript != "" {
		allowedRO[probeScript] = true
	}
	if strikeBin != "" {
		allowedRO[strikeBin] = true
	}
	binds := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--share-net", "--netdev":
			return fmt.Errorf("bwrap args keep a network")
		case "--bind":
			if i+2 >= len(args) {
				return fmt.Errorf("truncated --bind")
			}
			binds++
			if args[i+1] != ws || args[i+2] != "/workspace" {
				return fmt.Errorf("unexpected bind %s -> %s", args[i+1], args[i+2])
			}
			i += 2
		case "--ro-bind":
			if i+1 >= len(args) {
				return fmt.Errorf("truncated --ro-bind")
			}
			src := args[i+1]
			if !allowedRO[src] {
				return fmt.Errorf("unexpected ro-bind %s", src)
			}
			if src == state || strings.HasPrefix(src, filepath.Join(state, "secrets")) {
				return fmt.Errorf("ro-bind exposes host state %s", src)
			}
			i += 2
		}
	}
	if binds != 1 {
		return fmt.Errorf("bind count = %d, want 1", binds)
	}
	return nil
}

func executeFallback(ctx context.Context, strikeBin, state string) (*Report, error) {
	rep := &Report{
		Label:                "throwaway phase0 spike",
		Backend:              "bwrap",
		Accel:                "none",
		TimingRepresentative: false,
		TimingNote:           "bubblewrap user, mount, and network namespaces on the host. Not a microVM, not KVM, and not a boot-time budget. namespace_mem_* is host meminfo seen inside the namespace.",
		Failures:             []string{},
		ProbeLog:             filepath.Join(state, "bwrap-probe.log"),
	}
	if state != defaultStateDir {
		return rep, fmt.Errorf("state dir must be %s", defaultStateDir)
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return rep, fmt.Errorf("bwrap not installed: %w", err)
	}
	st, err := os.Stat(strikeBin)
	if err != nil {
		return rep, fmt.Errorf("strike binary: %w", err)
	}
	if st.IsDir() {
		return rep, fmt.Errorf("strike binary is a directory")
	}
	if err := prepareState(state); err != nil {
		return rep, err
	}
	probePath := filepath.Join(state, "probe.sh")
	if err := os.WriteFile(probePath, []byte(fallbackProbe), 0o755); err != nil {
		return rep, err
	}
	if n, err := hostMemAvailableKiB(); err == nil {
		rep.HostMemAvailableStartKiB = &n
	}

	t0 := time.Now()
	probeArgs, err := bwrapArgs(state, "", probePath, false)
	if err != nil {
		return rep, err
	}
	probeLog, err := os.OpenFile(rep.ProbeLog, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return rep, err
	}
	probeCmd := exec.CommandContext(ctx, bwrap, probeArgs...)
	probeCmd.Env = cleanEnv(os.Environ())
	probeCmd.Stdout = probeLog
	probeCmd.Stderr = probeLog
	fmt.Fprintln(os.Stderr, "phase0: bwrap probe")
	if err := probeCmd.Run(); err != nil {
		probeLog.Close()
		return rep, fmt.Errorf("bwrap probe: %w", err)
	}
	probeLog.Close()
	rep.BootToProbeMs = msSince(t0)
	probeFile := filepath.Join(state, workspaceName, "probe.txt")
	if fileHas(probeFile, "probe_end=ok") {
		rep.BootToProbeFileMs = msSince(t0)
	}
	fmt.Fprintf(os.Stderr, "phase0: bwrap probe done at %dms\n", *rep.BootToProbeMs)

	rpcArgs, err := bwrapArgs(state, strikeBin, "", true)
	if err != nil {
		return rep, err
	}
	rpcCtx, rpcCancel := context.WithTimeout(ctx, turnTimeout)
	defer rpcCancel()
	rpcCmd := exec.CommandContext(rpcCtx, bwrap, rpcArgs...)
	rpcCmd.Env = cleanEnv(os.Environ())
	stdout, err := rpcCmd.StdoutPipe()
	if err != nil {
		return rep, err
	}
	stdin, err := rpcCmd.StdinPipe()
	if err != nil {
		return rep, err
	}
	rpcLog, err := os.OpenFile(filepath.Join(state, "bwrap-rpc.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return rep, err
	}
	defer rpcLog.Close()
	rpcCmd.Stderr = rpcLog
	if err := rpcCmd.Start(); err != nil {
		return rep, err
	}
	defer func() {
		_ = stdin.Close()
		if rpcCmd.Process == nil {
			return
		}
		_ = rpcCmd.Process.Kill()
		_, _ = rpcCmd.Process.Wait()
	}()
	conn, err := stdioConn(stdout, stdin)
	if err != nil {
		return rep, err
	}
	fmt.Fprintln(os.Stderr, "phase0: bwrap strike rpc")
	got, err := echoTurn(rpcCtx, conn, echoPrompt, func() {
		rep.BootToRPCMs = msSince(t0)
		fmt.Fprintf(os.Stderr, "phase0: initialize ok at %dms\n", *rep.BootToRPCMs)
		// Tree sum, not the bwrap parent. The parent is a few MiB and the
		// strike child holds the engine.
		if n, serr := treeVmRSS("/proc", rpcCmd.Process.Pid); serr == nil {
			rep.SandboxRSSKiB = &n
		}
	})
	if err != nil {
		return rep, fmt.Errorf("echo turn: %w", err)
	}
	rep.BootToTurnMs = msSince(t0)
	rep.EchoText = got.Text
	rep.StopReason = got.StopReason
	fmt.Fprintf(os.Stderr, "phase0: turn done at %dms stop=%s\n", *rep.BootToTurnMs, got.StopReason)
	if n, err := hostMemAvailableKiB(); err == nil {
		rep.HostMemAvailableEndKiB = &n
	}
	return finishReport(rep, state)
}

type stdioPipe struct {
	r *os.File
	w *os.File
}

func stdioConn(r io.ReadCloser, w io.WriteCloser) (net.Conn, error) {
	rf, ok := r.(*os.File)
	if !ok {
		return nil, fmt.Errorf("stdout pipe is %T", r)
	}
	wf, ok := w.(*os.File)
	if !ok {
		return nil, fmt.Errorf("stdin pipe is %T", w)
	}
	return &stdioPipe{r: rf, w: wf}, nil
}

func (c *stdioPipe) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *stdioPipe) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *stdioPipe) Close() error {
	_ = c.w.Close()
	return c.r.Close()
}
func (c *stdioPipe) LocalAddr() net.Addr  { return stdioAddr("local") }
func (c *stdioPipe) RemoteAddr() net.Addr { return stdioAddr("remote") }
func (c *stdioPipe) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
func (c *stdioPipe) SetReadDeadline(t time.Time) error  { return c.r.SetReadDeadline(t) }
func (c *stdioPipe) SetWriteDeadline(t time.Time) error { return c.w.SetWriteDeadline(t) }

type stdioAddr string

func (a stdioAddr) Network() string { return "stdio" }
func (a stdioAddr) String() string  { return string(a) }
