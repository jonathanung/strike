package engine

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/harness/verify"
	"github.com/jonathanung/strike/pkg/protocol"
)

// runSoloVerification executes Options.Verify gates for a claimed solo/root
// turn. Emits verification.started/completed for timeline audit. Returns a
// non-nil report whenever gates were configured. ctx should be the turn
// context so cancel/timeout aborts hung cmd gates.
func (e *Engine) runSoloVerification(ctx context.Context, corr protocol.Correlation) *protocol.VerificationReport {
	if e == nil || len(e.opts.Verify) == 0 {
		return nil
	}
	workDir := e.opts.WorkDir
	modelID := strings.TrimSpace(e.model)
	if modelID == "" {
		modelID = strings.TrimSpace(e.opts.InitialModel)
	}
	return e.runVerification(ctx, corr, protocol.VerificationScopeTurn, e.opts.Verify, verify.Input{
		Claimed: true,
		Env: verify.EnvMetadata{
			WorkDir:   workDir,
			SessionID: e.opts.SessionID,
			ModelID:   modelID,
		},
	}, workDir)
}

// runChildVerification executes spawn-time gates against the claimed handoff.
// Returns a non-nil report whenever gates were configured.
func (e *Engine) runChildVerification(h *childHandle, child *Engine, handoff protocol.CompletionHandoff) *protocol.VerificationReport {
	if h == nil || len(h.gates) == 0 {
		return nil
	}
	workDir := ""
	modelID := ""
	if e != nil {
		workDir = e.opts.WorkDir
	}
	if child != nil {
		if child.opts.WorkDir != "" {
			workDir = child.opts.WorkDir
		}
		modelID = strings.TrimSpace(child.model)
		if modelID == "" {
			modelID = strings.TrimSpace(child.opts.InitialModel)
		}
	}

	hv := &verify.HandoffView{
		Summary:       handoff.Summary,
		Incomplete:    handoff.Incomplete,
		HasStructured: !handoff.Incomplete,
	}
	if handoff.Incomplete {
		hv.HasStructured = false
	}

	corr := protocol.Correlation{SessionID: h.id}
	if e != nil {
		corr.ParentSessionID = e.opts.SessionID
	}
	if child != nil {
		corr.Depth = child.opts.Depth
	}
	// Child exit already canceled the child turn ctx; use a detached budget.
	return e.runVerification(context.Background(), corr, protocol.VerificationScopeChild, h.gates, verify.Input{
		Claimed: true,
		Handoff: hv,
		Env: verify.EnvMetadata{
			WorkDir:   workDir,
			SessionID: h.id,
			ModelID:   modelID,
		},
	}, workDir)
}

