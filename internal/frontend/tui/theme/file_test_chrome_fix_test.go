package theme

// Omitted chrome resolves to bordered; chrome: soft stays the Family opt-in.
import "testing"

func TestParseDefaultChromeIsBordered(t *testing.T) {
	e, err := Parse([]byte(`{"id":"bordered-default","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeBordered {
		t.Errorf("omitted chrome = %v, want bordered", e.Theme.Chrome)
	}
	if e.Theme.BorderStyle.TopLeft != "┌" || e.Theme.BorderStyle.BottomRight != "┘" {
		t.Errorf("omitted chrome corners = %+v, want square", e.Theme.BorderStyle)
	}
	e, err = Parse([]byte(`{"id":"soft-explicit","chrome":"soft","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeSoft {
		t.Errorf("chrome soft = %v", e.Theme.Chrome)
	}
	if e.Theme.BorderStyle.TopLeft != "╭" || e.Theme.BorderStyle.BottomRight != "╯" {
		t.Errorf("chrome soft corners = %+v, want rounded", e.Theme.BorderStyle)
	}
	e, err = Parse([]byte(`{"id":"soft-light","chrome":"soft","border":"light","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeSoft || e.Theme.BorderStyle.TopLeft != "╭" {
		t.Errorf("chrome soft + border light = chrome %v corners %+v, want rounded soft", e.Theme.Chrome, e.Theme.BorderStyle)
	}
	e, err = Parse([]byte(`{"id":"solid-explicit","chrome":"solid","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeSolid {
		t.Errorf("chrome solid = %v", e.Theme.Chrome)
	}
}
