package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jonathanung/strike-cli/internal/version"
)

// ErrAlreadyAPS is returned when migrate is asked to convert an APS package.
var ErrAlreadyAPS = errors.New("already an Agent Plugins 1.0.0 package")

// ErrNeedYes is returned when modifying an installed plugin without Confirm.
var ErrNeedYes = errors.New("migrating an installed plugin requires --yes")

// writeLockfileAfterSwap persists the lockfile after the migrated tree is swapped
// in. Tests override it to inject a write failure after the filesystem rename.
var writeLockfileAfterSwap = WriteLockfile

// MigrateOptions controls Migrate.
type MigrateOptions struct {
	// Target is an installed plugin id or a filesystem path to a plugin root.
	Target                           string
	Scope                            Scope // empty: prefer project when resolving an installed id
	WorkDir, GlobalRoot, ProjectRoot string
	StrikeVersion                    string
	DryRun                           bool
	// Confirm is CLI --yes. Required to replace an installed plugin tree.
	Confirm bool
}

// MigrateMove is one planned source → destination path mapping.
type MigrateMove struct {
	From string
	To   string
	Note string
}

// MigratePlan is the human-readable conversion plan (dry-run and review).
type MigratePlan struct {
	ID          string
	DisplayName string
	Version     string
	SourceRoot  string
	Installed   bool
	Scope       Scope
	Moves       []MigrateMove
	MCPServers  []string
	Notes       []string
}

// Format renders the migrate plan (no secrets).
func (p MigratePlan) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Migrate plan: %s (legacy → Agent Plugins 1.0.0)\n", emptyDash(p.ID))
	fmt.Fprintf(&b, "  source:      %s\n", p.SourceRoot)
	if p.Installed {
		fmt.Fprintf(&b, "  installed:   yes (%s)\n", p.Scope)
	} else {
		fmt.Fprintf(&b, "  installed:   no\n")
	}
	fmt.Fprintf(&b, "  name:        %s (from legacy id)\n", p.ID)
	if p.DisplayName != "" {
		fmt.Fprintf(&b, "  displayName: %s (from legacy name)\n", p.DisplayName)
	}
	if p.Version != "" {
		fmt.Fprintf(&b, "  version:     %s\n", p.Version)
	}
	if len(p.MCPServers) > 0 {
		fmt.Fprintf(&b, "  mcp.json:    %s\n", strings.Join(p.MCPServers, ", "))
	}
	for _, mv := range p.Moves {
		if mv.Note != "" {
			fmt.Fprintf(&b, "  %s → %s (%s)\n", mv.From, mv.To, mv.Note)
		} else {
			fmt.Fprintf(&b, "  %s → %s\n", mv.From, mv.To)
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "  note:        %s\n", n)
	}
	fmt.Fprintf(&b, "  remove:      Strike-native root fields (schemaVersion, id, contributions, top-level strike)\n")
	if p.Installed {
		fmt.Fprintf(&b, "  trust:       will be cleared (do not auto-trust)\n")
	}
	return b.String()
}

// MigrateResult is the outcome of Migrate (including dry-run).
type MigrateResult struct {
	ID           string
	Version      string
	Root         string
	OldDigest    string
	Digest       string
	Installed    bool
	Scope        Scope
	Plan         MigratePlan
	TrustCleared bool
	Applied      bool
	Review       string
}

