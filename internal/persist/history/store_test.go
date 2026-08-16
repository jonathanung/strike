package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenUsesStableHashedProjectFilesAndIsolatesProjects(t *testing.T) {
	root := t.TempDir()
	projectA := "/private/work/secret-project"
	projectB := "/private/work/other-project"

	a := openHistory(t, root, projectA)
	b := openHistory(t, root, projectB)
	if err := a.Add("from A"); err != nil {
		t.Fatalf("a.Add() error = %v", err)
	}
	if err := b.Add("from B"); err != nil {
		t.Fatalf("b.Add() error = %v", err)
	}
	if got := a.Entries(); !slices.Equal(got, []string{"from A"}) {
		t.Errorf("a.Entries() = %q, want [from A]", got)
	}
	if got := b.Entries(); !slices.Equal(got, []string{"from B"}) {
		t.Errorf("b.Entries() = %q, want [from B]", got)
	}

	files, err := os.ReadDir(filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{hashedHistoryName(projectA), hashedHistoryName(projectB)}
	slices.Sort(wantNames)
	gotNames := make([]string, len(files))
	for i, file := range files {
		gotNames[i] = file.Name()
		if strings.Contains(file.Name(), "secret-project") || strings.Contains(file.Name(), "other-project") {
			t.Errorf("history filename %q leaks a project path", file.Name())
		}
	}
	slices.Sort(gotNames)
	if !slices.Equal(gotNames, wantNames) {
		t.Errorf("history files = %q, want stable SHA-256 files %q", gotNames, wantNames)
	}
}

func TestAddPreservesExactPromptAndStoredContent(t *testing.T) {
	root := t.TempDir()
	project := "exact-content"
	s := openHistory(t, root, project)
	prompt := "  λ雪🙂\nsecond line\r\nprovider=xai @file:./unchanged.txt\t  "
	if err := s.Add(prompt); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if got := s.Entries(); !slices.Equal(got, []string{prompt}) {
		t.Errorf("Entries() = %q, want exact prompt %q", got, prompt)
	}

	records := readDiskRecords(t, historyPath(root, project))
	if len(records) != 1 {
		t.Fatalf("stored record count = %d, want 1", len(records))
	}
	if records[0].Version != 1 {
		t.Errorf("stored version = %d, want 1", records[0].Version)
	}
	if records[0].Prompt != prompt {
		t.Errorf("stored prompt = %q, want exact provider/file content %q", records[0].Prompt, prompt)
	}
}

func TestAddIgnoresWhitespaceOnlyPrompts(t *testing.T) {
	root := t.TempDir()
	project := "blank"
	s := openHistory(t, root, project)
	for _, prompt := range []string{"", " \t\r\n", "\u00a0\u2003"} {
		if err := s.Add(prompt); err != nil {
			t.Errorf("Add(%q) error = %v", prompt, err)
		}
	}
	if got := s.Entries(); len(got) != 0 {
		t.Errorf("Entries() = %q, want empty", got)
	}
	data, err := os.ReadFile(historyPath(root, project))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("history file contains %q after blank prompts, want empty", data)
	}
}

func TestAddDuplicateMovesExactPromptToNewest(t *testing.T) {
	s := openHistory(t, t.TempDir(), "dedupe")
	for _, prompt := range []string{"first", "second", "first", "First"} {
		if err := s.Add(prompt); err != nil {
			t.Fatalf("Add(%q) error = %v", prompt, err)
		}
	}
	want := []string{"second", "first", "First"}
	if got := s.Entries(); !slices.Equal(got, want) {
		t.Errorf("Entries() = %q, want %q", got, want)
	}
}

func TestEntriesKeepsNewest100AndReturnsACopy(t *testing.T) {
	s := openHistory(t, t.TempDir(), "bounded")
	for i := range 105 {
		if err := s.Add(string(rune(0x1000 + i))); err != nil {
			t.Fatalf("Add(%d) error = %v", i, err)
		}
	}
	want := make([]string, 100)
	for i := range want {
		want[i] = string(rune(0x1000 + i + 5))
	}
	got := s.Entries()
	if !slices.Equal(got, want) {
		t.Fatalf("Entries() = %q, want newest 100 %q", got, want)
	}
	got[0] = "mutated by caller"
	if fresh := s.Entries(); !slices.Equal(fresh, want) {
		t.Errorf("Entries() after caller mutation = %q, want unchanged %q", fresh, want)
	}
}

