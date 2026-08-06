package protocol

import "testing"

func TestParsePermissionModeAcceptsEveryMode(t *testing.T) {
	cases := []struct {
		in   string
		want PermissionMode
	}{
		{"", PermissionModeDefault},
		{"  ", PermissionModeDefault},
		{"default", PermissionModeDefault},
		{"DEFAULT", PermissionModeDefault},
		{"plan", PermissionModePlan},
		{"Plan", PermissionModePlan},
		{"accept-edits", PermissionModeAcceptEdits},
		{"accept_edits", PermissionModeAcceptEdits},
		{"AcceptEdits", PermissionModeAcceptEdits},
		{"acceptedits", PermissionModeAcceptEdits},
		{"soft-approve", PermissionModeSoftApprove},
		{"soft_approve", PermissionModeSoftApprove},
		{"softapprove", PermissionModeSoftApprove},
		{"soft", PermissionModeSoftApprove},
		{" SOFT ", PermissionModeSoftApprove},
		{"yolo", PermissionModeYolo},
		{" YOLO ", PermissionModeYolo},
	}
	for _, tc := range cases {
		got, ok := ParsePermissionMode(tc.in)
		if !ok {
			t.Errorf("ParsePermissionMode(%q) rejected", tc.in)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePermissionMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParsePermissionModeRejectsUnknown(t *testing.T) {
	for _, bad := range []string{"auto", "supervised", "agent", "skip", "dangerous"} {
		if _, ok := ParsePermissionMode(bad); ok {
			t.Errorf("ParsePermissionMode(%q) accepted, want rejected", bad)
		}
	}
}

func TestPermissionModesOrderAndCycle(t *testing.T) {
	want := []PermissionMode{
		PermissionModeDefault, PermissionModePlan, PermissionModeSoftApprove,
		PermissionModeAcceptEdits, PermissionModeYolo,
	}
	got := PermissionModes()
	if len(got) != len(want) {
		t.Fatalf("PermissionModes len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PermissionModes[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if PermissionMode("").Normalize() != PermissionModeDefault {
		t.Errorf("empty Normalize = %q, want default", PermissionMode("").Normalize())
	}
	// Full cycle wraps.
	m := PermissionModeDefault
	seen := map[PermissionMode]bool{}
	for i := 0; i < len(want); i++ {
		if seen[m] {
			t.Fatalf("cycle repeated %q before full lap", m)
		}
		seen[m] = true
		m = m.Next()
	}
	if m != PermissionModeDefault {
		t.Errorf("after full cycle = %q, want default", m)
	}
	// Unknown normalizes via Next to default path.
	if PermissionMode("nope").Next() != PermissionModeDefault {
		t.Errorf("unknown Next = %q, want default", PermissionMode("nope").Next())
	}
}

func TestPermissionModeShortAndDescribe(t *testing.T) {
	cases := []struct {
		mode        PermissionMode
		short, desc string
	}{
		{PermissionModeDefault, "def", "normal permission prompts"},
		{PermissionModePlan, "plan", "read-only plan posture — write/edit denied"},
		{PermissionModeSoftApprove, "soft", "count down 15s then allow once — veto anytime"},
		{PermissionModeAcceptEdits, "edits", "auto-allow edit/write — still ask on bash/network"},
		{PermissionModeYolo, "yolo", "skip permission asks — explicit denies still apply"},
	}
	for _, tc := range cases {
		if got := tc.mode.Short(); got != tc.short {
			t.Errorf("%q.Short() = %q, want %q", tc.mode, got, tc.short)
		}
		if got := tc.mode.Describe(); got != tc.desc {
			t.Errorf("%q.Describe() = %q, want %q", tc.mode, got, tc.desc)
		}
	}
}

func TestPermissionModeSelectedRoundTripsThroughTheEnvelope(t *testing.T) {
	original := PermissionModeSelected{
		Correlation: Correlation{SessionID: "s1"},
		Mode:        PermissionModeAcceptEdits,
	}
	env, err := Wrap(original)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if env.Type != "permission.mode" {
		t.Errorf("envelope type = %q, want permission.mode", env.Type)
	}
	decoded, err := env.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, ok := decoded.(PermissionModeSelected)
	if !ok {
		t.Fatalf("decoded type = %T, want PermissionModeSelected", decoded)
	}
	if got != original {
		t.Errorf("round-trip = %#v, want %#v", got, original)
	}
}
