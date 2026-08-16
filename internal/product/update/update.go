// Package update implements self-update against GitHub Releases:
// check latest tag, download the matching archive, verify sha256, atomically
// replace the running binary, and re-exec on Unix.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/product/version"
)

const (
	// DefaultOwner/DefaultRepo identify the canonical release source.
	DefaultOwner = "jonathanung"
	DefaultRepo  = "strike-cli"

	defaultAPIBase = "https://api.github.com"
	userAgent      = "strike-cli-update"
	checksumsName  = "checksums.txt"
)

// Options configures Check/Upgrade. Zero values use production defaults.
// HTTPClient, APIBase, and overrides exist so tests can inject httptest.
type Options struct {
	Owner   string
	Repo    string
	APIBase string

	HTTPClient *http.Client

	// Current is the running version (defaults to version.Version).
	Current string
	// GOOS/GOARCH select the asset (default runtime.GOOS/GOARCH).
	GOOS   string
	GOARCH string

	// Executable is the binary path to replace (default os.Executable).
	Executable string
	// Stdout receives progress lines (default io.Discard).
	Stdout io.Writer
	// NoExec skips syscall.Exec after a successful replace.
	// Used by CLI `strike upgrade` (return to shell) and by tests.
	NoExec bool
	// ReexecArgs are argv for re-exec after upgrade (default: bare binary name).
	// Only used when NoExec is false (e.g. TUI /upgrade restart).
	ReexecArgs []string
}

func (o Options) withDefaults() Options {
	if o.Owner == "" {
		o.Owner = DefaultOwner
	}
	if o.Repo == "" {
		o.Repo = DefaultRepo
	}
	if o.APIBase == "" {
		o.APIBase = defaultAPIBase
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 120 * time.Second}
	}
	if o.Current == "" {
		o.Current = version.Version
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	return o
}

// Release is a subset of the GitHub Releases API payload.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset is one release file.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// ErrUnsupportedPlatform is returned when self-update is not available.
var ErrUnsupportedPlatform = errors.New("self-update is not supported on this platform")

// ErrNotWritable means the binary cannot be replaced in place.
type ErrNotWritable struct {
	Path string
	Err  error
}

func (e *ErrNotWritable) Error() string {
	return fmt.Sprintf("cannot replace %s: %v\nre-install with: curl -fsSL https://strike.jonathanung.ca/install | bash", e.Path, e.Err)
}

func (e *ErrNotWritable) Unwrap() error { return e.Err }

// Result describes a completed check or no-op upgrade.
type Result struct {
	Current string
	Latest  string
	// Updated is true when a newer binary was installed (and possibly re-exec'd).
	Updated bool
	// Message is a human-readable status line.
	Message string
}

// LatestRelease fetches the newest GitHub Release for the configured repo.
func LatestRelease(ctx context.Context, opts Options) (Release, error) {
	opts = opts.withDefaults()
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", strings.TrimRight(opts.APIBase, "/"), opts.Owner, opts.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Release{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github releases: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return Release{}, fmt.Errorf("parsing release json: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return Release{}, errors.New("latest release has empty tag_name")
	}
	return rel, nil
}

