package triggers

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
)

type memFiles map[string]host.FileContent

func (m memFiles) ReadScoped(path string) (host.FileContent, error) {
	if fc, ok := m[path]; ok {
		if fc.Path == "" {
			fc.Path = path
		}
		return fc, nil
	}
	return host.FileContent{Path: path, Skip: true, Notice: "not found"}, nil
}

func TestPreparePlainPassThrough(t *testing.T) {
	got := Prepare("hello there", nil, nil)
	if got.Action != ActionPass {
		t.Fatalf("action = %q, want pass", got.Action)
	}
}

func TestPrepareEmailIsNotAMention(t *testing.T) {
	got := Prepare("mail me at user@host.example", memFiles{}, nil)
	if got.Action != ActionPass {
		t.Fatalf("action = %q, want pass for an email", got.Action)
	}
}

func TestPrepareFileMention(t *testing.T) {
	files := memFiles{
		"docs/readme.md": {Path: "docs/readme.md", Content: "hello file"},
	}
	got := Prepare("look at @docs/readme.md please", files, nil)
	if got.Action != ActionText {
		t.Fatalf("action = %q", got.Action)
	}
	if got.Display != "look at @docs/readme.md please" {
		t.Fatalf("display = %q", got.Display)
	}
	if !strings.Contains(got.ModelText, "--- file: docs/readme.md ---") || !strings.Contains(got.ModelText, "hello file") {
		t.Fatalf("model = %q", got.ModelText)
	}
	if strings.Contains(got.Display, "hello file") {
		t.Fatalf("display leaked the file body: %q", got.Display)
	}
}

func TestPrepareFileMentionIdempotent(t *testing.T) {
	body := "look at @docs/readme.md\n\n--- file: docs/readme.md ---\nhello file\n--- end file: docs/readme.md ---"
	files := memFiles{
		"docs/readme.md": {Path: "docs/readme.md", Content: "hello file"},
	}
	got := Prepare(body, files, nil)
	if got.Action != ActionPass {
		t.Fatalf("action = %q, want pass when the fence is already present", got.Action)
	}
}

func TestPrepareFenceBodyIsNotMentionScanned(t *testing.T) {
	body := "review @src/App.java\n\n--- file: src/App.java ---\n    @Override\nSee @other.go\n--- end file: src/App.java ---"
	files := memFiles{
		"src/App.java": {Path: "src/App.java", Content: "    @Override\nSee @other.go\n"},
		"other.go":     {Path: "other.go", Content: "secret-from-fence"},
	}
	got := Prepare(body, files, nil)
	if got.Action != ActionPass || got.ModelText != "" || len(got.Notices) != 0 {
		t.Fatalf("fence body was scanned: %#v", got)
	}
}

func TestPreparePrefixMentionIgnoresFenceBody(t *testing.T) {
	body := "also @notes.md\n\n--- file: src/App.java ---\nSee @other.go\n--- end file: src/App.java ---"
	files := memFiles{
		"notes.md": {Path: "notes.md", Content: "notes body"},
		"other.go": {Path: "other.go", Content: "secret-from-fence"},
	}
	got := Prepare(body, files, nil)
	if got.Action != ActionText {
		t.Fatalf("action = %q", got.Action)
	}
	if !strings.Contains(got.ModelText, "--- file: notes.md ---") || !strings.Contains(got.ModelText, "notes body") {
		t.Fatalf("prefix mention missing: %q", got.ModelText)
	}
	if strings.Contains(got.ModelText, "secret-from-fence") || strings.Contains(got.ModelText, "--- file: other.go ---") {
		t.Fatalf("fence body was attached: %q", got.ModelText)
	}
}

func TestPrepareMissingFileNotice(t *testing.T) {
	got := Prepare("see @missing.go", memFiles{}, nil)
	if got.Action != ActionText {
		t.Fatalf("action = %q", got.Action)
	}
	if len(got.Notices) != 1 || !strings.Contains(got.Notices[0], "not found") {
		t.Fatalf("notices = %#v", got.Notices)
	}
	if strings.Contains(got.ModelText, "--- file:") {
		t.Fatalf("missing file was attached: %q", got.ModelText)
	}
}

func TestPrepareDotDotIsNotAMention(t *testing.T) {
	got := Prepare("read @../secret.txt", memFiles{}, nil)
	if got.Action != ActionPass {
		t.Fatalf("action = %q, want pass", got.Action)
	}
}

