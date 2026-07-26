package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
)

func TestParseFrontmatter(t *testing.T) {
	meta, body := parseFrontmatter("---\ndescription: reviews code\nmodel: gpt-5.5\n---\nYou are a reviewer.")
	if meta["description"] != "reviews code" || meta["model"] != "gpt-5.5" {
		t.Errorf("meta = %v", meta)
	}
	if body != "You are a reviewer." {
		t.Errorf("body = %q", body)
	}

	meta, body = parseFrontmatter("no frontmatter here")
	if meta != nil || body != "no frontmatter here" {
		t.Errorf("plain doc: meta=%v body=%q", meta, body)
	}
}

func TestLoadAgentsAndSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("---\ndescription: code review\nprovider: openai\n---\nReview carefully."), 0o644)

	skillDir := filepath.Join(work, ".strike", "skills")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "commit.md"), []byte("---\ndescription: write a commit\n---\nCommit the changes: $ARGUMENTS"), 0o644)

	agents := LoadAgents(work)
	byName := map[string]Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	// Built-ins always present.
	for _, want := range []string{"build", "plan", "explore", "general", "commit", "reviewer", "tester", "debugger"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("missing builtin/project agent %q among %+v", want, agents)
		}
	}
	// Project reviewer overrides the builtin (openai pin + custom body).
	rev := byName["reviewer"]
	if rev.Provider != "openai" || rev.Prompt != "Review carefully." {
		t.Errorf("project reviewer override = %+v", rev)
	}
	if byName["build"].Name != "build" || byName["explore"].Prompt == "" {
		t.Errorf("builtins malformed: build=%+v explore=%+v", byName["build"], byName["explore"])
	}

	skills := LoadSkills(work)
	var commit *Skill
	for i := range skills {
		if skills[i].Name == "commit" {
			commit = &skills[i]
			break
		}
	}
	if commit == nil {
		t.Fatalf("skills = %+v, want overridden commit", skills)
	}
	if got := commit.Render("with a good message"); got != "Commit the changes: with a good message" {
		t.Errorf("render = %q", got)
	}
	if got := commit.Render(""); got != "Commit the changes: " {
		t.Errorf("render empty = %q", got)
	}
	// Builtins still load alongside the project override.
	if !skillNames(skills)["ship"] {
		t.Errorf("expected builtin ship among %+v", skills)
	}
}

func skillNames(skills []Skill) map[string]bool {
	m := make(map[string]bool, len(skills))
	for _, s := range skills {
		m[s.Name] = true
	}
	return m
}

func TestLoadAgentsWithErrorRejectsUnsafeNames(t *testing.T) {
	tests := []struct {
		name      string
		agent     string
		frontName bool
		detail    string
	}{
		{name: "OSC52", agent: "review\x1b]52;c;dGVzdA==\x07", detail: "control character"},
		{name: "escape", agent: "review\x1bname", detail: "control character"},
		{name: "bell", agent: "review\x07name", detail: "control character"},
		{name: "newline", agent: "review\nname", detail: "control character"},
		{name: "tab", agent: "review\tname", detail: "control character"},
		{name: "C1 control", agent: "review\u009bname", frontName: true, detail: "control character"},
		{name: "invalid UTF-8", agent: "review" + string([]byte{0xff}), frontName: true, detail: "not valid UTF-8"},
		{name: "leading whitespace", agent: " reviewer", detail: "leading or trailing whitespace"},
		{name: "trailing whitespace", agent: "reviewer ", detail: "leading or trailing whitespace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			work := t.TempDir()
			dir := filepath.Join(work, ".strike", "agents")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			fileName := tt.agent
			body := "agent prompt"
			if tt.frontName {
				fileName = "reviewer"
				body = "---\nname: " + tt.agent + "\n---\nagent prompt"
			}
			path := filepath.Join(dir, fileName+".md")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			agents, err := LoadAgentsWithError(work)
			if err == nil {
				t.Fatalf("LoadAgentsWithError() = %+v, nil; want rejection", agents)
			}
			if !strings.Contains(err.Error(), "load agent "+path) || !strings.Contains(err.Error(), tt.detail) {
				t.Errorf("LoadAgentsWithError() error = %q, want path and detail %q", err, tt.detail)
			}
		})
	}
}

