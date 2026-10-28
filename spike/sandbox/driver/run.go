package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	probeTimeout    = 20 * time.Minute
	rpcWaitTimeout  = 2 * time.Minute
	turnTimeout     = 90 * time.Second
	shutdownTimeout = 45 * time.Second
	socketTimeout   = 30 * time.Second
)

// Report is the measured evidence for one run. Pointer fields are nil when
// that clock or sample was not taken. TimingRepresentative is false for TCG
// and for the bubblewrap fallback.
type Report struct {
	Label                    string   `json:"label"`
	Backend                  string   `json:"backend"`
	Accel                    string   `json:"accel"`
	TimingRepresentative     bool     `json:"timing_representative"`
	TimingNote               string   `json:"timing_note"`
	BootToProbeMs            *int64   `json:"boot_to_probe_ms"`
	BootToProbeFileMs        *int64   `json:"boot_to_probe_file_ms"`
	BootToRPCMs              *int64   `json:"boot_to_rpc_ms"`
	BootToTurnMs             *int64   `json:"boot_to_turn_ms"`
	VirtiofsdReadyMs         *int64   `json:"virtiofsd_ready_ms"`
	GuestMemConfiguredMB     int      `json:"guest_mem_configured_mb,omitempty"`
	QEMUVmRSSKiB             *int64   `json:"qemu_vm_rss_kib"`
	QEMUVmHWMKiB             *int64   `json:"qemu_vm_hwm_kib"`
	VirtiofsdTreeVmRSSKiB    *int64   `json:"virtiofsd_tree_vm_rss_kib"`
	SandboxRSSKiB            *int64   `json:"sandbox_rss_kib"`
	SandboxHWMKiB            *int64   `json:"sandbox_hwm_kib"`
	GuestMemTotalKiB         *int64   `json:"guest_mem_total_kib"`
	GuestMemAvailableKiB     *int64   `json:"guest_mem_available_kib"`
	NamespaceMemTotalKiB     *int64   `json:"namespace_mem_total_kib"`
	NamespaceMemAvailableKiB *int64   `json:"namespace_mem_available_kib"`
	HostMemAvailableStartKiB *int64   `json:"host_mem_available_start_kib"`
	HostMemAvailableEndKiB   *int64   `json:"host_mem_available_end_kib"`
	EchoText                 string   `json:"echo_text,omitempty"`
	StopReason               string   `json:"stop_reason,omitempty"`
	Failures                 []string `json:"failures"`
	Pass                     bool     `json:"pass"`
	QEMUCmdline              string   `json:"qemu_cmdline,omitempty"`
	QEMULog                  string   `json:"qemu_log,omitempty"`
	VirtiofsdLog             string   `json:"virtiofsd_log,omitempty"`
	ProbeLog                 string   `json:"probe_log,omitempty"`
}

func newReport(state string) *Report {
	return &Report{
		Label:                "throwaway phase0 spike",
		Backend:              "qemu-tcg",
		Accel:                "tcg",
		TimingRepresentative: false,
		TimingNote:           "QEMU TCG software emulation. Not a KVM boot time and not a budget.",
		GuestMemConfiguredMB: guestMemConfiguredMB,
		Failures:             []string{},
		QEMULog:              filepath.Join(state, "qemu.log"),
		VirtiofsdLog:         filepath.Join(state, "virtiofsd.log"),
	}
}

