package config

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

//go:embed skills/*.md
var builtinSkillFS embed.FS

// BuiltinSkills returns the shipping skills embedded in the binary
// (/commit, /push, /pr, /ship, /review, /learn, /deslop, /verify, /write-guards,
// /devcontainer). User global/project skills override same names.
func BuiltinSkills() []Skill {
	entries, err := fs.ReadDir(builtinSkillFS, "skills")
	if err != nil {
		return nil
	}
	skills := make([]Skill, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		data, err := builtinSkillFS.ReadFile(path.Join("skills", ent.Name()))
		if err != nil {
			continue
		}
		meta, body, _ := parseFrontmatter(string(data))
		if strings.TrimSpace(body) == "" {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ".md")
		if meta["name"] != "" {
			name = meta["name"]
		}
		if err := ValidateSkillName(name); err != nil {
			continue
		}
		skills = append(skills, Skill{Name: name, Description: meta["description"], Template: body, Builtin: true})
	}
	return skills
}

// mergeSkills overlays later skills onto earlier ones by name, preserving
// first-seen order and appending newly introduced names in overlay order.
func mergeSkills(base, overlay []Skill) []Skill {
	if len(overlay) == 0 {
		return append([]Skill(nil), base...)
	}
	if len(base) == 0 {
		return append([]Skill(nil), overlay...)
	}
	byName := make(map[string]Skill, len(base)+len(overlay))
	order := make([]string, 0, len(base)+len(overlay))
	for _, s := range base {
		if _, ok := byName[s.Name]; !ok {
			order = append(order, s.Name)
		}
		byName[s.Name] = s
	}
	for _, s := range overlay {
		if _, ok := byName[s.Name]; !ok {
			order = append(order, s.Name)
		}
		byName[s.Name] = s
	}
	out := make([]Skill, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}