func TestLoadAgentsWithErrorAcceptsMultiWordUnicodeAndProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	name := "代码 审查员"
	globalDir := filepath.Join(home, ".strike", "agents")
	projectDir := filepath.Join(work, ".strike", "agents")
	for _, dir := range []string{globalDir, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(globalDir, name+".md"), []byte("---\ndescription: global\nprovider: old\n---\nglobal prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, name+".md"), []byte("---\ndescription: project override\nprovider: openai\nmodel: secure\n---\nproject prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError() error = %v", err)
	}
	got, ok := lookupAgent(agents, name)
	if !ok {
		t.Fatalf("agents = %v, missing %q", agentNames(agents), name)
	}
	want := Agent{Name: name, Description: "project override", Provider: "openai", Model: "secure", Prompt: "project prompt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overridden agent = %+v, want %+v", got, want)
	}
}

func TestLoadAgentsPermissionsCompact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
description: code review
permission.write: deny
permission.edit: deny
permission.bash: deny
---
Review carefully.
`
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "reviewer")
	if !ok {
		t.Fatalf("missing reviewer among %v", agentNames(agents))
	}
	// Compact keys are sorted by permission name: bash, edit, write.
	want := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
		{Permission: "edit", Pattern: "*", Action: permission.Deny},
		{Permission: "write", Pattern: "*", Action: permission.Deny},
	}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("Permissions = %#v, want %#v", got.Permissions, want)
	}
	if got.Description != "code review" || got.Prompt != "Review carefully." {
		t.Errorf("agent meta/prompt = %+v", got)
	}
}

func TestLoadAgentsPermissionsJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
description: patterned
permissions: [{"permission":"bash","pattern":"git *","action":"allow"},{"permission":"write","pattern":"*","action":"deny"}]
---
JSON perms.
`
	if err := os.WriteFile(filepath.Join(dir, "patterned.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "patterned")
	if !ok {
		t.Fatalf("missing patterned among %v", agentNames(agents))
	}
	want := permission.Ruleset{
		{Permission: "bash", Pattern: "git *", Action: permission.Allow},
		{Permission: "write", Pattern: "*", Action: permission.Deny},
	}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("Permissions = %#v, want %#v", got.Permissions, want)
	}
}

func TestLoadAgentsPermissionsRejectUnknownTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bad.md")
	body := "---\npermission.nope: deny\n---\nbody\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAgentsWithError(work)
	if err == nil {
		t.Fatal("LoadAgentsWithError() = nil error, want rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("error %q missing path %q", msg, path)
	}
	if !strings.Contains(msg, "nope") {
		t.Errorf("error %q missing unknown tool detail", msg)
	}
}

func TestLoadAgentsPermissionsRejectBadAction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bad-action.md")
	body := "---\npermission.write: sometimes\n---\nbody\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAgentsWithError(work)
	if err == nil {
		t.Fatal("LoadAgentsWithError() = nil error, want rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("error %q missing path %q", msg, path)
	}
	if !strings.Contains(msg, "sometimes") && !strings.Contains(msg, "action") && !strings.Contains(msg, "write") {
		t.Errorf("error %q missing useful bad-action detail", msg)
	}
}

func TestLoadAgentsPermissionsRejectBadJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bad-json.md")
	body := "---\npermissions: not-json\n---\nbody\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAgentsWithError(work)
	if err == nil {
		t.Fatal("LoadAgentsWithError() = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q missing path %q", err, path)
	}
}

