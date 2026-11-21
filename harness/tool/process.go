package tool

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/jonathanung/strike/harness/fault"
	"github.com/jonathanung/strike/harness/sandbox"
)

// Process stream labels (match protocol.ProcessStream*).
const (
	ProcessStreamStdout = "stdout"
	ProcessStreamStderr = "stderr"
)

// ProcessStatus is the terminal outcome of RunProcess.
type ProcessStatus string

const (
	ProcessStatusExited   ProcessStatus = "exited"
	ProcessStatusTimeout  ProcessStatus = "timeout"
	ProcessStatusCanceled ProcessStatus = "canceled"
	ProcessStatusError    ProcessStatus = "error"
)

// processDefaultMaxOutput caps retained stdout+stderr when MaxOutput is unset.
// Kept in lockstep with bashMaxOutput for consistent shell/tool process budgets.
const processDefaultMaxOutput = 16_000

// ProcessLimits are optional OS resource caps applied to the child process.
// Zero fields are omitted. Enforcement is platform-specific (see
// applyProcessResourceLimits); unsupported platforms leave limits unset.
type ProcessLimits struct {
	// MemoryBytes, when > 0, caps address-space (RLIMIT_AS) on Linux.
	// Other platforms document the limit as unsupported (no-op).
	MemoryBytes uint64
	// CPUSeconds, when > 0, caps CPU time (RLIMIT_CPU) on Linux.
	// Other platforms document the limit as unsupported (no-op).
	CPUSeconds uint64
}

// ProcessSpec describes a bounded subprocess.
type ProcessSpec struct {
	Argv []string
	Dir  string
	// Env, when non-nil, replaces the inherited environment entirely.
	Env []string
	// Stdin is written to the process then closed (hooks send event JSON here).
	Stdin []byte
	// Timeout bounds the run independently of ctx; zero means ctx-only.
	Timeout time.Duration
	// MaxOutput caps retained stdout+stderr bytes (each stream shares the budget
	// when separate; combined mode uses one budget). Zero uses the default.
	MaxOutput int
	// SpillMax, when > 0, additionally retains up to this many bytes of the
	// full stream (not reported to observers) in ProcessResult.Full so callers
	// can spill overflow past MaxOutput to a file (#1199). Zero discards it.
	SpillMax int
	// TailMax, when > 0, additionally keeps the last TailMax bytes of the
	// stream (not reported to observers) in ProcessResult.Tail. MaxOutput and
	// SpillMax keep the head; a scheduled script wants the end, where the
	// failing assertion is. Split streams share the budget like MaxOutput.
	// Zero (the bash tool) keeps no tail.
	TailMax int
	// Combine merges stdout and stderr into one ordered stream (bash-style).
	// ProcessObserver.Output still labels chunks stdout/stderr when separate;
	// when Combine is true every chunk is labeled stdout.
	Combine bool
	// Sandbox, when Mode is non-off, prefixes Argv with the OS sandbox launcher
	// (bwrap / sandbox-exec). Observer.Started still receives the original Argv.
	// Zero value leaves the process unsandboxed (hooks, probes).
	Sandbox sandbox.Policy
	// Limits are optional OS resource caps (memory/CPU). Wall time uses Timeout.
	// Applied to the direct child PID after Start (Linux prlimit). When an OS
	// sandbox launcher is the direct child, the inner command may already have
	// forked — prefer Limits on unsandboxed runs, or wall-time Timeout under
	// sandbox. See docs/isolation.md.
	Limits ProcessLimits
}

// ProcessResult is the terminal outcome of RunProcess.
type ProcessResult struct {
	ID       string
	ExitCode int
	// Output is combined stdout+stderr when Combine, else stdout then stderr.
	Output string
	Stdout string
	Stderr string
	// BytesSeen is the total bytes observed before truncation.
	BytesSeen int
	Truncated bool
	// Full is the full stream (capped at ProcessSpec.SpillMax) when Truncated
	// and SpillMax > 0; empty otherwise.
	Full string
	// Tail is the last ProcessSpec.TailMax bytes of the stream (stdout tail
	// then stderr tail when split); empty when TailMax is zero.
	Tail   string
	Status ProcessStatus
	// SandboxApplied is true when an OS sandbox launcher prefixed the exec argv.
	SandboxApplied bool
	// SandboxBackend is "bwrap", "sandbox-exec", or empty when not applied.
	SandboxBackend string
	// SandboxDegraded is true when a sandbox was requested but not applied.
	SandboxDegraded bool
}