func TestEntriesPersistAcrossCloseAndOpen(t *testing.T) {
	root := t.TempDir()
	s := openHistory(t, root, "persistent")
	for _, prompt := range []string{"one", "two\nlines", "one"} {
		if err := s.Add(prompt); err != nil {
			t.Fatalf("Add(%q) error = %v", prompt, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openHistory(t, root, "persistent")
	want := []string{"two\nlines", "one"}
	if got := reopened.Entries(); !slices.Equal(got, want) {
		t.Errorf("reopened Entries() = %q, want %q", got, want)
	}
}

func TestOpenSecuresHistoryDirectoryAndFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not supported on Windows")
	}
	root := t.TempDir()
	historyDir := filepath.Join(root, "history")
	if err := os.Mkdir(historyDir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := historyPath(root, "permissions")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	openHistory(t, root, "permissions")

	assertPermissions(t, historyDir, 0o700)
	assertPermissions(t, path, 0o600)
}

func TestOpenCreatesEntirelyMissingGlobalRootAndUsableHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-global-root")
	project := "missing-root"
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("global root before Open() error = %v, want not exist", err)
	}

	s := openHistory(t, root, project)
	if err := s.Add("created from scratch"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if runtime.GOOS != "windows" {
		assertPermissions(t, root, 0o700)
		assertPermissions(t, filepath.Join(root, "history"), 0o700)
		assertPermissions(t, historyPath(root, project), 0o600)
	}
	reopened := openHistory(t, root, project)
	if got := reopened.Entries(); !slices.Equal(got, []string{"created from scratch"}) {
		t.Errorf("reopened Entries() = %q, want persisted prompt", got)
	}
}

func TestConcurrentAddOnOneStoreProducesCompleteRecords(t *testing.T) {
	root := t.TempDir()
	project := "concurrent-single-store"
	s := openHistory(t, root, project)
	const count = 80
	var wg sync.WaitGroup
	for i := range count {
		prompt := string(rune(0x2000+i)) + "\n@file unchanged"
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Add(prompt); err != nil {
				t.Errorf("Add(%q) error = %v", prompt, err)
			}
		}()
	}
	wg.Wait()

	entries := s.Entries()
	if len(entries) != count {
		t.Fatalf("Entries() count = %d, want %d", len(entries), count)
	}
	assertUniqueConcurrentPrompts(t, entries, count, 0x2000)
	records := readDiskRecords(t, historyPath(root, project))
	if len(records) != count {
		t.Errorf("valid disk record count = %d, want %d", len(records), count)
	}
}

func TestTwoOpenStoresAppendWithoutOverwritingEachOther(t *testing.T) {
	root := t.TempDir()
	project := "two-stores"
	first := openHistory(t, root, project)
	second := openHistory(t, root, project)
	const perStore = 30
	var wg sync.WaitGroup
	for storeIndex, store := range []*Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perStore {
				prompt := string(rune(0x3000+storeIndex*perStore+i)) + " process-like"
				if err := store.Add(prompt); err != nil {
					t.Errorf("Add(%q) error = %v", prompt, err)
				}
			}
		}()
	}
	wg.Wait()
	if err := first.Close(); err != nil {
		t.Errorf("first.Close() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Errorf("second.Close() error = %v", err)
	}

	reopened := openHistory(t, root, project)
	entries := reopened.Entries()
	if len(entries) != 2*perStore {
		t.Fatalf("reopened Entries() count = %d, want %d", len(entries), 2*perStore)
	}
	assertUniqueConcurrentPrompts(t, entries, 2*perStore, 0x3000)
}

func TestMultipleStoresKeepSupportedFilesWellFormed(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		want    []string
	}{
		{
			name: "initially empty",
			want: []string{"from first", "from second", "from first again"},
		},
		{
			name:    "valid final record without newline",
			initial: `{"version":1,"prompt":"existing"}`,
			want:    []string{"existing", "from first", "from second", "from first again"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			project := "multiple-supported-stores"
			if tt.initial != "" {
				writeHistoryFixture(t, root, project, tt.initial)
			}
			first := openHistory(t, root, project)
			second := openHistory(t, root, project)
			for _, add := range []struct {
				store  *Store
				prompt string
			}{
				{first, "from first"},
				{second, "from second"},
				{first, "from first again"},
			} {
				if err := add.store.Add(add.prompt); err != nil {
					t.Fatalf("Add(%q) error = %v", add.prompt, err)
				}
			}
			if err := first.Close(); err != nil {
				t.Fatalf("first.Close() error = %v", err)
			}
			if err := second.Close(); err != nil {
				t.Fatalf("second.Close() error = %v", err)
			}

			data, err := os.ReadFile(historyPath(root, project))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "\n\n") {
				t.Errorf("history contains an empty separator record: %q", data)
			}
			reopened := openHistory(t, root, project)
			if got := reopened.Entries(); !slices.Equal(got, tt.want) {
				t.Errorf("reopened Entries() = %q, want %q", got, tt.want)
			}
			records := readDiskRecords(t, historyPath(root, project))
			if len(records) != len(tt.want) {
				t.Errorf("disk record count = %d, want %d", len(records), len(tt.want))
			}
		})
	}
}

