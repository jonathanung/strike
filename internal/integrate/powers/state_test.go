package powers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "powers-state.json")
	when := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	in := State{Muted: []string{"zeta", "aws"}, LastUsed: map[string]time.Time{"aws": when}}
	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.Muted, []string{"zeta", "aws"}) {
		t.Fatalf("SaveState mutated the caller's slice: %v", in.Muted)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("state file mode = %o, want 600", mode)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Muted, []string{"aws", "zeta"}) {
		t.Errorf("muted = %v, want sorted [aws zeta]", got.Muted)
	}
	if !got.LastUsed["aws"].Equal(when) {
		t.Errorf("lastUsed = %v", got.LastUsed)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestLoadStateMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		body    *string
		wantErr bool
	}{
		{name: "missing file is empty state", body: nil},
		{name: "corrupt file is an error", body: stateBody("{not json"), wantErr: true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "state"+string(rune('a'+i))+".json")
			if tt.body != nil {
				if err := os.WriteFile(path, []byte(*tt.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st, err := LoadState(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(st.Muted) != 0 || len(st.LastUsed) != 0 {
				t.Errorf("state = %+v, want empty", st)
			}
		})
	}
}

func stateBody(s string) *string { return &s }