func TestPrepareShell(t *testing.T) {
	got := Prepare("  !pwd  ", nil, nil)
	if got.Action != ActionShell || got.Shell != "pwd" || got.Display != "!pwd" {
		t.Fatalf("outcome = %#v", got)
	}
	empty := Prepare("!", nil, nil)
	if empty.Action != ActionError || !strings.Contains(empty.Error, "empty command") {
		t.Fatalf("empty = %#v", empty)
	}
}

func TestPrepareShellDoesNotStartMidSentence(t *testing.T) {
	got := Prepare("please run !pwd", nil, nil)
	if got.Action == ActionShell {
		t.Fatalf("mid-sentence bang ran as shell: %#v", got)
	}
}

func TestPrepareModeAndUnknownCommand(t *testing.T) {
	got := Prepare("/mode yolo", nil, nil)
	if got.Action != ActionOp {
		t.Fatalf("action = %q", got.Action)
	}
	mode, ok := got.Op.(protocol.SetPermissionMode)
	if !ok || mode.Mode != protocol.PermissionModeYolo {
		t.Fatalf("op = %#v", got.Op)
	}
	unknown := Prepare("/pets panda", nil, nil)
	if unknown.Action != ActionError || !strings.Contains(unknown.Error, "unknown command") {
		t.Fatalf("unknown = %#v", unknown)
	}
	bare := Prepare("/mode", nil, nil)
	if bare.Action != ActionError || !strings.Contains(bare.Error, "usage:") {
		t.Fatalf("bare = %#v", bare)
	}
}

func TestPrepareCatalogInserts(t *testing.T) {
	for _, cmd := range Commands() {
		got := Prepare(cmd.Insert, nil, nil)
		switch got.Action {
		case ActionOp:
			if got.Op == nil {
				t.Fatalf("%s: nil op", cmd.ID)
			}
		case ActionError:
			if !strings.Contains(got.Error, "usage:") {
				t.Fatalf("%s: error %q, want a usage error for an incomplete insert", cmd.ID, got.Error)
			}
		default:
			t.Fatalf("%s: action %q, want op or usage", cmd.ID, got.Action)
		}
	}
}

func TestPrepareSkillAndDollar(t *testing.T) {
	skills := []Skill{{
		Name:        "commit",
		Description: "commit the work",
		Render: func(args string) string {
			return "Commit template\n\nArguments: " + args
		},
	}}
	got := Prepare("/commit the fix", nil, skills)
	if got.Action != ActionText {
		t.Fatalf("slash skill action = %q", got.Action)
	}
	if got.Display != "/commit the fix" {
		t.Fatalf("display = %q", got.Display)
	}
	if !strings.Contains(got.ModelText, "Arguments: the fix") {
		t.Fatalf("model = %q", got.ModelText)
	}
	dollar := Prepare("$commit the fix", nil, skills)
	if dollar.Action != ActionText || !strings.Contains(dollar.ModelText, "Arguments: the fix") {
		t.Fatalf("dollar = %#v", dollar)
	}
	literal := Prepare("$not-a-skill stays", nil, skills)
	if literal.Action != ActionPass {
		t.Fatalf("unknown dollar action = %q, want pass", literal.Action)
	}
	money := Prepare("price is $5", nil, skills)
	if money.Action != ActionPass {
		t.Fatalf("money action = %q", money.Action)
	}
}

func TestPrepareSkillExpandsFileMentions(t *testing.T) {
	files := memFiles{"notes.md": {Content: "body"}}
	skills := []Skill{{
		Name: "look",
		Render: func(string) string {
			return "read @notes.md"
		},
	}}
	got := Prepare("$look", files, skills)
	if got.Action != ActionText || !strings.Contains(got.ModelText, "--- file: notes.md ---") {
		t.Fatalf("outcome = %#v", got)
	}
	if got.Display != "$look" {
		t.Fatalf("display = %q", got.Display)
	}
}

func TestPrepareStopAndRewind(t *testing.T) {
	stop := Prepare("/stop", nil, nil)
	if _, ok := stop.Op.(protocol.Interrupt); !ok || stop.Action != ActionOp {
		t.Fatalf("stop = %#v", stop)
	}
	files := Prepare("/rewind files", nil, nil)
	rw, ok := files.Op.(protocol.Rewind)
	if !ok || !rw.RestoreFiles {
		t.Fatalf("rewind = %#v", files)
	}
}
