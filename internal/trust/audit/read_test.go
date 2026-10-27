package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike/pkg/telemetry"
)

func TestReadFamilyNewestPermissionOnly(t *testing.T) {
	dir := t.TempDir()
	sink, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Record(FamilyServeOp, "", "", "", "", map[string]string{"op": "interrupt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, action := range []string{"ask", "deny", "allow"} {
		err := sink.Record(FamilyPermission, "sess", "", "", "", telemetry.PermissionEvent{
			Permission: "bash",
			Action:     action,
			Patterns:   []string{"echo hi"},
			SessionID:  "sess",
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			// keep the first record, then cap
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadFamily(dir, FamilyPermission, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("len = %d", len(rows))
	}
	if rows[0].Family != FamilyPermission || rows[1].Family != FamilyPermission {
		t.Fatalf("rows = %+v", rows)
	}
	var first, second telemetry.PermissionEvent
	if err := json.Unmarshal(rows[0].Payload, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rows[1].Payload, &second); err != nil {
		t.Fatal(err)
	}
	if first.Action != "allow" || second.Action != "deny" {
		t.Fatalf("order = %s then %s", first.Action, second.Action)
	}
	missing, err := ReadFamily(filepath.Join(dir, "nope"), FamilyPermission, 10)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing = %v %v", missing, err)
	}
}
