package tool

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// evalContainerArgv returns a docker-exec argv that runs command inside the
// live eval image when both STRIKE_EVAL_CONTAINER and STRIKE_EVAL_WORKDIR
// are set (Terminal-Bench). SWE-bench sets only the container id so host
// bash + eval-test stay unchanged.
//
// Values must be a docker id/name and an absolute container path; otherwise
// we refuse to route (fail closed to host bash).
func evalContainerArgv(command string) ([]string, bool) {
	cid := strings.TrimSpace(os.Getenv("STRIKE_EVAL_CONTAINER"))
	work := strings.TrimSpace(os.Getenv("STRIKE_EVAL_WORKDIR"))
	if cid == "" || work == "" {
		return nil, false
	}
	if !validEvalContainerID(cid) || !validEvalWorkdir(work) {
		return nil, false
	}
	return []string{"docker", "exec", "-w", work, cid, "bash", "-lc", command}, true
}

func validEvalContainerID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i, r := range id {
		if i == 0 {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// mapEvalMountPath rewrites an in-image path (STRIKE_EVAL_WORKDIR, usually
// /app) onto the host workspace so read/write/edit/glob see the bind-mount.
// Paths outside the mount are returned unchanged.
func mapEvalMountPath(path, workDir string) string {
	mount := strings.TrimSpace(os.Getenv("STRIKE_EVAL_WORKDIR"))
	if workDir == "" || !validEvalWorkdir(mount) {
		return path
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	cleaned := filepath.Clean(path)
	rel, err := filepath.Rel(mount, cleaned)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return workDir
	}
	return filepath.Join(workDir, rel)
}

func validEvalWorkdir(p string) bool {
	if !strings.HasPrefix(p, "/") || p == "/" || len(p) > 256 {
		return false
	}
	if strings.Contains(p, "//") || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}
