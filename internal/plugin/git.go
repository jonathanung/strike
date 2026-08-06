package plugin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitInstallOptions controls a git-sourced install checkout.
type gitInstallOptions struct {
	URL    string
	Ref    string // optional branch/tag
	Commit string // optional full or short SHA; preferred over Ref when both set
	Subdir string // optional path inside repo
}

// gitMaterialize clones into a temp directory and copies the plugin payload
// (optional subdir) into destDir. Returns the full pinned commit SHA. The
// clone (including .git) is discarded after copy.
func gitMaterialize(ctx context.Context, destDir string, opts gitInstallOptions) (commit string, err error) {
	if strings.TrimSpace(opts.URL) == "" {
		return "", fmt.Errorf("git url is required")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	// Clone outside destDir so we never delete the working tree mid-copy.
	cloneDir, err := os.MkdirTemp("", "strike-plugin-git-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(cloneDir) }()

	// Shallow clone when we only have a branch/tag name; full clone when commit pinned.
	args := []string{"clone", "--quiet"}
	if opts.Commit == "" {
		args = append(args, "--depth", "1")
		if opts.Ref != "" {
			args = append(args, "--branch", opts.Ref)
		}
	}
	args = append(args, opts.URL, cloneDir)
	if err := runGit(ctx, nil, args...); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}

	if opts.Commit != "" {
		_ = runGit(ctx, &cloneDir, "fetch", "--quiet", "--unshallow")
		_ = runGit(ctx, &cloneDir, "fetch", "--quiet", "origin", opts.Commit)
		if err := runGit(ctx, &cloneDir, "checkout", "--quiet", opts.Commit); err != nil {
			return "", fmt.Errorf("git checkout %s: %w", opts.Commit, err)
		}
	}

	sha, err := gitRevParse(ctx, cloneDir, "HEAD")
	if err != nil {
		return "", err
	}
	if opts.Commit != "" && !commitMatches(opts.Commit, sha) {
		return "", fmt.Errorf("resolved commit %s does not match requested %s", sha, opts.Commit)
	}

	src := cloneDir
	if sub := strings.TrimSpace(opts.Subdir); sub != "" {
		if err := validateRelPathSyntax(sub); err != nil {
			return "", fmt.Errorf("subdir: %w", err)
		}
		subPath := filepath.Join(cloneDir, filepath.FromSlash(sub))
		subAbs, err := filepath.Abs(subPath)
		if err != nil {
			return "", err
		}
		cloneAbs, _ := filepath.Abs(cloneDir)
		if !isUnder(cloneAbs, subAbs) && subAbs != cloneAbs {
			return "", fmt.Errorf("subdir %q escapes repository", sub)
		}
		st, err := os.Stat(subAbs)
		if err != nil || !st.IsDir() {
			return "", fmt.Errorf("subdir %q not found in repository", sub)
		}
		src = subAbs
	}

	if err := copyTree(src, destDir); err != nil {
		return "", fmt.Errorf("copy plugin payload: %w", err)
	}
	return strings.ToLower(sha), nil
}

func commitMatches(want, got string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	got = strings.ToLower(strings.TrimSpace(got))
	if want == got {
		return true
	}
	// Allow short SHA prefix match when user passed abbreviated commit.
	if len(want) >= 7 && len(want) < 40 && strings.HasPrefix(got, want) {
		return true
	}
	return false
}

func gitRevParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", rev)
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	sha := strings.TrimSpace(out)
	if !isFullCommitSHA(sha) {
		return "", fmt.Errorf("git rev-parse returned non-SHA %q", sha)
	}
	return strings.ToLower(sha), nil
}

func runGit(ctx context.Context, dir *string, args ...string) error {
	_, err := gitOutput(ctx, ptrStr(dir), args...)
	return err
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// Never pass credentials via env from Strike; rely on user's git config.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// Scrub obvious credential-shaped spans from git stderr.
		msg = scrubGitMessage(msg)
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

func scrubGitMessage(s string) string {
	// Avoid echoing tokens that may appear in clone URLs in error text.
	// Replace userinfo in URLs: scheme://user:pass@host
	out := s
	if i := strings.Index(out, "://"); i >= 0 {
		// Best-effort: redact between :// and @ when @ follows.
		rest := out[i+3:]
		if at := strings.Index(rest, "@"); at > 0 && at < 200 {
			if strings.Contains(rest[:at], ":") {
				out = out[:i+3] + "[REDACTED]@" + rest[at+1:]
			}
		}
	}
	return out
}
