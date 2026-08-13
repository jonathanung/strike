package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const strikeCLIDir = "com.strike.cli"

func skipStrikeCLI(m Manifest) bool {
	return m.StrikeCLI != nil && m.StrikeCLI.SkipContributions
}

// discoverStrikeCLIContributions loads Strike-only passive files from
// com.strike.cli/ (agents, extra skills, workflows, themes, providers, panes).
// portableSkills are APS skills/<dir>/SKILL.md refs already discovered; extras
// with the same public name are skipped (portable wins).
func discoverStrikeCLIContributions(root string, base Diagnostic, portableSkills []FileRef) (agents, skills, workflows, themes, providers, panes []FileRef, diags []Diagnostic) {
	_, missing, err := resolveStrikeCLIDir(root)
	if err != nil {
		d := base
		d.Severity = SeverityError
		d.Code = "path"
		d.Path = strikeCLIDir
		d.Message = err.Error()
		return nil, nil, nil, nil, nil, nil, []Diagnostic{d}
	}
	if missing {
		return nil, nil, nil, nil, nil, nil, nil
	}

	agents, d1 := discoverStrikeCLIFiles(root, "agents", []string{".md"}, false, base)
	diags = append(diags, d1...)
	extraSkills, d2 := discoverStrikeCLIFiles(root, "skills", []string{".md"}, false, base)
	diags = append(diags, d2...)
	workflows, d3 := discoverStrikeCLIFiles(root, "workflows", []string{".json"}, false, base)
	diags = append(diags, d3...)
	themes, d4 := discoverStrikeCLIFiles(root, "themes", []string{".json"}, false, base)
	diags = append(diags, d4...)
	providers, d5 := discoverStrikeCLIFiles(root, "providers", nil, true, base)
	diags = append(diags, d5...)
	panes, d6 := discoverStrikeCLIFiles(root, "panes", []string{".json", ".jsonc"}, false, base)
	diags = append(diags, d6...)

	skills, skillDiags := mergeStrikeCLISkills(portableSkills, extraSkills, base)
	diags = append(diags, skillDiags...)
	return agents, skills, workflows, themes, providers, panes, diags
}

func resolveStrikeCLIDir(root string) (resolved string, missing bool, err error) {
	dir := filepath.Join(root, strikeCLIDir)
	_, err = os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, nil
		}
		return "", false, err
	}
	abs, err := confinedExistingPath(root, dir)
	if err != nil {
		return "", false, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", false, err
	}
	if !st.IsDir() {
		return "", false, fmt.Errorf("%s is not a directory", strikeCLIDir)
	}
	return abs, false, nil
}

func discoverStrikeCLIFiles(root, kind string, exts []string, anyExt bool, base Diagnostic) ([]FileRef, []Diagnostic) {
	relDir := filepath.ToSlash(filepath.Join(strikeCLIDir, kind))
	absDir := filepath.Join(root, filepath.FromSlash(relDir))
	st, err := os.Lstat(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		d := base
		d.Severity = SeverityWarning
		d.Code = "malformed"
		d.Path = relDir
		d.Message = err.Error()
		return nil, []Diagnostic{d}
	}
	resolved, err := confinedExistingPath(root, absDir)
	if err != nil {
		d := base
		d.Severity = SeverityError
		d.Code = "path"
		d.Path = relDir
		d.Message = err.Error()
		return nil, []Diagnostic{d}
	}
	st, err = os.Stat(resolved)
	if err != nil || !st.IsDir() {
		d := base
		d.Severity = SeverityWarning
		d.Code = "malformed"
		d.Path = relDir
		d.Message = fmt.Sprintf("%s is not a directory; skipping", relDir)
		return nil, []Diagnostic{d}
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		d := base
		d.Severity = SeverityWarning
		d.Code = "malformed"
		d.Path = relDir
		d.Message = err.Error()
		return nil, []Diagnostic{d}
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var refs []FileRef
	var diags []Diagnostic
	for _, name := range names {
		rel := filepath.ToSlash(filepath.Join(relDir, name))
		abs, err := ResolveUnderRoot(root, rel)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "path"
			d.Path = rel
			d.Message = err.Error()
			diags = append(diags, d)
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = rel
			d.Message = err.Error()
			diags = append(diags, d)
			continue
		}
		if info.IsDir() {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = rel
			d.Message = fmt.Sprintf("skipping %s: directories are not Strike-flat contributions", rel)
			diags = append(diags, d)
			continue
		}
		if !info.Mode().IsRegular() {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = rel
			d.Message = fmt.Sprintf("skipping %s: not a regular file", rel)
			diags = append(diags, d)
			continue
		}
		if !anyExt && !extAllowed(abs, exts) {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = rel
			d.Message = fmt.Sprintf("unexpected file type for %s contribution", kind)
			diags = append(diags, d)
			continue
		}
		refs = append(refs, FileRef{
			PluginID: base.PluginID,
			Version:  base.Version,
			Source:   base.Source,
			RelPath:  rel,
			AbsPath:  abs,
		})
	}
	return refs, diags
}

