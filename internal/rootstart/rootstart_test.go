package rootstart

import "testing"

func TestResolveMode(t *testing.T) {
	cases := []struct {
		name      string
		explicit  string
		workspace string
		want      string
		wantErr   bool
	}{
		{name: "neither is plan", want: SafestMode},
		{name: "blank is plan", explicit: "  ", workspace: " ", want: SafestMode},
		{name: "explicit when no workspace", explicit: "accept-edits", want: "accept-edits"},
		{name: "workspace wins over explicit", explicit: "plan", workspace: "yolo", want: "yolo"},
		{name: "workspace aliases", workspace: "soft", want: "soft-approve"},
		{name: "unknown explicit", explicit: "trust", wantErr: true},
		{name: "unknown workspace", workspace: "nope", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMode(tc.explicit, tc.workspace)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("mode %q, want %q", got, tc.want)
			}
		})
	}
}
