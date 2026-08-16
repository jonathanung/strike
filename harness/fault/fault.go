// Package fault provides test-only failure injection for harness chaos tests.
//
// Production code calls Check at documented points. When no fault is armed,
// Check is a cheap no-op (nil map lookup under a mutex). Faults are never
// armed in production binaries — only tests call Arm.
//
// This is not a production chaos monkey. See docs/chaos.md for the suite,
// safe outcomes, and how to add a new fault.
package fault

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Point names an injectable failure site on a harness path.
type Point string

// Injectable points wired into production code. Logical faults that need no
// production hook (provider stream drop, permission flip, log truncate) are
// still named here so the chaos suite and docs share one catalog.
const (
	// SessionSync replaces os.File.Sync during session header/append durability.
	SessionSync Point = "session.sync"
	// SessionWrite injects a failure before session log Write (disk-full /
	// short-write simulation). When armed, Append returns before any bytes.
	SessionWrite Point = "session.write"
	// ProcessAfterStart fires after a subprocess has started and before Wait,
	// simulating a mid-run kill of the process tree.
	ProcessAfterStart Point = "process.after_start"

	// ProviderStreamDrop is a logical fault: tests close the provider stream
	// without a terminal event (NormalizeStream injects ErrIncompleteStream).
	ProviderStreamDrop Point = "provider.stream_drop"
	// PermissionFlipMidTurn is a logical fault: tests reject or hard-deny a
	// tool after the turn has already started.
	PermissionFlipMidTurn Point = "permission.flip_mid_turn"
	// SessionLogTruncate is a logical fault: tests truncate or corrupt the
	// JSONL log on disk, then assert Replay loadability / CorruptError.
	SessionLogTruncate Point = "session.log_truncate"
)

// Err is the sentinel wrapped by injected failures so tests can detect them
// with errors.Is. Message text never includes secrets or path contents from
// the call site — only the point name.
var Err = errors.New("fault injected")

type arm struct {
	remaining atomic.Int64
	err       error
}

var (
	mu sync.Mutex
	// arms is only consulted when armedCount > 0 so production Check is a
	// single atomic load on the hot session/process paths.
	arms       = map[Point]*arm{}
	armedCount atomic.Int64
)

func noteArmedLocked() {
	armedCount.Store(int64(len(arms)))
}

// Arm schedules the next n successful Check hits of p to return err.
// When err is nil, Check returns a point-tagged wrap of Err.
// n <= 0 is treated as 1. The returned disarm clears the arm; call via
// t.Cleanup. Concurrent Arm on the same point replaces the previous arm.
// Do not combine Arm with t.Parallel in the same package — global process state.
func Arm(p Point, n int, err error) (disarm func()) {
	if n <= 0 {
		n = 1
	}
	a := &arm{err: err}
	a.remaining.Store(int64(n))
	mu.Lock()
	arms[p] = a
	noteArmedLocked()
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			if cur, ok := arms[p]; ok && cur == a {
				delete(arms, p)
				noteArmedLocked()
			}
			mu.Unlock()
		})
	}
}

// Check returns a non-nil error when p is armed, consuming one hit.
// Unarmed points always return nil (fast path: one atomic load).
func Check(p Point) error {
	if armedCount.Load() == 0 {
		return nil
	}
	mu.Lock()
	a, ok := arms[p]
	mu.Unlock()
	if !ok || a == nil {
		return nil
	}
	for {
		left := a.remaining.Load()
		if left <= 0 {
			return nil
		}
		if a.remaining.CompareAndSwap(left, left-1) {
			if left == 1 {
				mu.Lock()
				if cur, ok := arms[p]; ok && cur == a {
					delete(arms, p)
					noteArmedLocked()
				}
				mu.Unlock()
			}
			if a.err != nil {
				return a.err
			}
			return fmt.Errorf("%w: %s", Err, p)
		}
	}
}

// Remaining reports how many hits are left for p (0 if unarmed).
func Remaining(p Point) int {
	if armedCount.Load() == 0 {
		return 0
	}
	mu.Lock()
	a := arms[p]
	mu.Unlock()
	if a == nil {
		return 0
	}
	n := a.remaining.Load()
	if n < 0 {
		return 0
	}
	return int(n)
}

// Reset clears every armed point. Tests may call this in TestMain or cleanup
// when a suite shares process state; prefer per-test Arm + disarm.
func Reset() {
	mu.Lock()
	clear(arms)
	noteArmedLocked()
	mu.Unlock()
}

// Catalog lists every named point for docs and registry tests.
func Catalog() []Point {
	return []Point{
		SessionSync,
		SessionWrite,
		ProcessAfterStart,
		ProviderStreamDrop,
		PermissionFlipMidTurn,
		SessionLogTruncate,
	}
}
