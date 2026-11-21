package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
)

// filesOnly is a host.Files without the optional content-search capability.
type filesOnly struct{ host.Files }

func TestFilesGrepRoute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("package a\n// Needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Services: &host.Services{Files: local.NewFiles(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	noGrep, err := New(Options{Services: &host.Services{Files: filesOnly{local.NewFiles(dir)}}})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		srv      *Server
		url      string
		wantCode int
		wantN    int
	}{
		{"match", srv, "/v1/files/grep?q=needle&limit=10", http.StatusOK, 1},
		{"no results", srv, "/v1/files/grep?q=zzz-nothing", http.StatusOK, 0},
		{"missing q", srv, "/v1/files/grep", http.StatusBadRequest, 0},
		{"blank q", srv, "/v1/files/grep?q=%20%20", http.StatusBadRequest, 0},
		{"no files service", bare, "/v1/files/grep?q=x", http.StatusNotImplemented, 0},
		{"no content search capability", noGrep, "/v1/files/grep?q=x", http.StatusNotImplemented, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rr.Code != tc.wantCode {
				t.Fatalf("code %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var body filesGrepResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Matches == nil {
				t.Fatal("matches must be [] not null")
			}
			if len(body.Matches) != tc.wantN {
				t.Fatalf("matches %d, want %d: %s", len(body.Matches), tc.wantN, rr.Body.String())
			}
			if tc.wantN > 0 {
				m := body.Matches[0]
				if m.Path != "alpha.go" || m.Line != 2 || m.Snippet != "// Needle here" {
					t.Fatalf("match = %+v", m)
				}
			}
		})
	}
}
