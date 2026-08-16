package theme

import "testing"

// Locks Default / Unset chrome as bordered with square light corners.
func TestDefaultAndUnsetChromeIsBordered(t *testing.T) {
	if got := Default().Chrome; got != ChromeBordered {
		t.Errorf("Default().Chrome = %v, want ChromeBordered", got)
	}
	if got := (Theme{}).Resolve().Chrome; got != ChromeBordered {
		t.Errorf("unset Resolve chrome = %v, want ChromeBordered", got)
	}
	sq := lightBorderStyle()
	if got := Default().BorderStyle; got.TopLeft != sq.TopLeft || got.TopRight != sq.TopRight || got.BottomLeft != sq.BottomLeft || got.BottomRight != sq.BottomRight {
		t.Errorf("Default() corners = %+v, want square %+v", Default().BorderStyle, sq)
	}
	if got := (Theme{}).Resolve().BorderStyle; got.TopLeft != "┌" || got.TopRight != "┐" || got.BottomLeft != "└" || got.BottomRight != "┘" {
		t.Errorf("unset Resolve corners = %+v, want ┌┐└┘", got)
	}
	if got := (Theme{Chrome: ChromeSoft}).Resolve().Chrome; got != ChromeSoft {
		t.Errorf("explicit soft = %v", got)
	}
	if got := (Theme{Chrome: ChromeSoft}).Resolve().BorderStyle; got.TopLeft != "╭" || got.TopRight != "╮" || got.BottomLeft != "╰" || got.BottomRight != "╯" {
		t.Errorf("explicit soft corners = %+v, want ╭╮╰╯", got)
	}
	if got := (Theme{Chrome: ChromeSolid}).Resolve().Chrome; got != ChromeSolid {
		t.Errorf("solid chrome = %v", got)
	}
	if got := (Theme{Chrome: ChromeBordered}).Resolve().Chrome; got != ChromeBordered {
		t.Errorf("bordered chrome = %v", got)
	}
}
