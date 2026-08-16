package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkillSourceRoot returns the directory that bounds adjacent resource reads
// for a skill (directory containing the skill file, or the skill dir itself
// when path is …/skills/name/SKILL.md). Empty for builtins.
func SkillSourceRoot(skill Skill) string {
	p := strings.TrimSpace(skill.Path)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	base := filepath.Base(abs)
	dir := filepath.Dir(abs)
	// Conventional layout: skills/<name>/SKILL.md → root is skills/<name>
	if strings.EqualFold(base, "SKILL.md") || strings.EqualFold(base, "skill.md") {
		return dir
	}
	// Flat skills/<name>.md → root is skills/
	return dir
}

// ResolveSkillResource resolves rel against the skill source root with
// traversal protection. rel must be relative; absolute paths and ".." escape
// attempts are rejected.
func ResolveSkillResource(skill Skill, rel string) (string, error) {
	root := SkillSourceRoot(skill)
	if root == "" {
		return "", fmt.Errorf("skill %q has no source root", skill.Name)
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("empty resource path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute resource paths are not allowed")
	}
	// Clean and reject .. escape.
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resource path escapes skill root")
	}
	full := filepath.Join(root, clean)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// Ensure fullAbs is under rootAbs.
	sep := string(filepath.Separator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+sep) {
		return "", fmt.Errorf("resource path escapes skill root")
	}
	return fullAbs, nil
}

// ReadSkillResource reads an adjacent file under the skill root (bounded).
const maxSkillResourceBytes = 256 << 10 // 256 KiB

func ReadSkillResource(skill Skill, rel string) ([]byte, error) {
	path, err := ResolveSkillResource(skill, rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("resource is a directory")
	}
	if fi.Size() > maxSkillResourceBytes {
		return nil, fmt.Errorf("resource exceeds %d bytes", maxSkillResourceBytes)
	}
	return os.ReadFile(path)
}

// SkillCatalog is a reloadable skill list for a workdir.
type SkillCatalog struct {
	workDir string
	skills  []Skill
}

// NewSkillCatalog loads skills for workDir.
func NewSkillCatalog(workDir string) (*SkillCatalog, error) {
	skills, err := LoadSkillsWithError(workDir)
	if err != nil {
		return nil, err
	}
	return &SkillCatalog{workDir: workDir, skills: skills}, nil
}

// Skills returns a copy of the current catalog.
func (c *SkillCatalog) Skills() []Skill {
	if c == nil {
		return nil
	}
	out := make([]Skill, len(c.skills))
	copy(out, c.skills)
	return out
}

// Reload re-reads skills from disk without requiring a process restart.
// Active sessions can swap tool catalogs from the returned snapshot.
func (c *SkillCatalog) Reload() error {
	if c == nil {
		return fmt.Errorf("nil skill catalog")
	}
	skills, err := LoadSkillsWithError(c.workDir)
	if err != nil {
		return err
	}
	c.skills = skills
	return nil
}

// Lookup returns a skill by name.
func (c *SkillCatalog) Lookup(name string) (Skill, bool) {
	if c == nil {
		return Skill{}, false
	}
	for _, s := range c.skills {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}
