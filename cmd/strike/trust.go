package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

const trustUsage = `Decide whether a workspace's own configuration may load (#1352).

Usage:
  strike trust [grant] [--dir <path>]   Trust the workspace (prints what that enables)
  strike trust status [--dir <path>]    Show trust state and the repo config it gates
  strike trust revoke [--dir <path>]    Revoke trust; repo config goes inert again
  strike trust list                     List trusted workspace roots
  strike trust help

Trust is keyed by workspace root (git top level when known) and stored in
~/.strike/workspace-trust.json — never inside the repository. Until a workspace
is trusted, its .strike/config, mcp.jsonc, LSP and harness commands, hooks,
agents, skills, workflows, project plugins and AGENTS.md/CLAUDE.md steering are
not read or started; sessions run on global + builtin config only.

Trusting a workspace only lets that config load. Every tool, MCP call and hook
still goes through the permission system unchanged. The home directory and the
filesystem root cannot be trusted. Takes effect on the next strike start.
`

func runTrustCLI(args []string, stdout, stderr io.Writer) int {
	sub := "grant"
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(stdout, trustUsage)
			return 0
		case "grant", "status", "revoke", "list":
			sub = args[0]
			args = args[1:]
		default:
			if !strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(stderr, "strike trust: unknown subcommand %q\n", args[0])
				fmt.Fprint(stderr, trustUsage)
				return 2
			}
		}
	}
	fs := flag.NewFlagSet("trust "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "workspace directory (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "strike trust %s: unexpected arguments %v\n", sub, fs.Args())
		return 2
	}

	store, err := openWorkspaceTrustStore()
	if err != nil {
		fmt.Fprintln(stderr, "strike trust:", err)
		return 1
	}
	if sub == "list" {
		roots := store.Roots()
		if len(roots) == 0 {
			fmt.Fprintln(stdout, "No trusted workspaces.")
			return 0
		}
		for _, r := range roots {
			rec, _ := store.Record(r)
			fmt.Fprintf(stdout, "%s\t%s\n", r, rec.TrustedAt)
		}
		return 0
	}

	workDir := *dir
	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, "strike trust:", err)
			return 1
		}
	}
	identity, err := project.Resolve(context.Background(), workDir)
	if err != nil {
		fmt.Fprintln(stderr, "strike trust:", err)
		return 1
	}
	root := identity.Root
	surfaces := config.EnumerateProjectSurfaces(workDir, root)

	switch sub {
	case "status":
		state := "untrusted"
		if rec, ok := store.Record(root); ok {
			state = "trusted since " + rec.TrustedAt
		}
		fmt.Fprintf(stdout, "Workspace: %s\nState: %s\nRepo config (%s):\n", root, state, surfaces.Summary())
		writeProjectSurfaces(stdout, surfaces)
		return 0
	case "revoke":
		if err := store.Revoke(root); err != nil {
			fmt.Fprintln(stderr, "strike trust revoke:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Revoked trust for %s. Repo config is inert on the next start.\n", root)
		return 0
	default: // grant
		if err := store.Trust(root); err != nil {
			if errors.Is(err, workspacetrust.ErrUntrustable) {
				fmt.Fprintf(stderr, "strike trust: %v (trust a project directory instead)\n", err)
				return 1
			}
			fmt.Fprintln(stderr, "strike trust:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Trusted %s. On the next start this repo config loads (%s):\n", root, surfaces.Summary())
		writeProjectSurfaces(stdout, surfaces)
		fmt.Fprintln(stdout, "Trust does not widen permissions: tools, MCP calls and hooks still ask as before.")
		return 0
	}
}

// openWorkspaceTrustStore opens ~/.strike/workspace-trust.json.
func openWorkspaceTrustStore() (*workspacetrust.Store, error) {
	globalRoot := config.GlobalRoot()
	if globalRoot == "" {
		return nil, errors.New("global config root is unavailable")
	}
	return workspacetrust.Open(workspacetrust.DefaultPath(globalRoot))
}
