// Package swebench implements the E3.3 SWE-bench Verified subset runner (#561).
//
// It drives a fixed 50-instance subset with Docker isolation per instance and
// strike exec (--json) as the agent. Each instance records pass/fail, tokens,
// estimated cost, and wall-clock; runs write a versioned result JSON for trend
// tracking under evals/swebench/results/ (or a caller-chosen path).
//
// Internal regression signal only — do not publish pass rates in the README
// (SWE-ABS found ~1 in 5 leaderboard "passes" were semantically wrong).
//
// Container lifecycle uses internal/container.CLI via ContainerRuntime (#592)
// with optional scheduler.PoolContainer admission on Runner.Sched. CLIRuntime
// remains for tests and direct docker injection.
//
// Official SWE-bench grading (log parsers + repo test specs) is optional via
// --grader=harness when the swebench Python package is installed. The default
// --grader=docker applies the patch inside the instance image and runs the
// FAIL_TO_PASS / PASS_TO_PASS cases with a best-effort test command.
package swebench
