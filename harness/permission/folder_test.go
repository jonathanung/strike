package permission

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestDeriveFolderGrant(t *testing.T) {
	dirMeta := json.RawMessage(`{"path":"docs","directory":true}`)
	cases := []struct {
		name       string
		permission string
		patterns   []string
		metadata   json.RawMessage
		wantOK     bool
		wantFolder string
		wantPats   []string
	}{
		{name: "write nested file", permission: "write", patterns: []string{"docs/guide.md"}, wantOK: true, wantFolder: "docs", wantPats: []string{"docs", "docs/*", "docs/**"}},
		{name: "edit deep file", permission: "edit", patterns: []string{"a/b/c.go"}, wantOK: true, wantFolder: "a/b", wantPats: []string{"a/b", "a/b/*", "a/b/**"}},
		{name: "patch same folder", permission: "edit", patterns: []string{"docs/a.md", "docs/b.md"}, wantOK: true, wantFolder: "docs"},
		{name: "patch two folders", permission: "edit", patterns: []string{"a/x.md", "b/y.md"}, wantOK: false},
		{name: "bash has no folder scope", permission: "bash", patterns: []string{"ls docs"}, wantOK: false},
		{name: "webfetch has no folder scope", permission: "webfetch", patterns: []string{"https://x.test/docs"}, wantOK: false},
		{name: "no patterns", permission: "write", patterns: nil, wantOK: false},
		{name: "star pattern", permission: "write", patterns: []string{"*"}, wantOK: false},
		{name: "absolute pattern", permission: "write", patterns: []string{"/etc/passwd"}, wantOK: false},
		{name: "escaping pattern", permission: "write", patterns: []string{"../secret.md"}, wantOK: false},
		{name: "cleaned escape", permission: "write", patterns: []string{"docs/../../secret.md"}, wantOK: false},
		{name: "root file is out of v1 scope", permission: "write", patterns: []string{"main.go"}, wantOK: false},
		{name: "directory target grants itself", permission: "edit", patterns: []string{"docs"}, metadata: dirMeta, wantOK: true, wantFolder: "docs", wantPats: []string{"docs", "docs/*", "docs/**"}},
		{name: "directory target nested", permission: "edit", patterns: []string{"a/docs"}, metadata: dirMeta, wantOK: true, wantFolder: "a/docs"},
		{name: "glob char folder is escaped", permission: "write", patterns: []string{"a*b/f.md"}, wantOK: true, wantFolder: "a*b", wantPats: []string{`a\*b`, `a\*b/*`, `a\*b/**`}},
		{name: "bracket folder is escaped", permission: "write", patterns: []string{"we[ird/f.md"}, wantOK: true, wantFolder: "we[ird", wantPats: []string{`we\[ird`, `we\[ird/*`, `we\[ird/**`}},
		{name: "star inside a file name is literal", permission: "write", patterns: []string{"docs/a*b.md"}, wantOK: true, wantFolder: "docs"},
		{name: "glob segment is not a file path", permission: "write", patterns: []string{"docs/*/f.md"}, wantOK: false},
		{name: "double star segment", permission: "write", patterns: []string{"docs/**"}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, ok := DeriveFolderGrant(tc.permission, tc.patterns, tc.metadata)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (grant %+v)", ok, tc.wantOK, g)
			}
			if !ok {
				return
			}
			if g.Folder != tc.wantFolder {
				t.Errorf("folder = %q, want %q", g.Folder, tc.wantFolder)
			}
			if tc.wantPats != nil {
				if len(g.Patterns) != len(tc.wantPats) {
					t.Fatalf("patterns = %v, want %v", g.Patterns, tc.wantPats)
				}
				for i := range tc.wantPats {
					if g.Patterns[i] != tc.wantPats[i] {
						t.Errorf("patterns[%d] = %q, want %q", i, g.Patterns[i], tc.wantPats[i])
					}
				}
			}
			for _, pat := range g.Patterns {
				if err := ValidatePattern(pat); err != nil {
					t.Errorf("recorded pattern %q fails validation: %v", pat, err)
				}
			}
		})
	}
}

