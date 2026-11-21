package tool

import (
	"context"
	"strings"
	"testing"
)

// Powers (#1943): toolsearch must not list tools outside the caller's
// session tool set, even though they share the process-wide registry.
func TestToolSearchSkipsToolsHiddenByToolVisible(t *testing.T) {
	reg := NewRegistry(NewRead(), NewGrep())
	ts := NewToolSearch(reg)
	reg.Register(ts)

	tests := []struct {
		name     string
		visible  func(string) bool
		wantRead bool
		wantGrep bool
	}{
		{name: "nil filter lists all", visible: nil, wantRead: true, wantGrep: true},
		{name: "hidden tool omitted", visible: func(n string) bool { return n != "grep" }, wantRead: true, wantGrep: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := allowAll(t.TempDir())
			tc.ToolVisible = tt.visible
			// Both descriptions mention files; query each by name.
			for _, c := range []struct {
				query, line string
				want        bool
			}{{"read", "- read:", tt.wantRead}, {"grep", "- grep:", tt.wantGrep}} {
				res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{"query": c.query}), tc)
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Contains(res.Output, c.line); got != c.want {
					t.Errorf("query %q: listed=%v want %v; output=%q", c.query, got, c.want, res.Output)
				}
			}
		})
	}
}
