package main

import (
	"os"
	"testing"
	"time"
)

// Regression for #1678: a first Ctrl-C starts graceful shutdown, a
// second one must force an immediate exit instead of being swallowed
// by signal.Notify.
func TestExitOnSecondSignal(t *testing.T) {
	signals := make(chan os.Signal, 2)
	stop := make(chan struct{})
	exited := make(chan int, 1)
	go exitOnSecondSignal(signals, stop, func(code int) { exited <- code })

	signals <- os.Interrupt
	select {
	case code := <-exited:
		t.Fatalf("exit(%d) after only one signal, want graceful path to continue", code)
	case <-time.After(50 * time.Millisecond):
	}

	signals <- os.Interrupt
	select {
	case code := <-exited:
		if code != 130 {
			t.Fatalf("exit code = %d, want 130 (128+SIGINT)", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second signal did not force an exit")
	}
}

func TestExitOnSecondSignalStopsCleanly(t *testing.T) {
	signals := make(chan os.Signal, 2)
	stop := make(chan struct{})
	exited := make(chan int, 1)
	go exitOnSecondSignal(signals, stop, func(code int) { exited <- code })

	signals <- os.Interrupt
	close(stop)
	select {
	case code := <-exited:
		t.Fatalf("exit(%d) after stop, want watcher to end quietly", code)
	case <-time.After(50 * time.Millisecond):
	}
}

// The cleanup wait for live roots is bounded: a root whose engine
// never finishes must report false instead of hanging shutdown.
func TestWaitForServeRoots(t *testing.T) {
	if !waitForServeRoots(nil, time.Second) {
		t.Fatal("no roots: want true")
	}
	closed := make(chan struct{})
	close(closed)
	if !waitForServeRoots([]<-chan struct{}{closed}, time.Second) {
		t.Fatal("closed root: want true")
	}
	never := make(chan struct{})
	start := time.Now()
	if waitForServeRoots([]<-chan struct{}{closed, never}, 30*time.Millisecond) {
		t.Fatal("stuck root: want false on timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("wait took %v, want bounded", elapsed)
	}
}
