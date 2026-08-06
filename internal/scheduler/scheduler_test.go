package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewDefaultPools(t *testing.T) {
	s := NewDefault()
	t.Cleanup(s.Close)

	snap := s.Snapshot()
	if len(snap.Pools) != len(DefaultPoolNames) {
		t.Fatalf("pools=%d want %d", len(snap.Pools), len(DefaultPoolNames))
	}
	got := make(map[string]PoolSnapshot, len(snap.Pools))
	for _, p := range snap.Pools {
		got[p.Name] = p
	}
	for _, name := range DefaultPoolNames {
		p, ok := got[name]
		if !ok {
			t.Fatalf("missing default pool %q", name)
		}
		if !p.Unlimited || p.Capacity != 0 {
			t.Fatalf("pool %q: want unlimited capacity 0, got %+v", name, p)
		}
	}
}

func TestNewRejectsEmptyPoolName(t *testing.T) {
	_, err := New(Config{Pools: map[string]int{"": 1}})
	if err == nil {
		t.Fatal("expected error for empty pool name")
	}
}

func TestAcquireUnknownPool(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolProcess: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	_, err = s.Acquire(context.Background(), "nope")
	if !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("err=%v want ErrUnknownPool", err)
	}
}

func TestAcquireEmptyNames(t *testing.T) {
	s := NewDefault()
	t.Cleanup(s.Close)
	_, err := s.Acquire(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAcquireReleaseBasic(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolProcess: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	l1, err := s.Acquire(context.Background(), PoolProcess)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := s.Acquire(context.Background(), PoolProcess)
	if err != nil {
		t.Fatal(err)
	}
	assertPool(t, s, PoolProcess, 2, 0)

	l1.Release()
	assertPool(t, s, PoolProcess, 1, 0)
	l2.Release()
	assertPool(t, s, PoolProcess, 0, 0)
}

func TestReleaseIdempotent(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolModel: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	l, err := s.Acquire(context.Background(), PoolModel)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release()
	l.Release()
	assertPool(t, s, PoolModel, 0, 0)

	// Capacity must still be usable after double-release.
	l2, err := s.Acquire(context.Background(), PoolModel)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
}

func TestNilLeaseRelease(t *testing.T) {
	var l *Lease
	l.Release() // must not panic
}

func TestCapacityNeverExceededUnderContention(t *testing.T) {
	const (
		cap     = 3
		workers = 32
		iters   = 40
	)
	s, err := New(Config{Pools: map[string]int{PoolProcess: cap}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	var (
		inFlight atomic.Int32
		maxSeen  atomic.Int32
		wg       sync.WaitGroup
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				l, err := s.Acquire(context.Background(), PoolProcess)
				if err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				n := inFlight.Add(1)
				for {
					cur := maxSeen.Load()
					if n <= cur || maxSeen.CompareAndSwap(cur, n) {
						break
					}
				}
				if n > cap {
					t.Errorf("inFlight=%d exceeds capacity %d", n, cap)
				}
				// Brief hold so others contend.
				time.Sleep(time.Microsecond)
				inFlight.Add(-1)
				l.Release()
			}
		}()
	}
	wg.Wait()
	if max := maxSeen.Load(); max > cap {
		t.Fatalf("max in-flight %d > capacity %d", max, cap)
	}
	if max := maxSeen.Load(); max < 1 {
		t.Fatal("expected some acquisitions")
	}
	assertPool(t, s, PoolProcess, 0, 0)
}

func TestFIFOwithinPool(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolBuild: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	holder, err := s.Acquire(context.Background(), PoolBuild)
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	orderCh := make(chan int, n)
	started := make(chan int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			// Stagger starts slightly so enqueue order matches i when possible,
			// but we synchronize via started + barrier after all are waiting.
			ctx := context.Background()
			started <- i
			l, err := s.Acquire(ctx, PoolBuild)
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			orderCh <- i
			l.Release()
		}()
	}

	// Wait until all waiters have been scheduled far enough to block.
	// Poll snapshot Waiting until n (or timeout).
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := poolSnap(t, s, PoolBuild)
		if snap.Waiting >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters stuck at %d want %d", snap.Waiting, n)
		}
		time.Sleep(time.Millisecond)
	}
	// Drain started signals (ordering of goroutine start is not the FIFO key;
	// FIFO is admission order among waiters already queued).
	for i := 0; i < n; i++ {
		<-started
	}

	// Capture waiter identity order by releasing one slot at a time is hard
	// without knowing enqueue order. Instead: hold capacity, start waiters
	// sequentially ensuring each is queued before the next starts Acquire.
	// Restart with a cleaner sequential-enqueue test below.
	holder.Release()
	wg.Wait()
	close(orderCh)
	// With concurrent enqueue the order may match start order if Waiting hit n
	// before any grant; still verify all n completed and no capacity leak.
	got := make([]int, 0, n)
	for v := range orderCh {
		got = append(got, v)
	}
	if len(got) != n {
		t.Fatalf("completed %d want %d", len(got), n)
	}
	assertPool(t, s, PoolBuild, 0, 0)
}