// Migrate converts a legacy Strike-native bundle to Agent Plugins 1.0.0.
// Staging is validated with the APS loader before the source tree is replaced.
// A failed migrate leaves the prior tree (and enablement) unchanged.
func Migrate(opts MigrateOptions) (MigrateResult, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return MigrateResult{}, fmt.Errorf("migrate requires a plugin id or path")
	}
	strikeVer := opts.StrikeVersion
	if strikeVer == "" {
		strikeVer = version.Version
	}

	resolved, err := resolveMigrateTarget(opts)
	if err != nil {
		return MigrateResult{}, err
	}
	src := resolved.root
	m, _, err := ReadManifest(src)
	if err != nil {
		return MigrateResult{}, err
	}
	if m.Format == FormatAPS {
		return MigrateResult{ID: m.ID, Version: m.Version, Root: src, Installed: resolved.installed, Scope: resolved.scope},
			fmt.Errorf("%w: plugin %q", ErrAlreadyAPS, m.ID)
	}
	if m.Format != FormatLegacy {
		return MigrateResult{}, fmt.Errorf("plugin %q is not a Strike-native legacy bundle", m.ID)
	}
	if err := ValidateAPSName(m.ID); err != nil {
		return MigrateResult{}, fmt.Errorf("legacy id %q cannot be used as Agent Plugins name: %w", m.ID, err)
	}

	plan, err := planLegacyMigrate(src, m)
	if err != nil {
		return MigrateResult{}, err
	}
	plan.SourceRoot = src
	plan.Installed = resolved.installed
	plan.Scope = resolved.scope

	oldDigest := ""
	if d, err := ComputeDigest(src); err == nil {
		oldDigest = d
	}

	result := MigrateResult{
		ID:        m.ID,
		Version:   m.Version,
		Root:      src,
		OldDigest: oldDigest,
		Installed: resolved.installed,
		Scope:     resolved.scope,
		Plan:      plan,
	}

	if opts.DryRun {
		return result, nil
	}
	if resolved.installed && !opts.Confirm {
		return result, ErrNeedYes
	}

	stagingParent := filepath.Dir(src)
	if resolved.installed {
		roots, rerr := ResolveRoots(resolved.scope, Options{
			WorkDir:     opts.WorkDir,
			GlobalRoot:  opts.GlobalRoot,
			ProjectRoot: opts.ProjectRoot,
		})
		if rerr != nil {
			return MigrateResult{}, rerr
		}
		stagingParent = roots.PluginsDir
		if err := os.MkdirAll(stagingParent, 0o755); err != nil {
			return MigrateResult{}, err
		}
	}
	staging, err := os.MkdirTemp(stagingParent, ".staging-migrate-*")
	if err != nil {
		return MigrateResult{}, fmt.Errorf("stage migrate: %w", err)
	}
	stagingOK := false
	defer func() {
		if stagingOK {
			return
		}
		if resolved.installed {
			if roots, rerr := ResolveRoots(resolved.scope, Options{
				WorkDir: opts.WorkDir, GlobalRoot: opts.GlobalRoot, ProjectRoot: opts.ProjectRoot,
			}); rerr == nil {
				_ = roots.removeAllUnderPlugins(staging)
				return
			}
		}
		_ = os.RemoveAll(staging)
	}()

	if err := writeMigratedTree(src, staging, m, plan); err != nil {
		return MigrateResult{}, fmt.Errorf("convert: %w", err)
	}

	p, diags := loadOne(staging, resolved.scope, strikeVer)
	if p == nil {
		return MigrateResult{}, fmt.Errorf("validation failed: %s", formatDiagSummary(diags))
	}
	if p.Manifest.Format != FormatAPS {
		return MigrateResult{}, fmt.Errorf("validation failed: migrated tree is not Agent Plugins 1.0.0")
	}
	if p.ID != m.ID {
		return MigrateResult{}, fmt.Errorf("validation failed: migrated name %q does not match legacy id %q", p.ID, m.ID)
	}

	newDigest, err := ComputeDigest(staging)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("compute digest: %w", err)
	}

	result.Root = src
	result.Digest = newDigest
	if resolved.installed {
		cleared, err := commitInstalledMigrate(opts, resolved, m, staging, newDigest, &stagingOK)
		if err != nil {
			return MigrateResult{}, err
		}
		result.Applied = true
		result.TrustCleared = cleared
		result.Review = formatMigrateReview(result)
		return result, nil
	}
	if err := commitMigratedTree(resolved, staging, &stagingOK); err != nil {
		return MigrateResult{}, err
	}
	result.Applied = true
	return result, nil
}

type migrateTarget struct {
	root      string
	installed bool
	scope     Scope
	id        string
}

func resolveMigrateTarget(opts MigrateOptions) (migrateTarget, error) {
	target := strings.TrimSpace(opts.Target)
	abs, absErr := filepath.Abs(target)
	if absErr == nil {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			t := migrateTarget{root: abs}
			if ip, ok := findInstalledByRoot(opts, abs); ok {
				t.installed = true
				t.scope = ip.Scope
				t.id = ip.ID
				t.root = ip.Root
			} else if opts.Scope != "" {
				t.scope = opts.Scope
			} else {
				t.scope = ScopeGlobal
			}
			return t, nil
		}
	}

	id := target
	if err := ValidatePluginKey(id); err != nil {
		if absErr == nil {
			return migrateTarget{}, fmt.Errorf("not a plugin directory and not an installed plugin id: %s", target)
		}
		return migrateTarget{}, err
	}
	ip, err := Inspect(EnableOptions{
		ID:          id,
		Scope:       opts.Scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return migrateTarget{}, err
	}
	return migrateTarget{root: ip.Root, installed: true, scope: ip.Scope, id: ip.ID}, nil
}

func findInstalledByRoot(opts MigrateOptions, root string) (InstalledPlugin, bool) {
	list, _, err := ListInstalled(ListOptions{
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
		Scope:       opts.Scope,
	})
	if err != nil {
		return InstalledPlugin{}, false
	}
	for _, ip := range list {
		if sameDir(ip.Root, root) {
			return ip, true
		}
	}
	return InstalledPlugin{}, false
}