func extAllowed(path string, exts []string) bool {
	got := strings.ToLower(filepath.Ext(path))
	for _, e := range exts {
		if got == strings.ToLower(e) {
			return true
		}
	}
	return false
}

func mergeStrikeCLISkills(portable, extra []FileRef, base Diagnostic) ([]FileRef, []Diagnostic) {
	seen := map[string]struct{}{}
	for _, ref := range portable {
		seen[contributionPublicName(ref.RelPath)] = struct{}{}
	}
	var out []FileRef
	var diags []Diagnostic
	for _, ref := range extra {
		name := contributionPublicName(ref.RelPath)
		if _, ok := seen[name]; ok {
			d := base
			d.Severity = SeverityWarning
			d.Code = "collision"
			d.Path = ref.RelPath
			d.Collision = name
			d.Message = fmt.Sprintf("Strike-flat skill %q skipped: portable skills/%s/SKILL.md wins", name, name)
			diags = append(diags, d)
			continue
		}
		seen[name] = struct{}{}
		out = append(out, ref)
	}
	return out, diags
}

func strikeCLIJSONFiles(root, kind string, base Diagnostic) ([]FileRef, []Diagnostic) {
	return discoverStrikeCLIFiles(root, kind, []string{".json"}, false, base)
}

func loadStrikeCLIHarnessRaws(root string, base Diagnostic) ([]json.RawMessage, []Diagnostic) {
	refs, diags := strikeCLIJSONFiles(root, "harnesses", base)
	var raws []json.RawMessage
	for _, ref := range refs {
		data, err := os.ReadFile(ref.AbsPath)
		if err != nil {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = ref.RelPath
			d.Message = err.Error()
			diags = append(diags, d)
			continue
		}
		data = bytesTrimSpace(data)
		if len(data) == 0 || data[0] != '{' {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = ref.RelPath
			d.Message = "harness file must be a JSON object"
			diags = append(diags, d)
			continue
		}
		raws = append(raws, json.RawMessage(append([]byte(nil), data...)))
	}
	return raws, diags
}

func loadStrikeCLIHookRaws(root string, base Diagnostic) ([]json.RawMessage, []Diagnostic) {
	refs, diags := strikeCLIJSONFiles(root, "hooks", base)
	var raws []json.RawMessage
	for _, ref := range refs {
		data, err := os.ReadFile(ref.AbsPath)
		if err != nil {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = ref.RelPath
			d.Message = err.Error()
			diags = append(diags, d)
			continue
		}
		data = bytesTrimSpace(data)
		if len(data) == 0 {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = ref.RelPath
			d.Message = "empty hook file"
			diags = append(diags, d)
			continue
		}
		switch data[0] {
		case '{':
			raws = append(raws, json.RawMessage(append([]byte(nil), data...)))
		case '[':
			var arr []json.RawMessage
			if err := json.Unmarshal(data, &arr); err != nil {
				d := base
				d.Severity = SeverityWarning
				d.Code = "malformed"
				d.Path = ref.RelPath
				d.Message = "hook file array is not valid JSON"
				diags = append(diags, d)
				continue
			}
			raws = append(raws, arr...)
		default:
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = ref.RelPath
			d.Message = "hook file must be a JSON object or array"
			diags = append(diags, d)
		}
	}
	return raws, diags
}

func strikeCLIHasHarnesses(root string) bool {
	raws, _ := loadStrikeCLIHarnessRaws(root, Diagnostic{})
	return len(raws) > 0
}

func strikeCLIHasShellHooks(root string) bool {
	raws, _ := loadStrikeCLIHookRaws(root, Diagnostic{})
	for _, raw := range raws {
		h, err := parseHookEntry(raw)
		if err != nil {
			continue
		}
		if h.IsShell() {
			return true
		}
	}
	return false
}

func strikeCLIHasDeclarativeHooks(root string) bool {
	raws, _ := loadStrikeCLIHookRaws(root, Diagnostic{})
	for _, raw := range raws {
		h, err := parseHookEntry(raw)
		if err != nil {
			continue
		}
		if h.IsRule() {
			return true
		}
	}
	return false
}

func strikeCLIHasProcessPanes(root string) bool {
	refs, _ := discoverStrikeCLIFiles(root, "panes", []string{".json", ".jsonc"}, false, Diagnostic{})
	for _, ref := range refs {
		d, _, err := ReadPaneDefinition(root, ref.RelPath)
		if err != nil {
			// Fail closed: unreadable pane may be executable.
			return true
		}
		if d.Mode == PaneModeProcess {
			return true
		}
	}
	return false
}

func strikeCLIHasPanes(root string) bool {
	refs, _ := discoverStrikeCLIFiles(root, "panes", []string{".json", ".jsonc"}, false, Diagnostic{})
	return len(refs) > 0
}

