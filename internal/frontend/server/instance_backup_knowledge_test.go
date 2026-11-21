package server

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/backup"
)

// backupTestKnowledge is an in-memory host.Knowledge. A scope missing from
// pins behaves like an unconfigured scope (List and Put fail).
type backupTestKnowledge struct {
	mu   sync.Mutex
	pins map[string]map[string]host.KnowledgePin
	puts int
}

func newBackupTestKnowledge() *backupTestKnowledge {
	return &backupTestKnowledge{pins: map[string]map[string]host.KnowledgePin{
		"project": {},
		"global":  {},
	}}
}

func (k *backupTestKnowledge) List(scope string) ([]host.KnowledgePin, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, ok := k.pins[scope]
	if !ok {
		return nil, errors.New(scope + " knowledge is not configured")
	}
	out := make([]host.KnowledgePin, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (k *backupTestKnowledge) Put(scope, path, note, author string) (host.KnowledgePin, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, ok := k.pins[scope]
	if !ok {
		return host.KnowledgePin{}, errors.New(scope + " knowledge is not configured")
	}
	p := host.KnowledgePin{Path: path, Note: note, Author: author, Scope: scope, PinnedAt: time.Unix(0, 0).UTC()}
	m[path] = p
	k.puts++
	return p, nil
}

func (k *backupTestKnowledge) Delete(scope, path string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.pins[scope], path)
	return nil
}

func (k *backupTestKnowledge) Files(string) string { return "" }

func (k *backupTestKnowledge) get(scope, path string) (host.KnowledgePin, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, ok := k.pins[scope][path]
	return p, ok
}

func seedBackupKnowledge(t *testing.T, g *backupGateway) {
	t.Helper()
	for _, p := range []struct{ scope, path, note string }{
		{"project", "docs/design.md", "read before touching the planner"},
		{"project", "AGENTS.md", "token " + backupTestSecret},
		{"global", "/home/me/notes/style.md", ""},
	} {
		if _, err := g.know.Put(p.scope, p.path, p.note, "user"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupKnowledgePinsRoundTrip(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupKnowledge(t, src)
	archive := exportBackup(t, src)
	if strings.Contains(archive, backupTestSecret) {
		t.Fatalf("archive leaked a credential from a pin note:\n%s", archive)
	}
	a, err := backup.Decode([]byte(archive))
	if err != nil {
		t.Fatalf("own export does not verify: %v", err)
	}
	var pins []backupKnowledgePin
	if ok, err := a.Section(backup.SectionKnowledge, &pins); err != nil || !ok {
		t.Fatalf("knowledge section ok=%v err=%v", ok, err)
	}
	want := []string{"project:AGENTS.md", "project:docs/design.md", "global:/home/me/notes/style.md"}
	if len(pins) != len(want) {
		t.Fatalf("pins = %+v", pins)
	}
	for i, p := range pins {
		if got := p.Scope + ":" + p.Path; got != want[i] {
			t.Fatalf("pin %d = %s, want %s (%+v)", i, got, want[i], pins)
		}
	}
	for _, ex := range a.Manifest.Excluded {
		if ex.Kind == backup.ExcludeLater && strings.Contains(ex.Category, "knowledge") {
			t.Fatalf("knowledge pins still listed as not yet included: %+v", ex)
		}
	}

	dst := newBackupGateway(t, true)
	plan := dst.restore(t, archive, true)
	if got := plan.Counts[backup.SectionKnowledge][backup.ActionCreate]; got != 3 {
		t.Fatalf("plan knowledge = %v", plan.Counts[backup.SectionKnowledge])
	}
	if dst.know.puts != 0 {
		t.Fatalf("dry run pinned %d paths", dst.know.puts)
	}

	done := dst.restore(t, archive, false)
	if got := done.Counts[backup.SectionKnowledge][backup.ActionCreate]; got != 3 {
		t.Fatalf("restore knowledge = %+v", done.Items)
	}
	if p, ok := dst.know.get("project", "docs/design.md"); !ok || p.Note != "read before touching the planner" || p.Author != "user" {
		t.Fatalf("restored pin = %+v ok=%v", p, ok)
	}
	if p, ok := dst.know.get("project", "AGENTS.md"); !ok || strings.Contains(p.Note, backupTestSecret) {
		t.Fatalf("restored AGENTS.md pin = %+v ok=%v", p, ok)
	}
	if _, ok := dst.know.get("global", "/home/me/notes/style.md"); !ok {
		t.Fatal("global pin not restored")
	}

	again := dst.restore(t, archive, false)
	if got := again.Counts[backup.SectionKnowledge][backup.ActionSkip]; got != 3 {
		t.Fatalf("second restore knowledge = %v", again.Counts[backup.SectionKnowledge])
	}
	if dst.know.puts != 3 {
		t.Fatalf("second restore re-pinned: puts = %d", dst.know.puts)
	}
}

func TestBackupKnowledgeRestoreKeepsTargetNote(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupKnowledge(t, src)
	archive := exportBackup(t, src)

	dst := newBackupGateway(t, true)
	if _, err := dst.know.Put("project", "docs/design.md", "target's own note", "me"); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		rep := dst.restore(t, archive, dryRun)
		var found bool
		for _, it := range rep.Items {
			if it.Section != backup.SectionKnowledge || it.ID != "project:docs/design.md" {
				continue
			}
			found = true
			if it.Action != backup.ActionSkip || !strings.Contains(it.Detail, "kept this gateway's note") {
				t.Fatalf("dryRun=%v item = %+v", dryRun, it)
			}
		}
		if !found {
			t.Fatalf("dryRun=%v: no item for the conflicting pin: %+v", dryRun, rep.Items)
		}
	}
	if p, _ := dst.know.get("project", "docs/design.md"); p.Note != "target's own note" {
		t.Fatalf("target note overwritten: %+v", p)
	}
}

func TestBackupKnowledgeUnavailableScopes(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupKnowledge(t, src)
	archive := exportBackup(t, src)

	// Target without global knowledge: global pins fail, project pins land.
	dst := newBackupGateway(t, true)
	delete(dst.know.pins, "global")
	rep := dst.restore(t, archive, false)
	c := rep.Counts[backup.SectionKnowledge]
	if c[backup.ActionCreate] != 2 || c[backup.ActionFail] != 1 {
		t.Fatalf("knowledge counts = %v (%+v)", c, rep.Items)
	}

	// Source without global knowledge: export succeeds and names the gap.
	partial := newBackupGateway(t, true)
	delete(partial.know.pins, "global")
	a, err := backup.Decode([]byte(exportBackup(t, partial)))
	if err != nil {
		t.Fatal(err)
	}
	var named bool
	for _, ex := range a.Manifest.Excluded {
		if ex.Kind == backup.ExcludeUnavailable && strings.HasPrefix(ex.Category, "global knowledge pins") {
			named = true
		}
	}
	if !named {
		t.Fatalf("unreadable global scope not named: %+v", a.Manifest.Excluded)
	}

	// Archive carrying an unknown scope is refused per record, not applied.
	if got := restoreKnowledgeScopeItem(t, dst, "workspace"); got.Action != backup.ActionFail {
		t.Fatalf("unknown scope item = %+v", got)
	}
}

func restoreKnowledgeScopeItem(t *testing.T, g *backupGateway, scope string) backup.Item {
	t.Helper()
	rep := backup.NewReport(backup.New(time.Now(), ""), true)
	g.srv.restoreBackupKnowledge(rep, []backupKnowledgePin{{Scope: scope, Path: "x.md"}}, false)
	if len(rep.Items) != 1 {
		t.Fatalf("items = %+v", rep.Items)
	}
	return rep.Items[0]
}

func TestBackupKnowledgeMissingStore(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupKnowledge(t, src)
	archive := exportBackup(t, src)

	dst := newBackupGateway(t, true)
	dst.srv.opts.Services.Knowledge = nil
	rep := dst.restore(t, archive, true)
	if rep.Counts[backup.SectionKnowledge][backup.ActionFail] != 3 {
		t.Fatalf("knowledge on a gateway without the store = %v", rep.Counts[backup.SectionKnowledge])
	}
	a, err := backup.Decode([]byte(exportBackup(t, dst)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Sections[backup.SectionKnowledge]; ok {
		t.Fatal("export without a knowledge store carries a knowledge section")
	}
}