func TestFIFODeterministicSequentialEnqueue(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{"p": 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	holder, err := s.Acquire(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}

	const n = 10
	var (
		mu    sync.Mutex
		order []int
		wg    sync.WaitGroup
	)
	wg.Add(n)
	// Enqueue waiters one-by-one, confirming each is waiting before the next.
	for i := 0; i < n; i++ {
		i := i
		queued := make(chan struct{})
		go func() {
			defer wg.Done()
			// Signal once we're past the fast-path check by spinning until Waiting grows.
			// Use a side channel: parent waits on snapshot after starting goroutine.
			l, err := s.Acquire(context.Background(), "p")
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			l.Release()
		}()
		// Wait until this waiter is reflected in the queue (holder still holds slot).
		waitForWaiting(t, s, "p", i+1)
		close(queued)
		_ = queued
	}

	holder.Release()
	wg.Wait()

	if len(order) != n {
		t.Fatalf("order len=%d want %d: %v", len(order), n, order)
	}
	for i := 0; i < n; i++ {
		if order[i] != i {
			t.Fatalf("FIFO broken: order=%v", order)
		}
	}
}

func TestAcquireRejectedWhenContextAlreadyCanceled(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolModel: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l, err := s.Acquire(ctx, PoolModel)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if l != nil {
		t.Fatal("lease must be nil when ctx already canceled")
	}
	assertPool(t, s, PoolModel, 0, 0)
}

func TestCancelLeavesPromptlyWithoutCapacity(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolTest: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	holder, err := s.Acquire(context.Background(), PoolTest)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := s.Acquire(ctx, PoolTest)
		errCh <- err
	}()
	waitForWaiting(t, s, PoolTest, 1)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter did not return")
	}

	assertPool(t, s, PoolTest, 1, 0) // holder still has the only slot; no waiter
	holder.Release()
	assertPool(t, s, PoolTest, 0, 0)

	// Next acquire must succeed immediately (canceled waiter did not consume).
	l, err := s.Acquire(context.Background(), PoolTest)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}

func TestCancelManyUnderContention(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolProcess: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)
	var canceled atomic.Int32
	var acquired atomic.Int32
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			l, err := s.Acquire(ctx, PoolProcess)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					canceled.Add(1)
					return
				}
				t.Errorf("unexpected err: %v", err)
				return
			}
			acquired.Add(1)
			time.Sleep(time.Millisecond)
			l.Release()
		}()
	}
	wg.Wait()
	assertPool(t, s, PoolProcess, 0, 0)
	if acquired.Load()+canceled.Load() != n {
		t.Fatalf("acquired=%d canceled=%d want sum %d", acquired.Load(), canceled.Load(), n)
	}
}

