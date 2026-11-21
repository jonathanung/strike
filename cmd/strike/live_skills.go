package main

import (
	"sync"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/tools"
)

// liveSkills is the admitted skill catalog, read again from disk on use. A
// skill installed while the process runs (saved in Customize, or an
// accepted skill proposal, #1958) is then invocable in the next input and
// by the skill tool without a restart. Admission runs on every read, so a
// reloaded skill passes the same policy as one loaded at start. A failed
// read keeps the last good catalog.
type liveSkills struct {
	mu   sync.Mutex
	load func() ([]config.Skill, error)
	cur  []config.Skill
}

func newLiveSkills(initial []config.Skill, load func() ([]config.Skill, error)) *liveSkills {
	return &liveSkills{load: load, cur: append([]config.Skill(nil), initial...)}
}

// Current re-reads the catalog and returns a copy of it.
func (l *liveSkills) Current() []config.Skill {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reloadLocked()
	return append([]config.Skill(nil), l.cur...)
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

// Reload implements tools.SkillLoader.
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
