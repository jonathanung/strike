package engine

import (
	"encoding/json"
	"testing"
)

func TestToolLoopDetectorIdentical(t *testing.T) {
	t.Parallel()
	d := newToolLoopDetector(3, 8)
	args := json.RawMessage(`{"a":1}`)
	for i := 0; i < 2; i++ {
		tripped, _, _ := d.observe("read", args, false, "internal")
		if tripped {
			t.Fatalf("tripped early at %d", i+1)
		}
	}
	tripped, reason, count := d.observe("read", args, false, "internal")
	if !tripped || reason != toolLoopIdentical || count != 3 {
		t.Fatalf("got tripped=%v reason=%s count=%d", tripped, reason, count)
	}
	if !d.wouldTrip("read", args) {
		t.Fatal("wouldTrip after trip")
	}
}

func TestToolLoopDetectorSuccessResetsStreak(t *testing.T) {
	t.Parallel()
	d := newToolLoopDetector(3, 8)
	args := json.RawMessage(`{}`)
	d.observe("read", args, false, "internal")
	d.observe("read", args, false, "internal")
	d.observe("read", args, true, "")
	tripped, _, _ := d.observe("read", args, false, "internal")
	if tripped {
		t.Fatal("success should reset identical streak")
	}
}

func TestToolLoopDetectorOscillation(t *testing.T) {
	t.Parallel()
	d := newToolLoopDetector(2, 8) // need 4 alternating fails
	a := json.RawMessage(`{"x":"a"}`)
	b := json.RawMessage(`{"x":"b"}`)
	seq := []json.RawMessage{a, b, a, b}
	var tripped bool
	var reason string
	for i, args := range seq {
		name := "t"
		tripped, reason, _ = d.observe(name, args, false, "internal")
		if i < 3 && tripped {
			t.Fatalf("early trip at %d", i)
		}
	}
	if !tripped || reason != toolLoopOscillating {
		t.Fatalf("tripped=%v reason=%s", tripped, reason)
	}
}
