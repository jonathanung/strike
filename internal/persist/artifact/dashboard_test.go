package artifact

import (
	"errors"
	"strings"
	"testing"
)

const goodDashboard = `{"version":1,"template":"standup","blocks":[
 {"kind":"metric","title":"Running","metric":"sessions.running"},
 {"kind":"sessions_kanban","title":"Sessions"},
 {"kind":"text","title":"Yesterday","body":"Shipped the parser."},
 {"kind":"list","title":"Today","items":["Review PR"]},
 {"kind":"kanban","title":"Work","columns":[{"title":"Doing","cards":["parser"]}]}
]}`

func TestParseDashboardAcceptsEveryKind(t *testing.T) {
	spec, err := ParseDashboard(goodDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != 1 || spec.Template != "standup" || len(spec.Blocks) != 5 {
		t.Fatalf("spec = %+v", spec)
	}
	measured := 0
	for _, b := range spec.Blocks {
		if MeasuredBlock(b.Kind) {
			measured++
		}
	}
	if measured != 2 {
		t.Fatalf("measured blocks = %d", measured)
	}
	if spec, err := ParseDashboard(`{"blocks":[{"kind":"text","title":"t"}]}`); err != nil || spec.Version != 1 {
		t.Fatalf("missing version defaults to 1: %+v %v", spec, err)
	}
}

func TestParseDashboardRejects(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"not json", `nope`, "JSON page spec"},
		{"agent value on metric", `{"blocks":[{"kind":"metric","metric":"sessions.total","value":42}]}`, "daemon fills the value"},
		{"agent values on sessions kanban", `{"blocks":[{"kind":"sessions_kanban","columns":[{"title":"x","cards":[]}]}]}`, "filled by the daemon"},
		{"metric body", `{"blocks":[{"kind":"metric","metric":"sessions.total","body":"42"}]}`, "only kind, title and metric"},
		{"bad metric name", `{"blocks":[{"kind":"metric","metric":"Sessions Total"}]}`, "daemon metric"},
		{"unknown kind", `{"blocks":[{"kind":"chart"}]}`, "unknown kind"},
		{"no blocks", `{"blocks":[]}`, "1-40 blocks"},
		{"version 2", `{"version":2,"blocks":[{"kind":"text"}]}`, "version must be 1"},
		{"trailing", `{"blocks":[{"kind":"text"}]}{}`, "trailing"},
		{"kanban no columns", `{"blocks":[{"kind":"kanban","title":"k"}]}`, "1-8 columns"},
		{"long body", `{"blocks":[{"kind":"text","body":"` + strings.Repeat("a", 4001) + `"}]}`, "longer than"},
		{"text with items", `{"blocks":[{"kind":"text","items":["a"]}]}`, "title and body"},
	}
	for _, tc := range cases {
		_, err := ParseDashboard(tc.content)
		if err == nil || !errors.Is(err, errDashboard) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestStoreValidatesDashboardOnCreateAndUpdate(t *testing.T) {
	s, err := Open(t.TempDir(), "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !ValidType(TypeDashboard) || DaemonOnlyType(TypeDashboard) {
		t.Fatal("dashboard must be a valid, agent-writable type")
	}
	if _, err := s.Create(CreateInput{
		Type:         TypeDashboard,
		Content:      `{"blocks":[{"kind":"metric","metric":"cost.usd","value":0}]}`,
		OwnerSession: "s1",
	}); !errors.Is(err, errDashboard) {
		t.Fatalf("create with planted value err = %v", err)
	}
	a, err := s.Create(CreateInput{Type: TypeDashboard, Title: "Standup", Content: goodDashboard, OwnerSession: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	bad := `{"blocks":[{"kind":"metric","metric":"sessions.total","value":7}]}`
	if _, err := s.Update(a.ID, "s1", "s1", a.Version, UpdateInput{Content: &bad}); !errors.Is(err, errDashboard) {
		t.Fatalf("update with planted value err = %v", err)
	}
	cur, _, _ := s.Get(a.ID, "s1", "s1")
	if cur.Version != 1 || cur.Content != goodDashboard {
		t.Fatalf("rejected update changed the page: %+v", cur)
	}
	next := `{"blocks":[{"kind":"text","title":"Notes","body":"ok"}]}`
	up, err := s.Update(a.ID, "s1", "s1", a.Version, UpdateInput{Content: &next})
	if err != nil || up.Version != 2 {
		t.Fatalf("valid update = %+v %v", up, err)
	}
	// Other types are not parsed as dashboards.
	if _, err := s.Create(CreateInput{Type: TypeFindings, Content: "free text", OwnerSession: "s1"}); err != nil {
		t.Fatalf("findings free text: %v", err)
	}
}