func TestFolderGrantPatternMatching(t *testing.T) {
	mustGrant := func(permission, pattern string) FolderGrant {
		g, ok := DeriveFolderGrant(permission, []string{pattern}, nil)
		if !ok {
			t.Fatalf("derive %s %s: not ok", permission, pattern)
		}
		return g
	}
	rulesOf := func(g FolderGrant) Ruleset {
		var rs Ruleset
		for _, pat := range g.Patterns {
			rs = append(rs, Rule{Permission: g.Permission, Pattern: pat, Action: Allow})
		}
		return rs
	}

	docs := rulesOf(mustGrant("write", "docs/guide.md"))
	for _, pat := range []string{"docs/guide.md", "docs/other.md", "docs/sub/deep/x.md"} {
		if got := Evaluate("write", pat, docs); got != Allow {
			t.Errorf("write %s = %v, want allow", pat, got)
		}
	}
	for _, pat := range []string{"docs2/guide.md", "docs-old/guide.md", "src/docs/guide.md", "docs"} {
		// "docs" itself matches the exact-folder rule by design (directory
		// targets); it is a folder path, never a file outside the folder.
		if pat == "docs" {
			continue
		}
		if got := Evaluate("write", pat, docs); got == Allow {
			t.Errorf("write %s = allow, want not allow", pat)
		}
	}
	// A write grant does not cover the distinct edit permission.
	if got := Evaluate("edit", "docs/guide.md", docs); got == Allow {
		t.Errorf("edit under a write grant = allow, want not allow")
	}

	star := rulesOf(mustGrant("write", "a*b/f.md"))
	if got := Evaluate("write", "a*b/f.md", star); got != Allow {
		t.Errorf("literal-star folder file = %v, want allow", got)
	}
	if got := Evaluate("write", "axb/f.md", star); got == Allow {
		t.Errorf("axb under an a*b grant = allow, want not allow (glob injection)")
	}
	bracket := rulesOf(mustGrant("write", "we[ird/f.md"))
	if got := Evaluate("write", "we[ird/f.md", bracket); got != Allow {
		t.Errorf("bracket folder file = %v, want allow", got)
	}
	if got := Evaluate("write", "wei/f.md", bracket); got == Allow {
		t.Errorf("wei under a we[ird grant = allow, want not allow")
	}
}

// askDriver drives Ask calls that suspend, capturing PermissionAsked IDs.
type askDriver struct {
	svc     *Service
	askedCh chan protocol.PermissionAsked
}

func newAskDriver(base ...Ruleset) *askDriver {
	ch := make(chan protocol.PermissionAsked, 32)
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			ch <- a
		}
	}, base...)
	return &askDriver{svc: svc, askedCh: ch}
}

// prompt runs req expecting a suspension, replies with decision, and
// returns the Ask result.
func (d *askDriver) prompt(t *testing.T, req tool.AskRequest, decision protocol.Decision) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- d.svc.Ask(context.Background(), req) }()
	select {
	case a := <-d.askedCh:
		d.svc.Reply(protocol.PermissionReply{RequestID: a.RequestID, Decision: decision})
	case <-time.After(2 * time.Second):
		t.Fatalf("ask %v did not suspend", req.Patterns)
	}
	select {
	case err := <-errCh:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not return after reply")
		return nil
	}
}

// settled runs req and reports whether it suspended; a suspension is
// released with a reject reply.
func (d *askDriver) settled(t *testing.T, req tool.AskRequest) (prompted bool, err error) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- d.svc.Ask(context.Background(), req) }()
	select {
	case a := <-d.askedCh:
		d.svc.Reply(protocol.PermissionReply{RequestID: a.RequestID, Decision: protocol.DecisionReject})
		return true, <-errCh
	case err := <-errCh:
		return false, err
	case <-time.After(2 * time.Second):
		t.Fatal("ask neither suspended nor returned")
		return false, nil
	}
}

func writeAsk(pattern string) tool.AskRequest {
	return tool.AskRequest{Permission: "write", Patterns: []string{pattern}, Always: []string{"*"}}
}

