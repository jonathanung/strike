package main

import (
	"os"
	"strings"
	"sync"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/tools"
)

// liveSkills is the admitted skill catalog, read again from disk when it is
// stale. A skill installed while the process runs (saved in Customize, or
// an accepted skill proposal, #1958) is then invocable in the next input
// and by the skill tool without a restart. Admission runs on every read,
// so a reloaded skill passes the same policy as one loaded at start. A
// failed read keeps the last good catalog.
type liveSkills struct {
	mu   sync.Mutex
	load func() ([]config.Skill, error)
	cur  []config.Skill
}

func newLiveSkills(initial []config.Skill, load func() ([]config.Skill, error)) *liveSkills {
	return &liveSkills{load: load, cur: append([]config.Skill(nil), initial...)}
}

// ForInput returns the catalog for one composer input. Ordinary text reads
// nothing from disk. Input that invokes a skill (/name or $name) reloads
// the catalog when that skill is not cached, or its file is gone.
func (l *liveSkills) ForInput(text string) []config.Skill {
	l.mu.Lock()
	defer l.mu.Unlock()
	if name := invokedSkill(text); name != "" && l.staleLocked(name) {
		_ = l.reloadLocked()
	}
	return append([]config.Skill(nil), l.cur...)
}

// invokedSkill is the skill name a composer input would invoke, or "".
func invokedSkill(text string) string {
	raw := strings.TrimSpace(text)
	if !strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "$") {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return fields[0][1:]
}

func (l *liveSkills) staleLocked(name string) bool {
	for _, s := range l.cur {
		if s.Name != name {
			continue
		}
		if s.Path == "" {
			return false
		}
		_, err := os.Stat(s.Path)
		return err != nil
	}
	return true
}

func (l *liveSkills) reloadLocked() error {
	if l.load == nil {
		return nil
	}
	next, err := l.load()
	if err != nil {
		return err
	}
	l.cur = append([]config.Skill(nil), next...)
	return nil
}

// Reload implements tools.SkillLoader. The skill tool calls it once per
// call, which is rare next to composer input.
func (l *liveSkills) Reload() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reloadLocked()
}

// Skills implements tools.SkillLoader with the last loaded catalog.
func (l *liveSkills) Skills() []tools.SkillInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return skillInfosOf(l.cur)
}

func skillInfosOf(skills []config.Skill) []tools.SkillInfo {
	out := make([]tools.SkillInfo, len(skills))
	for i, s := range skills {
		out[i] = tools.SkillInfo{Name: s.Name, Description: s.Description, Template: s.Template, Path: s.Path}
	}
	return out
}
