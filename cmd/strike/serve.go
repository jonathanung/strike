package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jonathanung/strike-cli/internal/server"
	"github.com/jonathanung/strike-cli/internal/session"
)

const serveUsage = `Experimental read-only web attach server (Phase A scaffold).

Usage:
  strike serve [options]

Options:
  --addr <host:port>     bind address (default 127.0.0.1:8787)
  --token <token>        bearer token for /v1/* (required; auto-minted if omitted)
  --session-dir <path>   sessions directory (default ~/.strike/sessions)
  -h, --help             show help

The server exposes:
  GET /health                         JSON ok + version (no auth)
  GET /  and  GET /attach             minimal read-only transcript page
  GET /v1/sessions/{id}/events        SSE stream of session JSONL envelopes

Auth: pass Authorization: Bearer <token> or ?token= on /v1/* routes.

DANGER: binding outside localhost exposes session transcripts to the network.
Keep --addr on a loopback address unless you understand the risk. There is no
TLS and no production auth in this scaffold.`

type serveOptions struct {
	addr       string
	token      string
	sessionDir string
}

func runServeCLI(args []string, stdout, stderr io.Writer) int {
	opts, err := parseServeArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, serveUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, serveUsage)
		return 2
	}
	if err := runServe(opts, stdout, stderr); err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

func parseServeArgs(args []string) (serveOptions, error) {
	var opts serveOptions
	fs := flag.NewFlagSet("strike serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.addr, "addr", "127.0.0.1:8787", "")
	fs.StringVar(&opts.token, "token", "", "")
	fs.StringVar(&opts.sessionDir, "session-dir", "", "")
	if err := fs.Parse(args); err != nil {
		return serveOptions{}, err
	}
	if fs.NArg() != 0 {
		return serveOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.addr = strings.TrimSpace(opts.addr)
	opts.token = strings.TrimSpace(opts.token)
	opts.sessionDir = strings.TrimSpace(opts.sessionDir)
	if opts.addr == "" {
		opts.addr = "127.0.0.1:8787"
	}
	if opts.sessionDir == "" {
		opts.sessionDir = session.DefaultDir()
	}
	return opts, nil
}

func runServe(opts serveOptions, stdout, stderr io.Writer) error {
	token := opts.token
	minted := false
	if token == "" {
		t, err := server.MintToken()
		if err != nil {
			return fmt.Errorf("minting token: %w", err)
		}
		token = t
		minted = true
	}

	srv, err := server.New(server.Options{
		Addr:       opts.addr,
		Token:      token,
		SessionDir: opts.sessionDir,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", opts.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", opts.addr, err)
	}

	if !server.IsLocalhostBind(opts.addr) {
		fmt.Fprintln(stderr, "WARNING: strike serve is bound outside localhost.")
		fmt.Fprintln(stderr, "Session event streams are readable with the token; there is no TLS.")
		fmt.Fprintln(stderr, "Prefer --addr 127.0.0.1:8787 for local attach only.")
	}

	actual := ln.Addr().String()
	fmt.Fprintf(stdout, "strike serve listening on http://%s\n", actual)
	fmt.Fprintf(stdout, "  health:  http://%s/health\n", actual)
	fmt.Fprintf(stdout, "  attach:  http://%s/attach?session=<id>&token=<token>\n", actual)
	if minted {
		fmt.Fprintf(stdout, "  token:   %s  (auto-minted; pass --token to set)\n", token)
	} else {
		fmt.Fprintln(stdout, "  token:   (from --token)")
	}
	fmt.Fprintf(stdout, "  sessions dir: %s\n", opts.sessionDir)
	fmt.Fprintln(stdout, "experimental read-only scaffold — TUI remains primary")

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		err := <-errCh
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-errCh:
		return err
	}
}
