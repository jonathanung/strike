package fault_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/fault"
)

func TestArmCheckOneShot(t *testing.T) {
	t.Cleanup(fault.Reset)
	disarm := fault.Arm(fault.SessionSync, 1, nil)
	defer disarm()

	err := fault.Check(fault.SessionSync)
	if !errors.Is(err, fault.Err) {
		t.Fatalf("Check = %v, want fault.Err", err)
	}
	if fault.Remaining(fault.SessionSync) != 0 {
		t.Fatalf("remaining = %d, want 0", fault.Remaining(fault.SessionSync))
	}
	if err := fault.Check(fault.SessionSync); err != nil {
		t.Fatalf("second Check = %v, want nil", err)
	}
}

func TestArmCustomErrorAndCount(t *testing.T) {
	t.Cleanup(fault.Reset)
	want := errors.New("disk full")
	disarm := fault.Arm(fault.SessionSync, 2, want)
	defer disarm()

	for i := 0; i < 2; i++ {
		if err := fault.Check(fault.SessionSync); !errors.Is(err, want) {
			t.Fatalf("hit %d: %v", i+1, err)
		}
	}
	if err := fault.Check(fault.SessionSync); err != nil {
		t.Fatalf("exhausted Check = %v", err)
	}
}

func TestDisarmClears(t *testing.T) {
	t.Cleanup(fault.Reset)
	disarm := fault.Arm(fault.ProcessAfterStart, 5, nil)
	disarm()
	if err := fault.Check(fault.ProcessAfterStart); err != nil {
		t.Fatalf("after disarm: %v", err)
	}
}

func TestCheckUnarmedNil(t *testing.T) {
	t.Cleanup(fault.Reset)
	for _, p := range fault.Catalog() {
		if err := fault.Check(p); err != nil {
			t.Fatalf("unarmed %s: %v", p, err)
		}
	}
}

func TestConcurrentCheckConsumesExactlyN(t *testing.T) {
	t.Cleanup(fault.Reset)
	const n = 50
	disarm := fault.Arm(fault.ProcessAfterStart, n, nil)
	defer disarm()

	var hits atomicCounter
	var wg sync.WaitGroup
	for i := 0; i < n*4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fault.Check(fault.ProcessAfterStart); err != nil {
				hits.Add(1)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != n {
		t.Fatalf("hits = %d, want %d", hits.Load(), n)
	}
}

func TestCatalogHasAtLeastFive(t *testing.T) {
	if got := len(fault.Catalog()); got < 5 {
		t.Fatalf("catalog size = %d, want >= 5", got)
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) Add(d int) {
	c.mu.Lock()
	c.n += d
	c.mu.Unlock()
}

func (c *atomicCounter) Load() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
