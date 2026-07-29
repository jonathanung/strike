package telemetry

import "testing"

func TestDarwinMemPressureExcludesCache(t *testing.T) {
	const (
		page = uint64(4096)
		// 24 GiB MacBook-like total
		total = 24 * 1024 * 1024 * 1024
	)
	// Synthetic vm_stat-like snapshot: lots of inactive/speculative file cache,
	// modest wired+active app memory — the bug case where total-free looked ~full.
	free := uint64(500_000)        // ~2 GB free
	active := uint64(1_000_000)    // ~4 GB active
	inactive := uint64(2_500_000)  // ~10 GB inactive (mostly file cache)
	speculative := uint64(250_000) // ~1 GB speculative
	wired := uint64(500_000)       // ~2 GB wired
	purgeable := uint64(200_000)   // ~0.8 GB purgeable (subset of active)
	compressor := uint64(100_000)  // ~0.4 GB compressed

	used, cached := darwinMemPressure(page, total, free, active, inactive, speculative, wired, purgeable, compressor)

	// used = (wired + compressor + active - purgeable) * page
	//      = (500k + 100k + 800k) * 4096 = 1.4M * 4096 = ~5.47 GB
	wantUsed := (wired + compressor + (active - purgeable)) * page
	wantCached := (inactive + speculative + purgeable) * page
	if used != wantUsed {
		t.Fatalf("used = %d, want %d", used, wantUsed)
	}
	if cached != wantCached {
		t.Fatalf("cached = %d, want %d", cached, wantCached)
	}
	// Regression: old formula total-free-inactive counted speculative+wired+active+…
	// as used and looked nearly full on cache-heavy machines.
	oldUsed := total - free*page - inactive*page
	if used >= oldUsed {
		t.Fatalf("fixed used %d should be below old total-free-inactive %d", used, oldUsed)
	}
	if float64(used)/float64(total) > 0.5 {
		t.Fatalf("used ratio %.2f too high for cache-heavy snapshot", float64(used)/float64(total))
	}
}

func TestDarwinMemPressurePurgeableClamp(t *testing.T) {
	used, cached := darwinMemPressure(4096, 1024*1024*1024,
		0, 100, 50, 10, 20, 500, 5)
	// purgeable > active → appActive = 0
	wantUsed := (uint64(20) + 5 + 0) * 4096
	wantCached := (uint64(50) + 10 + 500) * 4096
	if used != wantUsed || cached != wantCached {
		t.Fatalf("used=%d cached=%d want used=%d cached=%d", used, cached, wantUsed, wantCached)
	}
}

func TestDarwinMemPressureZeroPageSize(t *testing.T) {
	used, cached := darwinMemPressure(0, 1<<30, 1, 1, 1, 1, 1, 1, 1)
	if used != 0 || cached != 0 {
		t.Fatalf("zero page size → 0,0; got %d,%d", used, cached)
	}
}

func TestDarwinMemPressureClampsToTotal(t *testing.T) {
	// Pathological counts that would overflow total.
	used, cached := darwinMemPressure(4096, 1000, 0, 1_000_000, 1_000_000, 0, 1_000_000, 0, 0)
	if used != 1000 || cached != 1000 {
		t.Fatalf("clamp used=%d cached=%d want 1000,1000", used, cached)
	}
}
