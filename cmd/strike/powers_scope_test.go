package main

import (
	"testing"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/tools"
)

func TestLateToolVisible(t *testing.T) {
	var l lateToolVisible
	if !l.check("s1", "mcp_docs_x") {
		t.Fatal("unset check must allow every tool")
	}
	l.set(func(session, name string) bool { return session == "s1" })
	if !l.check("s1", "mcp_docs_x") || l.check("s2", "mcp_docs_x") {
		t.Fatal("check did not delegate to the set function")
	}
}

func TestBindPowersSkillsWithoutSkillTool(t *testing.T) {
	// No skill tool registered: nothing to bind, and no panic.
	bindPowersSkills(tool.NewRegistry(), nil)
	reg := tool.NewRegistry(tools.NewSkill(nil))
	bindPowersSkills(reg, nil) // nil scope reports no skills
	sk, _ := reg.Get("skill")
	d, ok := sk.(tool.SessionDescriber)
	if !ok {
		t.Fatal("skill tool is not a SessionDescriber")
	}
	if got := d.DescriptionFor("s1"); got == "" {
		t.Fatal("bound skill tool should describe per session")
	}
}