// Check reports whether a newer release exists without downloading.
func Check(ctx context.Context, opts Options) (Result, error) {
	opts = opts.withDefaults()
	rel, err := LatestRelease(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	res := Result{Current: opts.Current, Latest: rel.TagName}
	if !IsNewer(rel.TagName, opts.Current) {
		res.Message = fmt.Sprintf("already up to date (%s)", versionLabel(opts.Current))
		return res, nil
	}
	res.Message = fmt.Sprintf("update available: %s → %s", versionLabel(opts.Current), rel.TagName)
	return res, nil
}

// Upgrade checks for a newer release, installs it when found, and re-execs
// the new binary on Unix (unless NoExec). When already current it returns a
// Result with Updated=false and a nil error.
func Upgrade(ctx context.Context, opts Options) (Result, error) {
	opts = opts.withDefaults()
	if opts.GOOS == "windows" {
		return Result{}, fmt.Errorf("%w (windows): re-run the install script or download from GitHub Releases", ErrUnsupportedPlatform)
	}

	rel, err := LatestRelease(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	res := Result{Current: opts.Current, Latest: rel.TagName}
	if !IsNewer(rel.TagName, opts.Current) {
		res.Message = fmt.Sprintf("already up to date (%s)", versionLabel(opts.Current))
		fmt.Fprintln(opts.Stdout, res.Message)
		return res, nil
	}

	assetName := AssetName(rel.TagName, opts.GOOS, opts.GOARCH)
	asset, ok := findAsset(rel.Assets, assetName)
	if !ok {
		return res, fmt.Errorf("release %s has no asset %s (unsupported os/arch %s/%s)", rel.TagName, assetName, opts.GOOS, opts.GOARCH)
	}
	sumsAsset, ok := findAsset(rel.Assets, checksumsName)
	if !ok {
		return res, fmt.Errorf("release %s is missing %s", rel.TagName, checksumsName)
	}

	fmt.Fprintf(opts.Stdout, "upgrading %s → %s\n", versionLabel(opts.Current), rel.TagName)

	wantSum, err := fetchChecksum(ctx, opts, sumsAsset.BrowserDownloadURL, assetName)
	if err != nil {
		return res, err
	}
	archive, err := download(ctx, opts, asset.BrowserDownloadURL)
	if err != nil {
		return res, err
	}
	got := sha256.Sum256(archive)
	gotHex := hex.EncodeToString(got[:])
	if !strings.EqualFold(gotHex, wantSum) {
		return res, fmt.Errorf("checksum mismatch for %s: got %s want %s (aborting; binary not replaced)", assetName, gotHex, wantSum)
	}

	bin, err := extractStrike(archive)
	if err != nil {
		return res, err
	}

	exe, err := resolveExecutable(opts.Executable)
	if err != nil {
		return res, err
	}
	if err := replaceBinary(exe, bin); err != nil {
		return res, err
	}

	res.Updated = true
	res.Message = fmt.Sprintf("upgraded to %s", rel.TagName)
	fmt.Fprintln(opts.Stdout, res.Message)

	if opts.NoExec {
		return res, nil
	}
	args := opts.ReexecArgs
	if args == nil {
		args = []string{}
	}
	if err := reexec(exe, args); err != nil {
		return res, fmt.Errorf("installed %s but failed to restart: %w\nrun: %s", rel.TagName, err, exe)
	}
	// reexec never returns on success
	return res, nil
}

func versionLabel(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "dev"
	}
	return v
}

func findAsset(assets []Asset, name string) (Asset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

func fetchChecksum(ctx context.Context, opts Options, url, assetName string) (string, error) {
	data, err := download(ctx, opts, url)
	if err != nil {
		return "", fmt.Errorf("downloading checksums: %w", err)
	}
	sum, err := parseChecksum(string(data), assetName)
	if err != nil {
		return "", err
	}
	return sum, nil
}

// parseChecksum reads sha256sum-style lines: "<hex>  <filename>" or "<hex> *<filename>".
func parseChecksum(text, assetName string) (string, error) {
	base := filepath.Base(assetName)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sum := fields[0]
		name := fields[1]
		name = strings.TrimPrefix(name, "*")
		if filepath.Base(name) == base {
			if len(sum) != 64 {
				return "", fmt.Errorf("invalid sha256 in checksums for %s", base)
			}
			return strings.ToLower(sum), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", base)
}

func download(ctx context.Context, opts Options, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("download %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	// Cap release archives at 256 MiB.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// extractStrike unpacks the first regular file named "strike" or "strike.exe"
// from a .tar.gz archive.
func extractStrike(archive []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := filepath.Base(hdr.Name)
		if name != "strike" && name != "strike.exe" {
			continue
		}
		if hdr.Size < 0 || hdr.Size > 256<<20 {
			return nil, fmt.Errorf("binary size %d out of range", hdr.Size)
		}
		data, err := io.ReadAll(io.LimitReader(tr, 256<<20))
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, errors.New("archive contains empty strike binary")
		}
		return data, nil
	}
	return nil, errors.New("archive does not contain a strike binary")
}

func resolveExecutable(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable: %w", err)
	}
	// Prefer the resolved path so symlinks (e.g. ~/.local/bin/strike) update the real file.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func replaceBinary(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	info, err := os.Stat(dest)
	if err != nil {
		return &ErrNotWritable{Path: dest, Err: err}
	}
	mode := info.Mode()
	if mode&0o111 == 0 {
		mode |= 0o755
	}

	tmp, err := os.CreateTemp(dir, ".strike-update-*")
	if err != nil {
		return &ErrNotWritable{Path: dest, Err: err}
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp binary: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return &ErrNotWritable{Path: dest, Err: err}
	}
	ok = true
	return nil
}
