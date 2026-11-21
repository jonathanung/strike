package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/backup"
	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/schedule"
)

const backupTestSecret = "sk-ant-api03-LEAKEDVALUEHERE99"

// backupTestSettings records what restore writes on top of testSettings.
type backupTestSettings struct {
	testSettings
	mu       sync.Mutex
	defaults host.UserDefaults
	theme    string
}

func (s *backupTestSettings) Defaults() host.UserDefaults {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.defaults
	d.Theme = s.theme
	return d
}

func (s *backupTestSettings) SaveDefaults(provider, model, agent, effort, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&s.defaults.Provider, provider)
	set(&s.defaults.Model, model)
	set(&s.defaults.Agent, agent)
	set(&s.defaults.Effort, effort)
	set(&s.defaults.PermissionMode, mode)
	return nil
}

func (s *backupTestSettings) SaveTheme(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.theme = id
	return nil
}

type backupGateway struct {
	srv      *Server
	dir      string
	mem      *testMemory
	settings *backupTestSettings
	sched    *schedule.Service
}

func newBackupGateway(t *testing.T, live bool) *backupGateway {
	t.Helper()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	sched, err := schedule.Open(t.TempDir(), schedule.Hooks{Now: func() time.Time { return now }}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &backupGateway{
		dir:      t.TempDir(),
		mem:      newTestMemory(),
		settings: &backupTestSettings{},
		sched:    sched,
	}
	opts := Options{
		SessionDir: g.dir,
		Schedules:  sched,
		Services:   &host.Services{Memory: g.mem, Settings: g.settings},
	}
	if live {
		opts.Live = NewLive("live", t.TempDir(), nil, make(chan protocol.Op))
	}
	g.srv, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (g *backupGateway) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	g.srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func (g *backupGateway) restore(t *testing.T, archive string, dryRun bool) backup.Report {
	t.Helper()
	path := "/v1/backup/restore"
	if dryRun {
		path += "?dryRun=1"
	}
	rec := g.do(t, http.MethodPost, path, archive)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore dryRun=%v = %d %s", dryRun, rec.Code, rec.Body.String())
	}
	var rep backup.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func seedBackupSource(t *testing.T, g *backupGateway) {
	t.Helper()
	m := session.NewManager(g.dir)
	for _, s := range []struct {
		opts session.CreateOptions
		text string
	}{
		{session.CreateOptions{ID: "s-root", Title: "Fix the flaky test", ProjectKey: "/repo"}, "my key is " + backupTestSecret},
		{session.CreateOptions{ID: "s-child", ParentSessionID: "s-root", LeadSessionID: "s-root", Title: "worker"}, "run the suite"},
	} {
		if _, err := m.Create(s.opts); err != nil {
			t.Fatal(err)
		}
		for _, ev := range []protocol.Event{protocol.UserMessage{Text: s.text}, protocol.TextDelta{Text: "on it"}} {
			if err := m.Append(s.opts.ID, ev); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Close(s.opts.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.mem.Put("prefs", "table tests", []string{"style"}); err != nil {
		t.Fatal(err)
	}
	if err := g.mem.Put("token-note", "ANTHROPIC_API_KEY="+backupTestSecret, nil); err != nil {
		t.Fatal(err)
	}
	g.settings.defaults = host.UserDefaults{Provider: "anthropic", Model: "claude-x", PermissionMode: "yolo"}
	g.settings.theme = "dusk"
	on := true
	if _, err := g.sched.Create(schedule.Spec{Name: "Morning", Kind: schedule.KindCron, Cron: "0 9 * * 1-5", Timezone: "UTC",
		Source: schedule.SourcePrompt, Prompt: "review CI", Enabled: &on}); err != nil {
		t.Fatal(err)
	}
}

func exportBackup(t *testing.T, g *backupGateway) string {
	t.Helper()
	rec := g.do(t, http.MethodGet, "/v1/backup/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, backup.Filename) {
		t.Fatalf("disposition = %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q", cc)
	}
	return rec.Body.String()
}

func TestBackupExportExcludesSecretsAndListsExclusions(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	body := exportBackup(t, src)
	if strings.Contains(body, backupTestSecret) {
		t.Fatalf("archive leaked a credential:\n%s", body)
	}
	if strings.Contains(body, "yolo") {
		t.Fatalf("archive carries the permission mode:\n%s", body)
	}
	a, err := backup.Decode([]byte(body))
	if err != nil {
		t.Fatalf("own export does not verify: %v", err)
	}
	items := map[string]int{}
	for _, s := range a.Manifest.Sections {
		items[s.Name] = s.Items
	}
	want := map[string]int{backup.SectionSessions: 2, backup.SectionMemory: 2, backup.SectionSchedules: 1, backup.SectionSettings: 1}
	for name, n := range want {
		if items[name] != n {
			t.Fatalf("manifest %s items = %d, want %d (%v)", name, items[name], n, items)
		}
	}
	kinds := map[string]bool{}
	text := ""
	for _, ex := range a.Manifest.Excluded {
		kinds[ex.Kind] = true
		text += ex.Category + "\n"
	}
	for _, k := range []string{backup.ExcludeSecret, backup.ExcludePolicy, backup.ExcludeLater} {
		if !kinds[k] {
			t.Fatalf("no %s exclusion listed: %s", k, text)
		}
	}
	for _, name := range []string{"provider credentials", "gateway bearer tokens", "script schedules", "permission mode"} {
		if !strings.Contains(text, name) {
			t.Fatalf("exclusions do not name %q:\n%s", name, text)
		}
	}
}

func TestBackupRoundTripRestoresEveryStoreAndIsIdempotent(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	archive := exportBackup(t, src)

	dst := newBackupGateway(t, true)
	plan := dst.restore(t, archive, true)
	if !plan.DryRun {
		t.Fatal("dry run not flagged")
	}
	wantCreate := map[string]int{backup.SectionSessions: 2, backup.SectionMemory: 2, backup.SectionSchedules: 1, backup.SectionSettings: 3}
	for section, n := range wantCreate {
		if got := plan.Counts[section][backup.ActionCreate]; got != n {
			t.Fatalf("plan %s create = %d, want %d (%v)", section, got, n, plan.Counts)
		}
	}
	if list, _ := session.NewManager(dst.dir).List(); len(list) != 0 {
		t.Fatalf("dry run wrote sessions: %+v", list)
	}
	if entries, _ := dst.mem.List(""); len(entries) != 0 {
		t.Fatalf("dry run wrote memory: %+v", entries)
	}
	if len(dst.sched.List()) != 0 || dst.settings.Defaults().Provider != "" {
		t.Fatal("dry run wrote schedules or settings")
	}

	done := dst.restore(t, archive, false)
	if done.DryRun {
		t.Fatal("apply flagged as dry run")
	}
	for section, n := range wantCreate {
		if got := done.Counts[section][backup.ActionCreate]; got != n {
			t.Fatalf("restore %s create = %d, want %d (%+v)", section, got, n, done.Items)
		}
	}

	// Sessions: same ids, same links, same redacted events.
	for _, id := range []string{"s-root", "s-child"} {
		want, err := session.ExportPackage(src.dir, id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := session.ExportPackage(dst.dir, id)
		if err != nil {
			t.Fatalf("restored session %s: %v", id, err)
		}
		if !session.SameEvents(want, got) {
			t.Fatalf("session %s events differ after restore", id)
		}
		if got.Meta.Title != want.Meta.Title || got.Meta.ParentSessionID != want.Meta.ParentSessionID {
			t.Fatalf("session %s meta = %+v, want %+v", id, got.Meta, want.Meta)
		}
	}
	// Memory: same keys and values (credential already scrubbed).
	if e, ok, _ := dst.mem.Get("prefs"); !ok || e.Value != "table tests" || len(e.Tags) != 1 {
		t.Fatalf("memory prefs = %+v ok=%v", e, ok)
	}
	if e, ok, _ := dst.mem.Get("token-note"); !ok || strings.Contains(e.Value, backupTestSecret) {
		t.Fatalf("memory token-note = %+v ok=%v", e, ok)
	}
	// Schedules: definition restored, paused.
	scheds := dst.sched.List()
	if len(scheds) != 1 || scheds[0].Name != "Morning" || scheds[0].Cron != "0 9 * * 1-5" || scheds[0].Prompt != "review CI" {
		t.Fatalf("schedules = %+v", scheds)
	}
	if scheds[0].Enabled {
		t.Fatal("restored schedule must start paused")
	}
	// Settings: non-secret defaults restored, permission mode never.
	d := dst.settings.Defaults()
	if d.Provider != "anthropic" || d.Model != "claude-x" || d.Theme != "dusk" {
		t.Fatalf("settings = %+v", d)
	}
	if d.PermissionMode != "" {
		t.Fatalf("permission mode restored from a file: %q", d.PermissionMode)
	}

	// A second restore of the same archive changes nothing.
	again := dst.restore(t, archive, false)
	for _, it := range again.Items {
		if it.Action != backup.ActionSkip {
			t.Fatalf("second restore item %+v, want skip", it)
		}
	}
	if list, _ := session.NewManager(dst.dir).List(); len(list) != 2 {
		t.Fatalf("second restore duplicated sessions: %d", len(list))
	}
	if len(dst.sched.List()) != 1 {
		t.Fatalf("second restore duplicated schedules: %+v", dst.sched.List())
	}
}

func TestBackupRestoreNeverOverwritesDifferingRecords(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	archive := exportBackup(t, src)

	dst := newBackupGateway(t, true)
	// The target already has differing state under the same names.
	m := session.NewManager(dst.dir)
	if _, err := m.Create(session.CreateOptions{ID: "s-root", Title: "local work"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Append("s-root", protocol.UserMessage{Text: "something else"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close("s-root"); err != nil {
		t.Fatal(err)
	}
	if err := dst.mem.Put("prefs", "local value", nil); err != nil {
		t.Fatal(err)
	}
	dst.settings.defaults.Provider = "openai"

	rep := dst.restore(t, archive, false)
	byID := map[string]backup.Item{}
	for _, it := range rep.Items {
		byID[it.Section+"/"+it.ID] = it
	}
	if it := byID["sessions/s-root"]; it.Action != backup.ActionCopy || it.Target == "" || it.Target == "s-root" {
		t.Fatalf("session conflict item = %+v", it)
	}
	if it := byID["memory/project:prefs"]; it.Action != backup.ActionCopy || it.Target != "prefs (restored)" {
		t.Fatalf("memory conflict item = %+v", it)
	}
	if it := byID["settings/provider"]; it.Action != backup.ActionSkip || !strings.Contains(it.Detail, "openai") {
		t.Fatalf("settings conflict item = %+v", it)
	}

	// The target's own records are untouched.
	if e, _, _ := dst.mem.Get("prefs"); e.Value != "local value" {
		t.Fatalf("memory prefs overwritten: %+v", e)
	}
	if e, ok, _ := dst.mem.Get("prefs (restored)"); !ok || e.Value != "table tests" {
		t.Fatalf("memory copy = %+v ok=%v", e, ok)
	}
	if dst.settings.Defaults().Provider != "openai" {
		t.Fatal("provider overwritten")
	}
	events, err := session.NewManager(dst.dir).Replay("s-root")
	if err != nil || len(events) != 1 {
		t.Fatalf("local session changed: %d events, err %v", len(events), err)
	}
	copyInfo, err := session.NewManager(dst.dir).Get(byID["sessions/s-root"].Target)
	if err != nil {
		t.Fatal(err)
	}
	if copyInfo.Title != "Fix the flaky test (restored)" {
		t.Fatalf("copy title = %q", copyInfo.Title)
	}
}

func TestBackupRestoreRejectsTamperedArchiveWithoutWriting(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	archive := exportBackup(t, src)
	tampered := strings.Replace(archive, "table tests", "evil tests!", 1)
	if tampered == archive {
		t.Fatal("tamper did not apply")
	}

	dst := newBackupGateway(t, true)
	for _, path := range []string{"/v1/backup/restore?dryRun=1", "/v1/backup/restore"} {
		rec := dst.do(t, http.MethodPost, path, tampered)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "integrity check") {
			t.Fatalf("%s tampered = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if list, _ := session.NewManager(dst.dir).List(); len(list) != 0 {
		t.Fatalf("tampered restore wrote sessions: %+v", list)
	}
	if entries, _ := dst.mem.List(""); len(entries) != 0 {
		t.Fatalf("tampered restore wrote memory: %+v", entries)
	}
	for _, body := range []string{"", "not json", `{"format":"strike.memory","version":1}`} {
		rec := dst.do(t, http.MethodPost, "/v1/backup/restore", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q = %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestBackupRestoreApplyNeedsLiveGateway(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	archive := exportBackup(t, src)

	ro := newBackupGateway(t, false)
	if rec := ro.do(t, http.MethodPost, "/v1/backup/restore", archive); rec.Code != http.StatusForbidden {
		t.Fatalf("attach-only apply = %d %s", rec.Code, rec.Body.String())
	}
	plan := ro.restore(t, archive, true)
	if plan.Counts[backup.SectionSessions][backup.ActionCreate] != 2 {
		t.Fatalf("attach-only dry run = %+v", plan.Counts)
	}
	if list, _ := session.NewManager(ro.dir).List(); len(list) != 0 {
		t.Fatalf("attach-only wrote sessions: %+v", list)
	}
}

func TestExportBackupSchedulesDropsScriptsAndPermissionModes(t *testing.T) {
	got := exportBackupSchedules([]schedule.Schedule{
		{ID: "sch_b", Name: "nightly", Kind: schedule.KindCron, Cron: "0 2 * * *", Source: schedule.SourcePrompt, Prompt: "token " + backupTestSecret, PermissionMode: "yolo"},
		{ID: "sch_a", Name: "deploy", Kind: schedule.KindInterval, Every: "1h", Source: schedule.SourceScript, Command: "curl -H 'Authorization: Bearer x' ..."},
	})
	if len(got) != 1 || got[0].ID != "sch_b" {
		t.Fatalf("schedules = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{backupTestSecret, "yolo", "curl", "Authorization"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("schedule export carries %q: %s", banned, raw)
		}
	}
}

func TestBackupRestoreReportsMissingStores(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	archive := exportBackup(t, src)

	bare, err := New(Options{SessionDir: t.TempDir(), Live: NewLive("live", t.TempDir(), nil, make(chan protocol.Op))})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	bare.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/backup/restore?dryRun=1", strings.NewReader(archive)))
	if rec.Code != http.StatusOK {
		t.Fatalf("bare dry run = %d %s", rec.Code, rec.Body.String())
	}
	var rep backup.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{backup.SectionMemory, backup.SectionSchedules, backup.SectionSettings} {
		if rep.Counts[section][backup.ActionFail] == 0 {
			t.Fatalf("%s on a gateway without the store = %v", section, rep.Counts[section])
		}
	}
	if rep.Counts[backup.SectionSessions][backup.ActionCreate] != 2 {
		t.Fatalf("sessions = %v", rep.Counts[backup.SectionSessions])
	}

	exp := httptest.NewRecorder()
	bare.Handler().ServeHTTP(exp, httptest.NewRequest(http.MethodGet, "/v1/backup/export", nil))
	if exp.Code != http.StatusOK || !strings.Contains(exp.Body.String(), backup.ExcludeUnavailable) {
		t.Fatalf("bare export = %d %s", exp.Code, exp.Body.String())
	}
}

func TestBackupExportSkipsUnreadableSessionAndNamesIt(t *testing.T) {
	src := newBackupGateway(t, true)
	seedBackupSource(t, src)
	// A log from a future schema cannot be packaged; the rest still can.
	bad := session.LogPath(src.dir, "s-future")
	if err := os.WriteFile(bad, []byte(`{"type":"session.header","schemaVersion":999}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := exportBackup(t, src)
	a, err := backup.Decode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []session.Package
	if _, err := a.Section(backup.SectionSessions, &pkgs); err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("sessions exported = %d, want the 2 readable ones", len(pkgs))
	}
	found := false
	for _, ex := range a.Manifest.Excluded {
		if strings.HasPrefix(ex.Category, "session s-future:") && ex.Kind == backup.ExcludeUnavailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("unreadable session not named in exclusions: %+v", a.Manifest.Excluded)
	}
}
