package tbench

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/swebench"
)

// BuildAgentPrompt formats a Terminal-Bench instruction for strike exec.
func BuildAgentPrompt(in Instance) string {
	return FormatAgentPrompt(in, "")
}

// FormatAgentPrompt is BuildAgentPrompt plus optional live-image instructions.
func FormatAgentPrompt(in Instance, evalContainer string) string {
	var b strings.Builder
	b.WriteString("You are an autonomous agent working in a Linux terminal environment.\n")
	if evalContainer != "" {
		b.WriteString("The current directory is bind-mounted at /app in the task image.\n")
		b.WriteString("The bash tool runs inside that image (cwd /app). Absolute /app paths work in bash and in read/write/edit/glob.\n")
		b.WriteString("Host Python/packages will NOT match the image. Prefer bash / eval-exec over host interpreters.\n")
		b.WriteString("eval-exec on PATH wraps docker exec into the same image:\n")
		b.WriteString("  eval-exec python3 script.py\n")
		b.WriteString("  eval-exec openssl version\n")
		fmt.Fprintf(&b, "STRIKE_EVAL_CONTAINER is set (%s).\n", evalContainer)
	} else {
		b.WriteString("The task working directory is the current directory (mapped from /app in the benchmark image).\n")
	}
	b.WriteString("Complete the instruction fully. Prefer minimal correct changes. Do not modify hidden grader files.\n\n")
	fmt.Fprintf(&b, "Task: %s\n", in.InstanceID)
	if in.Category != "" {
		fmt.Fprintf(&b, "Category: %s\n", in.Category)
	}
	if in.Difficulty != "" {
		fmt.Fprintf(&b, "Difficulty: %s\n", in.Difficulty)
	}
	b.WriteString("\n--- INSTRUCTION ---\n")
	b.WriteString(strings.TrimSpace(in.Instruction))
	b.WriteString("\n")
	return b.String()
}

// AgentTimeout returns the per-instance agent timeout from the task or default.
func AgentTimeout(in Instance, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if in.AgentTimeout > 0 {
		return time.Duration(in.AgentTimeout * float64(time.Second))
	}
	return 30 * time.Minute
}

func fromSWEUsage(u *swebench.Usage) *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		Input:         u.Input,
		Output:        u.Output,
		CacheRead:     u.CacheRead,
		CacheCreation: u.CacheCreation,
	}
}
