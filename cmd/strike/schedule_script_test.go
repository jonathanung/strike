package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/schedule"
	"github.com/jonathanung/strike/internal/schedule/scriptrun"
	"github.com/jonathanung/strike/internal/trust/audit"
)

func TestScriptPolicyFromConfig(t *testing.T) {
	cwd := t.TempDir()
	tokenFile := filepath.Join(cwd, "serve.token")
	deny := permission.Rule{Permission: "bash", Pattern: "touch marker", Action: permission.Deny}
	cases := []struct {
		name       string
		cfg        config.Config
		tokenFile  string
		wantLocked string
	}{
		{name: "plain config", cfg: config.Config{Permissions: permission.Ruleset{deny}}},
		{name: "token file is denied", cfg: config.Config{Permissions: permission.Ruleset{deny}}, tokenFile: tokenFile},
		{
			name: "managed permission mode locks the mode",
			cfg: config.Config{
				Permissions:    permission.Ruleset{deny},
				PermissionMode: protocol.PermissionModePlan,
				Managed:        config.ManagedInfo{PermissionMode: true},
			},
			wantLocked: "plan",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pol := scriptPolicyFromConfig(tc.cfg, "workspace-write", tc.tokenFile, cwd)
			// --auto / --dangerously-skip-permissions never reach a script:
			// the layers are the unattended ones, with no allow-all layer.
			want := permissionLayersWithPreset(tc.cfg.Permissions, tc.cfg.PermissionPreset, false)
			if len(pol.Rules) != len(want) {
				t.Fatalf("layers %d, want %d", len(pol.Rules), len(want))
			}
			for _, layer := range pol.Rules {
				for _, r := range layer {
					if r.Permission == "*" && r.Pattern == "*" && r.Action == permission.Allow {
						t.Fatal("script policy carries the allow-all bypass layer")
					}
				}
			}
			if pol.SandboxMode != sandbox.ResolveMode("workspace-write") {
				t.Fatalf("sandbox dial %v", pol.SandboxMode)
			}
			if pol.LockedMode != tc.wantLocked {
				t.Fatalf("locked mode %q", pol.LockedMode)
			}
			wantManaged := len(tokenFileDenyRules(tc.tokenFile, cwd))
			if len(pol.Managed) != wantManaged || (tc.tokenFile != "" && wantManaged == 0) {
				t.Fatalf("managed rules %d, want %d", len(pol.Managed), wantManaged)
			}

			// The config deny refuses the run even in yolo.
			r := &scriptrun.Runner{Policy: func(string) (scriptrun.Policy, error) { return pol, nil }}
			dir := t.TempDir()
			res := r.Run(context.Background(), scriptrun.Request{Command: "touch marker", Dir: dir, Timeout: 10 * time.Second, PermissionMode: "yolo"}, nil)
			if res.Status != scriptrun.StatusDenied || res.Decision != scriptrun.DecisionDeny {
				t.Fatalf("result %+v", res)
			}
		})
	}
}

func TestScriptHookAdaptsRunner(t *testing.T) {
	dir := t.TempDir()
	r := &scriptrun.Runner{Policy: func(string) (scriptrun.Policy, error) {
		return scriptPolicyFromConfig(config.Config{}, "off", "", dir), nil
	}}
	var starts []schedule.ScriptStart
	out := scriptHook(r)(context.Background(), schedule.ScriptRequest{
		Command: "echo hi", Dir: dir, Timeout: 10 * time.Second, PermissionMode: "default",
	}, func(st schedule.ScriptStart) { starts = append(starts, st) })
	if out.Status != schedule.StatusFinished || out.Decision != schedule.DecisionArmed || string(out.Output) != "hi\n" ||
		out.ExitCode == nil || *out.ExitCode != 0 || out.StartedAt.IsZero() || out.EndedAt.IsZero() {
		t.Fatalf("result %+v", out)
	}
	if len(starts) != 1 || starts[0].Decision != schedule.DecisionArmed || starts[0].Sandbox != "off" {
		t.Fatalf("starts %+v", starts)
	}
}

func TestScriptAuditHookWritesScrubbedRecord(t *testing.T) {
	dir := t.TempDir()
	sink, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	secret := "AKIAABCDEFGHIJKLMNOP"
	code := 1
	scriptAuditHook(sink)(schedule.ScriptAudit{
		ScheduleID: "sch_1", RunID: "run_1", Status: schedule.StatusFailed, Decision: schedule.DecisionArmed,
		Command: "deploy --key " + secret, CommandHash: schedule.HashCommand("deploy --key " + secret),
		Dir: dir, ExitCode: &code, Timeout: "30m", OutputTail: "using " + secret, Error: "exit code 1 " + secret,
	})
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(path)
			all.Write(data)
		}
		return nil
	})
	text := all.String()
	if !strings.Contains(text, audit.FamilyScriptRun) || !strings.Contains(text, "run_1") || !strings.Contains(text, schedule.HashCommand("deploy --key "+secret)) {
		t.Fatalf("script_run record missing: %s", text)
	}
	if strings.Contains(text, secret) {
		t.Fatal("script_run record leaked a credential-shaped token")
	}
}

func TestServeAuditSinkIsRetentionBounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		name       string
		cfg        config.Config
		wantEvents int
		wantAge    time.Duration
	}{
		{name: "defaults", cfg: serveAuditConfig(nil), wantEvents: 10000, wantAge: 90 * 24 * time.Hour},
		{
			name:       "configured",
			cfg:        config.Config{Session: config.SessionConfig{AuditRetentionMaxEvents: 50, AuditRetentionMaxAgeDays: 2}},
			wantEvents: 50, wantAge: 48 * time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := auditOptions(tc.cfg)
			if opts.Retention.MaxEvents != tc.wantEvents || opts.Retention.MaxAge != tc.wantAge {
				t.Fatalf("retention %+v", opts.Retention)
			}
		})
	}
	a := &assembled{cfg: config.Config{Session: config.SessionConfig{AuditRetentionMaxEvents: 7}}}
	if got := serveAuditConfig(a); got.Session.AuditRetentionMaxEvents != 7 {
		t.Fatalf("serve audit config ignored the assembled config: %+v", got.Session)
	}
}
