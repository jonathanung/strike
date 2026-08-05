package scheduler

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogShippedPresets(t *testing.T) {
	cat := Catalog()
	wantIDs := []string{"cmake", "ninja", "gradle", "bazel", "maven", "cargo", "npm"}
	if len(cat) != len(wantIDs) {
		t.Fatalf("catalog len=%d want %d: %+v", len(cat), len(wantIDs), cat)
	}
	for i, id := range wantIDs {
		p := cat[i]
		if p.ID != id {
			t.Fatalf("catalog[%d].ID=%q want %q", i, p.ID, id)
		}
		if p.Version < 1 {
			t.Errorf("%s: version %d", id, p.Version)
		}
		if strings.TrimSpace(p.Name) == "" {
			t.Errorf("%s: empty name", id)
		}
		if strings.TrimSpace(p.Rationale) == "" {
			t.Errorf("%s: empty rationale", id)
		}
		if !ValidClass(p.DefaultClass) {
			t.Errorf("%s: default class %q", id, p.DefaultClass)
		}
		if len(p.Rules) == 0 {
			t.Errorf("%s: no rules", id)
		}
		// Generated rules must compile.
		if _, err := CompileRules(p.Rules); err != nil {
			t.Errorf("%s: CompileRules: %v", id, err)
		}
		if err := ValidateLimits(p.Limits, "preset:"+id); err != nil {
			t.Errorf("%s: limits: %v", id, err)
		}
		// Defensive copy: mutating catalog result must not affect Lookup.
		if len(p.Rules) > 0 {
			p.Rules[0].Pattern = "MUTATED"
		}
		again, ok := Lookup(id)
		if !ok || again.Rules[0].Pattern == "MUTATED" {
			t.Errorf("%s: Catalog did not clone rules", id)
		}
	}
	if got := KnownPresetIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("KnownPresetIDs=%v want %v", got, wantIDs)
	}
}

func TestLookupUnknown(t *testing.T) {
	if _, ok := Lookup("nope"); ok {
		t.Fatal("expected miss")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("empty id")
	}
	if _, ok := Lookup("  cargo  "); !ok {
		// Lookup trims — config load also trims before lookup.
		t.Fatal("trimmed cargo should hit")
	}
}

func TestExpandPresetsDeterministicAndCatalogOrder(t *testing.T) {
	// Request order npm then cmake; expansion must follow catalog order (cmake before npm).
	aLimits, aRules, err := ExpandPresets([]string{"npm", "cmake"})
	if err != nil {
		t.Fatal(err)
	}
	bLimits, bRules, err := ExpandPresets([]string{"cmake", "npm", "cmake"}) // dup ignored
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(aLimits, bLimits) {
		t.Fatalf("limits differ: %v vs %v", aLimits, bLimits)
	}
	if !reflect.DeepEqual(aRules, bRules) {
		t.Fatalf("rules differ:\n%v\n%v", aRules, bRules)
	}
	if len(aRules) == 0 {
		t.Fatal("empty rules")
	}
	// First rule should be from cmake (catalog order).
	if !strings.HasPrefix(aRules[0].Source, "preset:cmake@v") {
		t.Fatalf("first source=%q want cmake", aRules[0].Source)
	}
	// Last cmake rule before first npm rule.
	var sawNPM bool
	for _, r := range aRules {
		if strings.HasPrefix(r.Source, "preset:npm@") {
			sawNPM = true
		}
		if sawNPM && strings.HasPrefix(r.Source, "preset:cmake@") {
			t.Fatal("cmake rule after npm — not catalog order")
		}
	}
	if !sawNPM {
		t.Fatal("missing npm rules")
	}
}

