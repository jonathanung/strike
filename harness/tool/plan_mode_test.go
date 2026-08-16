package tool

import "testing"

func TestPickPostPlanAgent(t *testing.T) {
	cases := []struct {
		agent        string
		steps, areas int
		multi        bool
		want         string
	}{
		{"build", 99, 99, true, "build"},
		{"orchestrator", 0, 0, false, "orchestrator"},
		{"", 4, 0, false, "orchestrator"},
		{"", 0, 3, false, "orchestrator"},
		{"", 0, 0, true, "orchestrator"},
		{"", 1, 1, false, "build"},
		{"unknown", 1, 1, false, "build"},
	}
	for _, tc := range cases {
		got := PickPostPlanAgent(tc.agent, tc.steps, tc.areas, tc.multi)
		if got != tc.want {
			t.Errorf("PickPostPlanAgent(%q,%d,%d,%v)=%q want %q",
				tc.agent, tc.steps, tc.areas, tc.multi, got, tc.want)
		}
	}
}