func TestFolderReplyApprovesFolderOnly(t *testing.T) {
	d := newAskDriver(Ruleset{
		{Permission: "write", Pattern: "*", Action: Ask},
		{Permission: "edit", Pattern: "*", Action: Ask},
	})
	if err := d.prompt(t, writeAsk("docs/a.md"), protocol.DecisionFolder); err != nil {
		t.Fatalf("folder reply ask: %v", err)
	}
	grants := d.svc.FolderGrants()
	if len(grants) != 1 || grants[0].Folder != "docs" || grants[0].Permission != "write" {
		t.Fatalf("folder grants = %+v, want one write grant for docs", grants)
	}
	// Sibling + deep-subfolder writes proceed without a prompt.
	for _, pat := range []string{"docs/b.md", "docs/sub/deep/c.md"} {
		prompted, err := d.settled(t, writeAsk(pat))
		if prompted || err != nil {
			t.Errorf("write %s: prompted=%v err=%v, want approved silently", pat, prompted, err)
		}
	}
	// A write outside the folder still prompts.
	prompted, err := d.settled(t, writeAsk("src/x.md"))
	if !prompted {
		t.Errorf("write src/x.md: prompted=false err=%v, want a prompt", err)
	}
	var rejected *RejectedError
	if !errors.As(err, &rejected) {
		t.Errorf("write src/x.md err = %v, want RejectedError from the release reply", err)
	}
	// The grant is for write only: an edit in the same folder still prompts.
	prompted, _ = d.settled(t, tool.AskRequest{Permission: "edit", Patterns: []string{"docs/a.md"}, Always: []string{"*"}})
	if !prompted {
		t.Error("edit docs/a.md under a write folder grant did not prompt")
	}
}

func TestFolderReplyForBashRecordsNothing(t *testing.T) {
	d := newAskDriver(Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})
	req := tool.AskRequest{Permission: "bash", Patterns: []string{"ls docs"}, Always: []string{"ls *"}}
	if err := d.prompt(t, req, protocol.DecisionFolder); err != nil {
		t.Fatalf("folder reply on bash ask: %v", err)
	}
	if got := d.svc.FolderGrants(); len(got) != 0 {
		t.Fatalf("folder grants after bash folder reply = %+v, want none", got)
	}
	// Nothing was remembered: the same command prompts again.
	prompted, _ := d.settled(t, req)
	if !prompted {
		t.Error("bash ask after a folder reply did not prompt again")
	}
}

func TestFolderReplyMultiFolderRecordsNothing(t *testing.T) {
	d := newAskDriver(Ruleset{{Permission: "edit", Pattern: "*", Action: Ask}})
	req := tool.AskRequest{Permission: "edit", Patterns: []string{"a/x.md", "b/y.md"}, Always: []string{"*"}}
	if err := d.prompt(t, req, protocol.DecisionFolder); err != nil {
		t.Fatalf("folder reply on multi-folder ask: %v", err)
	}
	if got := d.svc.FolderGrants(); len(got) != 0 {
		t.Fatalf("folder grants after multi-folder reply = %+v, want none", got)
	}
	prompted, _ := d.settled(t, tool.AskRequest{Permission: "edit", Patterns: []string{"a/x.md"}, Always: []string{"*"}})
	if !prompted {
		t.Error("edit a/x.md after a multi-folder folder reply did not prompt")
	}
}

func TestFolderGrantLosesToManagedDenyAndPlanMode(t *testing.T) {
	d := newAskDriver(Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})
	if err := d.prompt(t, writeAsk("docs/a.md"), protocol.DecisionFolder); err != nil {
		t.Fatalf("folder reply ask: %v", err)
	}
	// Managed (MDM) denies sit after session grants and still win.
	d.svc.SetManagedRules(Ruleset{{Permission: "write", Pattern: "docs/secret.md", Action: Deny}})
	prompted, err := d.settled(t, writeAsk("docs/secret.md"))
	if prompted {
		t.Error("managed-denied write prompted; want a hard deny")
	}
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Errorf("managed-denied write err = %v, want DeniedError", err)
	}
	// Other files in the folder are still covered by the grant.
	if prompted, err := d.settled(t, writeAsk("docs/b.md")); prompted || err != nil {
		t.Errorf("write docs/b.md: prompted=%v err=%v, want approved", prompted, err)
	}

	// Plan mode's late hard-deny of writes also beats the grant.
	d2 := newAskDriver(Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})
	d2.svc.SetPermissionMode(protocol.PermissionModePlan)
	// In plan mode a write ask is denied outright, so record the grant via
	// seeding to isolate the evaluation-order claim.
	g, ok := DeriveFolderGrant("write", []string{"docs/a.md"}, nil)
	if !ok {
		t.Fatal("derive failed")
	}
	d2.svc.SeedFolderGrants([]FolderGrant{g})
	prompted, err = d2.settled(t, writeAsk("docs/b.md"))
	if prompted {
		t.Error("plan-mode write prompted; want a hard deny")
	}
	if !errors.As(err, &denied) {
		t.Errorf("plan-mode write err = %v, want DeniedError", err)
	}
}

