package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jonathanung/strike-cli/internal/mcp"
	"github.com/jonathanung/strike-cli/internal/version"
)

const mcpServeUsage = `Expose strike as an MCP server (stdio JSON-RPC) for hosts like Claude Code or Codex.

Usage:
  strike mcp-serve [options]

The server advertises a single tool, strike_task, that runs one headless strike
turn and returns the assistant summary. MCP wire traffic uses stdout; logs and
diagnostics go to stderr.

Options:
  --provider <name>      provider (anthropic|openai|xai|google|kimi|deepseek|echo)
  --model <id>           model id; overrides config
  --effort <level>       reasoning effort (off|low|medium|high|xhigh|max)
  --sandbox <mode>       OS process sandbox for bash (off|read-only|workspace-write)
  --i-know               allow permissionMode yolo when sandbox is off
  --auto, --dangerously-skip-permissions
                         auto-allow permission asks (required for tools that would prompt)
  -h, --help             show help

Example (Claude Code / Codex mcp.json):
  {
    "mcpServers": {
      "strike": {
        "command": "strike",
        "args": ["mcp-serve", "--provider", "echo", "--auto"]
      }
    }
  }

Permission and question prompts cannot be answered interactively over MCP; asks
are rejected unless --auto or --dangerously-skip-permissions is set
(configured/agent denies still apply).`

const (
	strikeTaskToolName = "strike_task"
	strikeTaskToolDesc = `Delegate a coding task to the strike agent.

Runs one headless strike turn with the given prompt and returns the assistant
summary text. Use for scoped work you want strike to execute in the workspace
(cwd of the mcp-serve process). Optional model/effort override the server
defaults for this call only.`
	strikeTaskInputSchema = `{
		"type": "object",
		"properties": {
			"prompt": {
				"type": "string",
				"description": "Task instructions for the strike agent"
			},
			"model": {
				"type": "string",
				"description": "Optional model id override for this call"
			},
			"effort": {
				"type": "string",
				"description": "Optional reasoning effort: off, low, medium, high, xhigh, or max"
			}
		},
		"required": ["prompt"]
	}`
)

type strikeTaskArgs struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

func runMCPServeCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, err := parseMCPServeArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, mcpServeUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, mcpServeUsage)
		return 2
	}
	if err := runMCPServe(opts, stdin, stdout, stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

func parseMCPServeArgs(args []string) (cliOptions, error) {
	var opts cliOptions
	fs := flag.NewFlagSet("strike mcp-serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.provider, "provider", "", "")
	fs.StringVar(&opts.model, "model", "", "")
	fs.StringVar(&opts.effort, "effort", "", "")
	fs.StringVar(&opts.sandbox, "sandbox", "", "")
	fs.BoolVar(&opts.iKnow, "i-know", false, "")
	fs.BoolVar(&opts.dangerouslySkipPermissions, "auto", false, "")
	fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if fs.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "provider" && opts.provider != "" {
			opts.providerSet = true
		}
	})
	opts.provider = strings.TrimSpace(opts.provider)
	opts.model = strings.TrimSpace(opts.model)
	opts.effort = strings.TrimSpace(opts.effort)
	opts.sandbox = strings.TrimSpace(opts.sandbox)
	if opts.sandbox != "" {
		if _, err := parseSandboxFlag(opts.sandbox); err != nil {
			return cliOptions{}, err
		}
	}
	return opts, nil
}

func runMCPServe(opts cliOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &mcp.Server{
		Name:    "strike",
		Version: version.Version,
		Tools: []mcp.ServerTool{{
			Name:        strikeTaskToolName,
			Description: strikeTaskToolDesc,
			InputSchema: json.RawMessage(strikeTaskInputSchema),
			Handle:      strikeTaskHandler(opts, stderr),
		}},
	}
	return srv.Serve(ctx, stdin, stdout)
}

// strikeTaskHandler runs one headless strike turn per tools/call.
func strikeTaskHandler(opts cliOptions, stderr io.Writer) mcp.ToolHandler {
	return func(callCtx context.Context, raw json.RawMessage) (string, bool, error) {
		var a strikeTaskArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return "invalid arguments: " + err.Error(), true, nil
		}
		prompt := strings.TrimSpace(a.Prompt)
		if prompt == "" {
			return "prompt is empty", true, nil
		}
		callOpts := opts
		if m := strings.TrimSpace(a.Model); m != "" {
			callOpts.model = m
		}
		if e := strings.TrimSpace(a.Effort); e != "" {
			callOpts.effort = e
		}

		type outcome struct {
			text string
			err  error
		}
		ch := make(chan outcome, 1)
		go func() {
			var buf bytes.Buffer
			err := runExec(callOpts, prompt, &buf, stderr)
			ch <- outcome{text: buf.String(), err: err}
		}()
		select {
		case <-callCtx.Done():
			return "", false, callCtx.Err()
		case out := <-ch:
			if out.err != nil {
				msg := out.err.Error()
				if strings.TrimSpace(out.text) != "" {
					msg = strings.TrimRight(out.text, "\n") + "\n" + msg
				}
				return msg, true, nil
			}
			text := strings.TrimRight(out.text, "\n")
			if text == "" {
				text = "(no assistant text)"
			}
			return text, false, nil
		}
	}
}
