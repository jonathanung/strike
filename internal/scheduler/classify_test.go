package scheduler

import (
	"strings"
	"testing"
)

func TestClassifyDefaultGeneral(t *testing.T) {
	if got := Classify("echo hi", nil); got != ClassGeneral {
		t.Fatalf("got %q", got)
	}
	if got := Classify("go test ./...", nil); got != ClassGeneral {
		t.Fatalf("got %q", got)
	}
}

func TestClassifyLastMatchWins(t *testing.T) {
	rules, err := CompileRules([]CommandRule{
		{Pattern: "go *", Class: ClassBuild, Source: "global"},
		{Pattern: "go test *", Class: ClassTest, Source: "project"},
		{Pattern: "go test -c *", Class: ClassBuild, Source: "project"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		cmd  string
		want Class
	}{
		{"go build ./...", ClassBuild},
		{"go test ./...", ClassTest},     // second rule wins over first
		{"go test -c ./pkg", ClassBuild}, // third overrides second
		{"cargo test", ClassGeneral},     // no match
		{"GO test ./...", ClassGeneral},  // case-sensitive
	}
	for _, tc := range cases {
		got, win := ClassifyDetail(tc.cmd, rules)
		if got != tc.want {
			t.Errorf("Classify(%q)=%q want %q (rule=%v)", tc.cmd, got, tc.want, win)
		}
	}
}

func TestClassifyMultipleMatchesDocumentedOrder(t *testing.T) {
	// Explicit multi-match: both rules match; last wins.
	rules, err := CompileRules([]CommandRule{
		{Pattern: "*", Class: ClassBuild, Source: "a"},
		{Pattern: "make *", Class: ClassTest, Source: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, win := ClassifyDetail("make all", rules)
	if got != ClassTest {
		t.Fatalf("got %q want test", got)
	}
	if win == nil || win.Source != "b" {
		t.Fatalf("winning rule=%v want source b", win)
	}
	// Command matching only the first rule.
	got, win = ClassifyDetail("ninja", rules)
	if got != ClassBuild || win == nil || win.Source != "a" {
		t.Fatalf("got %q win=%v", got, win)
	}
}

func TestCompileRulesInvalid(t *testing.T) {
	cases := []struct {
		name string
		rule CommandRule
		sub  string
	}{
		{"empty pattern", CommandRule{Pattern: "", Class: ClassBuild, Source: "/cfg"}, "empty pattern"},
		{"whitespace pattern", CommandRule{Pattern: "   ", Class: ClassTest, Source: "/cfg"}, "empty pattern"},
		{"bad class", CommandRule{Pattern: "x", Class: "deploy", Source: "/cfg"}, "invalid class"},
		{"trailing backslash", CommandRule{Pattern: `foo\`, Class: ClassGeneral, Source: "/cfg"}, "invalid pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileRules([]CommandRule{tc.rule})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err=%v want substring %q", err, tc.sub)
			}
			if !strings.Contains(err.Error(), "/cfg") {
				t.Fatalf("err should name source: %v", err)
			}
			if !strings.Contains(err.Error(), "commands[0]") {
				t.Fatalf("err should name index: %v", err)
			}
		})
	}
}

func TestCompileRulesValidClasses(t *testing.T) {
	for _, class := range []Class{ClassGeneral, ClassBuild, ClassTest} {
		rules, err := CompileRules([]CommandRule{{Pattern: "cmd *", Class: class}})
		if err != nil {
			t.Fatalf("class %s: %v", class, err)
		}
		if Classify("cmd x", rules) != class {
			t.Fatalf("class %s not applied", class)
		}
	}
}

func TestGlobMetacharacters(t *testing.T) {
	rules, err := CompileRules([]CommandRule{
		{Pattern: "go test ?", Class: ClassTest},
		{Pattern: `literal\*star`, Class: ClassBuild},
		{Pattern: "path/with.dots", Class: ClassBuild},
	})
	if err != nil {
		t.Fatal(err)
	}
	if Classify("go test a", rules) != ClassTest {
		t.Fatal("? should match one rune")
	}
	if Classify("go test ab", rules) != ClassGeneral {
		t.Fatal("? should not match two runes")
	}
	if Classify(`literal*star`, rules) != ClassBuild {
		t.Fatal(`\* should match literal star`)
	}
	if Classify("path/withXdots", rules) != ClassGeneral {
		t.Fatal("dot should be literal")
	}
	if Classify("path/with.dots", rules) != ClassBuild {
		t.Fatal("literal dots")
	}
}

func TestPoolsForClass(t *testing.T) {
	cases := []struct {
		class Class
		want  []string
	}{
		{ClassGeneral, []string{PoolProcess}},
		{ClassBuild, []string{PoolBuild, PoolProcess}},
		{ClassTest, []string{PoolProcess, PoolTest}},
		{Class("other"), []string{PoolProcess}},
	}
	for _, tc := range cases {
		got := PoolsForClass(tc.class)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.class, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v want %v", tc.class, got, tc.want)
			}
		}
	}
}

func TestPoolsForCommand(t *testing.T) {
	rules, err := CompileRules([]CommandRule{
		{Pattern: "pytest *", Class: ClassTest},
		{Pattern: "cmake *", Class: ClassBuild},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := PoolsForCommand("pytest -q", rules)
	if len(got) != 2 || got[0] != PoolProcess || got[1] != PoolTest {
		t.Fatalf("got %v", got)
	}
	got = PoolsForCommand("cmake --build .", rules)
	if len(got) != 2 || got[0] != PoolBuild || got[1] != PoolProcess {
		t.Fatalf("got %v", got)
	}
	got = PoolsForCommand("ls", rules)
	if len(got) != 1 || got[0] != PoolProcess {
		t.Fatalf("got %v", got)
	}
}