func execute(ctx context.Context, runner, state string) (*Report, error) {
	rep := newReport(state)
	if state != defaultStateDir {
		return rep, fmt.Errorf("state dir must be %s (baked into the guest image)", defaultStateDir)
	}
	script, err := readRunnerScript(runner)
	if err != nil {
		return rep, err
	}
	if err := inspectScript(script); err != nil {
		return rep, err
	}
	runBin, _, shutBin, err := runnerBins(runner)
	if err != nil {
		return rep, err
	}
	fsBin, err := directVirtiofsd(runner)
	if err != nil {
		return rep, err
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return rep, err
	}
	if err := prepareState(state); err != nil {
		return rep, err
	}
	env := cleanEnv(os.Environ())

	fmt.Fprintln(os.Stderr, "phase0: starting virtiofsd")
	fsCmd, fsFile, err := startLogged(fsBin, state, rep.VirtiofsdLog, env)
	if err != nil {
		return rep, err
	}
	defer fsFile.Close()
	defer killGroup(fsCmd)
	fsWait := waitCmd(fsCmd)
	fsStart := time.Now()
	sock := filepath.Join(state, virtiofsSocketName)
	if err := waitExists(ctx, sock, socketTimeout); err != nil {
		return rep, fmt.Errorf("virtiofs socket: %w\n%s", err, tailFile(rep.VirtiofsdLog, 60))
	}
	if done, waitErr := pollWait(fsWait); done {
		return rep, fmt.Errorf("virtiofsd exited: %v\n%s", waitErr, tailFile(rep.VirtiofsdLog, 60))
	}
	ready := time.Since(fsStart).Milliseconds()
	rep.VirtiofsdReadyMs = &ready
	fmt.Fprintf(os.Stderr, "phase0: virtiofsd socket ready in %dms\n", ready)

	if n, err := hostMemAvailableKiB(); err == nil {
		rep.HostMemAvailableStartKiB = &n
	}

	fmt.Fprintln(os.Stderr, "phase0: starting qemu (TCG only)")
	t0 := time.Now()
	qCmd, qFile, err := startLogged(runBin, state, rep.QEMULog, env)
	if err != nil {
		return rep, err
	}
	defer qFile.Close()
	defer killGroup(qCmd)
	qWait := waitCmd(qCmd)

	if flat, err := cmdline(qCmd.Process.Pid); err == nil {
		rep.QEMUCmdline = flat
		if strings.Contains(flat, "-enable-kvm") || strings.Contains(flat, "accel=kvm") || strings.Contains(flat, "/dev/kvm") {
			return rep, fmt.Errorf("qemu cmdline requested kvm")
		}
	}

	probeFile := filepath.Join(state, workspaceName, "probe.txt")
	waitFor := probeTimeout
	if v := os.Getenv("PHASE0_PROBE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return rep, fmt.Errorf("PHASE0_PROBE_TIMEOUT: %q", v)
		}
		waitFor = d
	}
	deadline := time.Now().Add(waitFor)
	var sawSerial, sawFile bool
	nextLog := time.Now().Add(15 * time.Second)
	for !sawSerial || !sawFile {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if time.Now().After(deadline) {
			return rep, fmt.Errorf("guest probe timed out after %s (deadline %s)\n%s", time.Since(t0).Round(time.Second), waitFor, tailFile(rep.QEMULog, 80))
		}
		if done, waitErr := pollWait(qWait); done {
			return rep, fmt.Errorf("qemu exited before probe: %v\n%s", waitErr, tailFile(rep.QEMULog, 80))
		}
		if done, waitErr := pollWait(fsWait); done {
			return rep, fmt.Errorf("virtiofsd exited during boot: %v\n%s", waitErr, tailFile(rep.VirtiofsdLog, 60))
		}
		if !sawSerial && fileHas(rep.QEMULog, "PHASE0_PROBE_DONE") {
			sawSerial = true
			rep.BootToProbeMs = msSince(t0)
			fmt.Fprintf(os.Stderr, "phase0: probe console marker at %dms\n", *rep.BootToProbeMs)
		}
		if !sawFile && fileHas(probeFile, "probe_end=ok") {
			sawFile = true
			rep.BootToProbeFileMs = msSince(t0)
			fmt.Fprintf(os.Stderr, "phase0: probe file visible at %dms\n", *rep.BootToProbeFileMs)
		}
		if time.Now().After(nextLog) {
			fmt.Fprintf(os.Stderr, "phase0: waiting for probe (%s)\n", time.Since(t0).Round(time.Second))
			nextLog = time.Now().Add(15 * time.Second)
		}
		select {
		case <-ctx.Done():
			return rep, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	rpcDeadline := time.Now().Add(rpcWaitTimeout)
	for !fileHas(rep.QEMULog, "STRIKE_RPC_START") {
		if time.Now().After(rpcDeadline) {
			return rep, fmt.Errorf("strike rpc did not open the virtio-serial port\n%s", tailFile(rep.QEMULog, 80))
		}
		if done, waitErr := pollWait(qWait); done {
			return rep, fmt.Errorf("qemu exited before strike rpc: %v\n%s", waitErr, tailFile(rep.QEMULog, 80))
		}
		if fileHas(rep.QEMULog, "STRIKE_RPC_NO_PORT") {
			return rep, fmt.Errorf("guest reported virtio-serial port missing\n%s", tailFile(rep.QEMULog, 80))
		}
		time.Sleep(200 * time.Millisecond)
	}

	turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)
	defer turnCancel()
	conn, err := dialUnix(turnCtx, filepath.Join(state, rpcSocketName))
	if err != nil {
		return rep, fmt.Errorf("dial rpc: %w", err)
	}
	defer conn.Close()
	got, err := echoTurn(turnCtx, conn, echoPrompt, func() {
		rep.BootToRPCMs = msSince(t0)
		fmt.Fprintf(os.Stderr, "phase0: initialize ok at %dms\n", *rep.BootToRPCMs)
	})
	if err != nil {
		return rep, fmt.Errorf("echo turn: %w\n%s", err, tailFile(rep.QEMULog, 40))
	}
	rep.BootToTurnMs = msSince(t0)
	rep.EchoText = got.Text
	rep.StopReason = got.StopReason
	fmt.Fprintf(os.Stderr, "phase0: turn done at %dms stop=%s\n", *rep.BootToTurnMs, got.StopReason)

	if sample, err := readProcSample("/proc", qCmd.Process.Pid); err == nil {
		rep.QEMUVmRSSKiB = &sample.VmRSSKiB
		rep.QEMUVmHWMKiB = &sample.VmHWMKiB
	}
	if n, err := treeVmRSS("/proc", fsCmd.Process.Pid); err == nil {
		rep.VirtiofsdTreeVmRSSKiB = &n
	}
	if n, err := hostMemAvailableKiB(); err == nil {
		rep.HostMemAvailableEndKiB = &n
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutCancel()
	shut := exec.CommandContext(shutCtx, shutBin)
	shut.Dir = state
	shut.Env = env
	if err := shut.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "phase0: shutdown: %v\n", err)
	}
	select {
	case <-qWait:
	case <-time.After(5 * time.Second):
	}

	return finishReport(rep, state)
}

