package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestComposerPrepareFileSkillAndMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("from disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills := []config.Skill{{
		Name:     "commit",
		Template: "Ship it.\n\n$ARGUMENTS",
	}}

	file := composerPrepare("see @notes.txt", dir, nil)
	if file.Action != engine.InputText {
		t.Fatalf("file action = %q", file.Action)
	}
	if !strings.Contains(file.ModelText, "from disk") || strings.Contains(file.Display, "from disk") {
		t.Fatalf("file prep = %#v", file)
	}

	mode := composerPrepare("/mode plan", dir, nil)
	if mode.Action != engine.InputOp {
		t.Fatalf("mode action = %q", mode.Action)
	}
	if got, ok := mode.Op.(protocol.SetPermissionMode); !ok || got.Mode != protocol.PermissionModePlan {
		t.Fatalf("mode op = %#v", mode.Op)
	}

	skill := composerPrepare("$commit the notes", dir, skills)
	if skill.Action != engine.InputText || !strings.Contains(skill.ModelText, "the notes") {
		t.Fatalf("skill = %#v", skill)
	}
	if skill.Display != "$commit the notes" {
		t.Fatalf("display = %q", skill.Display)
	}

	bang := composerPrepare("!echo hi", dir, nil)
	if bang.Action != engine.InputShell || bang.Shell != "echo hi" {
		t.Fatalf("bang = %#v", bang)
	}
}