func TestExpandPresetsUnknown(t *testing.T) {
	_, _, err := ExpandPresets([]string{"cargo", "msbuild"})
	if err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("err=%v", err)
	}
	_, _, err = ExpandPresets([]string{"  "})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompileWithPresetsUserOverride(t *testing.T) {
	eff, err := CompileWithPresets(
		[]string{"cargo"},
		Limits{PoolBuild: 9}, // overrides preset suggested build
		[]CommandRule{
			{Pattern: "cargo bench *", Class: ClassBuild, Source: "user"},
			{Pattern: "cargo test *", Class: ClassGeneral, Source: "user-override"}, // reclassify
		},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Limits[PoolBuild] != 9 {
		t.Fatalf("build limit=%d want 9 (user overlay)", eff.Limits[PoolBuild])
	}
	if eff.Limits[PoolTest] != 2 {
		t.Fatalf("test limit=%d want preset 2", eff.Limits[PoolTest])
	}
	// Preset would classify cargo test as test; user rule last → general.
	if got := eff.Classify("cargo test --lib"); got != ClassGeneral {
		t.Fatalf("cargo test class=%q want general (user override)", got)
	}
	if got := eff.Classify("cargo build --release"); got != ClassBuild {
		t.Fatalf("cargo build class=%q", got)
	}
	if got := eff.Classify("cargo bench a"); got != ClassBuild {
		t.Fatalf("cargo bench class=%q", got)
	}
	rep := eff.Report()
	if !strings.Contains(rep, "preset:cargo@v") {
		t.Fatalf("report missing preset source:\n%s", rep)
	}
	if !strings.Contains(rep, "user-override") {
		t.Fatalf("report missing user source:\n%s", rep)
	}
}

func TestPresetCommandFixtures(t *testing.T) {
	// Representative commands for every shipped preset — classification only
	// via CompileWithPresets / Classify (no second path).
	type row struct {
		cmd  string
		want Class
	}
	cases := map[string][]row{
		"cmake": {
			{"cmake -S . -B build", ClassBuild},
			{"cmake --build build -j 8", ClassBuild},
			{"cmake", ClassBuild},
			{"ctest --test-dir build --output-on-failure", ClassTest},
			{"ctest", ClassTest},
			{"make all", ClassGeneral},
		},
		"ninja": {
			{"ninja -C build", ClassBuild},
			{"ninja", ClassBuild},
			{"ninja -t targets", ClassBuild},
			{"make", ClassGeneral},
		},
		"gradle": {
			{"gradle assemble", ClassBuild},
			{"./gradlew build", ClassBuild},
			{"gradlew build", ClassBuild},
			{"gradle test", ClassTest},
			{"./gradlew test --tests Foo", ClassTest},
			{"gradle check", ClassTest},
			{"./gradlew check", ClassTest},
		},
		"bazel": {
			{"bazel build //...", ClassBuild},
			{"bazelisk build //pkg:all", ClassBuild},
			{"bazel test //...", ClassTest},
			{"bazelisk test //foo:bar", ClassTest},
			{"bazel coverage //...", ClassTest},
			{"bazel query //...", ClassBuild},
		},
		"maven": {
			{"mvn package", ClassBuild},
			{"./mvnw -q install", ClassBuild},
			{"mvn test", ClassTest},
			{"mvnw test -Dtest=Foo", ClassTest},
			{"./mvnw verify", ClassTest},
		},
		"cargo": {
			{"cargo build --release", ClassBuild},
			{"cargo check", ClassBuild},
			{"cargo clippy -- -D warnings", ClassBuild},
			{"cargo test", ClassTest},
			{"cargo test --workspace", ClassTest},
			{"cargo nextest run", ClassTest},
			{"rustc --version", ClassGeneral},
		},
		"npm": {
			{"npm install", ClassBuild},
			{"npm run build", ClassBuild},
			{"yarn build", ClassBuild},
			{"pnpm run build", ClassBuild},
			{"bun run build", ClassBuild},
			{"npm test", ClassTest},
			{"npm run test:unit", ClassTest},
			{"yarn test", ClassTest},
			{"pnpm test", ClassTest},
			{"bun test", ClassTest},
			{"bun run test", ClassTest},
			{"node -v", ClassGeneral},
		},
	}

	for _, id := range KnownPresetIDs() {
		rows, ok := cases[id]
		if !ok {
			t.Fatalf("missing fixture rows for preset %q", id)
		}
		eff, err := CompileWithPresets([]string{id}, nil, nil, "")
		if err != nil {
			t.Fatalf("%s: compile: %v", id, err)
		}
		for _, tc := range rows {
			got := eff.Classify(tc.cmd)
			if got != tc.want {
				t.Errorf("%s: Classify(%q)=%q want %q", id, tc.cmd, got, tc.want)
			}
			// Pools follow class — still the single runtime path.
			pools := eff.PoolsForCommand(tc.cmd)
			wantPools := PoolsForClass(tc.want)
			if !reflect.DeepEqual(pools, wantPools) {
				t.Errorf("%s: PoolsForCommand(%q)=%v want %v", id, tc.cmd, pools, wantPools)
			}
		}
	}
}

func TestMergePresetIDs(t *testing.T) {
	got := MergePresetIDs([]string{"cargo", "npm"}, []string{"npm", "cmake", "cargo"})
	want := []string{"cargo", "npm", "cmake"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if MergePresetIDs(nil, nil) != nil {
		t.Fatal("nil+nil")
	}
	// base not mutated
	base := []string{"a"}
	_ = MergePresetIDs(base, []string{"b"})
	if len(base) != 1 {
		t.Fatalf("base mutated: %v", base)
	}
}

func TestValidatePresetIDs(t *testing.T) {
	if err := ValidatePresetIDs(nil, "/cfg"); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePresetIDs([]string{"cargo", "npm"}, "/cfg"); err != nil {
		t.Fatal(err)
	}
	err := ValidatePresetIDs([]string{"cargo", "cargo"}, "/cfg")
	if err == nil || !strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), "/cfg") {
		t.Fatalf("err=%v", err)
	}
	err = ValidatePresetIDs([]string{"nope"}, "proj")
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("err=%v", err)
	}
}