func TestMultiPoolAtomicAllOrNothing(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{
		"a": 1,
		"b": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// Hold b so multi-acquire of a+b must wait; a must stay free.
	holdB, err := s.Acquire(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		l, err := s.Acquire(context.Background(), "b", "a") // unsorted on purpose
		if err != nil {
			errCh <- err
			return
		}
		// While holding a+b, both in use.
		sa := poolSnap(t, s, "a")
		sb := poolSnap(t, s, "b")
		if sa.InUse != 1 || sb.InUse != 1 {
			errCh <- fmt.Errorf("during multi hold: a=%+v b=%+v", sa, sb)
			l.Release()
			return
		}
		l.Release()
		errCh <- nil
	}()

	waitForWaiting(t, s, "a", 1)
	waitForWaiting(t, s, "b", 1)
	// a must not be consumed while waiter is blocked on b.
	assertPool(t, s, "a", 0, 1)

	holdB.Release()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("multi-acquire did not complete")
	}
	assertPool(t, s, "a", 0, 0)
	assertPool(t, s, "b", 0, 0)
}

func TestMultiPoolNoDeadlockInconsistentOrder(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{
		"a": 1,
		"b": 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// Two goroutines request opposite orders concurrently; both must finish.
	var wg sync.WaitGroup
	const rounds = 100
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			l, err := s.Acquire(context.Background(), "a", "b")
			if err != nil {
				t.Errorf("ab: %v", err)
				return
			}
			l.Release()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			l, err := s.Acquire(context.Background(), "b", "a")
			if err != nil {
				t.Errorf("ba: %v", err)
				return
			}
			l.Release()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: multi-pool opposite order did not finish")
	}
	assertPool(t, s, "a", 0, 0)
	assertPool(t, s, "b", 0, 0)
}

func TestMultiPoolDedupNames(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolModel: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	l, err := s.Acquire(context.Background(), PoolModel, PoolModel, PoolModel)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Pools(); len(got) != 1 || got[0] != PoolModel {
		t.Fatalf("pools=%v", got)
	}
	assertPool(t, s, PoolModel, 1, 0)
	l.Release()
}

func TestUnlimitedPoolNeverBlocks(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolContainer: 0}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	const n = 20
	leases := make([]*Lease, n)
	for i := 0; i < n; i++ {
		l, err := s.Acquire(context.Background(), PoolContainer)
		if err != nil {
			t.Fatal(err)
		}
		leases[i] = l
	}
	assertPool(t, s, PoolContainer, n, 0)
	for _, l := range leases {
		l.Release()
	}
	assertPool(t, s, PoolContainer, 0, 0)
}

func TestCloseFailsWaitersAndNewAcquire(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolProcess: 1}})
	if err != nil {
		t.Fatal(err)
	}

	holder, err := s.Acquire(context.Background(), PoolProcess)
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := s.Acquire(context.Background(), PoolProcess)
		errCh <- err
	}()
	waitForWaiting(t, s, PoolProcess, 1)
	s.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("waiter err=%v want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not unblocked on Close")
	}

	_, err = s.Acquire(context.Background(), PoolProcess)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("acquire after close: %v", err)
	}

	// In-flight lease can still release without panic / double-count issues.
	holder.Release()
	holder.Release()
	s.Close() // idempotent
}

func TestSnapshotStableOrder(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{
		"z": 1,
		"a": 2,
		"m": 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	snap := s.Snapshot()
	if len(snap.Pools) != 3 {
		t.Fatalf("len=%d", len(snap.Pools))
	}
	want := []string{"a", "m", "z"}
	for i, name := range want {
		if snap.Pools[i].Name != name {
			t.Fatalf("order[%d]=%q want %q", i, snap.Pools[i].Name, name)
		}
	}
}

func TestAcquireAfterCancelRaceDoesNotLeak(t *testing.T) {
	// Stress cancel-vs-grant races; capacity must end at zero.
	s, err := New(Config{Pools: map[string]int{"p": 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	var wg sync.WaitGroup
	const n = 200
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%3)*time.Millisecond)
			defer cancel()
			l, err := s.Acquire(ctx, "p")
			if err != nil {
				return
			}
			l.Release()
		}()
	}
	wg.Wait()
	// Drain any winner still holding briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p := poolSnap(t, s, "p")
		if p.InUse == 0 && p.Waiting == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	p := poolSnap(t, s, "p")
	t.Fatalf("leak: %+v", p)
}

