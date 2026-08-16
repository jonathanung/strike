package theme

import "testing"

// Locks Family soft chrome as Default / Unset resolve target (issue #628 follow-up).
func TestDefaultAndUnsetChromeIsSoft(t *testing.T) {
	if got := Default().Chrome; got != ChromeSoft {
		t.Errorf("Default().Chrome = %v, want ChromeSoft", got)
	}
	if got := (Theme{}).Resolve().Chrome; got != ChromeSoft {
		t.Errorf("unset Resolve chrome = %v, want ChromeSoft", got)
	}
	if got := (Theme{Chrome: ChromeSoft}).Resolve().Chrome; got != ChromeSoft {
		t.Errorf("explicit soft = %v", got)
	}
	if got := (Theme{Chrome: ChromeSolid}).Resolve().Chrome; got != ChromeSolid {
		t.Errorf("solid chrome = %v", got)
	}
	if got := (Theme{Chrome: ChromeBordered}).Resolve().Chrome; got != ChromeBordered {
		t.Errorf("bordered chrome = %v", got)
	}
}
