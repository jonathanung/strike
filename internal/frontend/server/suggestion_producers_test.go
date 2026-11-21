package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/daemon/trigger"
	"github.com/jonathanung/strike/internal/persist/suggestion"
)

func TestScriptFailureDraft(t *testing.T) {
	code := 3
	cases := []struct {
		name               string
		schedule, run, nm  string
		exit               *int
		ok                 bool
		wantKey, wantTitle string
		wantWhy            string
	}{
		{"exit code", "sch-1", "run-9", "nightly backup", &code, true, "schedule:sch-1:failed", "Look into failing schedule: nightly backup", "exit 3"},
		{"no exit code", "sch-1", "run-9", "", nil, true, "schedule:sch-1:failed", "Look into failing schedule: Script schedule", "no exit code"},
		{"bad schedule id", "../x", "run-9", "n", nil, false, "", "", ""},
		{"bad run id", "sch-1", "", "n", nil, false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := scriptFailureDraft(tc.schedule, tc.run, tc.nm, tc.exit)
			if ok != tc.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if d.EvidenceKey != tc.wantKey || d.Title != tc.wantTitle || d.Kind != kindScheduleFailure {
				t.Fatalf("draft = %+v", d)
			}
			if !strings.Contains(d.Why, tc.wantWhy) || !strings.Contains(d.Prompt, tc.run) {
				t.Fatalf("why/prompt = %q / %q", d.Why, d.Prompt)
			}
		})
	}
}

func TestScriptFailureDraftScrubsName(t *testing.T) {
	d, ok := scriptFailureDraft("sch-1", "run-1", "deploy ghp_1234567890123456789012345678901234", nil)
	if !ok {
		t.Fatal("draft refused")
	}
	if strings.Contains(d.Title+d.Why+d.Prompt+d.Evidence[0].Label, "ghp_1234567890") {
		t.Fatalf("token leaked: %+v", d)
	}
}

func TestNotifyScriptRunDepositsOneIdeaPerFailingSchedule(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	code := 1
	srv.NotifyScriptRun("sch-1", "run-1", "nightly", &code, true)
	srv.NotifyScriptRun("sch-1", "run-2", "nightly", &code, true)
	srv.NotifyScriptRun("sch-2", "run-3", "hourly", &code, false) // success: no idea
	body := suggestionsBody(t, srv)
	if len(body.Suggestions) != 1 || body.Open != 1 {
		t.Fatalf("suggestions = %+v", body.Suggestions)
	}
	sg := body.Suggestions[0]
	if sg.Source != suggestion.SourceScheduler || sg.EvidenceKey != "schedule:sch-1:failed" || sg.Repeats != 1 {
		t.Fatalf("idea = %+v", sg)
	}
	// Dismissed: a later failure of the same schedule does not refile.
	if res := notifyCall(t, srv, http.MethodPost, "/v1/suggestions/"+sg.ID+"/dismiss", ""); res.code != http.StatusOK {
		t.Fatalf("dismiss = %d %s", res.code, res.raw)
	}
	srv.NotifyScriptRun("sch-1", "run-4", "nightly", &code, true)
	body = suggestionsBody(t, srv)
	if body.Open != 0 || len(body.Suggestions) != 1 {
		t.Fatalf("after dismiss = %+v", body)
	}
}

func TestMutedSchedulerSourceSuppressesDeposit(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	res := notifyCall(t, srv, http.MethodPut, "/v1/suggestions/prefs", `{"mutedSources":["scheduler"],"mutedKinds":[]}`)
	if res.code != http.StatusOK {
		t.Fatalf("mute = %d %s", res.code, res.raw)
	}
	srv.NotifyScriptRun("sch-1", "run-1", "nightly", nil, true)
	if body := suggestionsBody(t, srv); len(body.Suggestions) != 0 {
		t.Fatalf("muted scheduler deposited: %+v", body.Suggestions)
	}
}

func TestTriggerProblemDeposits(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	p := trigger.Problem{TriggerID: "trg-1", TriggerName: "ci hook", Event: "push", DeliveryID: "d-1"}
	srv.SuggestTriggerProblem(p)
	p.DeliveryID = "d-2"
	srv.SuggestTriggerProblem(p)
	srv.SuggestTriggerProblem(trigger.Problem{TriggerID: "bad id/../"}) // refused
	body := suggestionsBody(t, srv)
	if len(body.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v", body.Suggestions)
	}
	sg := body.Suggestions[0]
	if sg.Source != suggestion.SourceTrigger || sg.Kind != kindTriggerFailure || sg.EvidenceKey != "trigger:trg-1:start-failed" || sg.Repeats != 1 {
		t.Fatalf("idea = %+v", sg)
	}
	if !strings.Contains(sg.Why, `"push"`) || !strings.Contains(sg.Title, "ci hook") {
		t.Fatalf("idea text = %+v", sg)
	}
}

func TestTriggerProblemDraftDefaults(t *testing.T) {
	d, ok := triggerProblemDraft(trigger.Problem{TriggerID: "trg-1"})
	if !ok {
		t.Fatal("refused")
	}
	if !strings.Contains(d.Title, "trg-1") || !strings.Contains(d.Why, "a delivery") || d.Evidence[0].Label != "Trigger trg-1" {
		t.Fatalf("draft = %+v", d)
	}
}

func TestSuggestionsHTTPRefusesReservedProducerSource(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	for _, src := range suggestion.Producers() {
		body := `{"title":"a","prompt":"b","source":"` + src + `"}`
		if res := notifyCall(t, srv, http.MethodPost, "/v1/suggestions", body); res.code != http.StatusBadRequest {
			t.Fatalf("%s: create = %d %s, want 400", src, res.code, res.raw)
		}
	}
}

func TestSuggestionsPrefsQuietHours(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	steps := []struct {
		name, body string
		code       int
	}{
		{"bad start", `{"mutedSources":[],"mutedKinds":[],"quietHours":{"enabled":true,"start":"7am","end":"08:00"}}`, http.StatusBadRequest},
		{"bad window leaves mutes alone", `{"mutedSources":["x"],"mutedKinds":[],"quietHours":{"enabled":true,"start":"25:00","end":"08:00"}}`, http.StatusBadRequest},
		{"set all day", `{"mutedSources":[],"mutedKinds":[],"quietHours":{"enabled":true,"start":"00:00","end":"00:00"}}`, http.StatusOK},
		{"omitting keeps window", `{"mutedSources":[],"mutedKinds":[]}`, http.StatusOK},
	}
	for _, step := range steps {
		res := notifyCall(t, srv, http.MethodPut, "/v1/suggestions/prefs", step.body)
		if res.code != step.code {
			t.Fatalf("%s: %d %s, want %d", step.name, res.code, res.raw, step.code)
		}
	}
	body := suggestionsBody(t, srv)
	if len(body.Prefs.MutedSources) != 0 {
		t.Fatalf("rejected body changed mutes: %+v", body.Prefs)
	}
	if q := body.Prefs.QuietHours; !q.Enabled || q.Start != "00:00" || q.End != "00:00" {
		t.Fatalf("quiet = %+v", q)
	}
	// All-day quiet hours hold new deposits from producers and tooling.
	srv.NotifyScriptRun("sch-1", "run-1", "nightly", nil, true)
	if res := notifyCall(t, srv, http.MethodPost, "/v1/suggestions", ideaBody); res.code != http.StatusConflict {
		t.Fatalf("quiet create = %d %s, want 409", res.code, res.raw)
	}
	if body := suggestionsBody(t, srv); len(body.Suggestions) != 0 {
		t.Fatalf("quiet hours deposited: %+v", body.Suggestions)
	}
}
