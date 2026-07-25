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

const processDefaultMaxOutput = 30000

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
	// Combine merges stdout and stderr into one ordered stream (bash-style).
	// ProcessObserver.Output still labels chunks stdout/stderr when separate;
	// when Combine is true every chunk is labeled stdout.
	Combine bool
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
	Status    ProcessStatus
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

	cmd := exec.CommandContext(runCtx, spec.Argv[0], spec.Argv[1:]...)
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
		cmd.Stdout = stdoutW
		cmd.Stderr = stderrW
	}

	if err := cmd.Start(); err != nil {
		return ProcessResult{ID: id, Status: ProcessStatusError}, err
	}
	if obs.Started != nil {
		obs.Started(id, append([]string(nil), spec.Argv...))
	}

	waitErr := cmd.Wait()
	res := ProcessResult{ID: id, Status: ProcessStatusExited}
	if spec.Combine {
		res.Output, res.BytesSeen, res.Truncated = combinedW.result()
		res.Stdout = res.Output
	} else {
		out, outSeen, outTrunc := stdoutW.result()
		errOut, errSeen, errTrunc := stderrW.result()
		res.Stdout = out
		res.Stderr = errOut
		res.Output = out + errOut
		res.BytesSeen = outSeen + errSeen
		res.Truncated = outTrunc || errTrunc
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
}

func newProcessStreamWriter(max int, id, stream string, report func(id, stream, data string)) *processStreamWriter {
	return &processStreamWriter{max: max, id: id, stream: stream, report: report}
}

func (w *processStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.seen += n
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

// ensure processStreamWriter satisfies io.Writer
var _ io.Writer = (*processStreamWriter)(nil)