func TestLoadAgentsPermissionsCompactAndJSONMerge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Compact rules first (sorted), then JSON rules appended — last-match-wins
	// means the JSON write allow overrides the compact write deny when evaluated alone.
	body := `---
description: merge
permission.write: deny
permission.edit: deny
permissions: [{"permission":"write","pattern":"*","action":"allow"}]
---
Merged.
`
	if err := os.WriteFile(filepath.Join(dir, "merge.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "merge")
	if !ok {
		t.Fatalf("missing merge among %v", agentNames(agents))
	}
	want := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.Deny},
		{Permission: "write", Pattern: "*", Action: permission.Deny},
		{Permission: "write", Pattern: "*", Action: permission.Allow},
	}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("Permissions = %#v, want %#v", got.Permissions, want)
	}
	// Sanity: last-match-wins on the agent ruleset alone allows write.
	if ev := permission.Evaluate("write", "x.go", got.Permissions); ev != permission.Allow {
		t.Errorf("Evaluate write on merged rules = %q, want allow", ev)
	}
}

func lookupAgent(agents []Agent, name string) (Agent, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

func TestLoadSkillsWithErrorRejectsUnsafeAndReservedNamesClearly(t *testing.T) {
	tests := []struct {
		name       string
		fileName   string
		frontName  string
		wantDetail string
	}{
		{name: "empty", fileName: ".md", wantDetail: "skill name is empty"},
		{name: "reserved", fileName: "help.md", wantDetail: `skill name "help" is reserved`},
		{name: "reserved md-read", fileName: "md-read.md", wantDetail: `skill name "md-read" is reserved`},
		{name: "whitespace", fileName: "two words.md", wantDetail: "whitespace or a control character"},
		{name: "slash", fileName: "slash.md", frontName: "bad/name", wantDetail: "contains '/'"},
		{name: "control", fileName: "control.md", frontName: "bad\u0007name", wantDetail: "whitespace or a control character"},
		{name: "OSC52", fileName: "skill\x1b]52;c;dGVzdA==\x07.md", wantDetail: "whitespace or a control character"},
		{name: "escape", fileName: "skill\x1bname.md", wantDetail: "whitespace or a control character"},
		{name: "newline", fileName: "skill\nname.md", wantDetail: "whitespace or a control character"},
		{name: "tab", fileName: "skill\tname.md", wantDetail: "whitespace or a control character"},
		{name: "C1 control", fileName: "skill.md", frontName: "skill\u009bname", wantDetail: "whitespace or a control character"},
		{name: "invalid UTF-8", fileName: "skill.md", frontName: "skill" + string([]byte{0xff}), wantDetail: "not valid UTF-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			work := t.TempDir()
			dir := filepath.Join(work, ".strike", "skills")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := "template"
			if tt.frontName != "" {
				body = "---\nname: " + tt.frontName + "\n---\ntemplate"
			}
			path := filepath.Join(dir, tt.fileName)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			skills, err := LoadSkillsWithError(work)
			if err == nil {
				t.Fatalf("LoadSkillsWithError() = %+v, nil; want rejection", skills)
			}
			if !strings.Contains(err.Error(), "load skill "+path) || !strings.Contains(err.Error(), tt.wantDetail) {
				t.Errorf("LoadSkillsWithError() error = %q, want path and detail %q", err, tt.wantDetail)
			}
		})
	}
}

func TestLoadSkillsWithErrorAcceptsUnicodeHyphenUnderscoreAndProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	name := "部署-tool_v2"
	globalDir := filepath.Join(home, ".strike", "skills")
	projectDir := filepath.Join(work, ".strike", "skills")
	for _, dir := range []string{globalDir, projectDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(globalDir, name+".md"), []byte("global template"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, name+".md"), []byte("---\ndescription: project override\n---\nproject template"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError() error = %v", err)
	}
	var got *Skill
	for i := range skills {
		if skills[i].Name == name {
			got = &skills[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("skills = %+v, want overridden %q", skills, name)
	}
	if got.Description != "project override" || got.Template != "project template" {
		t.Errorf("overridden skill = %+v, want valid project definition for %q", got, name)
	}
}
