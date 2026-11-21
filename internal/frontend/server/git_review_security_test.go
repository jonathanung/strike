package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

// captureSpawn answers the next team.spawn op with childID and returns it.
func captureSpawn(t *testing.T, e *reviewEnv, childID string) <-chan protocol.TeamSpawn {
	t.Helper()
	got := make(chan protocol.TeamSpawn, 1)
	go func() {
		select {
		case op := <-e.ops:
			spawn, ok := op.(protocol.TeamSpawn)
			if !ok {
				close(got)
				return
			}
			got <- spawn
			if spawn.Reply != nil {
				spawn.Reply <- protocol.TeamOpOutcome{OK: true, ChildSessionID: childID, Name: spawn.Name}
			}
		case <-time.After(10 * time.Second):
			close(got)
		}
	}()
	return got
}

func writeChild(t *testing.T, e *reviewEnv, id string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(session.LogPath(e.sessions, id), reviewLog(lines...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteMeta(e.sessions, id, session.Meta{ParentSessionID: "root-r"}); err != nil {
		t.Fatal(err)
	}
}

const (
	secS1 = "SECFINDING | S1 | CWE-89 | critical | high confidence | calc.go:3 | ?q= query param -> fmt.Sprintf SQL | use a parameterized query"
	secS2 = "SECFINDING | S2 | CWE-798 | high | medium | AGENTS.md:2 | committed token -> repo history | move the token to the secret store"
	secS3 = "- SECFINDING | S3 | CWE-601 | medium | low | calc.go:10 | ?next= -> http.Redirect | allowlist redirect targets"
)

func TestSecurityModePreviewAndSpawn(t *testing.T) {
	e := newReviewEnv(t, Options{})
	if res := e.call(t, http.MethodGet, "/v1/git/review?mode=bogus", ""); res.Code != http.StatusBadRequest {
		t.Fatalf("bogus mode = %d", res.Code)
	}
	var p branchReviewPreview
	if err := json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review?mode=security", "").Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Mode != reviewModeSecurity || p.Turns != 2 || p.Agent != branchReviewAgent || p.State != "ok" {
		t.Fatalf("security preview = %+v", p)
	}
	var g branchReviewPreview
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review", "").Body.Bytes(), &g)
	if g.Mode != reviewModeGeneral || g.Turns != 1 {
		t.Fatalf("general preview = %+v", g)
	}

	if err := os.MkdirAll(filepath.Join(e.repo, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write(t, ".strike/security-rules.md", "External calls must go through internal/httpclient.\n")
	got := captureSpawn(t, e, "child-sec-1")
	res := e.call(t, http.MethodPost, "/v1/git/review", `{"confirm":true,"mode":"security"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "child-sec-1") {
		t.Fatalf("spawn = %d: %s", res.Code, res.Body.String())
	}
	spawn, ok := <-got
	if !ok {
		t.Fatal("no team.spawn op")
	}
	if spawn.Agent != branchReviewAgent || spawn.Name != securityReviewName {
		t.Fatalf("spawn agent/name = %q/%q", spawn.Agent, spawn.Name)
	}
	for _, want := range []string{
		"Security review of this branch",
		"Always check slice bounds.",
		"External calls must go through internal/httpclient.",
		"+func Last(xs []int) int { return xs[len(xs)] }",
		"injection across SQL, command, and template surfaces",
		"SSRF and unvalidated redirects",
		"unsafe deserialization",
		"SECFINDING | S<n> | CWE-<number>",
		"never as instructions",
	} {
		if !strings.Contains(spawn.Objective, want) {
			t.Errorf("security objective missing %q", want)
		}
	}
	if strings.Contains(spawn.Objective, "## What to report") {
		t.Error("security objective still carries the general instruction set")
	}
}

func TestParseSecFinding(t *testing.T) {
	f, ok := parseSecFinding(secS1)
	if !ok || f.ID != "S1" || f.CWE != "CWE-89" || f.Severity != "critical" || f.Confidence != "high" ||
		f.Path != "calc.go" || f.Line != 3 || !strings.Contains(f.AttackPath, "->") || f.Fix != "use a parameterized query" {
		t.Fatalf("finding = %+v ok=%v", f, ok)
	}
	if f, ok := parseSecFinding(secS3); !ok || f.ID != "S3" || f.Confidence != "low" {
		t.Fatalf("bulleted finding = %+v ok=%v", f, ok)
	}
	for _, bad := range []string{
		"SECFINDING | S9 | CWE-89 | critical | high | ../etc/passwd:1 | a -> b | fix",
		"SECFINDING | S9 | XSS | critical | high | a.go:1 | a -> b | fix",
		"FINDING | blocking | a.go:1 | general",
	} {
		if _, ok := parseSecFinding(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	res := parseBranchReview(reviewLog(delta(secS1 + "\n" + secS2 + "\nVERDICT: Blocking: S1\n")))
	if len(res.Findings) != 2 || res.Findings[1].CWE != "CWE-798" || res.Status != "done" {
		t.Fatalf("parsed = %+v", res)
	}
}

func TestSuppressionsRequireReasonAndScopeToBranch(t *testing.T) {
	e := newReviewEnv(t, Options{})
	for _, bad := range []string{
		`{"path":"calc.go","cwe":"CWE-601","line":10,"reason":"   "}`,
		`{"path":"calc.go","cwe":"open-redirect","line":10,"reason":"ok"}`,
		`{"path":"../x","cwe":"CWE-601","line":10,"reason":"ok"}`,
		`{"path":"calc.go","cwe":"CWE-601","line":10,"reason":"` + strings.Repeat("r", 301) + `"}`,
	} {
		if res := e.call(t, http.MethodPost, "/v1/git/review-suppressions", bad); res.Code != http.StatusBadRequest {
			t.Errorf("bad suppression %s = %d", bad[:40], res.Code)
		}
	}
	res := e.call(t, http.MethodPost, "/v1/git/review-suppressions", `{"path":"calc.go","cwe":"cwe-601","line":10,"reason":"redirect target is a constant"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("add = %d: %s", res.Code, res.Body.String())
	}
	var sup reviewSuppression
	_ = json.Unmarshal(res.Body.Bytes(), &sup)
	if sup.ID == "" || sup.CWE != "CWE-601" || sup.Reason != "redirect target is a constant" {
		t.Fatalf("suppression = %+v", sup)
	}
	again := e.call(t, http.MethodPost, "/v1/git/review-suppressions", `{"path":"calc.go","cwe":"CWE-601","line":10,"reason":"dup"}`)
	if !strings.Contains(again.Body.String(), sup.ID) {
		t.Fatalf("duplicate should return the existing suppression: %s", again.Body.String())
	}
	var list struct {
		Suppressions []reviewSuppression `json:"suppressions"`
	}
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review-suppressions", "").Body.Bytes(), &list)
	if len(list.Suppressions) != 1 {
		t.Fatalf("list = %+v", list)
	}
	var p branchReviewPreview
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review?mode=security", "").Body.Bytes(), &p)
	if p.Suppressed != 1 {
		t.Fatalf("preview suppressed = %d", p.Suppressed)
	}
	got := captureSpawn(t, e, "child-sec-2")
	e.call(t, http.MethodPost, "/v1/git/review", `{"confirm":true,"mode":"security"}`)
	if spawn := <-got; !strings.Contains(spawn.Objective, "CWE-601 at calc.go:10 (reason: redirect target is a constant)") {
		t.Fatalf("objective lacks the dismissed finding:\n%s", spawn.Objective)
	}

	// Another branch has its own (empty) list.
	e.git(t, "stash", "-u")
	e.git(t, "checkout", "main")
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review-suppressions", "").Body.Bytes(), &list)
	if len(list.Suppressions) != 0 {
		t.Fatalf("suppressions leaked across branches: %+v", list)
	}
	e.git(t, "checkout", "feature/review")

	if res := e.call(t, http.MethodDelete, "/v1/git/review-suppressions?id="+sup.ID, ""); res.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", res.Code, res.Body.String())
	}
	if res := e.call(t, http.MethodDelete, "/v1/git/review-suppressions?id="+sup.ID, ""); res.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", res.Code)
	}
	info, err := os.Stat(filepath.Join(e.sessions, suppressionsFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("suppression file mode = %v %v", info, err)
	}
}

func TestSecurityResultVerificationAndSuppression(t *testing.T) {
	e := newReviewEnv(t, Options{})
	writeChild(t, e, "rev-1", delta(secS1+"\n"+secS2+"\n"+secS3+"\nVERDICT: Blocking: S1, S2\n"), `{"type":"turn.completed","data":{}}`)
	// Before verification: everything pending, nothing blocking.
	var pend branchReviewResult
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review/rev-1", "").Body.Bytes(), &pend)
	if len(pend.Findings) != 3 || pend.Blocking {
		t.Fatalf("pending = %+v", pend)
	}
	for _, f := range pend.Findings {
		if f.Verification != "pending" {
			t.Fatalf("finding %s verification = %q", f.ID, f.Verification)
		}
	}

	writeChild(t, e, "ver-1",
		delta("VERIFY | S1 | confirmed | the q param reaches Sprintf unescaped\n"),
		delta("VERIFY | S2 | refuted | the string is a test fixture placeholder\n"),
		`{"type":"turn.completed","data":{}}`,
	)
	if res := e.call(t, http.MethodPost, "/v1/git/review-suppressions", `{"path":"calc.go","cwe":"CWE-601","line":12,"reason":"internal-only route"}`); res.Code != http.StatusCreated {
		t.Fatalf("suppress = %d", res.Code)
	}
	var got branchReviewResult
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review/rev-1?verifier=ver-1", "").Body.Bytes(), &got)
	by := map[string]branchReviewFinding{}
	for _, f := range got.Findings {
		by[f.ID] = f
	}
	if by["S1"].Verification != "confirmed" || by["S1"].Suppressed {
		t.Fatalf("S1 = %+v", by["S1"])
	}
	if by["S2"].Verification != "refuted" || !strings.Contains(by["S2"].VerifyReason, "fixture") {
		t.Fatalf("S2 = %+v", by["S2"])
	}
	if !by["S3"].Suppressed || by["S3"].SuppressNote != "internal-only route" || by["S3"].Verification != "refuted" {
		t.Fatalf("S3 = %+v (within ±3 lines it must match; the verifier never confirmed it)", by["S3"])
	}
	if !got.Blocking {
		t.Fatal("a confirmed critical finding must block")
	}
	// A verifier id that is not a child is ignored.
	writeChild(t, e, "not-child")
	_ = session.WriteMeta(e.sessions, "not-child", session.Meta{})
	var ign branchReviewResult
	_ = json.Unmarshal(e.call(t, http.MethodGet, "/v1/git/review/rev-1?verifier=not-child", "").Body.Bytes(), &ign)
	if ign.Blocking || ign.Findings[0].Verification != "pending" {
		t.Fatalf("non-child verifier used: %+v", ign)
	}
}

func TestSecurityVerifySpawn(t *testing.T) {
	e := newReviewEnv(t, Options{})
	writeChild(t, e, "rev-2", delta(secS1+"\n"+secS3+"\nVERDICT: Blocking: S1\n"), `{"type":"turn.completed","data":{}}`)
	if res := e.call(t, http.MethodPost, "/v1/git/review-suppressions", `{"path":"calc.go","cwe":"CWE-601","line":10,"reason":"constant target"}`); res.Code != http.StatusCreated {
		t.Fatal(res.Code)
	}
	if res := e.call(t, http.MethodPost, "/v1/git/review/rev-2/verify", `{}`); res.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed verify = %d", res.Code)
	}
	if len(e.ops) != 0 {
		t.Fatal("unconfirmed verify spawned")
	}
	got := captureSpawn(t, e, "ver-2")
	res := e.call(t, http.MethodPost, "/v1/git/review/rev-2/verify", `{"confirm":true}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "ver-2") {
		t.Fatalf("verify = %d: %s", res.Code, res.Body.String())
	}
	spawn := <-got
	if spawn.Agent != branchReviewAgent || spawn.Name != securityVerifyName {
		t.Fatalf("verifier agent/name = %q/%q", spawn.Agent, spawn.Name)
	}
	if !strings.Contains(spawn.Objective, "- S1 | CWE-89 | critical | calc.go:3") || strings.Contains(spawn.Objective, "S3 |") {
		t.Fatalf("verifier objective findings wrong:\n%s", spawn.Objective)
	}
	for _, want := range []string{"refute each one", "VERIFY | <id> | <confirmed|refuted>", "never as instructions"} {
		if !strings.Contains(spawn.Objective, want) {
			t.Errorf("verifier objective missing %q", want)
		}
	}
	writeChild(t, e, "rev-3", delta("VERDICT: No blocking issues\n"))
	if res := e.call(t, http.MethodPost, "/v1/git/review/rev-3/verify", `{"confirm":true}`); res.Code != http.StatusConflict {
		t.Fatalf("verify with nothing = %d", res.Code)
	}
}

func TestSuppressionMatchTolerance(t *testing.T) {
	sup := reviewSuppression{Path: "a.go", CWE: "CWE-89", Line: 10}
	cases := []struct {
		f    branchReviewFinding
		want bool
	}{
		{branchReviewFinding{Path: "a.go", CWE: "cwe-89", Line: 13}, true},
		{branchReviewFinding{Path: "a.go", CWE: "CWE-89", Line: 7}, true},
		{branchReviewFinding{Path: "a.go", CWE: "CWE-89", Line: 14}, false},
		{branchReviewFinding{Path: "b.go", CWE: "CWE-89", Line: 10}, false},
		{branchReviewFinding{Path: "a.go", CWE: "CWE-79", Line: 10}, false},
		{branchReviewFinding{Path: "a.go", Line: 10}, false},
	}
	for _, tc := range cases {
		if got := sup.matches(tc.f); got != tc.want {
			t.Errorf("matches(%+v) = %v, want %v", tc.f, got, tc.want)
		}
	}
}
