package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestAppendWritesCompleteLinesAndHeader(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "durable-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(protocol.UserMessage{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(protocol.TurnCompleted{StopReason: "end_turn"}); err != nil {
		t.Fatal(err)
	}
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (header+2 events)\n%s", len(lines), raw)
	}
	var hdr logHeader
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.Type != headerType || hdr.SchemaVersion != LogSchemaVersion {
		t.Fatalf("header = %+v", hdr)
	}
	// Every line must be complete JSON (no torn records).
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d not valid JSON: %q", i+1, line)
		}
	}

	got, err := Replay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("replayed %d events, want 2", len(got))
	}
	ver, err := InspectSchemaVersion(path)
	if err != nil || ver != LogSchemaVersion {
		t.Fatalf("InspectSchemaVersion = %d, %v", ver, err)
	}
}

func TestReplaySkipsTrailingPartialLine(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "partial-tail")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(protocol.UserMessage{Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate crash mid-append: incomplete JSON, no trailing newline.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"text.delta","time":"2020-01-01T00:00:00Z","data":{"text":"cut`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Confirm fixture has no trailing newline (torn write).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] == '\n' {
		t.Fatalf("fixture should end mid-line, got ending %q", raw[len(raw)-1:])
	}

	got, err := Replay(path)
	if err != nil {
		t.Fatalf("Replay after crash residue: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1 complete record", len(got))
	}
	if um, ok := got[0].(protocol.UserMessage); !ok || um.Text != "kept" {
		t.Fatalf("got %#v", got[0])
	}
}

func TestReplayCorruptInteriorActionable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-mid.jsonl")
	// Legacy-style log (no header) with corrupt middle line.
	body := "" +
		`{"type":"user.message","time":"2020-01-01T00:00:00Z","data":{"text":"a"}}` + "\n" +
		`not-json` + "\n" +
		`{"type":"text.delta","time":"2020-01-01T00:00:01Z","data":{"text":"b"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Replay(path)
	if err == nil {
		t.Fatal("expected corrupt error")
	}
	var ce *CorruptError
	if !errors.As(err, &ce) {
		t.Fatalf("err type %T: %v", err, err)
	}
	if ce.Line != 2 {
		t.Fatalf("line = %d, want 2", ce.Line)
	}
	msg := err.Error()
	for _, want := range []string{"line 2", "repair", path} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestReplayUnknownNewerSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "future.jsonl")
	hdr := `{"type":"session.header","schemaVersion":99,"time":"2020-01-01T00:00:00Z"}` + "\n"
	ev := `{"type":"user.message","time":"2020-01-01T00:00:01Z","data":{"text":"x"}}` + "\n"
	if err := os.WriteFile(path, []byte(hdr+ev), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Replay(path)
	if err == nil {
		t.Fatal("expected schema version error")
	}
	var se *SchemaVersionError
	if !errors.As(err, &se) {
		t.Fatalf("err type %T: %v", err, err)
	}
	if se.Found != 99 || se.Support != LogSchemaVersion {
		t.Fatalf("SchemaVersionError = %+v", se)
	}
	if !strings.Contains(err.Error(), "upgrade strike") {
		t.Errorf("error should tell operator to upgrade: %v", err)
	}

	// Manager.Open must also fail closed.
	m := NewManager(dir)
	// Need matching filename id.
	id := "future-open"
	path2 := LogPath(dir, id)
	if err := os.WriteFile(path2, []byte(hdr+ev), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(id); err == nil {
		t.Fatal("Open should reject newer schema")
	} else if !errors.As(err, &se) {
		t.Fatalf("Open err = %v", err)
	}
}

func TestReplayLegacyWithoutHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	contents := "" +
		`{"type":"user.message","time":"2020-01-01T00:00:00Z","data":{"text":"hello"}}` + "\n" +
		`{"type":"turn.completed","time":"2020-01-01T00:00:01Z","data":{"stopReason":"end_turn"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Replay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	ver, err := InspectSchemaVersion(path)
	if err != nil || ver != LogSchemaVersion {
		t.Fatalf("legacy version = %d, %v", ver, err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	info, err := m.Create(CreateOptions{Title: "support bundle", ProjectKey: "/repos/x"})
	if err != nil {
		t.Fatal(err)
	}
	key := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	events := []protocol.Event{
		protocol.UserMessage{Text: "my key is " + key},
		protocol.TurnStarted{},
		protocol.TextDelta{Text: "ok"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	for _, ev := range events {
		if err := m.Append(info.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}

	exportPath := filepath.Join(t.TempDir(), "bundle.json")
	if err := m.Export(info.ID, exportPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("export leaked secret:\n%s", raw)
	}
	pkg, err := ReadPackage(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Format != PackageFormat || pkg.Version != PackageVersion {
		t.Fatalf("pkg header = %+v", pkg)
	}
	if !pkg.Redacted || pkg.SourceID != info.ID {
		t.Fatalf("pkg = %+v", pkg)
	}
	if len(pkg.Events) != len(events) {
		t.Fatalf("events = %d, want %d", len(pkg.Events), len(events))
	}

	imported, err := m.Import(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID == info.ID {
		t.Fatal("import must mint a new session id")
	}
	if imported.Title != "support bundle" {
		t.Fatalf("title = %q", imported.Title)
	}
	if imported.ProjectKey != "/repos/x" {
		t.Fatalf("project = %q", imported.ProjectKey)
	}
	meta, err := ReadMeta(dir, imported.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ForkedFrom != "" {
		t.Fatalf("import must not set ForkedFrom (got %q); use Fork for lineage", meta.ForkedFrom)
	}

	got, err := m.Replay(imported.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("imported events = %d, want %d", len(got), len(events))
	}
	um := got[0].(protocol.UserMessage)
	if strings.Contains(um.Text, key) {
		t.Fatalf("imported event leaked key: %q", um.Text)
	}
	if _, ok := got[3].(protocol.TurnCompleted); !ok {
		t.Fatalf("last event = %T", got[3])
	}
}

func TestImportRejectsUnknownPackageVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	body := `{"format":"strike.session","version":99,"schemaVersion":1,"events":[]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(t.TempDir()).Import(path); err == nil {
		t.Fatal("expected version error")
	}
}

func TestForkIndependentIDLinkedParent(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	root, err := m.Create(CreateOptions{Title: "original", ProjectKey: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []protocol.Event{
		protocol.UserMessage{Text: "one"},
		protocol.TurnCompleted{StopReason: "end_turn"},
		protocol.UserMessage{Text: "two"},
	} {
		if err := m.Append(root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	fork, err := m.Fork(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ID == root.ID {
		t.Fatal("fork id must differ")
	}
	if fork.ParentSessionID != "" {
		t.Fatalf("fork should remain a root, ParentSessionID=%q", fork.ParentSessionID)
	}
	meta, err := ReadMeta(dir, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ForkedFrom != root.ID {
		t.Fatalf("ForkedFrom = %q, want %q", meta.ForkedFrom, root.ID)
	}
	// Parent intact.
	parentEvs, err := m.Replay(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	forkEvs, err := m.Replay(fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentEvs) != 3 || len(forkEvs) != 3 {
		t.Fatalf("parent=%d fork=%d", len(parentEvs), len(forkEvs))
	}
}

func TestRetentionMaxSessions(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	var ids []string
	for i := 0; i < 5; i++ {
		info, err := m.Create(CreateOptions{Title: "s"})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Append(info.ID, protocol.UserMessage{Text: "x"}); err != nil {
			t.Fatal(err)
		}
		if err := m.Close(info.ID); err != nil {
			t.Fatal(err)
		}
		// Touch mtimes so ordering is stable: sleep tiny + rewrite meta.
		ids = append(ids, info.ID)
		time.Sleep(2 * time.Millisecond)
	}
	// Bump UpdatedAt via rename touch on last two by re-open append.
	for _, id := range ids[3:] {
		if _, err := m.Open(id); err != nil {
			t.Fatal(err)
		}
		if err := m.Append(id, protocol.TextDelta{Text: "touch"}); err != nil {
			t.Fatal(err)
		}
		if err := m.Close(id); err != nil {
			t.Fatal(err)
		}
	}

	res, err := m.ApplyRetention(RetentionPolicy{MaxSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) < 3 {
		t.Fatalf("deleted = %v, want at least 3", res.Deleted)
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("remaining = %d (%v), want 2", len(list), list)
	}
}

func TestRetentionMaxAge(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	old, err := m.Create(CreateOptions{ID: "old-sess", Title: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(old.ID); err != nil {
		t.Fatal(err)
	}
	// Backdate the log mtime.
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(LogPath(dir, old.ID), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	fresh, err := m.Create(CreateOptions{ID: "new-sess", Title: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(fresh.ID); err != nil {
		t.Fatal(err)
	}

	res, err := m.ApplyRetention(RetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != old.ID {
		t.Fatalf("deleted = %v, want [%s]", res.Deleted, old.ID)
	}
	if _, err := m.Get(fresh.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionSkipsOpen(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	openInfo, err := m.Create(CreateOptions{Title: "live"})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := m.Create(CreateOptions{Title: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(closed.ID); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(LogPath(dir, closed.ID), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	res, err := m.ApplyRetention(RetentionPolicy{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range res.Deleted {
		if id == openInfo.ID {
			t.Fatal("must not delete open session")
		}
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != closed.ID {
		t.Fatalf("deleted = %v, want [%s]", res.Deleted, closed.ID)
	}
	if _, err := m.Get(openInfo.ID); err != nil {
		t.Fatal(err)
	}
	_ = m.Close(openInfo.ID)
}

func TestRetentionFromConfig(t *testing.T) {
	p := RetentionFromConfig(10, 7, 1024)
	if p.MaxSessions != 10 || p.MaxBytes != 1024 || p.MaxAge != 7*24*time.Hour {
		t.Fatalf("%+v", p)
	}
	if !p.Active() {
		t.Fatal("expected active")
	}
	if RetentionFromConfig(0, 0, 0).Active() {
		t.Fatal("zero policy should be inactive")
	}
}

func TestRetentionMaxBytes(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	var ids []string
	for i := 0; i < 3; i++ {
		info, err := m.Create(CreateOptions{Title: "b"})
		if err != nil {
			t.Fatal(err)
		}
		// Pad log so each session is non-trivial.
		for j := 0; j < 20; j++ {
			if err := m.Append(info.ID, protocol.UserMessage{Text: strings.Repeat("x", 64)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Close(info.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, info.ID)
		time.Sleep(2 * time.Millisecond)
	}
	// Cap below total size so at least one session is evicted.
	var total int64
	for _, id := range ids {
		total += sessionBytes(dir, id)
	}
	if total < 100 {
		t.Fatalf("fixture too small: %d", total)
	}
	res, err := m.ApplyRetention(RetentionPolicy{MaxBytes: total / 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) == 0 {
		t.Fatal("expected at least one deletion under MaxBytes")
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	var remain int64
	for _, info := range list {
		remain += sessionBytes(dir, info.ID)
	}
	if remain > total/2 {
		t.Fatalf("remaining bytes %d > cap %d", remain, total/2)
	}
}

func TestReplaySliceIgnoresHeader(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "slice-hdr")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := st.Append(protocol.UserMessage{Text: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	path := st.Path()
	_ = st.Close()
	evs, total, err := ReplaySlice(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("total = %d", total)
	}
	if len(evs) != 2 {
		t.Fatalf("len = %d", len(evs))
	}
	if um := evs[0].(protocol.UserMessage); um.Text != "b" {
		t.Fatalf("first = %q", um.Text)
	}
}
