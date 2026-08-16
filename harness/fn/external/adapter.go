package external

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Adapter starts one harness process and returns its duplex protocol pipe.
type Adapter interface {
	Start(context.Context) (Pipe, error)
}

// Pipe is the language-neutral subprocess boundary used by the JSONL runner.
type Pipe interface {
	io.Reader
	io.Writer
	CloseWrite() error
	Wait() error
	Kill() error
}

type Config struct {
	Command string
	Args    []string
	Env     map[string]string
}

type commandAdapter struct {
	cfg Config
}

// Command returns an adapter that starts the configured executable.
func Command(cfg Config) (Adapter, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("external harness: command is required")
	}
	return commandAdapter{cfg: cfg}, nil
}

func (a commandAdapter) Start(ctx context.Context) (Pipe, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(a.cfg.Command, a.cfg.Args...)
	configureHarnessCmd(cmd)
	cmd.Env = os.Environ()
	for k, v := range a.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &commandPipe{Reader: stdout, Writer: stdin, stdin: stdin, cmd: cmd}, nil
}

type commandPipe struct {
	io.Reader
	io.Writer
	stdin io.Closer
	cmd   *exec.Cmd
}

func (p *commandPipe) CloseWrite() error { return p.stdin.Close() }
func (p *commandPipe) Wait() error       { return p.cmd.Wait() }
func (p *commandPipe) Kill() error       { return killHarnessProcess(p.cmd) }
