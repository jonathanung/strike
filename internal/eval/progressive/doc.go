// Package progressive measures progressive tool disclosure (#992 / epic #993).
//
// Offline fixtures compare deferTools off (full exposure) vs on (progressive
// default): first-turn schema tokens, tool-call shape, toolsearch usage, and
// wall time. Legacy compatibility tools remain registered and executable.
//
// Rollback: if progressive mode regresses completion rate by more than
// RollbackCompletionDelta absolute points, or increases median wall time by
// more than RollbackWallTimeRatio relative, keep or restore deferTools=off
// until investigated. See evals/progressive/README.md.
package progressive
