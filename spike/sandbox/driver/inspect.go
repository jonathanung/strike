package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// inspectScript rejects a microvm-run script that would open KVM, attach a
// NIC, or share anything other than the one workspace virtiofs. The session
// bus in this spike is virtio-serial; vhost-vsock is rejected because this
// host has no /dev/vhost-vsock and the kernel cannot load the module.
func inspectScript(script string) error {
	var problems []string
	if strings.Contains(script, "-enable-kvm") || strings.Contains(script, "accel=kvm") || strings.Contains(script, "/dev/kvm") {
		problems = append(problems, "kvm requested (KVM_CREATE_VCPU oopses this cloud kernel)")
	}
	if strings.Contains(script, "'-cpu' 'host") {
		problems = append(problems, "cpu host requires kvm")
	}
	if strings.Contains(script, "-netdev") {
		problems = append(problems, "netdev present")
	}
	if strings.Contains(script, "vhost-vsock") {
		problems = append(problems, "vhost-vsock present")
	}
	if n := strings.Count(script, "vhost-user-fs-"); n != 1 {
		problems = append(problems, fmt.Sprintf("vhost-user-fs count = %d, want 1", n))
	}
	if !strings.Contains(script, "tag=workspace") {
		problems = append(problems, "workspace virtiofs tag missing")
	}
	if strings.Contains(script, "virtio-9p-") {
		problems = append(problems, "9p share present")
	}
	if !strings.Contains(script, "virtserialport") {
		problems = append(problems, "virtio-serial rpc port missing")
	}
	if !strings.Contains(script, "accel=tcg") {
		problems = append(problems, "accel=tcg missing")
	}
	if len(problems) > 0 {
		return fmt.Errorf("runner policy: %s", strings.Join(problems, "; "))
	}
	return nil
}

func readRunnerScript(runner string) (string, error) {
	p := filepath.Join(runner, "bin", "microvm-run")
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p, err)
	}
	return string(b), nil
}

// directVirtiofsd returns the virtiofsd program from the runner's
// supervisord config. microvm's virtiofsd-run starts supervisord with
// user=root, which exits immediately when the host driver is not root.
func directVirtiofsd(runner string) (string, error) {
	script, err := os.ReadFile(filepath.Join(runner, "bin", "virtiofsd-run"))
	if err != nil {
		return "", err
	}
	const flag = "--configuration "
	i := strings.Index(string(script), flag)
	if i < 0 {
		return "", fmt.Errorf("virtiofsd-run has no supervisord configuration")
	}
	rest := strings.TrimSpace(string(script)[i+len(flag):])
	conf := rest
	if j := strings.IndexAny(rest, " \t\n"); j >= 0 {
		conf = rest[:j]
	}
	b, err := os.ReadFile(conf)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", conf, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "command=") {
			continue
		}
		cmd := strings.TrimPrefix(line, "command=")
		if strings.Contains(cmd, "virtiofsd") {
			return cmd, nil
		}
	}
	return "", fmt.Errorf("supervisord config %s has no virtiofsd command", conf)
}

func runnerBins(runner string) (run, virtiofsd, shutdown string, err error) {
	run = filepath.Join(runner, "bin", "microvm-run")
	virtiofsd = filepath.Join(runner, "bin", "virtiofsd-run")
	shutdown = filepath.Join(runner, "bin", "microvm-shutdown")
	for _, p := range []string{run, virtiofsd, shutdown} {
		if _, statErr := os.Stat(p); statErr != nil {
			return "", "", "", fmt.Errorf("runner bin %s: %w", p, statErr)
		}
	}
	return run, virtiofsd, shutdown, nil
}
