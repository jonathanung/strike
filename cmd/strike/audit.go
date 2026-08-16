package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/product/config"
	"github.com/jonathanung/strike-cli/internal/trust/audit"
)

func runAuditCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		writeAuditUsage(stdout)
		return 0
	}
	switch args[0] {
	case "export":
		return runAuditExport(args[1:], stdout, stderr)
	case "prune":
		return runAuditPrune(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "strike audit: unknown subcommand %q\n", args[0])
		writeAuditUsage(stderr)
		return 2
	}
}

func writeAuditUsage(w io.Writer) {
	fmt.Fprint(w, `strike audit — durable security audit sink

Usage:
  strike audit export [-o path]   Export redacted machine-readable bundle
  strike audit prune              Apply retention now

Storage: ~/.strike/audit/ (segmented JSONL). Does not include full session
transcripts. See docs/audit.md.
`)
}

func runAuditExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output path (default: ./strike-audit-<ts>.json)")
	dir := fs.String("dir", "", "audit directory (default: ~/.strike/audit)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path := strings.TrimSpace(*out)
	if path == "" {
		path = fmt.Sprintf("strike-audit-%s.json", time.Now().UTC().Format("20060102T150405Z"))
	}
	cfg, _ := config.Load("")
	sink, err := openAuditFromFlags(*dir, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "strike audit export:", err)
		return 1
	}
	defer sink.Close()
	if err := sink.ExportBundle(path); err != nil {
		fmt.Fprintln(stderr, "strike audit export:", err)
		return 1
	}
	abs, _ := filepath.Abs(path)
	fmt.Fprintln(stdout, abs)
	return 0
}

func runAuditPrune(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "audit directory (default: ~/.strike/audit)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, _ := config.Load("")
	sink, err := openAuditFromFlags(*dir, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "strike audit prune:", err)
		return 1
	}
	if err := sink.Prune(); err != nil {
		_ = sink.Close()
		fmt.Fprintln(stderr, "strike audit prune:", err)
		return 1
	}
	if err := sink.Close(); err != nil {
		fmt.Fprintln(stderr, "strike audit prune:", err)
		return 1
	}
	fmt.Fprintln(stdout, "audit retention applied:", sink.Dir())
	return 0
}

func openAuditFromFlags(dir string, cfg config.Config) (*audit.Sink, error) {
	opts := audit.Options{Dir: strings.TrimSpace(dir)}
	if opts.Dir == "" {
		opts.Dir = audit.DefaultDir()
	}
	ret := audit.Retention{}
	if cfg.Session.AuditRetentionMaxEvents > 0 {
		ret.MaxEvents = cfg.Session.AuditRetentionMaxEvents
	}
	if cfg.Session.AuditRetentionMaxAgeDays > 0 {
		ret.MaxAge = time.Duration(cfg.Session.AuditRetentionMaxAgeDays) * 24 * time.Hour
	}
	if ret.MaxEvents == 0 && ret.MaxAge == 0 {
		ret.MaxEvents = 10000
		ret.MaxAge = 90 * 24 * time.Hour
	}
	opts.Retention = ret
	return audit.Open(opts)
}