func sameDir(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	if ra, err := filepath.EvalSymlinks(aa); err == nil {
		aa = ra
	}
	if rb, err := filepath.EvalSymlinks(bb); err == nil {
		bb = rb
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func planLegacyMigrate(src string, m Manifest) (MigratePlan, error) {
	plan := MigratePlan{
		ID:          m.ID,
		DisplayName: strings.TrimSpace(m.Name),
		Version:     m.Version,
	}
	c := m.Contributions
	used := map[string]struct{}{}
	for _, e := range c.Agents {
		plan.Moves = append(plan.Moves, MigrateMove{
			From: e.Path, To: uniqueStrikeCLIDest(used, "agents", e.Path), Note: "strike-only",
		})
	}
	for _, e := range c.Skills {
		to, note, err := planSkillMove(src, e.Path, used)
		if err != nil {
			return MigratePlan{}, err
		}
		plan.Moves = append(plan.Moves, MigrateMove{From: e.Path, To: to, Note: note})
	}
	for _, e := range c.Workflows {
		plan.Moves = append(plan.Moves, MigrateMove{
			From: e.Path, To: uniqueStrikeCLIDest(used, "workflows", e.Path), Note: "strike-only",
		})
	}
	for _, e := range c.Themes {
		plan.Moves = append(plan.Moves, MigrateMove{
			From: e.Path, To: uniqueStrikeCLIDest(used, "themes", e.Path), Note: "strike-only",
		})
	}
	for _, e := range c.Providers {
		plan.Moves = append(plan.Moves, MigrateMove{
			From: e.Path, To: uniqueStrikeCLIDest(used, "providers", e.Path), Note: "strike-only",
		})
	}
	for i, raw := range c.Harnesses {
		e, err := parseHarnessEntry(raw)
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = fmt.Sprintf("harness-%d", i+1)
		}
		to := uniqueRel(used, strikeCLIRel("harnesses", sanitizeJSONFileName(name)))
		note := "strike-only"
		if err != nil {
			note = "strike-only (invalid harness kept as JSON)"
		}
		plan.Moves = append(plan.Moves, MigrateMove{From: "contributions.harnesses[" + strconv.Itoa(i) + "]", To: to, Note: note})
	}
	for i := range c.Hooks {
		to := strikeCLIRel("hooks", fmt.Sprintf("hook-%d.json", i+1))
		plan.Moves = append(plan.Moves, MigrateMove{
			From: "contributions.hooks[" + strconv.Itoa(i) + "]", To: to, Note: "strike-only",
		})
	}
	for i, raw := range c.Panes {
		e, err := ParsePaneEntry(raw)
		from := "contributions.panes[" + strconv.Itoa(i) + "]"
		to := uniqueStrikeCLIDest(used, "panes", fmt.Sprintf("pane-%d.json", i+1))
		if err == nil {
			from = e.Path
			to = uniqueStrikeCLIDest(used, "panes", e.Path)
		}
		plan.Moves = append(plan.Moves, MigrateMove{
			From: from, To: to, Note: "strike-only",
		})
	}
	for _, raw := range c.MCP {
		e, err := parseMCPEntry(raw)
		if err != nil || strings.TrimSpace(e.Name) == "" {
			continue
		}
		plan.MCPServers = append(plan.MCPServers, strings.TrimSpace(e.Name))
	}
	sort.Strings(plan.MCPServers)
	if len(c.MCP) > 0 {
		plan.Notes = append(plan.Notes, "emit mcp.json from contributions.mcp (http → streamable-http; relative commands become ./…)")
	}
	plan.Notes = append(plan.Notes, leftoverSkillNotes(src, m)...)
	plan.Notes = append(plan.Notes, "write plugin.json with APS $schema and extensions.com.strike.cli")
	return plan, nil
}

func planSkillMove(src, rel string, used map[string]struct{}) (to, note string, err error) {
	rel = strings.TrimSpace(rel)
	base := filepath.Base(rel)
	if strings.EqualFold(base, "SKILL.md") {
		dir := filepath.Base(filepath.Dir(rel))
		if dir == "" || dir == "." {
			return "", "", fmt.Errorf("skill path %q is not a directory SKILL.md", rel)
		}
		return uniqueRel(used, filepath.ToSlash(filepath.Join("skills", dir, "SKILL.md"))), "portable", nil
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	abs, rerr := ResolveUnderRoot(src, rel)
	if rerr != nil {
		return "", "", fmt.Errorf("skill %q: %w", rel, rerr)
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		return "", "", fmt.Errorf("skill %q: %w", rel, rerr)
	}
	if isPortableAgentSkill(data, stem) {
		return uniqueRel(used, filepath.ToSlash(filepath.Join("skills", stem, "SKILL.md"))), "portable wrap", nil
	}
	return uniqueStrikeCLIDest(used, "skills", rel), "strike-only extra", nil
}

func uniqueStrikeCLIDest(used map[string]struct{}, kind, rel string) string {
	base := filepath.Base(filepath.ToSlash(strings.TrimSpace(rel)))
	if base == "" || base == "." {
		base = "entry"
	}
	dest := filepath.ToSlash(filepath.Join(strikeCLIDir, kind, base))
	return uniqueRel(used, dest)
}

func uniqueRel(used map[string]struct{}, dest string) string {
	dest = filepath.ToSlash(dest)
	if _, ok := used[dest]; !ok {
		used[dest] = struct{}{}
		return dest
	}
	if strings.EqualFold(filepath.Base(dest), "SKILL.md") {
		dir := filepath.ToSlash(filepath.Dir(dest))
		for i := 2; ; i++ {
			cand := filepath.ToSlash(filepath.Join(fmt.Sprintf("%s-%d", dir, i), "SKILL.md"))
			if _, ok := used[cand]; !ok {
				used[cand] = struct{}{}
				return cand
			}
		}
	}
	ext := filepath.Ext(dest)
	stem := strings.TrimSuffix(dest, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, ok := used[cand]; !ok {
			used[cand] = struct{}{}
			return cand
		}
	}
}

func strikeCLIRel(kind, name string) string {
	return filepath.ToSlash(filepath.Join(strikeCLIDir, kind, name))
}

func sanitizeJSONFileName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), ".-")
	if s == "" {
		s = "entry"
	}
	if !strings.HasSuffix(strings.ToLower(s), ".json") {
		s += ".json"
	}
	return s
}

