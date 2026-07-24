package config

import (
	"os"
	"path/filepath"
	"testing"
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
	if len(agents) != 1 || agents[0].Name != "reviewer" || agents[0].Provider != "openai" || agents[0].Prompt != "Review carefully." {
		t.Errorf("agents = %+v", agents)
	}

	skills := LoadSkills(work)
	if len(skills) != 1 || skills[0].Name != "commit" {
		t.Fatalf("skills = %+v", skills)
	}
	if got := skills[0].Render("with a good message"); got != "Commit the changes: with a good message" {
		t.Errorf("render = %q", got)
	}
	if got := skills[0].Render(""); got != "Commit the changes: " {
		t.Errorf("render empty = %q", got)
	}
}
