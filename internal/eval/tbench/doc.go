// Package tbench implements the E3.4 Terminal-Bench runner (#562).
//
// It mirrors the E3.3 SWE-bench harness shape: Docker per instance, strike
// exec (--json) as the agent, and a versioned result JSON with pass rate,
// tokens, cost, and wall-clock. The task pack is Terminal-Bench 2 (Harbor
// format: instruction.md + task.toml + tests/), not SWE-bench patches.
//
// Flow per instance:
//  1. Materialize the image workdir (/app) onto the host
//  2. Start a live task container with the host checkout bind-mounted at /app
//  3. Run strike exec --json --auto --dangerously-skip-permissions
//     --sandbox=off; bash is docker-exec'd into that image
//     (STRIKE_EVAL_CONTAINER + STRIKE_EVAL_WORKDIR). network.allow is still
//     written for isolation; DSP bypasses it so host git/python/curl work.
//  4. Grade in the live container (keep /app + system installs); reset
//     /tests and verifier logs so a leftover reward.txt cannot pass.
//
// Container lifecycle uses the Docker CLI via swebench.Runtime so #592 can
// swap in internal/integrate/container later without changing this runner.
//
// Internal regression signal only — do not publish pass rates in the README.
package tbench