func writeMigratedTree(src, dest string, m Manifest, plan MigratePlan) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	skip, err := migrateSkipSet(src, m)
	if err != nil {
		return err
	}
	relocatedBins := map[string]string{} // original rel command → com.strike.cli/bin/base
	if err := relocateStrikeBinaries(src, dest, m, relocatedBins); err != nil {
		return err
	}
	for orig := range relocatedBins {
		if !mcpUsesRelCommand(m, orig) {
			skip[filepath.ToSlash(orig)] = struct{}{}
		}
	}
	if err := copyLeftovers(src, dest, skip); err != nil {
		return err
	}
	if err := writeAPSPluginJSON(dest, m); err != nil {
		return err
	}
	if err := copyPlannedFiles(src, dest, plan, pathList(m.Contributions.Agents)); err != nil {
		return err
	}
	if err := copyPlannedFiles(src, dest, plan, pathList(m.Contributions.Workflows)); err != nil {
		return err
	}
	if err := copyPlannedFiles(src, dest, plan, pathList(m.Contributions.Themes)); err != nil {
		return err
	}
	if err := copyMigratedProviders(src, dest, m, plan); err != nil {
		return err
	}
	if err := copyMigratedSkills(src, dest, m, plan); err != nil {
		return err
	}
	if err := writeHarnessFiles(src, dest, m, plan, relocatedBins); err != nil {
		return err
	}
	if err := writeHookFiles(src, dest, m, relocatedBins); err != nil {
		return err
	}
	if err := copyPaneFiles(src, dest, m, plan, relocatedBins); err != nil {
		return err
	}
	if err := writeAPSMCPJSON(dest, m); err != nil {
		return err
	}
	return nil
}

func leftoverSkillNotes(src string, m Manifest) []string {
	declared := map[string]struct{}{}
	for _, e := range m.Contributions.Skills {
		rel := filepath.ToSlash(strings.TrimSpace(e.Path))
		if rel == "" {
			continue
		}
		if strings.EqualFold(filepath.Base(rel), "SKILL.md") {
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir != "" && dir != "." {
				declared[dir] = struct{}{}
			}
			continue
		}
		declared[rel] = struct{}{}
	}
	entries, err := os.ReadDir(filepath.Join(src, "skills"))
	if err != nil {
		return nil
	}
	var notes []string
	for _, e := range entries {
		rel := filepath.ToSlash(filepath.Join("skills", e.Name()))
		if _, ok := declared[rel]; ok {
			continue
		}
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(src, "skills", e.Name(), "SKILL.md")); err != nil {
				continue
			}
			notes = append(notes, "skip undeclared leftover skill dir "+rel+" (not in contributions.skills)")
			continue
		}
		notes = append(notes, "skip undeclared leftover "+rel+" (not in contributions.skills)")
	}
	return notes
}

