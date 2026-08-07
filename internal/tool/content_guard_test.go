package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

func TestScanContentFindingsCredentials(t *testing.T) {
	t.Parallel()
	findings := scanContentFindings("export KEY=AKIAIOSFODNN7EXAMPLE\n")
	if len(findings) == 0 {
		t.Fatal("want credential finding")
	}
	var sawAWS bool
	for _, f := range findings {
		if f.RuleID == redact.RuleAWSAccessKeyID {
			sawAWS = true
			if f.Severity != ContentSeverityDeny || f.Kind != ContentKindCredential {
				t.Fatalf("aws finding = %+v", f)
			}
		}
	}
	if !sawAWS {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestScanContentFindingsPEM(t *testing.T) {
	t.Parallel()
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQC7\n-----END PRIVATE KEY-----\n"
	findings := scanContentFindings(pem)
	if len(findings) == 0 || findings[0].RuleID != redact.RulePEMPrivateKey {
		t.Fatalf("pem findings = %+v", findings)
	}
}

func TestScanContentFindingsDangerousSinks(t *testing.T) {
	t.Parallel()
	findings := scanContentFindings("def bad():\n    eval(user_input)\n")
	if len(findings) == 0 {
		t.Fatal("want sink finding")
	}
	if findings[0].Kind != ContentKindDangerousSink || findings[0].Severity != ContentSeverityAsk {
		t.Fatalf("sink = %+v", findings[0])
	}
}

func TestScanContentFindingsClean(t *testing.T) {
	t.Parallel()
	if got := scanContentFindings("package main\nfunc Hello() {}\n"); len(got) != 0 {
		t.Fatalf("clean code findings = %+v", got)
	}
}

func TestCheckContentGuardDenyDefault(t *testing.T) {
	t.Parallel()
	tc := allowAll(t.TempDir())
	err := checkContentGuard(context.Background(), tc, "secret.pem",
		"-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n")
	if err == nil {
		t.Fatal("want deny")
	}
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("code = %q, want content_guard_denied; err=%v", CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "pem_private_key") {
		t.Fatalf("err = %v, want rule id", err)
	}
}

func TestCheckContentGuardModeOff(t *testing.T) {
	t.Parallel()
	tc := allowAll(t.TempDir())
	tc.ContentGuard = ContentGuardSettings{Mode: ContentGuardModeOff}
	err := checkContentGuard(context.Background(), tc, "k.txt", "AKIAIOSFODNN7EXAMPLE")
	if err != nil {
		t.Fatalf("off should allow: %v", err)
	}
}

func TestCheckContentGuardForcedDenyBeatsOff(t *testing.T) {
	t.Parallel()
	tc := allowAll(t.TempDir())
	tc.ContentGuard = ContentGuardSettings{Mode: ContentGuardModeOff, ForcedDeny: true}
	err := checkContentGuard(context.Background(), tc, "k.txt", "AKIAIOSFODNN7EXAMPLE")
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("managed deny ceiling: code=%q err=%v", CodeOf(err), err)
	}
}

func TestCheckContentGuardForcedDenyBeatsPathAllow(t *testing.T) {
	t.Parallel()
	tc := allowAll(t.TempDir())
	tc.ContentGuard = ContentGuardSettings{
		Mode:       ContentGuardModeDeny,
		ForcedDeny: true,
		PathAllow:  []string{"**/testdata/**"},
	}
	err := checkContentGuard(context.Background(), tc, "testdata/key.pem",
		"-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n")
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("ForcedDeny must ignore pathAllow: code=%q err=%v", CodeOf(err), err)
	}
}

func TestCheckContentGuardModeAsk(t *testing.T) {
	t.Parallel()
	var asked AskRequest
	tc := &Context{
		WorkDir:      t.TempDir(),
		ContentGuard: ContentGuardSettings{Mode: ContentGuardModeAsk},
		Ask: func(_ context.Context, req AskRequest) error {
			asked = req
			return nil
		},
	}
	err := checkContentGuard(context.Background(), tc, "creds.env", "AKIAIOSFODNN7EXAMPLE")
	if err != nil {
		t.Fatalf("ask mode allow: %v", err)
	}
	if asked.Permission != PermissionContentGuard {
		t.Fatalf("permission = %q", asked.Permission)
	}
	if len(asked.Patterns) != 1 || asked.Patterns[0] != "creds.env" {
		t.Fatalf("patterns = %v", asked.Patterns)
	}
}

