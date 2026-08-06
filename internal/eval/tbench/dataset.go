package tbench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// LoadTaskPack walks a Harbor/Terminal-Bench tasks root (each child dir is a
// task with instruction.md + task.toml). When ids is non-empty, only those
// task folders are loaded (order follows ids).
func LoadTaskPack(root string, ids []string) ([]Instance, error) {
	if root == "" {
		return nil, fmt.Errorf("tbench: empty tasks root")
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("tbench: tasks root: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("tbench: tasks root %s is not a directory", root)
	}

	var want []string
	if len(ids) > 0 {
		want = append([]string{}, ids...)
	} else {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			// Skip non-task dirs commonly present in clones.
			if e.Name() == "tests" || e.Name() == "docs" {
				continue
			}
			want = append(want, e.Name())
		}
	}

	out := make([]Instance, 0, len(want))
	var missing []string
	for _, id := range want {
		dir := filepath.Join(root, id)
		in, err := LoadTaskDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, id)
				continue
			}
			// LoadTaskDir wraps not-exist; also treat missing instruction as missing.
			if _, statErr := os.Stat(dir); statErr != nil && os.IsNotExist(statErr) {
				missing = append(missing, id)
				continue
			}
			return nil, fmt.Errorf("tbench: task %s: %w", id, err)
		}
		out = append(out, in)
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("tbench: %d task dir(s) missing under %s (e.g. %v)",
			len(missing), root, trimSample(missing, 5))
	}
	return validateInstances(out)
}

// LoadTaskDir loads one Harbor task folder.
func LoadTaskDir(dir string) (Instance, error) {
	instructionPath := filepath.Join(dir, "instruction.md")
	instr, err := os.ReadFile(instructionPath)
	if err != nil {
		return Instance{}, err
	}
	id := filepath.Base(dir)
	in := Instance{
		InstanceID:  id,
		Instruction: strings.TrimSpace(string(instr)),
		TaskDir:     dir,
	}
	tomlPath := filepath.Join(dir, "task.toml")
	if raw, err := os.ReadFile(tomlPath); err == nil {
		meta := parseTaskTOML(string(raw))
		if meta.DockerImage != "" {
			in.DockerImage = meta.DockerImage
		}
		if meta.Category != "" {
			in.Category = meta.Category
		}
		if meta.Difficulty != "" {
			in.Difficulty = meta.Difficulty
		}
		in.AgentTimeout = meta.AgentTimeout
		in.VerifyTimeout = meta.VerifyTimeout
		if meta.Name != "" {
			// Prefer folder name as instance id; name is informational only.
			_ = meta.Name
		}
	}
	if in.DockerImage == "" {
		// Convention used by TB2 prebuilt images.
		in.DockerImage = fmt.Sprintf("alexgshaw/%s:20251031", id)
	}
	if in.Instruction == "" {
		return Instance{}, fmt.Errorf("empty instruction.md")
	}
	return in, nil
}

// LoadInstancesJSONL reads Instance records from a JSONL (or JSON array) file.
func LoadInstancesJSONL(path string) ([]Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseInstances(data)
}

// ParseInstances accepts JSONL (one object per line) or a JSON array.
func ParseInstances(data []byte) ([]Instance, error) {
	trim := bytesTrimSpace(data)
	if len(trim) == 0 {
		return nil, fmt.Errorf("tbench: empty dataset")
	}
	if trim[0] == '[' {
		var all []Instance
		if err := json.Unmarshal(trim, &all); err != nil {
			return nil, fmt.Errorf("tbench: dataset json array: %w", err)
		}
		return validateInstances(all)
	}
	var all []Instance
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var in Instance
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			return nil, fmt.Errorf("tbench: dataset line %d: %w", lineNo, err)
		}
		all = append(all, in)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("tbench: dataset scan: %w", err)
	}
	return validateInstances(all)
}

func validateInstances(all []Instance) ([]Instance, error) {
	if len(all) == 0 {
		return nil, fmt.Errorf("tbench: dataset has no instances")
	}
	for i, in := range all {
		if in.InstanceID == "" {
			return nil, fmt.Errorf("tbench: dataset record %d: missing instance_id", i)
		}
		if strings.TrimSpace(in.Instruction) == "" {
			return nil, fmt.Errorf("tbench: %s: missing instruction", in.InstanceID)
		}
	}
	return all, nil
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

func trimSample(ss []string, n int) []string {
	if len(ss) <= n {
		return ss
	}
	return ss[:n]
}

// taskTOMLMeta is the subset of Harbor task.toml we need.
type taskTOMLMeta struct {
	Name          string
	DockerImage   string
	Category      string
	Difficulty    string
	AgentTimeout  float64
	VerifyTimeout float64
}

var (
	reTOMLString  = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\s*=\s*"([^"]*)"`)
	reTOMLFloat   = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_]+)\s*=\s*([0-9]+(?:\.[0-9]+)?)`)
	reTOMLSection = regexp.MustCompile(`(?m)^\s*\[([^\]]+)\]\s*$`)
)

// parseTaskTOML extracts known keys without a full TOML dependency.
func parseTaskTOML(raw string) taskTOMLMeta {
	var meta taskTOMLMeta
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := reTOMLSection.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		if m := reTOMLString.FindStringSubmatch(line); m != nil {
			key, val := m[1], m[2]
			switch {
			case section == "task" && key == "name":
				meta.Name = val
			case section == "environment" && key == "docker_image":
				meta.DockerImage = val
			case section == "metadata" && key == "category":
				meta.Category = val
			case section == "metadata" && key == "difficulty":
				meta.Difficulty = val
			}
			continue
		}
		if m := reTOMLFloat.FindStringSubmatch(line); m != nil {
			key := m[1]
			f, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				continue
			}
			switch {
			case section == "agent" && key == "timeout_sec":
				meta.AgentTimeout = f
			case section == "verifier" && key == "timeout_sec":
				meta.VerifyTimeout = f
			}
		}
	}
	return meta
}
