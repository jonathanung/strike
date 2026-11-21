package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/backup"
)

// newRecordsGateway is a backup gateway that also runs issue and plan stores.
func newRecordsGateway(t *testing.T) (*backupGateway, *testIssues, *fakePlans) {
	t.Helper()
	g := newBackupGateway(t, true)
	iss, pl := newTestIssues(), newFakePlans()
	g.srv.opts.Services.Issues = iss
	g.srv.opts.Services.Plans = pl
	return g, iss, pl
}

func seedBackupRecords(t *testing.T, iss *testIssues, pl *fakePlans) {
	t.Helper()
	if _, err := iss.Create("Flaky planner test", "fails on CI with token "+backupTestSecret); err != nil {
		t.Fatal(err)
	}
	done, err := iss.Create("Ship backup", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iss.Close(done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pl.Create("s-root", "Refactor planner", []host.PlanSection{
		{Title: "Goal", Body: "use key " + backupTestSecret},
		{Title: "Steps", Body: "1. split\n2. test"},
	}); err != nil {
		t.Fatal(err)
	}
	appr, err := pl.Create("s-root", "Release notes", []host.PlanSection{{Title: "Draft", Body: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pl.SetStatus(appr.ID, "s-root", "approved", appr.Version); err != nil {
		t.Fatal(err)
	}
}

func issuesByTitle(t *testing.T, iss *testIssues) map[string]host.Issue {
	t.Helper()
	list, _ := iss.List("")
	out := map[string]host.Issue{}
	for _, it := range list {
		out[it.Title] = it
	}
	return out
}

func plansByTitle(t *testing.T, pl *fakePlans) map[string]host.Plan {
	t.Helper()
	metas, _ := pl.List()
	out := map[string]host.Plan{}
	for _, m := range metas {
		p, _, _ := pl.Get(m.ID)
		out[p.Title] = p
	}
	return out
}

func TestBackupIssuesAndPlansRoundTrip(t *testing.T) {
	src, srcIss, srcPl := newRecordsGateway(t)
	seedBackupRecords(t, srcIss, srcPl)
	archive := exportBackup(t, src)
	if strings.Contains(archive, backupTestSecret) {
		t.Fatalf("archive leaked a credential from an issue or plan:\n%s", archive)
	}
	a, err := backup.Decode([]byte(archive))
	if err != nil {
		t.Fatalf("own export does not verify: %v", err)
	}
	for _, ex := range a.Manifest.Excluded {
		if ex.Kind == backup.ExcludeLater && (strings.Contains(ex.Category, "issues") || strings.Contains(ex.Category, "plans")) {
			t.Fatalf("issues/plans still listed as not yet included: %+v", ex)
		}
		if ex.Kind == backup.ExcludeUnavailable && (ex.Category == "issues" || ex.Category == "plans") {
			t.Fatalf("store reported unavailable: %+v", ex)
		}
	}

	dst, dstIss, dstPl := newRecordsGateway(t)
	// The target already numbers its own issues: restored ones get new ids.
	if _, err := dstIss.Create("Target's own issue", "keep me"); err != nil {
		t.Fatal(err)
	}
	plan := dst.restore(t, archive, true)
	if got := plan.Counts[backup.SectionIssues][backup.ActionCreate]; got != 2 {
		t.Fatalf("plan issues = %v", plan.Counts[backup.SectionIssues])
	}
	if got := plan.Counts[backup.SectionPlans][backup.ActionCreate]; got != 2 {
		t.Fatalf("plan plans = %v", plan.Counts[backup.SectionPlans])
	}
	if list, _ := dstIss.List(""); len(list) != 1 {
		t.Fatalf("dry run wrote issues: %+v", list)
	}
	if metas, _ := dstPl.List(); len(metas) != 0 {
		t.Fatalf("dry run wrote plans: %+v", metas)
	}

	done := dst.restore(t, archive, false)
	if got := done.Counts[backup.SectionIssues][backup.ActionCreate]; got != 2 {
		t.Fatalf("restore issues = %+v", done.Items)
	}
	if got := done.Counts[backup.SectionPlans][backup.ActionCreate]; got != 2 {
		t.Fatalf("restore plans = %+v", done.Items)
	}
	issues := issuesByTitle(t, dstIss)
	if it, ok := issues["Ship backup"]; !ok || it.Status != "closed" {
		t.Fatalf("closed issue not restored closed: %+v", issues)
	}
	if it := issues["Flaky planner test"]; it.Status != "open" || strings.Contains(it.Body, backupTestSecret) || it.ID < 2 {
		t.Fatalf("open issue = %+v", it)
	}
	if issues["Target's own issue"].ID != 1 {
		t.Fatalf("target issue renumbered: %+v", issues)
	}
	plans := plansByTitle(t, dstPl)
	if p := plans["Release notes"]; p.Status != "approved" || p.OwnerRoot != "s-root" || len(p.Sections) != 1 {
		t.Fatalf("approved plan = %+v", p)
	}
	if p := plans["Refactor planner"]; p.Status != "draft" || len(p.Sections) != 2 || strings.Contains(p.Sections[0].Body, backupTestSecret) {
		t.Fatalf("draft plan = %+v", p)
	}

	again := dst.restore(t, archive, false)
	if got := again.Counts[backup.SectionIssues][backup.ActionSkip]; got != 2 {
		t.Fatalf("second restore issues = %v", again.Counts[backup.SectionIssues])
	}
	if got := again.Counts[backup.SectionPlans][backup.ActionSkip]; got != 2 {
		t.Fatalf("second restore plans = %v", again.Counts[backup.SectionPlans])
	}
	if list, _ := dstIss.List(""); len(list) != 3 {
		t.Fatalf("second restore duplicated issues: %+v", list)
	}
	if metas, _ := dstPl.List(); len(metas) != 2 {
		t.Fatalf("second restore duplicated plans: %+v", metas)
	}

	// Restoring onto the exporting gateway is a no-op, even though the
	// archive's text is redacted and the source's is not.
	self := src.restore(t, archive, true)
	if self.Counts[backup.SectionIssues][backup.ActionSkip] != 2 || self.Counts[backup.SectionPlans][backup.ActionSkip] != 2 {
		t.Fatalf("self restore = issues %v plans %v", self.Counts[backup.SectionIssues], self.Counts[backup.SectionPlans])
	}
}

func TestBackupIssueRestoreRecordChecks(t *testing.T) {
	cases := []struct {
		name   string
		seed   []host.Issue
		rec    backupIssue
		action string
		detail string
	}{
		{"empty title", nil, backupIssue{ID: 1, Title: "  "}, backup.ActionFail, "empty title"},
		{"unknown status", nil, backupIssue{ID: 1, Title: "x", Status: "wontfix"}, backup.ActionFail, "unknown issue status"},
		{"empty status means open", nil, backupIssue{ID: 1, Title: "x"}, backup.ActionCreate, ""},
		{"same text, same status", []host.Issue{{ID: 7, Title: "x", Body: "b", Status: "open"}}, backupIssue{ID: 1, Title: " x ", Body: "b\n", Status: "open"}, backup.ActionSkip, "identical"},
		{"same text, other status keeps target's", []host.Issue{{ID: 7, Title: "x", Body: "b", Status: "open"}}, backupIssue{ID: 7, Title: "x", Body: "b", Status: "closed"}, backup.ActionSkip, "kept its open status"},
		{"same id, other text creates", []host.Issue{{ID: 1, Title: "mine", Status: "open"}}, backupIssue{ID: 1, Title: "theirs", Status: "open"}, backup.ActionCreate, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _, _ := newRecordsGateway(t)
			iss := newTestIssues(tc.seed...)
			g.srv.opts.Services.Issues = iss
			before, _ := iss.List("")
			rep := backup.NewReport(backup.New(time.Now(), ""), false)
			g.srv.restoreBackupIssues(rep, []backupIssue{tc.rec}, true)
			if len(rep.Items) != 1 || rep.Items[0].Action != tc.action || !strings.Contains(rep.Items[0].Detail, tc.detail) {
				t.Fatalf("items = %+v, want one %s (%q)", rep.Items, tc.action, tc.detail)
			}
			after, _ := iss.List("")
			for _, b := range before {
				got, _, _ := iss.Get(b.ID)
				if got != b {
					t.Fatalf("target issue changed: %+v -> %+v", b, got)
				}
			}
			wantGrowth := 0
			if tc.action == backup.ActionCreate {
				wantGrowth = 1
			}
			if len(after)-len(before) != wantGrowth {
				t.Fatalf("issues %d -> %d", len(before), len(after))
			}
		})
	}
}

func TestBackupIssueRestoreDuplicateInArchiveDryRunMatchesApply(t *testing.T) {
	recs := []backupIssue{{ID: 1, Title: "dup", Body: "b"}, {ID: 2, Title: "dup", Body: "b", Status: "closed"}}
	for _, apply := range []bool{false, true} {
		g, iss, _ := newRecordsGateway(t)
		rep := backup.NewReport(backup.New(time.Now(), ""), !apply)
		g.srv.restoreBackupIssues(rep, recs, apply)
		c := rep.Counts[backup.SectionIssues]
		if c[backup.ActionCreate] != 1 || c[backup.ActionSkip] != 1 {
			t.Fatalf("apply=%v counts = %v", apply, c)
		}
		want := 0
		if apply {
			want = 1
		}
		if list, _ := iss.List(""); len(list) != want {
			t.Fatalf("apply=%v issues = %+v", apply, list)
		}
	}
}

func TestBackupPlanRestoreSameIDDifferentContentCopiesOnce(t *testing.T) {
	g, _, pl := newRecordsGateway(t)
	mine, err := pl.Create("s-root", "Roadmap", []host.PlanSection{{Title: "Q1", Body: "mine"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := backupPlan{ID: mine.ID, OwnerRoot: "s-root", Title: "Roadmap", Status: "closed",
		Sections: []backupPlanSection{{Title: "Q1", Body: "theirs"}}}

	dry := backup.NewReport(backup.New(time.Now(), ""), true)
	g.srv.restoreBackupPlans(dry, []backupPlan{rec}, false)
	if len(dry.Items) != 1 || dry.Items[0].Action != backup.ActionCopy || !strings.Contains(dry.Items[0].Detail, `"Roadmap (restored)"`) {
		t.Fatalf("dry items = %+v", dry.Items)
	}

	rep := backup.NewReport(backup.New(time.Now(), ""), false)
	g.srv.restoreBackupPlans(rep, []backupPlan{rec}, true)
	if len(rep.Items) != 1 || rep.Items[0].Action != backup.ActionCopy || rep.Items[0].Target == "" {
		t.Fatalf("items = %+v", rep.Items)
	}
	plans := plansByTitle(t, pl)
	if p := plans["Roadmap"]; p.ID != mine.ID || p.Sections[0].Body != "mine" || p.Status != "draft" {
		t.Fatalf("target plan changed: %+v", p)
	}
	if p := plans["Roadmap (restored)"]; p.Sections[0].Body != "theirs" || p.Status != "closed" {
		t.Fatalf("copy = %+v", p)
	}

	again := backup.NewReport(backup.New(time.Now(), ""), false)
	g.srv.restoreBackupPlans(again, []backupPlan{rec}, true)
	if len(again.Items) != 1 || again.Items[0].Action != backup.ActionSkip || again.Items[0].Detail != "already restored as a copy" {
		t.Fatalf("second restore items = %+v", again.Items)
	}
	if metas, _ := pl.List(); len(metas) != 2 {
		t.Fatalf("plans = %+v", metas)
	}
}

func TestBackupPlanRestoreRecordChecks(t *testing.T) {
	cases := []struct {
		name   string
		rec    backupPlan
		action string
		detail string
	}{
		{"empty title", backupPlan{OwnerRoot: "r", Title: " "}, backup.ActionFail, "empty title"},
		{"no owner", backupPlan{Title: "t"}, backup.ActionFail, "no owning root"},
		{"unknown status", backupPlan{OwnerRoot: "r", Title: "t", Status: "shipped"}, backup.ActionFail, "unknown plan status"},
		{"empty status means draft", backupPlan{OwnerRoot: "r", Title: "t"}, backup.ActionCreate, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _, pl := newRecordsGateway(t)
			rep := backup.NewReport(backup.New(time.Now(), ""), false)
			g.srv.restoreBackupPlans(rep, []backupPlan{tc.rec}, true)
			if len(rep.Items) != 1 || rep.Items[0].Action != tc.action || !strings.Contains(rep.Items[0].Detail, tc.detail) {
				t.Fatalf("items = %+v, want one %s (%q)", rep.Items, tc.action, tc.detail)
			}
			metas, _ := pl.List()
			if tc.action == backup.ActionCreate && (len(metas) != 1 || metas[0].Status != "draft") {
				t.Fatalf("plans = %+v", metas)
			}
			if tc.action == backup.ActionFail && len(metas) != 0 {
				t.Fatalf("failed record wrote a plan: %+v", metas)
			}
		})
	}
}

func TestBackupIssuesAndPlansMissingStores(t *testing.T) {
	src, srcIss, srcPl := newRecordsGateway(t)
	seedBackupRecords(t, srcIss, srcPl)
	archive := exportBackup(t, src)

	dst := newBackupGateway(t, true) // no issue or plan store
	rep := dst.restore(t, archive, true)
	if rep.Counts[backup.SectionIssues][backup.ActionFail] != 2 || rep.Counts[backup.SectionPlans][backup.ActionFail] != 2 {
		t.Fatalf("restore without stores = issues %v plans %v", rep.Counts[backup.SectionIssues], rep.Counts[backup.SectionPlans])
	}
	a, err := backup.Decode([]byte(exportBackup(t, dst)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{backup.SectionIssues, backup.SectionPlans} {
		if _, ok := a.Sections[name]; ok {
			t.Fatalf("export without the store carries %s", name)
		}
		var named bool
		for _, ex := range a.Manifest.Excluded {
			if ex.Kind == backup.ExcludeUnavailable && ex.Category == name {
				named = true
			}
		}
		if !named {
			t.Fatalf("missing %s store not named: %+v", name, a.Manifest.Excluded)
		}
	}
}

func TestBackupRestoreRejectsTamperedIssuesSection(t *testing.T) {
	src, srcIss, srcPl := newRecordsGateway(t)
	seedBackupRecords(t, srcIss, srcPl)
	archive := exportBackup(t, src)
	for _, needle := range []string{"Ship backup", "Release notes"} {
		tampered := strings.Replace(archive, needle, "Evil record", 1)
		if tampered == archive {
			t.Fatalf("tamper %q did not apply", needle)
		}
		dst, dstIss, dstPl := newRecordsGateway(t)
		rec := dst.do(t, http.MethodPost, "/v1/backup/restore", tampered)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "integrity check") {
			t.Fatalf("tampered %q = %d %s", needle, rec.Code, rec.Body.String())
		}
		if list, _ := dstIss.List(""); len(list) != 0 {
			t.Fatalf("tampered restore wrote issues: %+v", list)
		}
		if metas, _ := dstPl.List(); len(metas) != 0 {
			t.Fatalf("tampered restore wrote plans: %+v", metas)
		}
	}
}

func TestBackupEstimateMatchesExport(t *testing.T) {
	g, iss, pl := newRecordsGateway(t)
	seedBackupSource(t, g)
	seedBackupRecords(t, iss, pl)

	rec := g.do(t, http.MethodGet, "/v1/backup/estimate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("estimate = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), backupTestSecret) || strings.Contains(rec.Body.String(), "Flaky planner test") {
		t.Fatalf("estimate carries record data: %s", rec.Body.String())
	}
	var est backupEstimate
	if err := json.Unmarshal(rec.Body.Bytes(), &est); err != nil {
		t.Fatal(err)
	}
	a, err := backup.Decode([]byte(exportBackup(t, g)))
	if err != nil {
		t.Fatal(err)
	}
	if len(est.Sections) != len(a.Manifest.Sections) {
		t.Fatalf("estimate sections %+v, export %+v", est.Sections, a.Manifest.Sections)
	}
	sum := 0
	for i, s := range est.Sections {
		want := a.Manifest.Sections[i]
		// Session packages carry their own exportedAt, so only the other
		// sections are byte-for-byte stable between two builds.
		if s.Name != want.Name || s.Items != want.Items || (s.Name != backup.SectionSessions && s.Bytes != want.Bytes) {
			t.Fatalf("section %d: estimate %+v, export %+v", i, s, want)
		}
		sum += s.Bytes
	}
	if est.TotalBytes <= sum {
		t.Fatalf("total %d should exceed the section bytes %d (manifest overhead)", est.TotalBytes, sum)
	}
	if len(est.Excluded) == 0 {
		t.Fatal("estimate lists no exclusions")
	}

	// The estimate reads only: it is allowed on an attach-only gateway.
	ro := newBackupGateway(t, false)
	if rec := ro.do(t, http.MethodGet, "/v1/backup/estimate", ""); rec.Code != http.StatusOK {
		t.Fatalf("attach-only estimate = %d %s", rec.Code, rec.Body.String())
	}
}
