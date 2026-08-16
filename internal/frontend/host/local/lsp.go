package local

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/integrate/lsp"
)

// NewLSP adapts an lsp.Manager to host.LSP. A nil manager yields a nil host.LSP.
func NewLSP(mgr *lsp.Manager) host.LSP {
	if mgr == nil {
		return nil
	}
	return lspAdapter{mgr: mgr}
}

type lspAdapter struct {
	mgr *lsp.Manager
}

func (a lspAdapter) Statuses() []host.LSPServerStatus {
	raw := a.mgr.Statuses()
	out := make([]host.LSPServerStatus, len(raw))
	for i, s := range raw {
		out[i] = host.LSPServerStatus{
			Name:       s.Name,
			Command:    s.Command,
			State:      s.State,
			Extensions: append([]string(nil), s.Extensions...),
			Error:      s.Error,
			OpenDocs:   s.OpenDocs,
		}
	}
	return out
}

func (a lspAdapter) Retry(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return a.mgr.Retry(ctx, name)
}

func (a lspAdapter) Disable(name string) error {
	return a.mgr.Disable(name)
}

func (a lspAdapter) Diagnostics() []host.Diagnostic {
	byPath := a.mgr.AllDiagnostics()
	if len(byPath) == 0 {
		return nil
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []host.Diagnostic
	for _, path := range paths {
		diags := byPath[path]
		// Stable order within a file: line, character, message.
		sort.SliceStable(diags, func(i, j int) bool {
			di, dj := diags[i], diags[j]
			if di.Range.Start.Line != dj.Range.Start.Line {
				return di.Range.Start.Line < dj.Range.Start.Line
			}
			if di.Range.Start.Character != dj.Range.Start.Character {
				return di.Range.Start.Character < dj.Range.Start.Character
			}
			return di.Message < dj.Message
		})
		for _, d := range diags {
			out = append(out, host.Diagnostic{
				Path:      path,
				Line:      d.Range.Start.Line + 1,
				Character: d.Range.Start.Character + 1,
				Severity:  lsp.SeverityName(d.Severity),
				Source:    d.Source,
				Code:      formatDiagCode(d.Code),
				Message:   d.Message,
			})
		}
	}
	return out
}

func formatDiagCode(code any) string {
	if code == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(code))
	if s == "" || s == "<nil>" {
		return ""
	}
	return s
}