func TestRevokeFolderGrant(t *testing.T) {
	d := newAskDriver(Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})
	if err := d.prompt(t, writeAsk("docs/a.md"), protocol.DecisionFolder); err != nil {
		t.Fatalf("folder reply ask: %v", err)
	}
	if prompted, err := d.settled(t, writeAsk("docs/b.md")); prompted || err != nil {
		t.Fatalf("pre-revoke sibling: prompted=%v err=%v", prompted, err)
	}
	if d.svc.RevokeFolderGrant(3) {
		t.Error("revoke out-of-range index returned true")
	}
	if d.svc.RevokeFolderGrant(-1) {
		t.Error("revoke negative index returned true")
	}
	if !d.svc.RevokeFolderGrant(0) {
		t.Fatal("revoke of the recorded grant returned false")
	}
	if got := d.svc.FolderGrants(); len(got) != 0 {
		t.Fatalf("folder grants after revoke = %+v, want none", got)
	}
	// Removal actually stops the grant: the folder prompts again.
	if prompted, _ := d.settled(t, writeAsk("docs/b.md")); !prompted {
		t.Error("write docs/b.md after revoke did not prompt")
	}
}

func TestRevokeOneOfDuplicateFolderGrants(t *testing.T) {
	d := newAskDriver(Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})
	g, ok := DeriveFolderGrant("write", []string{"docs/a.md"}, nil)
	if !ok {
		t.Fatal("derive failed")
	}
	d.svc.SeedFolderGrants([]FolderGrant{g, g})
	if !d.svc.RevokeFolderGrant(0) {
		t.Fatal("first revoke failed")
	}
	if prompted, err := d.settled(t, writeAsk("docs/b.md")); prompted || err != nil {
		t.Errorf("after one revoke: prompted=%v err=%v, want still approved by the duplicate", prompted, err)
	}
	if !d.svc.RevokeFolderGrant(0) {
		t.Fatal("second revoke failed")
	}
	if prompted, _ := d.settled(t, writeAsk("docs/b.md")); !prompted {
		t.Error("after both revokes the write did not prompt")
	}
}

func TestSeedFolderGrantsAndAgentSwitchClears(t *testing.T) {
	d := newAskDriver(Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})
	g, ok := DeriveFolderGrant("write", []string{"docs/a.md"}, nil)
	if !ok {
		t.Fatal("derive failed")
	}
	d.svc.SeedFolderGrants([]FolderGrant{g})
	if got := d.svc.FolderGrants(); len(got) != 1 || got[0].Folder != "docs" {
		t.Fatalf("seeded grants = %+v", got)
	}
	if prompted, err := d.settled(t, writeAsk("docs/deep/x.md")); prompted || err != nil {
		t.Errorf("seeded grant sibling: prompted=%v err=%v", prompted, err)
	}
	// An agent switch clears session grants, folder grants included.
	d.svc.SetAgentRules(Ruleset{{Permission: "task", Pattern: "*", Action: Allow}})
	if got := d.svc.FolderGrants(); len(got) != 0 {
		t.Fatalf("folder grants after agent switch = %+v, want none", got)
	}
	if prompted, _ := d.settled(t, writeAsk("docs/deep/x.md")); !prompted {
		t.Error("write after agent switch did not prompt")
	}
}
