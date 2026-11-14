// Package packaging holds package manager templates (Homebrew, AUR) and the
// test that scripts/render-packaging.sh turns them into manifests that parse.
package packaging

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`@[A-Z0-9_]+@`)

var testTargets = []struct{ goos, goarch string }{
	{"darwin", "arm64"},
	{"darwin", "amd64"},
	{"linux", "arm64"},
	{"linux", "amd64"},
}

// fakeDigest returns a distinct, valid-looking sha256 hex string per index.
func fakeDigest(i int) string {
	return strings.Repeat(fmt.Sprintf("%x", i+10), 64)[:64]
}

// writeChecksums writes a checksums.txt like release.yml's sha256sum output.
// skip names one os_arch pair to leave out.
func writeChecksums(t *testing.T, tag, skip string) (path string, digests map[string]string) {
	t.Helper()
	digests = map[string]string{}
	var b strings.Builder
	for i, tg := range testTargets {
		key := tg.goos + "_" + tg.goarch
		if key == skip {
			continue
		}
		d := fakeDigest(i)
		digests[key] = d
		fmt.Fprintf(&b, "%s  strike_%s_%s.tar.gz\n", d, tag, key)
	}
	path = filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, digests
}

func render(t *testing.T, tag, sums string) (out string, combined []byte, err error) {
	t.Helper()
	if _, lookErr := exec.LookPath("bash"); lookErr != nil {
		t.Skip("bash not available")
	}
	out = filepath.Join(t.TempDir(), "out")
	cmd := exec.Command("bash", filepath.Join("..", "scripts", "render-packaging.sh"), tag, sums, out)
	combined, err = cmd.CombinedOutput()
	return out, combined, err
}

func TestRenderedManifestsParse(t *testing.T) {
	for _, tc := range []struct {
		tag, version, pkgver string
	}{
		{"v1.2.3", "1.2.3", "1.2.3"},
		{"v0.10.0-rc.1", "0.10.0-rc.1", "0.10.0_rc.1"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			sums, digests := writeChecksums(t, tc.tag, "")
			out, combined, err := render(t, tc.tag, sums)
			if err != nil {
				t.Fatalf("render failed: %v\n%s", err, combined)
			}
			t.Run("PKGBUILD", func(t *testing.T) {
				checkPKGBUILD(t, filepath.Join(out, "aur", "PKGBUILD"), tc.tag, tc.pkgver, digests)
			})
			t.Run("formula", func(t *testing.T) {
				checkFormula(t, filepath.Join(out, "homebrew", "strike.rb"), tc.tag, tc.version, digests)
			})
		})
	}
}

func checkPKGBUILD(t *testing.T, path, tag, pkgver string, digests map[string]string) {
	t.Helper()
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n PKGBUILD: %v\n%s", err, out)
	}
	// Source it the way makepkg does and read the fields back.
	script := `set -eu; source "$1"; printf '%s\n' "$pkgname" "$pkgver" "$_tag" "${source_x86_64[0]}" "${source_aarch64[0]}" "${sha256sums_x86_64[0]}" "${sha256sums_aarch64[0]}"; declare -F package >/dev/null`
	raw, err := exec.Command("bash", "-c", script, "bash", path).CombinedOutput()
	if err != nil {
		t.Fatalf("source PKGBUILD: %v\n%s", err, raw)
	}
	got := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	base := "https://github.com/jonathanung/strike/releases/download/" + tag + "/"
	want := []string{
		"strike-bin",
		pkgver,
		tag,
		"strike_" + tag + "_linux_amd64.tar.gz::" + base + "strike_" + tag + "_linux_amd64.tar.gz",
		"strike_" + tag + "_linux_arm64.tar.gz::" + base + "strike_" + tag + "_linux_arm64.tar.gz",
		digests["linux_amd64"],
		digests["linux_arm64"],
	}
	if len(got) != len(want) {
		t.Fatalf("PKGBUILD fields = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PKGBUILD field %d = %q, want %q", i, got[i], want[i])
		}
	}
	// makepkg rejects hyphens in pkgver.
	if strings.Contains(got[1], "-") {
		t.Errorf("pkgver %q contains '-'", got[1])
	}
}

func checkFormula(t *testing.T, path, tag, version string, digests map[string]string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, s := range []string{
		"class Strike < Formula",
		`version "` + version + `"`,
		`bin.install "strike"`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("formula missing %q", s)
		}
	}
	// Each url must be followed directly by its own sha256 line.
	for key, d := range digests {
		pair := `url "https://github.com/jonathanung/strike/releases/download/` + tag + `/strike_` + tag + `_` + key + `.tar.gz"` +
			"\n      sha256 \"" + d + `"`
		if !strings.Contains(body, pair) {
			t.Errorf("formula missing url/sha256 pair for %s", key)
		}
	}
	if m := placeholder.FindString(body); m != "" {
		t.Errorf("formula has unfilled placeholder %s", m)
	}
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Log("ruby not available; skipped ruby -c syntax check")
		return
	}
	if out, err := exec.Command(ruby, "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("ruby -c formula: %v\n%s", err, out)
	}
}

func TestRenderRejectsMissingChecksum(t *testing.T) {
	sums, _ := writeChecksums(t, "v1.2.3", "linux_arm64")
	_, combined, err := render(t, "v1.2.3", sums)
	if err == nil {
		t.Fatalf("render succeeded without linux_arm64 checksum:\n%s", combined)
	}
	if !strings.Contains(string(combined), "strike_v1.2.3_linux_arm64.tar.gz") {
		t.Errorf("error does not name the missing tarball:\n%s", combined)
	}
}

func TestRenderRejectsBadTag(t *testing.T) {
	sums, _ := writeChecksums(t, "v1.2.3", "")
	if _, combined, err := render(t, "1.2.3", sums); err == nil {
		t.Fatalf("render accepted tag without v prefix:\n%s", combined)
	}
}
