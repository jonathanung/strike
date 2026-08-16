package container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CacheDirName is the per-repo directory under the project root for container state.
// Lives at <repo>/.strike/container/ (not global ~/.strike).
const CacheDirName = ".strike/container"

// Cache persists image/container/network ids and the config hash for one repo.
type Cache struct {
	dir string
}

// NewCache returns a Cache rooted at repoDir/.strike/container.
func NewCache(repoDir string) *Cache {
	return &Cache{dir: filepath.Join(repoDir, filepath.FromSlash(CacheDirName))}
}

// Dir returns the absolute cache directory path.
func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// EnsureDir creates the cache directory.
func (c *Cache) EnsureDir() error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("container cache: mkdir: %w", err)
	}
	return nil
}

func (c *Cache) writeAtomic(name, content string) error {
	if err := c.EnsureDir(); err != nil {
		return err
	}
	tmp := filepath.Join(c.dir, ".tmp-"+name)
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("container cache: write tmp: %w", err)
	}
	target := filepath.Join(c.dir, name)
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("container cache: rename %s: %w", name, err)
	}
	return nil
}

func (c *Cache) readTrimmed(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, name))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("container cache: read %s: %w", name, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// SetImageID / ImageID persist the built image reference or id.
func (c *Cache) SetImageID(id string) error { return c.writeAtomic("image_id", id) }
func (c *Cache) ImageID() (string, error)   { return c.readTrimmed("image_id") }

// SetContainerID / ContainerID persist the running container id.
func (c *Cache) SetContainerID(id string) error { return c.writeAtomic("container_id", id) }
func (c *Cache) ContainerID() (string, error)   { return c.readTrimmed("container_id") }

// SetNetworkID / NetworkID persist the user-defined network id/name.
func (c *Cache) SetNetworkID(id string) error { return c.writeAtomic("network_id", id) }
func (c *Cache) NetworkID() (string, error)   { return c.readTrimmed("network_id") }

// SetConfigHash / ConfigHash persist the build cache key.
func (c *Cache) SetConfigHash(h string) error { return c.writeAtomic("config.hash", h) }
func (c *Cache) ConfigHash() (string, error)  { return c.readTrimmed("config.hash") }

// ClearRuntime clears container and network ids (keeps image + hash).
func (c *Cache) ClearRuntime() error {
	_ = os.Remove(filepath.Join(c.dir, "container_id"))
	_ = os.Remove(filepath.Join(c.dir, "network_id"))
	return nil
}

// Clean removes the entire cache directory.
func (c *Cache) Clean() error {
	if c == nil || c.dir == "" {
		return nil
	}
	return os.RemoveAll(c.dir)
}

// ComputeConfigHash returns sha256 hex of config JSON + dockerfile body + version.
// Used as the build cache key (rebuild when any input changes).
func ComputeConfigHash(cfg Config, dockerfileBody, version string) (string, error) {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	h := sha256.New()
	_, _ = h.Write(cfgJSON)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(dockerfileBody))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(version))
	return hex.EncodeToString(h.Sum(nil)), nil
}
