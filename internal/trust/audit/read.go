package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadFamily returns the newest records in dir whose family matches, up to
// limit. A missing directory is an empty list. A corrupt line is skipped so
// one bad segment does not hide the rest of the log.
func ReadFamily(dir, family string, limit int) ([]Record, error) {
	dir = strings.TrimSpace(dir)
	family = strings.TrimSpace(family)
	if dir == "" || family == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 40
	}
	if limit > 200 {
		limit = 200
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	var matched []Record
	for _, path := range files {
		rows, err := readFamilyFile(path, family)
		if err != nil {
			return nil, err
		}
		matched = append(matched, rows...)
	}
	if len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	return matched, nil
}

func readFamilyFile(path, family string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1<<20)
	var out []Record
	for sc.Scan() {
		line := bytesTrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Family == family {
			out = append(out, rec)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