func TestEnqueueAcceptedBeforeImmediateCloseIsDrained(t *testing.T) {
	root := t.TempDir()
	project := "enqueue-close-drain"
	s := openHistory(t, root, project)
	done := s.Enqueue("accepted before close")

	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := awaitHistoryResult(t, done); err != nil {
		t.Fatalf("accepted Enqueue result = %v", err)
	}
	if err := awaitHistoryResult(t, s.Enqueue("after close")); err == nil {
		t.Fatal("Enqueue() after Close() error = nil, want an error")
	}

	reopened := openHistory(t, root, project)
	if got := reopened.Entries(); !slices.Equal(got, []string{"accepted before close"}) {
		t.Errorf("reopened Entries() = %q, want accepted prompt", got)
	}
}

func TestEnqueueConcurrentWithCloseDoesNotPanicOrDeadlock(t *testing.T) {
	s := openHistory(t, t.TempDir(), "enqueue-during-close")
	const count = 40
	results := make(chan (<-chan error), count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- s.Enqueue(string(rune(0x4000 + i)))
		}()
	}
	closeDone := make(chan error, 1)
	go func() {
		<-start
		closeDone <- s.Close()
	}()
	close(start)

	waitGroupWithTimeout(t, &wg)
	close(results)
	if err := awaitHistoryResult(t, closeDone); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for result := range results {
		// A request racing with Close may be accepted or rejected, but it must
		// always complete rather than panic or deadlock.
		_ = awaitHistoryResult(t, result)
	}
	if err := awaitHistoryResult(t, s.Enqueue("definitely closed")); err == nil {
		t.Fatal("Enqueue() after concurrent Close() error = nil, want an error")
	}
}

func TestEnqueuePersistsAcceptanceOrderWhenResultsAwaitedInReverse(t *testing.T) {
	root := t.TempDir()
	project := "enqueue-order"
	s := openHistory(t, root, project)
	const count = 50
	prompts := make([]string, count)
	results := make([]<-chan error, count)
	for i := range count {
		prompts[i] = string(rune(0x5000+i)) + " exact enqueue order"
		results[i] = s.Enqueue(prompts[i])
	}
	for i := len(results) - 1; i >= 0; i-- {
		if err := awaitHistoryResult(t, results[i]); err != nil {
			t.Fatalf("Enqueue(%q) result = %v", prompts[i], err)
		}
	}

	records := readDiskRecords(t, historyPath(root, project))
	got := make([]string, len(records))
	for i := range records {
		got[i] = records[i].Prompt
	}
	if !slices.Equal(got, prompts) {
		t.Errorf("disk prompt order = %q, want enqueue order %q", got, prompts)
	}
}

