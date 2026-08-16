package theme

// This file documents intentional chrome Soft default; paired with file_test.go
// updates below via TestParseDefaultChromeIsSoft.
import "testing"

func TestParseDefaultChromeIsSoft(t *testing.T) {
	e, err := Parse([]byte(`{"id":"soft-default","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeSoft {
		t.Errorf("omitted chrome = %v, want soft", e.Theme.Chrome)
	}
	e, err = Parse([]byte(`{"id":"soft-explicit","chrome":"soft","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeSoft {
		t.Errorf("chrome soft = %v", e.Theme.Chrome)
	}
	e, err = Parse([]byte(`{"id":"solid-explicit","chrome":"solid","colors":{}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Theme.Chrome != ChromeSolid {
		t.Errorf("chrome solid = %v", e.Theme.Chrome)
	}
}
