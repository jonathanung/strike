package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/protocol"
)

// newGuardrailServer wires the real local settings adapter over a temp HOME so
// PATCH → ~/.strike/config → GET is exercised end to end (#1577).
func newGuardrailServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	svc := local.New(nil, nil, nil, nil, nil, nil, nil, "")
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &svc, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func getSettingsJSON(t *testing.T, srv *Server) settingsResponse {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET settings = %d %s", res.Code, res.Body.String())
	}
	var got settingsResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSettingsGuardrailsReportDaemonDefaults(t *testing.T) {
	srv := newGuardrailServer(t)
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	body := res.Body.String()
	for _, want := range []string{
		`"turnTimeoutS":1800`, `"maxSessionCostUSD":0`, `"maxTurnTokens":0`,
		`"agentBudget":{"maxWallClockS":0`, `"delegationPolicy":{"mode":"enforce"`,
		`"childHandoffRequired":true`, `"childIsolation":"shared"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("default settings %s missing %s", body, want)
		}
	}
}

func TestSettingsGuardrailsRoundTripEachDial(t *testing.T) {
	srv := newGuardrailServer(t)
	cases := []struct {
		name  string
		patch string
		check func(settingsResponse) bool
	}{
		{"turn timeout duration", `{"turnTimeoutS":"45m"}`, func(s settingsResponse) bool { return s.TurnTimeoutS == 2700 }},
		{"turn timeout off", `{"turnTimeoutS":"off"}`, func(s settingsResponse) bool { return s.TurnTimeoutS == -1 }},
		{"turn timeout default", `{"turnTimeoutS":"default"}`, func(s settingsResponse) bool { return s.TurnTimeoutS == 1800 }},
		{"session cost", `{"maxSessionCostUSD":"3.25"}`, func(s settingsResponse) bool { return s.MaxSessionCostUSD == 3.25 }},
		{"turn tokens", `{"maxTurnTokens":"120000"}`, func(s settingsResponse) bool { return s.MaxTurnTokens == 120000 }},
		{"agent budget", `{"agentBudget":{"maxCostUsd":"0.5","maxToolCalls":"25","stallAfterS":"60"}}`, func(s settingsResponse) bool {
			return s.AgentBudget == agentBudgetJSON{MaxCostUSD: 0.5, MaxToolCalls: 25, StallAfterS: 60}
		}},
		{"delegation policy", `{"delegationPolicy":{"mode":"advise","maxLiveChildren":"3","maxPathsLocal":"off"}}`, func(s settingsResponse) bool {
			return s.DelegationPolicy == delegationPolicyJSON{Mode: "advise", MaxPathsLocal: -1, MaxLiveChildren: 3}
		}},
		{"handoff optional", `{"childHandoffRequired":"off"}`, func(s settingsResponse) bool { return !s.ChildHandoffRequired }},
		{"isolation worktree", `{"childIsolation":"worktree"}`, func(s settingsResponse) bool { return s.ChildIsolation == "worktree" }},
		// Host (shared) mode is a normal, first-class choice.
		{"isolation shared", `{"childIsolation":"shared"}`, func(s settingsResponse) bool { return s.ChildIsolation == "shared" }},
	}
	for _, tc := range cases {
		res := patchSettings(t, srv, tc.patch)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: PATCH = %d %s", tc.name, res.Code, res.Body.String())
		}
		var patched settingsResponse
		if err := json.Unmarshal(res.Body.Bytes(), &patched); err != nil {
			t.Fatal(err)
		}
		if !tc.check(patched) {
			t.Fatalf("%s: PATCH response %+v", tc.name, patched)
		}
		if got := getSettingsJSON(t, srv); !tc.check(got) {
			t.Fatalf("%s: GET after PATCH %+v", tc.name, got)
		}
	}
}

func TestSettingsGuardrailsRejectRendersReason(t *testing.T) {
	srv := newGuardrailServer(t)
	if res := patchSettings(t, srv, `{"maxSessionCostUSD":"10","turnTimeoutS":"600"}`); res.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", res.Code, res.Body.String())
	}
	for patch, field := range map[string]string{
		`{"maxSessionCostUSD":"-2"}`:                     "maxSessionCostUSD",
		`{"turnTimeoutS":"whenever"}`:                    "turnTimeoutS",
		`{"maxTurnTokens":"many"}`:                       "maxTurnTokens",
		`{"agentBudget":{"maxTokens":"-1"}}`:             "agentBudget.maxTokens",
		`{"delegationPolicy":{"mode":"sometimes"}}`:      "delegationPolicy.mode",
		`{"childHandoffRequired":"perhaps"}`:             "childHandoffRequired",
		`{"childIsolation":"vm"}`:                        "childIsolation",
		`{"maxSessionCostUSD":"99","maxTurnTokens":"x"}`: "maxTurnTokens",
	} {
		res := patchSettings(t, srv, patch)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), field) {
			t.Fatalf("%s = %d %s, want 400 naming %s", patch, res.Code, res.Body.String(), field)
		}
	}
	// Numbers are not accepted: the dials use the string vocabulary.
	if res := patchSettings(t, srv, `{"maxSessionCostUSD":5}`); res.Code != http.StatusBadRequest {
		t.Fatalf("numeric cost = %d %s, want 400", res.Code, res.Body.String())
	}
	got := getSettingsJSON(t, srv)
	if got.MaxSessionCostUSD != 10 || got.TurnTimeoutS != 600 || got.MaxTurnTokens != 0 {
		t.Fatalf("rejected patches changed config: %+v", got)
	}
}

// guardrailOrderSettings fails SaveGuardrailDials so a mixed patch can prove
// keepAwake (live daemon state) is not persisted after a guardrail failure.
type guardrailOrderSettings struct {
	testSettings
	keepAwake *string
}

func (g guardrailOrderSettings) SaveGuardrailDials(host.GuardrailDials) error {
	return errors.New("invalid maxSessionCostUSD")
}

func (g guardrailOrderSettings) SaveKeepAwake(mode string) error {
	*g.keepAwake = mode
	return nil
}

func TestApplySettingsPatchGuardrailsBeforeKeepAwake(t *testing.T) {
	var keep string
	err := applySettingsPatch(guardrailOrderSettings{keepAwake: &keep}, settingsPatchBody{KeepAwake: "on", MaxSessionCostUSD: "-1"})
	if err == nil {
		t.Fatal("guardrail failure not surfaced")
	}
	if keep != "" {
		t.Fatalf("keepAwake persisted after guardrail failure: %q", keep)
	}
}

func TestSettingsPatchFieldsNamesGuardrails(t *testing.T) {
	got := settingsPatchFields(settingsPatchBody{
		TurnTimeoutS:     "30m",
		AgentBudget:      &agentBudgetPatch{MaxTokens: "10"},
		DelegationPolicy: &delegationPolicyPatch{Mode: "off"},
		ChildIsolation:   "worktree",
	})
	want := "turnTimeoutS,agentBudget,delegationPolicy,childIsolation"
	if strings.Join(got, ",") != want {
		t.Fatalf("fields = %v, want %s", got, want)
	}
	if got := settingsPatchFields(settingsPatchBody{AgentBudget: &agentBudgetPatch{}}); len(got) != 0 {
		t.Fatalf("empty agentBudget object audited as change: %v", got)
	}
}
