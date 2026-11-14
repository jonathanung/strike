package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateLoadHome(t *testing.T) (home, work string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ResetPluginCache()
	return home, t.TempDir()
}

func diagFor(diags []LoadDiagnostic, file, code string) (LoadDiagnostic, bool) {
	for _, d := range diags {
		if filepath.Base(d.Path) == file && d.Code == code {
			return d, true
		}
	}
	return LoadDiagnostic{}, false
}

func TestLoadSkillsReportDiagnostics(t *testing.T) {
	cases := []struct {
		name      string
		rel       string // relative to work dir
		body      string
		code      string
		severity  string
		fatal     bool
		wantErr   bool
		wantSkill string // skill expected to load ("" = not checked)
		noSkill   string // skill expected NOT to load
	}{
		{
			name: "empty body", rel: ".strike/skills/hollow.md",
			body: "---\ndescription: nothing here\n---\n",
			code: DiagEmpty, severity: "warning", noSkill: "hollow",
		},
		{
			name: "unterminated frontmatter", rel: ".strike/skills/open.md",
			body: "---\ndescription: never closed\nDo the thing.\n",
			code: DiagMalformedFrontmatter, severity: "warning", wantSkill: "open",
		},
		{
			name: "crlf frontmatter", rel: ".strike/skills/crlf.md",
			body: "---\r\ndescription: windows\r\n---\r\nBody.\r\n",
			code: DiagMalformedFrontmatter, severity: "warning", wantSkill: "crlf",
		},
		{
			name: "missing description", rel: ".strike/skills/nodesc.md",
			body: "---\nname: nodesc\n---\nBody.\n",
			code: DiagMissingDescription, severity: "warning", wantSkill: "nodesc",
		},
		{
			name: "strict invalid name fails whole load", rel: ".strike/skills/bad.md",
			body: "---\nname: has space\ndescription: x\n---\nBody.\n",
			code: DiagInvalid, severity: "error", fatal: true, wantErr: true,
		},
		{
			name: "external invalid name is skipped", rel: ".claude/skills/bad.md",
			body: "---\nname: has space\ndescription: x\n---\nBody.\n",
			code: DiagSkipped, severity: "warning", wantSkill: "good",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, work := isolateLoadHome(t)
			writeMD(t, filepath.Join(work, ".strike", "skills", "good.md"), "---\ndescription: fine\n---\nGood.\n")
			writeMD(t, filepath.Join(work, filepath.FromSlash(tc.rel)), tc.body)

			skills, diags, err := LoadSkillsReport(work)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			d, ok := diagFor(diags, filepath.Base(tc.rel), tc.code)
			if !ok {
				t.Fatalf("no %s diagnostic for %s in %v", tc.code, tc.rel, diags)
			}
			if d.Kind != "skill" || d.Severity != tc.severity || d.Fatal != tc.fatal || d.Fingerprint == "" || d.Message == "" {
				t.Fatalf("diag = %+v", d)
			}
			if tc.fatal && !strings.Contains(d.Message, "fails the whole skill load") {
				t.Fatalf("fatal message = %q", d.Message)
			}
			if tc.wantErr && skills != nil {
				t.Fatalf("strict failure should still drop all skills, got %v", skillNameList(skills))
			}
			if tc.wantSkill != "" {
				if _, ok := lookupSkill(skills, tc.wantSkill); !ok {
					t.Fatalf("skills = %v, missing %s", skillNameList(skills), tc.wantSkill)
				}
			}
			if tc.noSkill != "" {
				if _, ok := lookupSkill(skills, tc.noSkill); ok {
					t.Fatalf("%s should not load", tc.noSkill)
				}
			}
			// Unchanged semantics: the plain loader returns the same error.
			_, plainErr := LoadSkillsWithError(work)
			if (plainErr != nil) != tc.wantErr {
				t.Fatalf("LoadSkillsWithError err = %v", plainErr)
			}
		})
	}
}

func TestLoadSkillsReportCleanTreeHasNoDiagnostics(t *testing.T) {
	_, work := isolateLoadHome(t)
	writeMD(t, filepath.Join(work, ".strike", "skills", "good.md"), "---\ndescription: fine\n---\nGood.\n")
	_, diags, err := LoadSkillsReport(work)
	if err != nil || len(diags) != 0 {
		t.Fatalf("diags = %v err = %v", diags, err)
	}
}

func TestLoadSkillsReportUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-000 files")
	}
	_, work := isolateLoadHome(t)
	path := filepath.Join(work, ".strike", "skills", "locked.md")
	writeMD(t, path, "---\ndescription: x\n---\nBody.\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	skills, diags, err := LoadSkillsReport(work)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := diagFor(diags, "locked.md", DiagUnreadable)
	if !ok || d.Severity != "error" || !strings.Contains(d.Message, "permission denied") {
		t.Fatalf("diags = %v", diags)
	}
	if _, ok := lookupSkill(skills, "locked"); ok {
		t.Fatal("unreadable skill loaded")
	}
}