func migrateSkipSet(src string, m Manifest) (map[string]struct{}, error) {
	skip := map[string]struct{}{
		"plugin.json":  {},
		"plugin.jsonc": {},
		"mcp.json":     {},
		"skills":       {},
	}
	add := func(rel string) {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel != "" {
			skip[rel] = struct{}{}
		}
	}
	for _, e := range m.Contributions.Agents {
		add(e.Path)
	}
	for _, e := range m.Contributions.Workflows {
		add(e.Path)
	}
	for _, e := range m.Contributions.Themes {
		add(e.Path)
	}
	for _, e := range m.Contributions.Providers {
		add(e.Path)
	}
	for _, e := range m.Contributions.Skills {
		add(e.Path)
		base := filepath.Base(e.Path)
		if strings.EqualFold(base, "SKILL.md") {
			dir := filepath.ToSlash(filepath.Dir(e.Path))
			if dir != "" && dir != "." {
				// Skip the whole skill directory; it is copied as portable.
				if err := markDirSkipped(src, dir, skip); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, raw := range m.Contributions.Panes {
		e, err := ParsePaneEntry(raw)
		if err != nil {
			continue
		}
		add(e.Path)
	}
	return skip, nil
}

func markDirSkipped(root, rel string, skip map[string]struct{}) error {
	abs, err := ResolveUnderRoot(root, rel)
	if err != nil {
		return nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	skip[filepath.ToSlash(rel)] = struct{}{}
	return filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return err
		}
		if r == "." {
			return nil
		}
		skip[filepath.ToSlash(r)] = struct{}{}
		return nil
	})
}

func copyPlannedFiles(src, dest string, plan MigratePlan, froms []string) error {
	idx := map[string]string{}
	for _, mv := range plan.Moves {
		idx[mv.From] = mv.To
	}
	for _, from := range froms {
		to, ok := idx[from]
		if !ok {
			return fmt.Errorf("internal: missing migrate plan for %s", from)
		}
		if err := copyFileTo(src, dest, from, to); err != nil {
			return err
		}
	}
	return nil
}

func pathList(entries []PathEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

func copyMigratedProviders(src, dest string, m Manifest, plan MigratePlan) error {
	idx := map[string]string{}
	for _, mv := range plan.Moves {
		idx[mv.From] = mv.To
	}
	for _, e := range m.Contributions.Providers {
		to, ok := idx[e.Path]
		if !ok {
			return fmt.Errorf("internal: missing migrate plan for %s", e.Path)
		}
		if err := copyFileTo(src, dest, e.Path, to); err != nil {
			return err
		}
		if strings.TrimSpace(e.ProfileName) == "" {
			continue
		}
		abs := filepath.Join(dest, filepath.FromSlash(to))
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		rewritten, err := rewriteProviderProfileName(data, e.ProfileName)
		if err != nil {
			return fmt.Errorf("provider %s: %w", e.Path, err)
		}
		if err := os.WriteFile(abs, rewritten, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func rewriteProviderProfileName(data []byte, profileName string) ([]byte, error) {
	profileName = strings.TrimSpace(profileName)
	stripped, err := stripJSONC(data)
	if err != nil {
		stripped = data
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bytesTrimSpace(stripped), &obj); err != nil {
		return nil, err
	}
	skip := map[string]struct{}{"$schema": {}}
	var keys []string
	for k := range obj {
		if _, ok := skip[k]; ok {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) != 1 {
		return append(append([]byte(nil), bytesTrimSpace(stripped)...), '\n'), nil
	}
	if keys[0] == profileName {
		out, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(out, '\n'), nil
	}
	obj[profileName] = obj[keys[0]]
	delete(obj, keys[0])
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func copyMigratedSkills(src, dest string, m Manifest, plan MigratePlan) error {
	byFrom := map[string]MigrateMove{}
	for _, mv := range plan.Moves {
		byFrom[mv.From] = mv
	}
	for _, e := range m.Contributions.Skills {
		mv, ok := byFrom[e.Path]
		if !ok {
			return fmt.Errorf("internal: missing skill plan for %s", e.Path)
		}
		base := filepath.Base(e.Path)
		if strings.EqualFold(base, "SKILL.md") {
			fromDir := filepath.Dir(e.Path)
			toDir := filepath.Dir(mv.To)
			fromAbs, err := ResolveUnderRoot(src, fromDir)
			if err != nil {
				return fmt.Errorf("skill dir %s: %w", fromDir, err)
			}
			toAbs := filepath.Join(dest, filepath.FromSlash(toDir))
			if err := confineDestPath(dest, toAbs); err != nil {
				return err
			}
			if err := copyTree(fromAbs, toAbs); err != nil {
				return fmt.Errorf("copy skill dir %s: %w", fromDir, err)
			}
			continue
		}
		if err := copyFileTo(src, dest, e.Path, mv.To); err != nil {
			return err
		}
	}
	return nil
}

func copyFileTo(srcRoot, destRoot, fromRel, toRel string) error {
	fromAbs, err := ResolveUnderRoot(srcRoot, fromRel)
	if err != nil {
		return fmt.Errorf("%s: %w", fromRel, err)
	}
	toAbs := filepath.Join(destRoot, filepath.FromSlash(toRel))
	if err := confineDestPath(destRoot, toAbs); err != nil {
		return err
	}
	return copyFile(fromAbs, toAbs)
}

func confineDestPath(root, abs string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	abs, err = filepath.Abs(abs)
	if err != nil {
		return err
	}
	if !isUnder(rootAbs, abs) && rootAbs != abs {
		return fmt.Errorf("path %q escapes migrate dest", abs)
	}
	return nil
}

func relocateStrikeBinaries(src, dest string, m Manifest, relocated map[string]string) error {
	used := map[string]struct{}{}
	consider := func(command string) error {
		command = strings.TrimSpace(command)
		if command == "" || filepath.IsAbs(command) {
			return nil
		}
		if !strings.Contains(command, "/") && !strings.Contains(command, `\`) {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(command), "./")
		if _, ok := relocated[rel]; ok {
			return nil
		}
		if _, err := ResolveUnderRoot(src, rel); err != nil {
			// Command may be reviewed-absolute-style or missing; leave as-is.
			return nil
		}
		toRel := uniqueRel(used, strikeCLIBinDest(rel))
		if err := copyFileTo(src, dest, rel, toRel); err != nil {
			return fmt.Errorf("copy binary %s: %w", rel, err)
		}
		relocated[rel] = toRel
		return nil
	}
	for _, raw := range m.Contributions.Harnesses {
		e, err := parseHarnessEntry(raw)
		if err != nil {
			continue
		}
		if err := consider(e.Command); err != nil {
			return err
		}
	}
	for _, raw := range m.Contributions.Hooks {
		e, err := parseHookEntry(raw)
		if err != nil {
			continue
		}
		if err := consider(e.Command); err != nil {
			return err
		}
	}
	for _, raw := range m.Contributions.Panes {
		e, err := ParsePaneEntry(raw)
		if err != nil {
			continue
		}
		abs, err := ResolveUnderRoot(src, e.Path)
		if err != nil {
			continue
		}
		d, _, err := ReadPaneDefinition(src, e.Path)
		if err != nil {
			_ = abs
			continue
		}
		if err := consider(d.Command); err != nil {
			return err
		}
	}
	return nil
}

func strikeCLIBinDest(rel string) string {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	if strings.HasPrefix(rel, "bin/") {
		return filepath.ToSlash(filepath.Join(strikeCLIDir, rel))
	}
	return filepath.ToSlash(filepath.Join(strikeCLIDir, "bin", rel))
}

func mcpUsesRelCommand(m Manifest, rel string) bool {
	want := strings.TrimPrefix(filepath.ToSlash(rel), "./")
	for _, raw := range m.Contributions.MCP {
		e, err := parseMCPEntry(raw)
		if err != nil {
			continue
		}
		cmd := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(e.Command)), "./")
		if cmd == want {
			return true
		}
	}
	return false
}

func rewriteRelCommand(command string, relocated map[string]string) string {
	command = strings.TrimSpace(command)
	if command == "" || filepath.IsAbs(command) {
		return command
	}
	rel := strings.TrimPrefix(filepath.ToSlash(command), "./")
	if to, ok := relocated[rel]; ok {
		return to
	}
	return command
}

func writeHarnessFiles(src, dest string, m Manifest, plan MigratePlan, relocated map[string]string) error {
	_ = src
	idx := map[string]string{}
	for _, mv := range plan.Moves {
		idx[mv.From] = mv.To
	}
	for i, raw := range m.Contributions.Harnesses {
		from := "contributions.harnesses[" + strconv.Itoa(i) + "]"
		toRel, ok := idx[from]
		if !ok {
			return fmt.Errorf("internal: missing migrate plan for %s", from)
		}
		e, err := parseHarnessEntry(raw)
		out := raw
		if err == nil && strings.TrimSpace(e.Command) != "" {
			newCmd := rewriteRelCommand(e.Command, relocated)
			if patched, perr := patchJSONStringField(raw, "command", newCmd); perr == nil {
				out = patched
			}
		}
		if err := writeJSONFile(dest, toRel, out); err != nil {
			return err
		}
	}
	return nil
}

func writeHookFiles(src, dest string, m Manifest, relocated map[string]string) error {
	_ = src
	for i, raw := range m.Contributions.Hooks {
		toRel := strikeCLIRel("hooks", fmt.Sprintf("hook-%d.json", i+1))
		out := raw
		e, err := parseHookEntry(raw)
		if err == nil && strings.TrimSpace(e.Command) != "" {
			newCmd := rewriteRelCommand(e.Command, relocated)
			if patched, perr := patchJSONStringField(raw, "command", newCmd); perr == nil {
				out = patched
			}
		}
		if err := writeJSONFile(dest, toRel, out); err != nil {
			return err
		}
	}
	return nil
}

func copyPaneFiles(src, dest string, m Manifest, plan MigratePlan, relocated map[string]string) error {
	idx := map[string]string{}
	for _, mv := range plan.Moves {
		idx[mv.From] = mv.To
	}
	for i, raw := range m.Contributions.Panes {
		e, err := ParsePaneEntry(raw)
		from := "contributions.panes[" + strconv.Itoa(i) + "]"
		if err == nil {
			from = e.Path
		}
		toRel, ok := idx[from]
		if !ok {
			return fmt.Errorf("internal: missing migrate plan for pane %s", from)
		}
		if err != nil || strings.TrimSpace(e.Path) == "" {
			if err := writeJSONFile(dest, toRel, raw); err != nil {
				return err
			}
			continue
		}
		if err := copyFileTo(src, dest, e.Path, toRel); err != nil {
			return err
		}
		d, _, err := ReadPaneDefinition(src, e.Path)
		if err != nil || strings.TrimSpace(d.Command) == "" {
			continue
		}
		newCmd := rewriteRelCommand(d.Command, relocated)
		if newCmd == d.Command {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(toRel)))
		if err != nil {
			return err
		}
		patched, err := patchJSONStringField(json.RawMessage(data), "command", newCmd)
		if err != nil {
			return err
		}
		if err := writeJSONFile(dest, toRel, patched); err != nil {
			return err
		}
	}
	return nil
}

func patchJSONStringField(raw json.RawMessage, key, value string) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	obj[key] = b
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	return out, nil
}

func writeJSONFile(destRoot, rel string, raw json.RawMessage) error {
	abs := filepath.Join(destRoot, filepath.FromSlash(rel))
	if err := confineDestPath(destRoot, abs); err != nil {
		return err
	}
	data := bytesTrimSpace(raw)
	if len(data) == 0 {
		return fmt.Errorf("empty JSON for %s", rel)
	}
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON for %s", rel)
	}
	var pretty json.RawMessage
	if err := json.Unmarshal(data, &pretty); err != nil {
		return err
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, out, 0o644)
}

type apsPluginJSON struct {
	Schema      string                      `json:"$schema"`
	Name        string                      `json:"name"`
	Version     string                      `json:"version,omitempty"`
	Description string                      `json:"description,omitempty"`
	Extensions  map[string]apsStrikeCLIJSON `json:"extensions"`
}

type apsStrikeCLIJSON struct {
	DisplayName  string      `json:"displayName,omitempty"`
	Strike       StrikeRange `json:"strike,omitempty"`
	Capabilities []string    `json:"capabilities,omitempty"`
}

func writeAPSPluginJSON(dest string, m Manifest) error {
	out := apsPluginJSON{
		Schema:      APSPluginSchemaV1,
		Name:        m.ID,
		Version:     m.Version,
		Description: m.Description,
		Extensions: map[string]apsStrikeCLIJSON{
			strikeCLINs: {
				DisplayName:  strings.TrimSpace(m.Name),
				Strike:       m.Strike,
				Capabilities: append([]string(nil), m.Capabilities...),
			},
		},
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dest, "plugin.json"), data, 0o644)
}

type apsMCPJSON struct {
	Schema     string                      `json:"$schema"`
	MCPServers map[string]apsMCPServerJSON `json:"mcpServers"`
}

type apsMCPServerJSON struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func writeAPSMCPJSON(dest string, m Manifest) error {
	if len(m.Contributions.MCP) == 0 {
		return nil
	}
	servers := map[string]apsMCPServerJSON{}
	for _, raw := range m.Contributions.MCP {
		e, err := parseMCPEntry(raw)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(e.Transport))
		switch typ {
		case "", "stdio":
			if strings.TrimSpace(e.URL) != "" && typ == "" {
				typ = "streamable-http"
			} else {
				typ = "stdio"
			}
		case "http", "streamable-http", "streamable_http", "sse":
			typ = "streamable-http"
		}
		srv := apsMCPServerJSON{Type: typ}
		if typ == "stdio" {
			srv.Command = apsRelativeCommand(e.Command)
			srv.Args = e.Args
			srv.Env = copyStringMap(e.Env)
		} else {
			srv.URL = e.URL
			srv.Headers = copyStringMap(e.Headers)
		}
		servers[name] = srv
	}
	if len(servers) == 0 {
		return nil
	}
	out := apsMCPJSON{Schema: APSMCPSchemaV1, MCPServers: servers}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dest, "mcp.json"), data, 0o644)
}

func apsRelativeCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" || filepath.IsAbs(command) {
		return command
	}
	slash := strings.ReplaceAll(command, `\`, "/")
	if !strings.Contains(slash, "/") {
		return command
	}
	if strings.HasPrefix(slash, "./") {
		return slash
	}
	return "./" + strings.TrimPrefix(slash, "/")
}

func copyLeftovers(src, dest string, skip map[string]struct{}) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(srcAbs); err == nil {
		srcAbs = resolved
	}
	return filepath.WalkDir(srcAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcAbs, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if _, ok := skip[relSlash]; ok {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return copyTreeFile(srcAbs, dest, relSlash)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, filepath.Join(dest, rel))
	})
}

func copyTreeFile(srcRoot, destRoot, relSlash string) error {
	return copyFile(filepath.Join(srcRoot, filepath.FromSlash(relSlash)), filepath.Join(destRoot, filepath.FromSlash(relSlash)))
}

func commitInstalledMigrate(opts MigrateOptions, resolved migrateTarget, m Manifest, staging, digest string, stagingOK *bool) (trustCleared bool, err error) {
	roots, err := ResolveRoots(resolved.scope, Options{
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return false, err
	}
	dest := resolved.root
	var backup string
	err = WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		backup = filepath.Join(roots.PluginsDir, ".bak-migrate-"+sanitizeDirComponent(m.ID)+"-"+strconv.Itoa(os.Getpid()))
		_ = os.RemoveAll(backup)
		if err := os.Rename(dest, backup); err != nil {
			backup = ""
			return lf, true, fmt.Errorf("replace existing tree: %w", err)
		}
		if err := os.Rename(staging, dest); err != nil {
			_ = os.Rename(backup, dest)
			backup = ""
			return lf, true, fmt.Errorf("activate migrated tree: %w", err)
		}
		*stagingOK = true
		e := lf.Plugins[m.ID]
		if e.Trust != nil {
			trustCleared = true
		}
		e.Digest = digest
		e.Version = m.Version
		e.Trust = nil
		if e.InstalledAt == "" {
			e.InstalledAt = nowRFC3339()
		}
		lf = setLockEntry(lf, m.ID, e)
		if err := writeLockfileAfterSwap(roots.LockPath, lf); err != nil {
			return lf, true, fmt.Errorf("write lockfile: %w", err)
		}
		return lf, true, nil
	})
	if err != nil {
		if *stagingOK {
			_ = roots.removeAllUnderPlugins(dest)
			if backup != "" {
				_ = os.Rename(backup, dest)
				backup = ""
			}
			*stagingOK = false
		}
		if backup != "" {
			_ = roots.removeAllUnderPlugins(backup)
		}
		return false, err
	}
	if backup != "" {
		_ = roots.removeAllUnderPlugins(backup)
	}
	return trustCleared, nil
}

func commitMigratedTree(resolved migrateTarget, staging string, stagingOK *bool) error {
	dest := resolved.root
	backupParent := filepath.Dir(dest)
	backup := filepath.Join(backupParent, ".bak-migrate-"+sanitizeDirComponent(resolved.idOrBase())+"-"+strconv.Itoa(os.Getpid()))
	_ = os.RemoveAll(backup)
	if err := os.Rename(dest, backup); err != nil {
		return fmt.Errorf("replace existing tree: %w", err)
	}
	if err := os.Rename(staging, dest); err != nil {
		_ = os.Rename(backup, dest)
		return fmt.Errorf("activate migrated tree: %w", err)
	}
	*stagingOK = true
	_ = os.RemoveAll(backup)
	return nil
}

func (t migrateTarget) idOrBase() string {
	if t.id != "" {
		return t.id
	}
	return filepath.Base(t.root)
}

func formatMigrateReview(res MigrateResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Migrate review: %s\n", res.ID)
	fmt.Fprintf(&b, "  format:   legacy → aps\n")
	fmt.Fprintf(&b, "  digest:   %s → %s\n", emptyDash(res.OldDigest), emptyDash(res.Digest))
	if res.TrustCleared {
		fmt.Fprintf(&b, "  trust:    CLEARED (re-review required before executable activation)\n")
	} else {
		fmt.Fprintf(&b, "  trust:    none recorded; executable activation still requires trust\n")
	}
	fmt.Fprintf(&b, "  note:     migrated tree is not auto-trusted\n")
	return b.String()
}

func isPortableAgentSkill(data []byte, stem string) bool {
	if strings.TrimSpace(stem) == "" || strings.ContainsAny(stem, `/\`) {
		return false
	}
	fm, ok := parseSimpleFrontmatter(data)
	if !ok {
		return false
	}
	if strings.TrimSpace(fm["description"]) == "" {
		return false
	}
	for k := range fm {
		lk := strings.ToLower(strings.TrimSpace(k))
		switch {
		case lk == "effort", lk == "model", lk == "provider", lk == "harness",
			lk == "permission", lk == "permissions",
			strings.HasPrefix(lk, "permission."), strings.HasPrefix(lk, "permissions."):
			return false
		}
	}
	return true
}

func parseSimpleFrontmatter(data []byte) (map[string]string, bool) {
	s := string(data)
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(s, "---") {
		return nil, false
	}
	rest := s[3:]
	if rest != "" && rest[0] != '\n' && rest[0] != '\r' {
		return nil, false
	}
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		if strings.HasPrefix(rest, "---") {
			end = 0
		} else {
			return nil, false
		}
	}
	block := rest[:end]
	out := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		val = strings.Trim(val, `"'`)
		if key != "" {
			out[key] = val
		}
	}
	return out, true
}
