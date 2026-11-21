package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func suggestionsBody(t *testing.T, srv *Server) suggestionsResponse {
	t.Helper()
	got := notifyCall(t, srv, http.MethodGet, "/v1/suggestions", "")
	if got.code != http.StatusOK {
		t.Fatalf("GET = %d %s", got.code, got.raw)
	}
	var out suggestionsResponse
	if err := json.Unmarshal([]byte(got.raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const ideaBody = `{"title":"Fix flaky TestFoo","why":"failed 3 of 10 runs","prompt":"Investigate TestFoo","source":"scheduler.heartbeat","kind":"flaky-test","evidenceKey":"test:TestFoo","evidence":[{"label":"CI run","url":"https://example.com/run/1"}],"ttlHours":24}`

func createIdea(t *testing.T, srv *Server, body string) suggestionCreateResponse {
	t.Helper()
	got := notifyCall(t, srv, http.MethodPost, "/v1/suggestions", body)
	if got.code != http.StatusCreated && got.code != http.StatusOK {
		t.Fatalf("create = %d %s", got.code, got.raw)
	}
	var out suggestionCreateResponse
	if err := json.Unmarshal([]byte(got.raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSuggestionsEmptyList(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	body := suggestionsBody(t, srv)
	if body.Suggestions == nil || len(body.Suggestions) != 0 || body.Open != 0 {
		t.Fatalf("empty = %+v", body)
	}
	if body.Prefs.MutedSources == nil || body.Prefs.MutedKinds == nil {
		t.Fatalf("prefs lists must be [] not null: %+v", body.Prefs)
	}
}

func TestSuggestionsCreateDedupeAndLifecycle(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	first := createIdea(t, srv, ideaBody)
	if !first.Created || first.Suggestion.State != "open" {
		t.Fatalf("first = %+v", first)
	}
	again := createIdea(t, srv, ideaBody)
	if again.Created || again.Suggestion.ID != first.Suggestion.ID {
		t.Fatalf("dedupe = %+v", again)
	}
	if got := suggestionsBody(t, srv); got.Open != 1 || len(got.Suggestions) != 1 {
		t.Fatalf("list = %+v", got)
	}
	id := first.Suggestion.ID

	steps := []struct {
		name, path, body string
		code             int
	}{
		{"snooze zero", "/v1/suggestions/" + id + "/snooze", `{"hours":0}`, http.StatusBadRequest},
		{"snooze too long", "/v1/suggestions/" + id + "/snooze", `{"hours":1000}`, http.StatusBadRequest},
		{"snooze", "/v1/suggestions/" + id + "/snooze", `{"hours":2}`, http.StatusOK},
		{"accept snoozed", "/v1/suggestions/" + id + "/accept", "", http.StatusOK},
		{"accept twice", "/v1/suggestions/" + id + "/accept", "", http.StatusConflict},
		{"dismiss accepted", "/v1/suggestions/" + id + "/dismiss", "", http.StatusConflict},
		{"missing", "/v1/suggestions/nope/accept", "", http.StatusNotFound},
	}
	for _, step := range steps {
		got := notifyCall(t, srv, http.MethodPost, step.path, step.body)
		if got.code != step.code {
			t.Fatalf("%s: %d %s, want %d", step.name, got.code, got.raw, step.code)
		}
	}
	if got := suggestionsBody(t, srv); got.Open != 0 || got.Suggestions[0].State != "accepted" {
		t.Fatalf("after accept = %+v", got)
	}
}

func TestSuggestionsDismissNeverMutesKind(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	first := createIdea(t, srv, ideaBody)
	got := notifyCall(t, srv, http.MethodPost, "/v1/suggestions/"+first.Suggestion.ID+"/dismiss", `{"never":true}`)
	if got.code != http.StatusOK {
		t.Fatalf("dismiss = %d %s", got.code, got.raw)
	}
	body := suggestionsBody(t, srv)
	if len(body.Prefs.MutedKinds) != 1 || body.Prefs.Dismissals["scheduler.heartbeat/flaky-test"] != 1 {
		t.Fatalf("prefs = %+v", body.Prefs)
	}
	other := `{"title":"Another flaky test","prompt":"x","source":"scheduler.heartbeat","kind":"flaky-test"}`
	if res := notifyCall(t, srv, http.MethodPost, "/v1/suggestions", other); res.code != http.StatusConflict {
		t.Fatalf("muted kind create = %d %s", res.code, res.raw)
	}
	res := notifyCall(t, srv, http.MethodPut, "/v1/suggestions/prefs", `{"mutedSources":[],"mutedKinds":[]}`)
	if res.code != http.StatusOK {
		t.Fatalf("unmute = %d %s", res.code, res.raw)
	}
	if res := notifyCall(t, srv, http.MethodPost, "/v1/suggestions", other); res.code != http.StatusCreated {
		t.Fatalf("after unmute create = %d %s", res.code, res.raw)
	}
}

func TestSuggestionsRejectBadBodies(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	cases := []struct {
		name, method, path, body string
	}{
		{"create empty", http.MethodPost, "/v1/suggestions", ""},
		{"create unknown field", http.MethodPost, "/v1/suggestions", `{"title":"a","prompt":"b","source":"s","run":true}`},
		{"create no title", http.MethodPost, "/v1/suggestions", `{"prompt":"b","source":"s"}`},
		{"create bad source", http.MethodPost, "/v1/suggestions", `{"title":"a","prompt":"b","source":"Not Valid"}`},
		{"create negative ttl", http.MethodPost, "/v1/suggestions", `{"title":"a","prompt":"b","source":"s","ttlHours":-1}`},
		{"create js evidence", http.MethodPost, "/v1/suggestions", `{"title":"a","prompt":"b","source":"s","evidence":[{"label":"x","url":"javascript:alert(1)"}]}`},
		{"create trailing", http.MethodPost, "/v1/suggestions", `{"title":"a","prompt":"b","source":"s"}{}`},
		{"prefs bad token", http.MethodPut, "/v1/suggestions/prefs", `{"mutedSources":["Bad Token"]}`},
		{"prefs unknown", http.MethodPut, "/v1/suggestions/prefs", `{"quiet":true}`},
	}
	for _, tc := range cases {
		got := notifyCall(t, srv, tc.method, tc.path, tc.body)
		if got.code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", tc.name, got.code, got.raw)
		}
	}
	if body := suggestionsBody(t, srv); len(body.Suggestions) != 0 {
		t.Fatalf("bad bodies stored rows: %+v", body)
	}
}