func TestEnqueueReturnsWhileWorkerIsBlockedAndCloseDrainsInOrder(t *testing.T) {
	root := t.TempDir()
	project := "enqueue-while-worker-blocked"
	s := openHistory(t, root, project)

	// Holding the existing per-path append lock blocks the worker without
	// depending on filesystem performance.
	s.state.mu.Lock()
	stateLocked := true
	defer func() {
		if stateLocked {
			s.state.mu.Unlock()
		}
	}()

	first := s.Enqueue("first")
	dequeueDeadline := time.Now().Add(5 * time.Second)
	for {
		s.submitMu.Lock()
		dequeued := len(s.requests) == 0
		s.submitMu.Unlock()
		if dequeued {
			break
		}
		if time.Now().After(dequeueDeadline) {
			s.state.mu.Unlock()
			stateLocked = false
			t.Fatal("worker did not dequeue the first request")
		}
		runtime.Gosched()
	}
	select {
	case err := <-first:
		s.state.mu.Unlock()
		stateLocked = false
		t.Fatalf("blocked worker completed first request with %v", err)
	default:
	}

	want := []string{"first", "second", "third", "fourth"}
	enqueued := make(chan []<-chan error, 1)
	go func() {
		results := make([]<-chan error, 0, len(want)-1)
		for _, prompt := range want[1:] {
			results = append(results, s.Enqueue(prompt))
		}
		enqueued <- results
	}()

	var results []<-chan error
	select {
	case results = <-enqueued:
	case <-time.After(5 * time.Second):
		s.state.mu.Unlock()
		stateLocked = false
		t.Fatal("Enqueue blocked while the history worker was occupied")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	closeDeadline := time.Now().Add(5 * time.Second)
	for {
		s.submitMu.Lock()
		closed := s.closed
		s.submitMu.Unlock()
		if closed {
			break
		}
		if time.Now().After(closeDeadline) {
			s.state.mu.Unlock()
			stateLocked = false
			t.Fatal("Close did not reject new requests promptly")
		}
		runtime.Gosched()
	}

	s.state.mu.Unlock()
	stateLocked = false
	if err := awaitHistoryResult(t, closeDone); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := awaitHistoryResult(t, first); err != nil {
		t.Fatalf("first Enqueue result = %v", err)
	}
	for i, result := range results {
		if err := awaitHistoryResult(t, result); err != nil {
			t.Fatalf("Enqueue(%q) result = %v", want[i+1], err)
		}
	}

	records := readDiskRecords(t, historyPath(root, project))
	got := make([]string, len(records))
	for i := range records {
		got[i] = records[i].Prompt
	}
	if !slices.Equal(got, want) {
		t.Errorf("disk prompt order after Close = %q, want %q", got, want)
	}
}

func TestAddRemainsSynchronousAndIgnoresBlankPrompts(t *testing.T) {
	root := t.TempDir()
	project := "synchronous-add"
	s := openHistory(t, root, project)
	if err := s.Add("durable before return"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if got := readDiskRecords(t, historyPath(root, project)); len(got) != 1 || got[0].Prompt != "durable before return" {
		t.Fatalf("records immediately after Add = %+v, want durable prompt", got)
	}
	if err := s.Add(" \t\r\n"); err != nil {
		t.Fatalf("blank Add() error = %v", err)
	}
	if got := readDiskRecords(t, historyPath(root, project)); len(got) != 1 {
		t.Errorf("record count after blank Add = %d, want 1", len(got))
	}
}

func TestOpenRejectsHistoryFileSymlinkWithoutChangingReferent(t *testing.T) {
	root := t.TempDir()
	project := "file-symlink"
	historyDir := filepath.Join(root, "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	referent := filepath.Join(root, "referent.jsonl")
	wantContents := []byte("referent must stay unchanged")
	if err := os.WriteFile(referent, wantContents, 0o640); err != nil {
		t.Fatal(err)
	}
	wantMode := fileMode(t, referent)
	makeSymlinkOrSkip(t, referent, historyPath(root, project))

	if _, err := Open(root, project); err == nil {
		t.Fatal("Open() through history file symlink error = nil, want rejection")
	}
	assertFileUnchanged(t, referent, wantContents, wantMode)
}

func TestOpenRejectsGlobalRootSymlinkOrReparsePointWithoutChangingReferent(t *testing.T) {
	parent := t.TempDir()
	referent := filepath.Join(parent, "global-root-referent")
	if err := os.Mkdir(referent, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(referent, "marker")
	wantContents := []byte("global root referent must stay unchanged")
	if err := os.WriteFile(marker, wantContents, 0o640); err != nil {
		t.Fatal(err)
	}
	wantDirMode := fileMode(t, referent)
	wantMarkerMode := fileMode(t, marker)
	root := filepath.Join(parent, "global-root-link")
	makeSymlinkOrSkip(t, referent, root)

	if _, err := Open(root, "global-root-symlink"); err == nil {
		t.Fatal("Open() through global root symlink/reparse point error = nil, want rejection")
	}
	if got := fileMode(t, referent); got != wantDirMode {
		t.Errorf("global root referent mode = %o, want unchanged %o", got, wantDirMode)
	}
	assertFileUnchanged(t, marker, wantContents, wantMarkerMode)
	if _, err := os.Lstat(filepath.Join(referent, "history")); !os.IsNotExist(err) {
		t.Errorf("referent history path after rejected Open error = %v, want not exist", err)
	}
}

func TestOpenRejectsNonDirectoryGlobalRootWithoutChangingIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "global-root-file")
	wantContents := []byte("not a global directory")
	if err := os.WriteFile(root, wantContents, 0o640); err != nil {
		t.Fatal(err)
	}
	wantMode := fileMode(t, root)

	if _, err := Open(root, "non-directory-global-root"); err == nil {
		t.Fatal("Open() with non-directory global root error = nil, want rejection")
	}
	assertFileUnchanged(t, root, wantContents, wantMode)
}

func TestOpenRejectsHistoryDirectorySymlinkWithoutChangingReferent(t *testing.T) {
	root := t.TempDir()
	referent := filepath.Join(root, "referent-directory")
	if err := os.Mkdir(referent, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(referent, "marker")
	wantContents := []byte("directory referent must stay unchanged")
	if err := os.WriteFile(marker, wantContents, 0o640); err != nil {
		t.Fatal(err)
	}
	wantDirMode := fileMode(t, referent)
	wantMarkerMode := fileMode(t, marker)
	makeSymlinkOrSkip(t, referent, filepath.Join(root, "history"))

	if _, err := Open(root, "directory-symlink"); err == nil {
		t.Fatal("Open() through history directory symlink error = nil, want rejection")
	}
	if got := fileMode(t, referent); got != wantDirMode {
		t.Errorf("directory referent mode = %o, want unchanged %o", got, wantDirMode)
	}
	assertFileUnchanged(t, marker, wantContents, wantMarkerMode)
}

func TestOpenRejectsNonDirectoryHistoryPathWithoutChangingIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "history")
	wantContents := []byte("not a directory")
	if err := os.WriteFile(path, wantContents, 0o640); err != nil {
		t.Fatal(err)
	}
	wantMode := fileMode(t, path)

	if _, err := Open(root, "non-directory-history"); err == nil {
		t.Fatal("Open() with non-directory history path error = nil, want rejection")
	}
	assertFileUnchanged(t, path, wantContents, wantMode)
}

func TestValidFinalRecordWithoutNewlineGetsExactlyOneAppendSeparator(t *testing.T) {
	root := t.TempDir()
	project := "unterminated-valid-final"
	first := `{"version":1,"prompt":"first"}`
	writeHistoryFixture(t, root, project, first)
	s := openHistory(t, root, project)
	if got := s.Entries(); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("Entries() = %q, want [first]", got)
	}
	if err := s.Add("second"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	data, err := os.ReadFile(historyPath(root, project))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), first+"\n{") || strings.HasPrefix(string(data), first+"\n\n") {
		t.Errorf("appended history = %q, want exactly one separator after final record", data)
	}
	if strings.Count(string(data), "\n") != 2 {
		t.Errorf("appended history newline count = %d, want 2", strings.Count(string(data), "\n"))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened := openHistory(t, root, project)
	if got := reopened.Entries(); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("reopened Entries() = %q, want [first second]", got)
	}
}