func TestLeasePoolsCopy(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{"a": 1, "b": 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	l, err := s.Acquire(context.Background(), "b", "a")
	if err != nil {
		t.Fatal(err)
	}
	got := l.Pools()
	got[0] = "mutated"
	got2 := l.Pools()
	if got2[0] != "a" {
		t.Fatalf("Pools did not copy: %v", got2)
	}
	l.Release()
}

// Helpers

func assertPool(t *testing.T, s *Scheduler, name string, inUse, waiting int) {
	t.Helper()
	p := poolSnap(t, s, name)
	if p.InUse != inUse || p.Waiting != waiting {
		t.Fatalf("pool %s: inUse=%d waiting=%d want inUse=%d waiting=%d",
			name, p.InUse, p.Waiting, inUse, waiting)
	}
}

func poolSnap(t *testing.T, s *Scheduler, name string) PoolSnapshot {
	t.Helper()
	for _, p := range s.Snapshot().Pools {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("pool %q not in snapshot", name)
	return PoolSnapshot{}
}

func waitForWaiting(t *testing.T, s *Scheduler, name string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if poolSnap(t, s, name).Waiting >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q Waiting>=%d (got %d)", name, n, poolSnap(t, s, name).Waiting)
}

func TestAcquireNotifyImmediateAdmittedOnly(t *testing.T) {
	s := NewDefault()
	defer s.Close()
	var phases []AcquirePhase
	lease, err := s.AcquireNotify(context.Background(), func(ev AcquireEvent) {
		phases = append(phases, ev.Phase)
	}, PoolModel)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if len(phases) != 1 || phases[0] != PhaseAdmitted {
		t.Fatalf("phases=%v want [admitted]", phases)
	}
}

func TestAcquireNotifyQueuedThenAdmitted(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolModel: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	holder, err := s.Acquire(context.Background(), PoolModel)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var phases []AcquirePhase
	done := make(chan struct{})
	go func() {
		defer close(done)
		lease, err := s.AcquireNotify(context.Background(), func(ev AcquireEvent) {
			mu.Lock()
			phases = append(phases, ev.Phase)
			mu.Unlock()
		}, PoolModel)
		if err != nil {
			t.Errorf("acquire: %v", err)
			return
		}
		lease.Release()
	}()
	waitForWaiting(t, s, PoolModel, 1)
	// Queued must fire before grant.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(phases)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued never notified")
		}
		time.Sleep(time.Millisecond)
	}
	holder.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter stuck")
	}
	mu.Lock()
	got := append([]AcquirePhase(nil), phases...)
	mu.Unlock()
	if len(got) != 2 || got[0] != PhaseQueued || got[1] != PhaseAdmitted {
		t.Fatalf("phases=%v want [queued admitted]", got)
	}
}

func TestAcquireNotifyCancelNoAdmitted(t *testing.T) {
	s, err := New(Config{Pools: map[string]int{PoolTest: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	holder, err := s.Acquire(context.Background(), PoolTest)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var phases []AcquirePhase
	errCh := make(chan error, 1)
	go func() {
		_, err := s.AcquireNotify(ctx, func(ev AcquireEvent) {
			mu.Lock()
			phases = append(phases, ev.Phase)
			mu.Unlock()
		}, PoolTest)
		errCh <- err
	}()
	waitForWaiting(t, s, PoolTest, 1)
	// Ensure queued observed.
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(phases)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued never notified")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel stuck")
	}
	mu.Lock()
	got := append([]AcquirePhase(nil), phases...)
	mu.Unlock()
	if len(got) != 2 || got[0] != PhaseQueued || got[1] != PhaseCanceled {
		t.Fatalf("phases=%v want [queued canceled]", got)
	}
	for _, p := range got {
		if p == PhaseAdmitted {
			t.Fatal("admitted after cancel")
		}
	}
}
