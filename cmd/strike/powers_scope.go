package main

import (
	"sync/atomic"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/tools"
)

// lateToolVisible lets root engines take a tool-visibility check before
// Powers are assembled (#1943): engines are built first, Powers after the MCP
// manager. Until set, every tool is visible; no pack can be active then.
type lateToolVisible struct {
	fn atomic.Pointer[func(session, name string) bool]
}

func (l *lateToolVisible) set(fn func(session, name string) bool) {
	l.fn.Store(&fn)
}

// check is the engine.Options.ToolVisible hook.
func (l *lateToolVisible) check(session, name string) bool {
	if f := l.fn.Load(); f != nil {
		return (*f)(session, name)
	}
	return true
}

// bindPowersSkills makes the registry's skill tool list and load the skills
// a session's active packs supply, in that session only.
func bindPowersSkills(reg *tool.Registry, scope *local.PowersScope) {
	t, ok := reg.Get("skill")
	if !ok {
		return
	}
	setter, ok := t.(tools.SessionSkillSetter)
	if !ok {
		return
	}
	setter.SetSessionSkills(func(rootSession string) []tools.SkillInfo {
		skills := scope.Skills(rootSession)
		out := make([]tools.SkillInfo, 0, len(skills))
		for _, s := range skills {
			out = append(out, tools.SkillInfo{Name: s.Name, Description: s.Description, Template: s.Template, Path: s.Path})
		}
		return out
	})
}
