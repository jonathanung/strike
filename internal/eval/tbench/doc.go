// Package tbench implements the E3.4 Terminal-Bench runner (#562).
//
// It mirrors the E3.3 SWE-bench harness shape: Docker per instance, strike
// exec (--json) as the agent, and a versioned result JSON with pass rate,
// tokens, cost, and wall-clock. The task pack is Terminal-Bench 2 (Harbor
// format: instruction.md + task.toml + tests/), not SWE-bench patches.
//
// Flow per instance:
//  1. Materialize the image workdir (/app) onto the host
//  2. Run strike exec --json --auto against that workspace
//  3. Grade by replaying the workspace into a fresh container, copying
//     tests/, running tests/test.sh, and reading /logs/verifier/reward.txt
//
// Container lifecycle uses the Docker CLI via swebench.Runtime so #592 can
// swap in internal/container later without changing this runner.
//
// Internal regression signal only — do not publish pass rates in the README.
package tbench