func strikeCLIHasExecutable(root string) bool {
	return strikeCLIHasHarnesses(root) || strikeCLIHasShellHooks(root) || strikeCLIHasProcessPanes(root)
}

func inferStrikeCLICaps(root string, set map[string]struct{}) {
	if strikeCLIHasHarnesses(root) {
		set[CapHarnesses] = struct{}{}
	}
	if strikeCLIHasShellHooks(root) {
		set[CapHooksCommand] = struct{}{}
	}
	if strikeCLIHasDeclarativeHooks(root) {
		set[CapHooksDeclarative] = struct{}{}
	}
	if strikeCLIHasPanes(root) {
		set[CapPanes] = struct{}{}
	}
	if strikeCLIHasProcessPanes(root) {
		set[CapPanes] = struct{}{}
		set[CapPanesProcess] = struct{}{}
	}
}

func inferStrikeCLIPassiveTags(root string, set map[string]struct{}) {
	kinds := []struct {
		dir  string
		tag  string
		exts []string
		any  bool
	}{
		{"agents", "agents", []string{".md"}, false},
		{"skills", "skills", []string{".md"}, false},
		{"workflows", "workflows", []string{".json"}, false},
		{"themes", "themes", []string{".json"}, false},
		{"providers", "providers", nil, true},
		{"harnesses", "harnesses", []string{".json"}, false},
		{"hooks", "hooks", []string{".json"}, false},
		{"panes", "panes", []string{".json", ".jsonc"}, false},
	}
	for _, k := range kinds {
		refs, _ := discoverStrikeCLIFiles(root, k.dir, k.exts, k.any, Diagnostic{})
		if len(refs) > 0 {
			set[k.tag] = struct{}{}
		}
	}
}

// PaneRef is a discovered pane definition (legacy contributions.panes or APS dir).
type PaneRef struct {
	FileRef
	// EntryID is contributions.panes[].id for legacy packages; empty for APS
	// (id comes from the definition file).
	EntryID string
}

// PluginPaneRefs lists pane definition files for an enabled plugin.
func PluginPaneRefs(m Manifest, root string) []PaneRef {
	if m.Format == FormatAPS {
		if skipStrikeCLI(m) {
			return nil
		}
		refs, _ := discoverStrikeCLIFiles(root, "panes", []string{".json", ".jsonc"}, false, Diagnostic{
			PluginID: m.ID,
			Version:  m.Version,
		})
		out := make([]PaneRef, 0, len(refs))
		for _, r := range refs {
			out = append(out, PaneRef{FileRef: r})
		}
		return out
	}
	var out []PaneRef
	for _, raw := range m.Contributions.Panes {
		e, err := ParsePaneEntry(raw)
		if err != nil {
			continue
		}
		abs, err := ResolveUnderRoot(root, e.Path)
		if err != nil {
			continue
		}
		out = append(out, PaneRef{
			FileRef: FileRef{
				PluginID: m.ID,
				Version:  m.Version,
				RelPath:  e.Path,
				AbsPath:  abs,
			},
			EntryID: e.ID,
		})
	}
	return out
}

func summarizeStrikeCLIExec(root string) (harnesses []DoctorHarness, hooks, panes int) {
	raws, _ := loadStrikeCLIHarnessRaws(root, Diagnostic{})
	for _, raw := range raws {
		e, err := parseHarnessEntry(raw)
		if err != nil {
			harnesses = append(harnesses, DoctorHarness{Name: "(invalid)"})
			continue
		}
		harnesses = append(harnesses, DoctorHarness{
			Name:    e.Name,
			Command: e.Command,
			Args:    e.Args,
		})
	}
	hookRaws, _ := loadStrikeCLIHookRaws(root, Diagnostic{})
	hooks = len(hookRaws)
	paneRefs, _ := discoverStrikeCLIFiles(root, "panes", []string{".json", ".jsonc"}, false, Diagnostic{})
	panes = len(paneRefs)
	return harnesses, hooks, panes
}

func strikeCLIExecSnapshotLines(root string) []string {
	var lines []string
	raws, _ := loadStrikeCLIHarnessRaws(root, Diagnostic{})
	for _, raw := range raws {
		lines = append(lines, "harness:"+safeExecJSONLine(raw))
	}
	hookRaws, _ := loadStrikeCLIHookRaws(root, Diagnostic{})
	for _, raw := range hookRaws {
		lines = append(lines, "hook:"+safeExecJSONLine(raw))
	}
	paneRefs, _ := discoverStrikeCLIFiles(root, "panes", []string{".json", ".jsonc"}, false, Diagnostic{})
	for _, ref := range paneRefs {
		data, err := os.ReadFile(ref.AbsPath)
		if err != nil {
			continue
		}
		lines = append(lines, "pane:"+safeExecJSONLine(json.RawMessage(data)))
	}
	return lines
}