func TestLoadAgentsReportDiagnostics(t *testing.T) {
	cases := []struct {
		name    string
		rel     string
		body    string
		code    string
		fatal   bool
		wantErr bool
	}{
		{"strict bad effort", ".strike/agents/fast.md", "---\neffort: warp\n---\nPrompt.\n", DiagInvalid, true, true},
		{"external bad effort", ".claude/agents/fast.md", "---\neffort: warp\n---\nPrompt.\n", DiagSkipped, false, false},
		{"empty agent", ".strike/agents/hollow.md", "---\ndescription: x\n---\n", DiagEmpty, false, false},
		{"unterminated agent", ".strike/agents/open.md", "---\ndescription: x\nPrompt.\n", DiagMalformedFrontmatter, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, work := isolateLoadHome(t)
			writeMD(t, filepath.Join(work, filepath.FromSlash(tc.rel)), tc.body)
			agents, diags, err := LoadAgentsReport(work)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			d, ok := diagFor(diags, filepath.Base(tc.rel), tc.code)
			if !ok || d.Kind != "agent" || d.Fatal != tc.fatal {
				t.Fatalf("diags = %v", diags)
			}
			if tc.wantErr && agents != nil {
				t.Fatal("strict agent failure should drop all agents")
			}
			if !tc.wantErr && len(agents) == 0 {
				t.Fatal("builtins should still load")
			}
		})
	}
}

func TestLoadDiagnosticsNeverCarryFileContents(t *testing.T) {
	_, work := isolateLoadHome(t)
	const marker = "BODY-MARKER-do-not-echo"
	writeMD(t, filepath.Join(work, ".strike", "skills", "open.md"), "---\nnote: "+marker+"\n"+marker+"\n")
	writeMD(t, filepath.Join(work, ".strike", "skills", "hollow.md"), "---\nnote: "+marker+"\n---\n")
	_, diags, _ := LoadSkillsReport(work)
	if len(diags) == 0 {
		t.Fatal("expected diagnostics")
	}
	for _, d := range diags {
		if strings.Contains(d.String(), "BODY-MARKER") {
			t.Fatalf("diagnostic leaked file contents: %s", d)
		}
	}
}

func TestLoadDiagnosticFingerprintTracksFileChanges(t *testing.T) {
	_, work := isolateLoadHome(t)
	path := filepath.Join(work, ".strike", "skills", "nodesc.md")
	writeMD(t, path, "---\nname: nodesc\n---\nBody.\n")
	fp := func() string {
		_, diags, _ := LoadSkillsReport(work)
		d, ok := diagFor(diags, "nodesc.md", DiagMissingDescription)
		if !ok {
			t.Fatalf("diags = %v", diags)
		}
		return d.Fingerprint
	}
	first := fp()
	if again := fp(); again != first {
		t.Fatalf("unchanged file fingerprint moved: %s vs %s", first, again)
	}
	writeMD(t, path, "---\nname: nodesc\n---\nA longer body now.\n")
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if changed := fp(); changed == first {
		t.Fatal("edited file kept the same fingerprint")
	}
}

func TestLoadSkillsReportPluginSkillFailure(t *testing.T) {
	_, work := isolateLoadHome(t)
	writeTree(t, filepath.Join(work, ".strike", "plugins", "acme.bad"), map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.bad",
  "version": "1.0.0",
  "name": "Bad",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "skills": [{ "path": "skills/broken.md" }, { "path": "skills/hollow.md" }]
  }
}`,
		"skills/broken.md": "---\nname: has space\ndescription: x\n---\nBody.\n",
		"skills/hollow.md": "---\ndescription: x\n---\n",
	})
	_, diags, err := LoadSkillsReport(work)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := diagFor(diags, "broken.md", DiagSkipped); !ok || !strings.Contains(d.Message, "acme.bad") || d.Fatal {
		t.Fatalf("diags = %v", diags)
	}
	if _, ok := diagFor(diags, "hollow.md", DiagEmpty); !ok {
		t.Fatalf("diags = %v", diags)
	}
}

func TestLoadSkillsReportNoiseRules(t *testing.T) {
	_, work := isolateLoadHome(t)
	// Claude command files: description optional, no warning.
	writeMD(t, filepath.Join(work, ".claude", "commands", "deploy.md"), "Deploy the app.\n")
	// Malformed frontmatter reports once, not also missing_description.
	writeMD(t, filepath.Join(work, ".strike", "skills", "open.md"), "---\nname: open\nBody.\n")
	// No frontmatter at all in a skills root still warns.
	writeMD(t, filepath.Join(work, ".strike", "skills", "plain.md"), "Just a body.\n")
	_, diags, err := LoadSkillsReport(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if filepath.Base(d.Path) == "deploy.md" {
			t.Fatalf("command file warned: %s", d)
		}
	}
	if _, ok := diagFor(diags, "open.md", DiagMissingDescription); ok {
		t.Fatalf("malformed file also got missing_description: %v", diags)
	}
	if _, ok := diagFor(diags, "open.md", DiagMalformedFrontmatter); !ok {
		t.Fatalf("diags = %v", diags)
	}
	if _, ok := diagFor(diags, "plain.md", DiagMissingDescription); !ok {
		t.Fatalf("diags = %v", diags)
	}
}

func TestLoadDiagnosticsInvalidPathsOmitBody(t *testing.T) {
	home, work := isolateLoadHome(t)
	const marker = "BODY-MARKER-do-not-echo"
	// Global external root loads before the project strict root.
	writeMD(t, filepath.Join(home, ".claude", "skills", "ext.md"), "---\nname: has space\ndescription: x\n---\n"+marker+"\n")
	writeMD(t, filepath.Join(work, ".strike", "skills", "strict.md"), "---\nname: also bad\ndescription: x\n---\n"+marker+"\n")
	_, diags, _ := LoadSkillsReport(work)
	var codes []string
	for _, d := range diags {
		codes = append(codes, d.Code)
		if strings.Contains(d.String(), marker) {
			t.Fatalf("diagnostic leaked body: %s", d)
		}
	}
	if _, ok := diagFor(diags, "strict.md", DiagInvalid); !ok {
		t.Fatalf("codes = %v", codes)
	}
	if _, ok := diagFor(diags, "ext.md", DiagSkipped); !ok {
		t.Fatalf("codes = %v", codes)
	}
}
