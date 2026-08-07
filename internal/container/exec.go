package container

import (
	"bytes"
	"context"
	"os/exec"
)

// ExecFunc runs a single container-engine CLI invocation.
// name is the binary (e.g. "docker", "podman", or an absolute path); args are
// the remaining argv. On a non-zero process exit, err is nil and code is set
// (callers inspect code). err is reserved for launch/IO failures.
//
// Tests inject fakes; production uses DefaultExecFunc.
type ExecFunc func(ctx context.Context, name string, args ...string) (stdout, stderr string, code int, err error)

// DefaultExecFunc runs name with args via os/exec.CommandContext.
func DefaultExecFunc(ctx context.Context, name string, args ...string) (stdout, stderr string, code int, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if runErr == nil {
		return stdout, stderr, 0, nil
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		return stdout, stderr, ee.ExitCode(), nil
	}
	return stdout, stderr, -1, runErr
}
