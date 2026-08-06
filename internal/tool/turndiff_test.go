package tool

import (
	"reflect"
	"testing"
)

func TestTurnDiffKinds(t *testing.T) {
	d := &TurnDiff{}
	d.Note("a.go", false, false) // create
	d.Note("b.go", true, false)  // update
	d.Note("c.go", true, true)   // delete
	got := d.Snapshot()
	want := []FileChange{
		{Path: "a.go", Kind: ChangeCreate},
		{Path: "b.go", Kind: ChangeUpdate},
		{Path: "c.go", Kind: ChangeDelete},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestTurnDiffCreateThenDeleteNetsNothing(t *testing.T) {
	d := &TurnDiff{}
	d.Note("tmp", false, false)
	d.Note("tmp", true, true)
	if got := d.Snapshot(); len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestTurnDiffNilAndReset(t *testing.T) {
	var d *TurnDiff
	d.Note("x", false, false)
	if d.Snapshot() != nil {
		t.Fatal("nil snapshot")
	}
	d = &TurnDiff{}
	d.Note("x", false, false)
	d.Reset()
	if got := d.Snapshot(); len(got) != 0 {
		t.Fatalf("after reset: %#v", got)
	}
}

func TestNoteTurnChangeViaContext(t *testing.T) {
	td := &TurnDiff{}
	tc := &Context{WorkDir: "/ws", TurnDiff: td}
	tc.NoteTurnChange("/ws/pkg/a.go", false, false)
	got := td.Snapshot()
	if len(got) != 1 || got[0].Path != "pkg/a.go" || got[0].Kind != ChangeCreate {
		t.Fatalf("got %#v", got)
	}
}
