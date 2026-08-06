package swebench

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed testdata/subset.json
var embeddedSubsetJSON []byte

// DefaultSubsetIDs returns the fixed 50-instance E3.3 subset (sorted).
// Selection is stable: SHA-256("strike-e3.3-v1:"+id) order over SWE-bench
// Verified, first 50 — see testdata/subset.json.
func DefaultSubsetIDs() []string {
	ids, err := ParseSubsetIDs(embeddedSubsetJSON)
	if err != nil {
		// embed is validated in tests; panic only if the binary is corrupt.
		panic("swebench: embedded subset.json: " + err.Error())
	}
	return ids
}

// ParseSubsetIDs decodes a JSON array of instance id strings.
func ParseSubsetIDs(data []byte) ([]string, error) {
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("swebench: subset ids: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("swebench: subset ids: empty")
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("swebench: subset ids: empty entry")
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("swebench: subset ids: duplicate %q", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// FilterSubset keeps instances whose id is in want (order follows want).
func FilterSubset(all []Instance, want []string) ([]Instance, error) {
	byID := make(map[string]Instance, len(all))
	for _, in := range all {
		byID[in.InstanceID] = in
	}
	out := make([]Instance, 0, len(want))
	var missing []string
	for _, id := range want {
		in, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		out = append(out, in)
	}
	if len(missing) > 0 {
		var b bytes.Buffer
		fmt.Fprintf(&b, "swebench: %d subset id(s) missing from dataset", len(missing))
		if len(missing) <= 5 {
			fmt.Fprintf(&b, ": %v", missing)
		} else {
			fmt.Fprintf(&b, " (e.g. %v)", missing[:5])
		}
		return out, fmt.Errorf("%s", b.String())
	}
	return out, nil
}
