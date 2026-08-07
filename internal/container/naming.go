package container

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Label keys applied to every strike-managed container (com.zone.* → com.strike.*).
const (
	LabelManaged    = "com.strike.managed"
	LabelRepoPath   = "com.strike.repo-path"
	LabelConfigHash = "com.strike.config-hash"
	LabelImageID    = "com.strike.image-id"
	LabelPurpose    = "com.strike.purpose"
)

var nameCleanRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// ContainerName returns a deterministic container name from an absolute repo path.
// Format: strike-<sanitized-repo-name>-<sha256[:16]>
func ContainerName(repoPath string) string {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		absPath = repoPath
	}
	hash := sha256.Sum256([]byte(absPath))
	shortHash := hex.EncodeToString(hash[:])[:16]
	repoName := filepath.Base(absPath)
	repoName = nameCleanRe.ReplaceAllString(repoName, "-")
	if repoName == "" || repoName == "." {
		repoName = "repo"
	}
	return fmt.Sprintf("strike-%s-%s", repoName, shortHash)
}

// NetworkName returns the deterministic network name for a repo path.
func NetworkName(repoPath string) string {
	return ContainerName(repoPath) + "-net"
}

// Labels returns Docker/Podman labels applied to strike-managed containers.
// purpose is a short tag (e.g. "dev", "eval"); empty omits LabelPurpose.
func Labels(repoPath, configHash, imageID, purpose string) map[string]string {
	out := map[string]string{
		LabelManaged:  "true",
		LabelRepoPath: repoPath,
	}
	if configHash != "" {
		out[LabelConfigHash] = configHash
	}
	if imageID != "" {
		out[LabelImageID] = imageID
	}
	if purpose != "" {
		out[LabelPurpose] = purpose
	}
	return out
}

// LabelArgs flattens labels into repeated --label k=v CLI args (without the flag name).
func LabelArgs(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	// Stable order for tests and cache keys.
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys))
	for _, k := range keys {
		args = append(args, k+"="+labels[k])
	}
	return args
}

// SanitizeName component for optional user-facing suffixes.
func SanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = nameCleanRe.ReplaceAllString(s, "-")
	return s
}
