package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldProbeAndAuto(t *testing.T) {
	if ShouldProbe("off") || ShouldProbe("") || ShouldProbe("nope") {
		t.Fatal("off/empty/unknown must not probe")
	}
	if !ShouldProbe("notify") || !ShouldProbe("auto") {
		t.Fatal("notify/auto must probe")
	}
	if ShouldAutoInstall("notify") || ShouldAutoInstall("off") {
		t.Fatal("only auto installs")
	}
	if !ShouldAutoInstall("auto") {
		t.Fatal("auto must install")
	}
}

func TestIsNixStorePath(t *testing.T) {
	if !isNixStorePath("/nix/store/abc-strike/bin/strike") {
		t.Fatal("expected nix path")
	}
	if isNixStorePath("/usr/local/bin/strike") {
		t.Fatal("usr path is not nix")
	}
}

func TestDetectInstallNix(t *testing.T) {
	info := DetectInstall("/nix/store/hash-strike-1.0/bin/strike")
	if info.Kind != InstallNix || info.CanReplace {
		t.Fatalf("%+v", info)
	}
	if !strings.Contains(info.Hint, "Nix") {
		t.Fatalf("hint = %q", info.Hint)
	}
}

func TestDetectInstallWritable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "strike")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := DetectInstall(exe)
	if info.Kind != InstallWritable || !info.CanReplace {
		t.Fatalf("%+v", info)
	}
}

func TestStartupProbeOff(t *testing.T) {
	res, err := StartupProbe(context.Background(), ProbeOptions{Mode: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.SkipReason != "autoupdate off" {
		t.Fatalf("%+v", res)
	}
}

func TestStartupProbeSkipNetwork(t *testing.T) {
	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:        "notify",
		SkipNetwork: true,
		CacheFile:   filepath.Join(t.TempDir(), "c.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.SkipReason != "network skipped" {
		t.Fatalf("%+v", res)
	}
}

func TestStartupProbeNotifyAvailable(t *testing.T) {
	srv := newReleaseServer(t, "v0.9.0", nil)
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "update-check.json")
	install := InstallInfo{Kind: InstallWritable, CanReplace: true}
	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "notify",
		CacheFile: cache,
		Install:   &install,
		Options: Options{
			APIBase:    srv.URL,
			HTTPClient: srv.Client(),
			Current:    "v0.1.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || !res.Available {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Message, "/upgrade") || !strings.Contains(res.Message, "v0.9.0") {
		t.Fatalf("message = %q", res.Message)
	}

	// Second probe within interval + already notified → skip.
	res2, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "notify",
		CacheFile: cache,
		Install:   &install,
		Options: Options{
			APIBase:    srv.URL,
			HTTPClient: srv.Client(),
			Current:    "v0.1.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped || res2.SkipReason != "already notified" {
		t.Fatalf("second probe = %+v", res2)
	}
}

func TestStartupProbeNixMessage(t *testing.T) {
	srv := newReleaseServer(t, "v2.0.0", nil)
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "c.json")
	install := InstallInfo{Kind: InstallNix, Hint: "Nix install — update the flake/lock input"}
	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "notify",
		CacheFile: cache,
		Install:   &install,
		Options: Options{
			APIBase:    srv.URL,
			HTTPClient: srv.Client(),
			Current:    "v1.0.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || res.CanReplace {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Message, "Nix") {
		t.Fatalf("nix message = %q", res.Message)
	}
	// Must not advertise a working /upgrade path for Nix installs.
	if strings.Contains(res.Message, "— /upgrade") || strings.Contains(res.Message, "or strike upgrade") {
		t.Fatalf("nix message should not push /upgrade as primary path: %q", res.Message)
	}
}

func TestStartupProbeUpToDate(t *testing.T) {
	srv := newReleaseServer(t, "v1.0.0", nil)
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "c.json")
	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "notify",
		CacheFile: cache,
		Options: Options{
			APIBase:    srv.URL,
			HTTPClient: srv.Client(),
			Current:    "v1.0.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Available || res.Message != "" || res.Skipped {
		t.Fatalf("%+v", res)
	}
}

func TestStartupProbeIntervalSkip(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "c.json")
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := saveProbeCache(cache, probeCache{
		CheckedAt: now.Add(-time.Hour),
		Current:   "v0.1.0",
		Latest:    "v0.1.0",
		Available: false,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "notify",
		CacheFile: cache,
		Interval:  24 * time.Hour,
		Now:       func() time.Time { return now },
		Options:   Options{Current: "v0.1.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.SkipReason != "probe interval" {
		t.Fatalf("%+v", res)
	}
}

func TestStartupProbeNetworkFailureSilent(t *testing.T) {
	// Closed server → connection error → silent skip.
	srv := newReleaseServer(t, "v1.0.0", nil)
	url := srv.URL
	client := srv.Client()
	srv.Close()

	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "notify",
		CacheFile: filepath.Join(t.TempDir(), "c.json"),
		Options: Options{
			APIBase:    url,
			HTTPClient: client,
			Current:    "v0.1.0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || !strings.Contains(res.SkipReason, "check failed") {
		t.Fatalf("%+v", res)
	}
}

func TestNotifyMessage(t *testing.T) {
	got := notifyMessage("v0.1.0", "v0.2.0", InstallInfo{CanReplace: true})
	if !strings.Contains(got, "/upgrade") {
		t.Fatalf("%q", got)
	}
	got = notifyMessage("dev", "v1.0.0", InstallInfo{Kind: InstallNix})
	if !strings.Contains(got, "Nix") {
		t.Fatalf("%q", got)
	}
}

func TestStartupProbeAutoInstalls(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "strike")
	if err := os.WriteFile(exe, []byte("old-binary-content!!"), 0o755); err != nil {
		t.Fatal(err)
	}
	newPayload := []byte("new-binary-payload-xyz")
	assetName := AssetName("v0.2.0", "linux", "amd64")
	archive := mustTarGz(t, "strike", newPayload)
	sumArr := sha256.Sum256(archive)
	sums := hex.EncodeToString(sumArr[:]) + "  " + assetName + "\n"
	srv := newReleaseServer(t, "v0.2.0", map[string][]byte{
		assetName:     archive,
		checksumsName: []byte(sums),
	})
	defer srv.Close()

	install := InstallInfo{Kind: InstallWritable, CanReplace: true, Executable: exe}
	res, err := StartupProbe(context.Background(), ProbeOptions{
		Mode:      "auto",
		CacheFile: filepath.Join(t.TempDir(), "c.json"),
		Install:   &install,
		Options: Options{
			APIBase:    srv.URL,
			HTTPClient: srv.Client(),
			Current:    "v0.1.0",
			GOOS:       "linux",
			GOARCH:     "amd64",
			Executable: exe,
			NoExec:     true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.AutoInstalled || !strings.Contains(res.Message, "auto-updated") {
		t.Fatalf("%+v", res)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newPayload) {
		t.Fatalf("binary not replaced")
	}
}