func TestCheckContentGuardAskRejected(t *testing.T) {
	t.Parallel()
	tc := &Context{
		WorkDir:      t.TempDir(),
		ContentGuard: ContentGuardSettings{Mode: ContentGuardModeDefault},
		Ask: func(context.Context, AskRequest) error {
			return errors.New("user rejected")
		},
	}
	// Dangerous sink is ask under default.
	err := checkContentGuard(context.Background(), tc, "x.py", "eval(cmd)\n")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("want ask rejection, got %v", err)
	}
}

func TestCheckContentGuardPathAllow(t *testing.T) {
	t.Parallel()
	tc := allowAll(t.TempDir())
	tc.ContentGuard = ContentGuardSettings{
		PathAllow: []string{"**/testdata/**", "fixtures/*"},
	}
	if err := checkContentGuard(context.Background(), tc, "testdata/keys/sample.pem",
		"-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n"); err != nil {
		t.Fatalf("pathAllow: %v", err)
	}
	if err := checkContentGuard(context.Background(), tc, "src/main.go",
		"-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n"); err == nil {
		t.Fatal("non-allow path should deny")
	}
}

func TestCheckContentGuardModeDenySinks(t *testing.T) {
	t.Parallel()
	tc := allowAll(t.TempDir())
	tc.ContentGuard = ContentGuardSettings{Mode: ContentGuardModeDeny}
	err := checkContentGuard(context.Background(), tc, "x.py", "os.system(cmd)\n")
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("deny mode sink: %v", err)
	}
}

func TestWriteToolContentGuardDeniesPEM(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Z3VS5JJcds3xfn\n-----END RSA PRIVATE KEY-----\n"
	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "key.pem",
		"content":  pem,
	}), tc)
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("write pem: code=%q err=%v", CodeOf(err), err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "key.pem")); !os.IsNotExist(statErr) {
		t.Fatal("file must not be written")
	}
}

func TestWriteToolContentGuardDeniesAWS(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "aws.txt",
		"content":  "access_key=AKIAIOSFODNN7EXAMPLE\n",
	}), tc)
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("write aws: %v", err)
	}
}

func TestWriteToolAllowsCleanContent(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "ok.go",
		"content":  "package main\n",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEditToolContentGuardOnResultingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.txt")
	// File has no secret; edit introduces AWS key only in resulting content.
	if err := os.WriteFile(path, []byte("token=placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	_, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "cfg.txt",
		"oldString": "placeholder",
		"newString": "AKIAIOSFODNN7EXAMPLE",
	}), tc)
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("edit resulting content: code=%q err=%v", CodeOf(err), err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "AKIA") {
		t.Fatalf("file mutated despite guard: %s", got)
	}
}

func TestApplyPatchContentGuardOnResultingFile(t *testing.T) {
	dir := t.TempDir()
	// Patch only adds a comment-looking line that is actually an AWS key —
	// guard must evaluate resulting file content.
	patch := "*** Begin Patch\n*** Add File: leaked.txt\n+# note AKIAIOSFODNN7EXAMPLE\n*** End Patch\n"
	tc := allowAll(dir)
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{
		"patch": patch,
	}), tc)
	if CodeOf(err) != string(CodeContentGuardDenied) {
		t.Fatalf("apply_patch: code=%q err=%v", CodeOf(err), err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "leaked.txt")); !os.IsNotExist(statErr) {
		t.Fatal("patch must not commit")
	}
}

func TestContentGuardPathAllowWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	tc.ContentGuard = ContentGuardSettings{PathAllow: []string{"**/testdata/**"}}
	pem := "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n"
	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "testdata/sample.pem",
		"content":  pem,
	}), tc)
	if err != nil {
		t.Fatalf("pathAllow write: %v", err)
	}
}

func TestNormalizeContentGuardMode(t *testing.T) {
	t.Parallel()
	if got := NormalizeContentGuardMode(""); got != ContentGuardModeDefault {
		t.Fatalf("empty = %q", got)
	}
	if got := NormalizeContentGuardMode("DENY"); got != ContentGuardModeDeny {
		t.Fatalf("DENY = %q", got)
	}
	if got := NormalizeContentGuardMode("nope"); got != ContentGuardModeDefault {
		t.Fatalf("unknown = %q", got)
	}
}

func TestDecideRetryContentGuardDenied(t *testing.T) {
	t.Parallel()
	if d := DecideRetry(CodeContentGuardDenied, IdempotencyConditional); d != DecisionFail {
		t.Fatalf("decision = %s", d)
	}
}
