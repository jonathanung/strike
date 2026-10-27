package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapListsComposerCommands(t *testing.T) {
	srv, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		ComposerCommands []struct {
			ID     string `json:"id"`
			Insert string `json:"insert"`
		} `json:"composerCommands"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, cmd := range body.ComposerCommands {
		if cmd.Insert == "/mode yolo" && cmd.ID == "mode-yolo" {
			saw = true
		}
		if cmd.Insert == "" || cmd.ID == "" {
			t.Fatalf("blank command: %#v", cmd)
		}
	}
	if !saw {
		t.Fatalf("missing /mode yolo in %#v", body.ComposerCommands)
	}
}