// ProcessObserver receives lifecycle notifications while a subprocess runs.
// Nil fields are skipped. Callbacks must not block indefinitely (engine emit
// is OK; avoid nested process runs).
type ProcessObserver struct {
	Started func(id string, argv []string)
	Output  func(id, stream, data string)
	Exited  func(id string, exitCode int, status ProcessStatus)
}

// RunProcess executes argv with cancel/timeout bounds, optional stdin, and
// streaming observer callbacks. Non-zero exits are not errors — inspect
// ProcessResult.ExitCode / Status. Start failures and I/O setup errors return err.
func RunProcess(ctx context.Context, spec ProcessSpec, obs ProcessObserver) (ProcessResult, error) {
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return ProcessResult{}, fmt.Errorf("empty argv")
	}
	maxOut := spec.MaxOutput
	if maxOut <= 0 {
		maxOut = processDefaultMaxOutput
	}
	id := rand.Text()
	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	// Apply OS sandbox at the exec seam (process_unix SysProcAttr still owns
	// process-group kill). Started observers see the pre-wrap argv.
	wrap := sandbox.WrapResult(spec.Argv, spec.Sandbox)
	// Fail-closed: non-off sandbox that could not be applied must not run
	// unsandboxed unless Policy.AllowDegrade is set (#1030).
	if wrap.Degraded && spec.Sandbox.Mode != sandbox.ModeOff && !spec.Sandbox.AllowDegrade {
		return ProcessResult{
			ID:              id,
			Status:          ProcessStatusError,
			SandboxDegraded: true,
		}, ErrSandboxDenied("OS sandbox required but unavailable (set sandboxAllowDegrade: true to permit unsandboxed fallback, or sandbox: off)")
	}
	execArgv := wrap.Argv
	if len(execArgv) == 0 || execArgv[0] == "" {
		return ProcessResult{}, fmt.Errorf("empty argv")
	}
	cmd := exec.CommandContext(runCtx, execArgv[0], execArgv[1:]...)
	configureProcessCmd(cmd)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}

	var (
		stdoutW, stderrW *processStreamWriter
		combinedW        *processStreamWriter
	)
	if spec.Combine {
		combinedW = newProcessStreamWriter(maxOut, id, ProcessStreamStdout, obs.Output)
		combinedW.spillMax = spec.SpillMax
		combinedW.tailMax = spec.TailMax
		cmd.Stdout = combinedW
		cmd.Stderr = combinedW
	} else {
		// Split the byte budget evenly so one noisy stream cannot starve the other.
		half := maxOut / 2
		if half < 1 {
			half = maxOut
		}
		stdoutW = newProcessStreamWriter(half, id, ProcessStreamStdout, obs.Output)
		stderrW = newProcessStreamWriter(maxOut-half, id, ProcessStreamStderr, obs.Output)
		stdoutW.spillMax = spec.SpillMax / 2
		stderrW.spillMax = spec.SpillMax - spec.SpillMax/2
		stdoutW.tailMax = spec.TailMax / 2
		stderrW.tailMax = spec.TailMax - spec.TailMax/2
		cmd.Stdout = stdoutW
		cmd.Stderr = stderrW
	}

	if err := cmd.Start(); err != nil {
		return ProcessResult{ID: id, Status: ProcessStatusError}, err
	}
	// Best-effort resource caps after Start (Linux prlimit). Failures are
	// non-fatal so a missing capability does not abort an otherwise healthy run.
	if cmd.Process != nil {
		_ = applyProcessResourceLimits(cmd.Process.Pid, spec.Limits)
	}
	if obs.Started != nil {
		obs.Started(id, append([]string(nil), spec.Argv...))
	}

	// Chaos: kill the tree immediately after start (mid-run crash). Safe
	// outcome is canceled status with Wait reaped — never hang (docs/chaos.md).
	injectedKill := false
	if ferr := fault.Check(fault.ProcessAfterStart); ferr != nil {
		injectedKill = true
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		} else if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	waitErr := cmd.Wait()
	res := ProcessResult{
		ID:              id,
		Status:          ProcessStatusExited,
		SandboxApplied:  wrap.Applied,
		SandboxBackend:  wrap.Backend,
		SandboxDegraded: wrap.Degraded,
	}
	if spec.Combine {
		res.Output, res.BytesSeen, res.Truncated = combinedW.result()
		res.Stdout = res.Output
		if res.Truncated {
			res.Full = combinedW.spilled()
		}
		res.Tail = combinedW.tailString()
	} else {
		out, outSeen, outTrunc := stdoutW.result()
		errOut, errSeen, errTrunc := stderrW.result()
		res.Stdout = out
		res.Stderr = errOut
		res.Output = out + errOut
		res.BytesSeen = outSeen + errSeen
		res.Truncated = outTrunc || errTrunc
		res.Tail = stdoutW.tailString() + stderrW.tailString()
		if res.Truncated {
			if full := stdoutW.spilled() + stderrW.spilled(); full != "" {
				res.Full = full
			}
		}
	}
	if injectedKill {
		res.ExitCode = -1
		res.Status = ProcessStatusCanceled
		if obs.Exited != nil {
			obs.Exited(id, res.ExitCode, res.Status)
		}
		return res, nil
	}

	if waitErr != nil {
		res.ExitCode = -1
		if cmd.ProcessState != nil {
			res.ExitCode = cmd.ProcessState.ExitCode()
		}
		switch {
		case runCtx.Err() == context.DeadlineExceeded:
			res.Status = ProcessStatusTimeout
		case errors.Is(runCtx.Err(), context.Canceled) || errors.Is(waitErr, context.Canceled):
			res.Status = ProcessStatusCanceled
		case res.ExitCode < 0:
			res.Status = ProcessStatusError
			if obs.Exited != nil {
				obs.Exited(id, res.ExitCode, res.Status)
			}
			return res, waitErr
		default:
			// Non-zero exit is a normal ProcessResult.
			res.Status = ProcessStatusExited
		}
	}

	if obs.Exited != nil {
		obs.Exited(id, res.ExitCode, res.Status)
	}
	return res, nil
}

type processStreamWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	total  int
	seen   int
	max    int
	id     string
	stream string
	report func(id, stream, data string)
	// spill retains up to spillMax bytes of the full stream (0 = disabled).
	spill    bytes.Buffer
	spillMax int
	// tail keeps the last tailMax bytes of the stream (0 = disabled).
	tail    []byte
	tailMax int
}

func newProcessStreamWriter(max int, id, stream string, report func(id, stream, data string)) *processStreamWriter {
	return &processStreamWriter{max: max, id: id, stream: stream, report: report}
}

func (w *processStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.seen += n
	if room := w.spillMax - w.spill.Len(); room > 0 {
		w.spill.Write(p[:min(room, n)])
	}
	w.keepTail(p)
	if w.total >= w.max {
		return n, nil
	}
	chunk := p
	if w.total+len(chunk) > w.max {
		chunk = p[:w.max-w.total]
	}
	if len(chunk) == 0 {
		return n, nil
	}
	w.buf.Write(chunk)
	w.total += len(chunk)
	if w.report != nil {
		w.report(w.id, w.stream, string(chunk))
	}
	return n, nil
}

func (w *processStreamWriter) result() (string, int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String(), w.seen, w.seen > w.total
}

// keepTail appends p to the tail window and drops the front past tailMax.
// The window is compacted in place so memory stays at most 2*tailMax.
func (w *processStreamWriter) keepTail(p []byte) {
	if w.tailMax <= 0 || len(p) == 0 {
		return
	}
	if len(p) >= w.tailMax {
		w.tail = append(w.tail[:0], p[len(p)-w.tailMax:]...)
		return
	}
	if w.tail == nil {
		w.tail = make([]byte, 0, 2*w.tailMax)
	}
	if len(w.tail)+len(p) > w.tailMax {
		drop := len(w.tail) + len(p) - w.tailMax
		w.tail = w.tail[:copy(w.tail, w.tail[drop:])]
	}
	w.tail = append(w.tail, p...)
}

// tailString returns the retained tail (empty when TailMax is zero).
func (w *processStreamWriter) tailString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.tail)
}

// spilled returns the retained full stream (empty when spill is disabled).
func (w *processStreamWriter) spilled() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spill.String()
}

// ensure processStreamWriter satisfies io.Writer
var _ io.Writer = (*processStreamWriter)(nil)
