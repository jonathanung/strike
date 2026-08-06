package permission

import (
	"testing"
)

func TestPresetsShipped(t *testing.T) {
	list := Presets()
	if len(list) < 2 {
		t.Fatalf("presets = %d, want >= 2", len(list))
	}
	ro, ok := PresetByID(PresetIDReadOnly)
	if !ok {
		t.Fatal("missing read-only")
	}
	dev, ok := PresetByID(PresetIDDev)
	if !ok {
		t.Fatal("missing dev")
	}
	// Documented differences: read-only denies bash; dev allows go *.
	if Evaluate("bash", "ls", Defaults(), ro.Rules) != Deny {
		t.Fatal("read-only should deny bash")
	}
	if Evaluate("bash", "go test ./...", Defaults(), dev.Rules) != Allow {
		t.Fatal("dev should allow go test ./...")
	}
	if Evaluate("bash", "go test foo", Defaults(), dev.Rules) != Allow {
		t.Fatal("dev should allow go test foo")
	}
	if Evaluate("write", "x.go", Defaults(), ro.Rules) != Deny {
		t.Fatal("read-only should deny write")
	}
	if Evaluate("write", "x.go", Defaults(), dev.Rules) != Ask {
		t.Fatal("dev should leave write as ask (defaults)")
	}
	if Evaluate("write", ".env", Defaults(), dev.Rules) != Deny {
		t.Fatal("dev should deny .env write")
	}
}

func TestPresetYoloSandboxAllows(t *testing.T) {
	p, ok := PresetByID(PresetIDYoloSandbox)
	if !ok {
		t.Fatal("missing yolo-with-sandbox")
	}
	if Evaluate("bash", "anything", Defaults(), p.Rules) != Allow {
		t.Fatal("yolo-with-sandbox should allow bash")
	}
	// Later deny still wins (last-match).
	deny := Ruleset{{Permission: "bash", Pattern: "rm *", Action: Deny}}
	if Evaluate("bash", "rm -rf x", Defaults(), p.Rules, deny) != Deny {
		t.Fatal("later deny must beat preset allow")
	}
}

func TestValidPresetID(t *testing.T) {
	if !ValidPresetID("") || !ValidPresetID("dev") || !ValidPresetID("READ-ONLY") {
		t.Fatal("expected valid")
	}
	if ValidPresetID("nope") {
		t.Fatal("unknown should be invalid")
	}
}