func finishReport(rep *Report, state string) (*Report, error) {
	body, err := os.ReadFile(filepath.Join(state, workspaceName, "probe.txt"))
	if err != nil {
		rep.Failures = append(rep.Failures, "read probe: "+err.Error())
	} else {
		probe, perr := parseProbe(string(body))
		if perr != nil {
			rep.Failures = append(rep.Failures, perr.Error())
		} else {
			rep.Failures = append(rep.Failures, probe.Failures()...)
			if probe.Hostname != "strike-phase0" {
				rep.Failures = append(rep.Failures, "hostname="+probe.Hostname)
			}
			if probe.MemTotalKiB > 0 {
				n := probe.MemTotalKiB
				if rep.Backend == "bwrap" {
					rep.NamespaceMemTotalKiB = &n
				} else {
					rep.GuestMemTotalKiB = &n
				}
			}
			if probe.seen["mem_available_kb"] {
				n := probe.MemAvailableKiB
				if rep.Backend == "bwrap" {
					rep.NamespaceMemAvailableKiB = &n
				} else {
					rep.GuestMemAvailableKiB = &n
				}
			}
		}
	}
	leaked, err := workspaceHasDummy(state)
	if err != nil {
		rep.Failures = append(rep.Failures, err.Error())
	} else if leaked {
		rep.Failures = append(rep.Failures, "dummy provider marker found in workspace share")
	}
	if !echoAccepted(rep.EchoText, echoPrompt) {
		rep.Failures = append(rep.Failures, "echo text missing")
	}
	if rep.StopReason != "" && rep.StopReason != "end_turn" {
		rep.Failures = append(rep.Failures, "stop reason "+rep.StopReason)
	}
	rep.Pass = len(rep.Failures) == 0
	return rep, nil
}

func startLogged(bin, dir, logPath string, env []string) (*exec.Cmd, *os.File, error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, nil, err
	}
	return cmd, f, nil
}

func waitCmd(cmd *exec.Cmd) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch
}

func pollWait(ch <-chan error) (bool, error) {
	select {
	case err := <-ch:
		return true, err
	default:
		return false, nil
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func cmdline(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", err
	}
	flat := strings.ReplaceAll(string(b), "\x00", " ")
	if len(flat) > 4000 {
		flat = flat[:4000]
	}
	return flat, nil
}

func waitExists(ctx context.Context, path string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear within %s", path, d)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func fileHas(path, needle string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), needle)
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func msSince(t time.Time) *int64 {
	n := time.Since(t).Milliseconds()
	return &n
}

func writeReport(path string, rep *Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
