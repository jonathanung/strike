package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.2.0", false},
		{"v1.0.0", "v0.9.9", true},
		{"0.2.0", "0.1.9", true},
		{"v0.1.0", "dev", true},
		{"v0.1.0", "", true},
		{"dev", "v0.1.0", false},
		{"v0.1.0-rc.1", "v0.0.9", true},
	}
	for _, tt := range tests {
		if got := IsNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("IsNewer(%q, %q)=%v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	got := AssetName("v0.1.0", "linux", "amd64")
	want := "strike_v0.1.0_linux_amd64.tar.gz"
	if got != want {
		t.Fatalf("AssetName = %q, want %q", got, want)
	}
	if AssetName("0.1.0", "darwin", "arm64") != "strike_v0.1.0_darwin_arm64.tar.gz" {
		t.Fatalf("AssetName missing v prefix handling")
	}
}

func TestParseChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	text := sum + "  strike_v0.1.0_linux_amd64.tar.gz\n" +
		strings.Repeat("cd", 32) + " *other.tar.gz\n"
	got, err := parseChecksum(text, "strike_v0.1.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != sum {
		t.Fatalf("sum = %q, want %q", got, sum)
	}
	if _, err := parseChecksum(text, "missing.tar.gz"); err == nil {
		t.Fatal("expected error for missing asset")
	}
}

func TestExtractStrike(t *testing.T) {
	payload := []byte("#!/bin/sh\necho strike-bin\n")
	archive := mustTarGz(t, "strike", payload)
	got, err := extractStrike(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch")
	}
	if _, err := extractStrike(mustTarGz(t, "readme.txt", []byte("hi"))); err == nil {
		t.Fatal("expected missing binary error")
	}
}

func TestUpgradeAlreadyCurrent(t *testing.T) {
	srv := newReleaseServer(t, "v0.1.0", nil)
	defer srv.Close()

	var out bytes.Buffer
	res, err := Upgrade(context.Background(), Options{
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
		Current:    "v0.1.0",
		GOOS:       "linux",
		GOARCH:     "amd64",
		Stdout:     &out,
		NoExec:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated {
		t.Fatal("expected no update")
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUpgradeDownloadsAndReplaces(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "strike")
	if err := os.WriteFile(oldPath, []byte("old-binary-content!!"), 0o755); err != nil {
		t.Fatal(err)
	}

	newPayload := []byte("new-binary-payload-xyz")
	assetName := AssetName("v0.2.0", "linux", "amd64")
	archive := mustTarGz(t, "strike", newPayload)
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + assetName + "\n"

	srv := newReleaseServer(t, "v0.2.0", map[string][]byte{
		assetName:     archive,
		checksumsName: []byte(sums),
	})
	defer srv.Close()

	var out bytes.Buffer
	res, err := Upgrade(context.Background(), Options{
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
		Current:    "v0.1.0",
		GOOS:       "linux",
		GOARCH:     "amd64",
		Executable: oldPath,
		Stdout:     &out,
		NoExec:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated {
		t.Fatalf("expected update: %+v stdout=%q", res, out.String())
	}
	got, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newPayload) {
		t.Fatalf("binary = %q, want %q", got, newPayload)
	}
}

func TestUpgradeChecksumMismatchAborts(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "strike")
	orig := []byte("untouched-original")
	if err := os.WriteFile(oldPath, orig, 0o755); err != nil {
		t.Fatal(err)
	}

	assetName := AssetName("v0.2.0", "linux", "amd64")
	archive := mustTarGz(t, "strike", []byte("new"))
	badSums := strings.Repeat("00", 32) + "  " + assetName + "\n"

	srv := newReleaseServer(t, "v0.2.0", map[string][]byte{
		assetName:     archive,
		checksumsName: []byte(badSums),
	})
	defer srv.Close()

	_, err := Upgrade(context.Background(), Options{
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
		Current:    "v0.1.0",
		GOOS:       "linux",
		GOARCH:     "amd64",
		Executable: oldPath,
		NoExec:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	got, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("binary was modified on checksum failure")
	}
}

func TestUpgradeUnsupportedArch(t *testing.T) {
	srv := newReleaseServer(t, "v0.2.0", map[string][]byte{
		checksumsName: []byte("x"),
	})
	defer srv.Close()

	_, err := Upgrade(context.Background(), Options{
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
		Current:    "v0.1.0",
		GOOS:       "linux",
		GOARCH:     "mips",
		NoExec:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("err = %v, want no asset", err)
	}
}

func TestCheck(t *testing.T) {
	srv := newReleaseServer(t, "v0.3.0", nil)
	defer srv.Close()
	res, err := Check(context.Background(), Options{
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
		Current:    "v0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Latest != "v0.3.0" || !strings.Contains(res.Message, "update available") {
		t.Fatalf("%+v", res)
	}
}

func mustTarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newReleaseServer(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	type assetJSON struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		rel := struct {
			TagName string      `json:"tag_name"`
			Assets  []assetJSON `json:"assets"`
		}{TagName: tag}
		for name := range assets {
			rel.Assets = append(rel.Assets, assetJSON{
				Name:               name,
				BrowserDownloadURL: srv.URL + "/download/" + name,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		data, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	return srv
}