// runVerification is the shared gate runner for solo and child paths.
// Emits verification.started before gates and verification.completed after.
// parent is the cancel scope (turn ctx for solo; Background for child exit).
func (e *Engine) runVerification(parent context.Context, corr protocol.Correlation, scope string, gates []tool.VerifyGate, in verify.Input, workDir string) *protocol.VerificationReport {
	if len(gates) == 0 {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	vgates := make([]verify.Gate, 0, len(gates))
	for _, g := range gates {
		vgates = append(vgates, verify.Gate{
			Kind:        g.Kind,
			Value:       g.Value,
			Description: g.Description,
		})
	}

	if e != nil {
		e.emit(protocol.VerificationStarted{
			Correlation: corr,
			Scope:       scope,
			GateCount:   len(vgates),
		})
		e.fireVerificationGate(parent, corr, scope, "started", "gates="+itoaSafe(len(vgates)))
	}

	// Bound total gate wall time; parent cancel (solo turn interrupt) aborts early.
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()

	runner := &verify.Runner{WorkDir: workDir}
	if e != nil {
		runner.Triage = &e.verifyTriage
	}
	if in.Env.WorkDir == "" {
		in.Env.WorkDir = workDir
	}
	res := runner.Run(ctx, vgates, in)
	rep := verifyResultToProtocol(res)

	if e != nil {
		e.emit(protocol.VerificationCompleted{
			Correlation: corr,
			Scope:       scope,
			Report:      rep,
		})
		st := "failed"
		if rep.Passed {
			st = "passed"
		}
		e.fireVerificationGate(ctx, corr, scope, st, "passed="+boolStr(rep.Passed))
	}
	return &rep
}

func itoaSafe(n int) string {
	return strconv.Itoa(n)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func verifyResultToProtocol(res verify.Result) protocol.VerificationReport {
	checks := make([]protocol.VerificationCheck, 0, len(res.Checks))
	for _, c := range res.Checks {
		checks = append(checks, protocol.VerificationCheck{
			Name:       c.Name,
			Kind:       c.Kind,
			Value:      c.Value,
			Passed:     c.Passed,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Error:      c.Error,
			DurationMs: c.DurationMs,
			Triage:     triageToProtocol(c.Triage),
		})
	}
	if checks == nil {
		checks = []protocol.VerificationCheck{}
	}
	return protocol.VerificationReport{
		Passed:   res.Passed,
		Claimed:  res.Claimed,
		Verified: res.Verified,
		Checks:   checks,
		Env: protocol.VerificationEnv{
			WorkDir:    res.Env.WorkDir,
			SessionID:  res.Env.SessionID,
			WorktreeID: res.Env.WorktreeID,
			ModelID:    res.Env.ModelID,
			StartedAt:  res.Env.StartedAt,
			FinishedAt: res.Env.FinishedAt,
		},
		Summary:    res.Summary,
		DurationMs: res.DurationMs,
	}
}

func protocolReportToVerifyResult(rep *protocol.VerificationReport) verify.Result {
	if rep == nil {
		return verify.Result{}
	}
	checks := make([]verify.CheckResult, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		checks = append(checks, verify.CheckResult{
			Name:       c.Name,
			Kind:       c.Kind,
			Value:      c.Value,
			Passed:     c.Passed,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Error:      c.Error,
			DurationMs: c.DurationMs,
			Triage:     triageFromProtocol(c.Triage),
		})
	}
	return verify.Result{
		Passed:   rep.Passed,
		Claimed:  rep.Claimed,
		Verified: rep.Verified,
		Checks:   checks,
		Env: verify.EnvMetadata{
			WorkDir:    rep.Env.WorkDir,
			SessionID:  rep.Env.SessionID,
			WorktreeID: rep.Env.WorktreeID,
			ModelID:    rep.Env.ModelID,
			StartedAt:  rep.Env.StartedAt,
			FinishedAt: rep.Env.FinishedAt,
		},
		Summary:    rep.Summary,
		DurationMs: rep.DurationMs,
	}
}

func triageToProtocol(t *verify.TriageReport) *protocol.VerificationTriage {
	if t == nil {
		return nil
	}
	out := &protocol.VerificationTriage{
		FocusedCommand:  t.FocusedCommand,
		OriginalOutput:  t.OriginalOutput,
		FullGateSkipped: t.FullGateSkipped,
		Exhausted:       t.Exhausted,
	}
	for _, f := range t.Failures {
		out.Failures = append(out.Failures, protocol.VerificationFailure{
			Package: f.Package, Dir: f.Dir, Tests: append([]string(nil), f.Tests...), Source: f.Source,
		})
	}
	for _, a := range t.Attempts {
		out.Attempts = append(out.Attempts, protocol.VerificationAttempt{
			Command: a.Command, Passed: a.Passed, ExitCode: a.ExitCode, Output: a.Output, Error: a.Error,
		})
	}
	return out
}

func triageFromProtocol(t *protocol.VerificationTriage) *verify.TriageReport {
	if t == nil {
		return nil
	}
	out := &verify.TriageReport{
		FocusedCommand:  t.FocusedCommand,
		OriginalOutput:  t.OriginalOutput,
		FullGateSkipped: t.FullGateSkipped,
		Exhausted:       t.Exhausted,
	}
	for _, f := range t.Failures {
		out.Failures = append(out.Failures, verify.Failure{
			Package: f.Package, Dir: f.Dir, Tests: append([]string(nil), f.Tests...), Source: f.Source,
		})
	}
	for _, a := range t.Attempts {
		out.Attempts = append(out.Attempts, verify.TriageAttempt{
			Command: a.Command, Passed: a.Passed, ExitCode: a.ExitCode, Output: a.Output, Error: a.Error,
		})
	}
	return out
}
