package external

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// blockingPipe finishes Wait only when Kill runs. Write never blocks, so
// shutdown can reach its process wait without a reader.
type blockingPipe struct {
	mu       sync.Mutex
	released bool
	done     chan struct{}
	err      error
}

func newBlockingPipe() *blockingPipe {
	return &blockingPipe{done: make(chan struct{})}
}

func (p *blockingPipe) Read([]byte) (int, error) {
	<-p.done
	return 0, io.EOF
}

func (p *blockingPipe) Write(b []byte) (int, error) { return len(b), nil }

func (p *blockingPipe) CloseWrite() error { return nil }

func (p *blockingPipe) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *blockingPipe) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.released {
		return nil
	}
	p.released = true
	p.err = errors.New("killed")
	close(p.done)
	return nil
}

// TestShutdownAndFailAllBothObserveProcessExit is the #1135 hang: shutdown
// and failAll both wait on process exit, and a single-value wait channel
// leaves one of them blocked after the process is reaped.
func TestShutdownAndFailAllBothObserveProcessExit(t *testing.T) {
	pipe := newBlockingPipe()
	p := &pool{
		name: "race",
		opts: WorkerOptions{MaxConcurrent: 1, IdleTimeout: -1, MaxRestarts: 3}.Normalize(),
	}
	lw := &liveWorker{
		pool:        p,
		pipe:        pipe,
		invocations: map[string]*invocation{},
		processDone: make(chan struct{}),
	}
	go func() {
		err := pipe.Wait()
		lw.mu.Lock()
		lw.waitErr = err
		lw.mu.Unlock()
		close(lw.processDone)
	}()

	shutdownDone := make(chan struct{})
	go func() {
		lw.shutdown()
		close(shutdownDone)
	}()

	deadline := time.Now().Add(time.Second)
	var stopping bool
	for time.Now().Before(deadline) {
		lw.mu.Lock()
		stopping = lw.stopping
		lw.mu.Unlock()
		if stopping {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !stopping {
		t.Fatal("shutdown did not mark the worker stopping")
	}
	// Wait is still blocked, so shutdown is parked on processDone.
	time.Sleep(20 * time.Millisecond)

	failDone := make(chan struct{})
	go func() {
		lw.failAll(errors.New("cancel grace exceeded"))
		close(failDone)
	}()

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	select {
	case <-shutdownDone:
	case <-timeout.C:
		t.Fatal("shutdown blocked waiting for process exit")
	}
	select {
	case <-failDone:
	case <-timeout.C:
		t.Fatal("failAll blocked waiting for process exit")
	}
}
