package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"

	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

// runComposerShell executes a composer ! command without starting a model
// turn. Permission goes through the live permission service (the same Ask
// path as the bash tool). The OS sandbox policy is the permission-compiled
// bash policy. Per-file Checkpoint snapshots stay unset so the shell does
// not race the turn's first-touch copies. CheckpointUncovered is the same
// MarkUncovered the bash tool uses: a no-op when no turn is active, and the
// signal /rewind files needs when the shell mutates the workspace mid-turn.
func (e *Engine) runComposerShell(ctx context.Context, display, command string) {
	if e == nil {
		return
	}
	corr := e.sessionCorr()
	e.emit(protocol.UserMessage{Correlation: corr, Text: display})
	e.maybeTitleSession(display)
	callID := "bang-" + rand.Text()
	policy := e.bashSandboxPolicy()
	network := sandbox.CloneNetworkAllow(e.opts.NetworkAllow)
	go e.execComposerShell(ctx, corr, callID, command, policy, network)
}

func (e *Engine) execComposerShell(ctx context.Context, corr protocol.Correlation, callID, command string, policy sandbox.Policy, network []string) {
	args, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		e.emit(protocol.ToolCallEnd{
			Correlation: corr,
			CallID:      callID,
			Title:       command,
			Output:      err.Error(),
			IsError:     true,
			ErrorCode:   protocol.ErrorCodeInvalidArgs,
		})
		return
	}
	e.emit(protocol.ToolCallBegin{
		Correlation: corr,
		CallID:      callID,
		Name:        "bash",
		Args:        redact.JSON(args),
	})
	tc := &tool.Context{
		WorkDir:             e.opts.WorkDir,
		SandboxMode:         e.opts.SandboxMode,
		Sandbox:             policy,
		NetworkAllow:        network,
		BashSecrets:         e.opts.BashSecrets,
		Scheduler:           e.opts.Scheduler,
		SchedulerPolicy:     e.opts.SchedulerPolicy,
		CheckpointUncovered: e.checkpoints.MarkUncovered,
		OnSecretRefUse: func(refClass, refHash, action string) {
			if e.opts.Audit == nil {
				return
			}
			_ = e.opts.Audit.RecordSecretRefUse(corr.SessionID, "", callID, refClass, refHash, action, "bash")
		},
		Ask: func(ctx context.Context, req tool.AskRequest) error {
			if e.perms == nil {
				return errPermsUnavailable
			}
			return e.perms.AskWithCorrelation(ctx, req, corr)
		},
	}
	res, execErr := tool.NewBash().Execute(ctx, args, tc)
	output, isErr := composerShellOutcome(res, execErr)
	e.emit(protocol.ToolCallEnd{
		Correlation: corr,
		CallID:      callID,
		Title:       command,
		Output:      redact.ScrubToolOutput(output),
		IsError:     isErr,
		ErrorCode:   composerShellCode(isErr, execErr),
		Metadata:    res.Metadata,
	})
}

var errPermsUnavailable = composerShellError("permission service unavailable")

type composerShellError string

func (e composerShellError) Error() string { return string(e) }

func composerShellOutcome(res tool.Result, err error) (string, bool) {
	output := res.Output
	isErr := err != nil
	if err != nil && strings.TrimSpace(output) == "" {
		output = err.Error()
	}
	if res.Metadata != nil {
		var meta struct {
			ExitCode int `json:"exitCode"`
		}
		if json.Unmarshal(res.Metadata, &meta) == nil && meta.ExitCode != 0 {
			isErr = true
		}
	}
	if strings.TrimSpace(output) == "" {
		output = "(no output)"
	}
	return output, isErr
}

func composerShellCode(isErr bool, err error) string {
	if !isErr || err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "permission"):
		return "permission_denied"
	case strings.Contains(msg, "sandbox"):
		return "sandbox_denied"
	default:
		return protocol.ErrorCodeInvalidArgs
	}
}