func TestOpenRejectsMalformedInteriorRecord(t *testing.T) {
	tests := []struct {
		name      string
		malformed string
	}{
		{name: "syntax", malformed: "not-json"},
		{name: "truncation", malformed: `{"version":1,"prompt":"cut`},
		{name: "JSON type mismatch", malformed: `{"version":1,"prompt":7}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			project := "malformed-interior"
			contents := "{\"version\":1,\"prompt\":\"before\"}\n" + tt.malformed + "\n{\"version\":1,\"prompt\":\"after\"}\n"
			writeHistoryFixture(t, root, project, contents)

			_, err := Open(root, project)
			if err == nil {
				t.Fatal("Open() error = nil, want malformed interior record error")
			}
			if !strings.Contains(err.Error(), "decode history line 2") {
				t.Errorf("Open() error = %q, want line 2 decode error", err)
			}
		})
	}
}

func TestOpenAcceptsMalformedFinalRecordButDisablesAppend(t *testing.T) {
	tests := []struct {
		name string
		tail string
	}{
		{name: "truncated without newline", tail: "{\"version\":1,\"prompt\":\"cut"},
		{name: "malformed syntax with newline", tail: "not-json\n"},
		{name: "JSON type mismatch with newline", tail: "{\"version\":1,\"prompt\":7}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			project := "malformed-tail"
			contents := "{\"version\":1,\"prompt\":\"kept\"}\n" + tt.tail
			writeHistoryFixture(t, root, project, contents)
			s := openHistory(t, root, project)
			if got := s.Entries(); !slices.Equal(got, []string{"kept"}) {
				t.Errorf("Entries() = %q, want [kept]", got)
			}
			if err := s.Add("must not append"); err == nil || err.Error() != "history: cannot append after a malformed final record" {
				t.Errorf("Add() error = %v, want append-disabled error", err)
			}
			data, err := os.ReadFile(historyPath(root, project))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != contents {
				t.Errorf("history after rejected Add = %q, want unchanged %q", data, contents)
			}
		})
	}
}

func TestOpenRejectsUnsupportedRecordVersion(t *testing.T) {
	tests := []struct {
		name    string
		record  string
		version int
	}{
		{name: "unsupported", record: `{"version":2,"prompt":"future"}`, version: 2},
		{name: "missing", record: `{"prompt":"missing version"}`, version: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			project := "invalid-final-version"
			writeHistoryFixture(t, root, project, tt.record)

			_, err := Open(root, project)
			if err == nil {
				t.Fatal("Open() error = nil, want unsupported version error")
			}
			want := fmt.Sprintf("unsupported record version %d", tt.version)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Open() error = %q, want %q", err, want)
			}
		})
	}
}

func TestAddAfterCloseReturnsError(t *testing.T) {
	s := openHistory(t, t.TempDir(), "closed")
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := s.Add("after close"); err == nil {
		t.Fatal("Add() after Close() error = nil, want an error")
	}
}

type diskRecord struct {
	Version int    `json:"version"`
	Prompt  string `json:"prompt"`
}

func openHistory(t *testing.T, root, project string) *Store {
	t.Helper()
	s, err := Open(root, project)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", project, err)
	}
	return s
}

func hashedHistoryName(project string) string {
	digest := sha256.Sum256([]byte(project))
	return hex.EncodeToString(digest[:]) + ".jsonl"
}

func historyPath(root, project string) string {
	return filepath.Join(root, "history", hashedHistoryName(project))
}

func readDiskRecords(t *testing.T, path string) []diskRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var records []diskRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec diskRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("invalid JSONL record %q: %v", scanner.Text(), err)
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func writeHistoryFixture(t *testing.T, root, project, contents string) {
	t.Helper()
	dir := filepath.Join(root, "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath(root, project), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s permissions = %o, want %o", path, got, want)
	}
}

func assertUniqueConcurrentPrompts(t *testing.T, entries []string, count, firstRune int) {
	t.Helper()
	seen := make(map[rune]bool, count)
	for _, prompt := range entries {
		r, _ := firstRuneIn(prompt)
		if seen[r] {
			t.Errorf("duplicate prompt beginning with %q", r)
		}
		seen[r] = true
	}
	for i := range count {
		if r := rune(firstRune + i); !seen[r] {
			t.Errorf("missing prompt beginning with %q", r)
		}
	}
}

func awaitHistoryResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err, ok := <-result:
		if !ok {
			t.Fatal("history operation closed its result channel without a result")
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for history operation")
		return nil
	}
}

func waitGroupWithTimeout(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for concurrent history operations")
	}
}

func makeSymlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink creation is unsupported: %v", err)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func assertFileUnchanged(t *testing.T, path string, wantContents []byte, wantMode os.FileMode) {
	t.Helper()
	gotContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotContents, wantContents) {
		t.Errorf("%s contents = %q, want unchanged %q", path, gotContents, wantContents)
	}
	if gotMode := fileMode(t, path); gotMode != wantMode {
		t.Errorf("%s mode = %o, want unchanged %o", path, gotMode, wantMode)
	}
}

func firstRuneIn(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func TestOpenAcceptsResolvedPathBehindFormerSymlink(t *testing.T) {
	// Users symlink ~/.strike → elsewhere; callers must pass EvalSymlinks(root)
	// (config.GlobalRoot). Open still rejects an unresolved symlink root.
	parent := t.TempDir()
	referent := filepath.Join(parent, "real-global")
	if err := os.Mkdir(referent, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link-global")
	makeSymlinkOrSkip(t, referent, link)

	if _, err := Open(link, "proj"); err == nil {
		t.Fatal("Open(unresolved symlink root) error = nil, want rejection")
	}
	s, err := Open(referent, "proj")
	if err != nil {
		t.Fatalf("Open(resolved referent) error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Add("hello via relocated state"); err != nil {
		t.Fatal(err)
	}
}
