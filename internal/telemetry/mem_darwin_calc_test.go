package telemetry

import "testing"

const (
	darwinPageSize = 16384                   // Apple silicon
	darwinTotal    = 24 * 1024 * 1024 * 1024 // 24 GiB, the machine in #521
)

// darwinSnapshot is a real sysctl/vm_stat capture from a 64 GiB M-series Mac,
// taken while Activity Monitor reported ~52.8 GB used and ~13.5 GB cached files.
type darwinSnapshot struct {
	pageSize    uint64
	total       uint64
	free        uint64
	external    uint64 // vm_stat "File-backed pages"
	purgeable   uint64
	speculative uint64

	// Mach-only counters, used to derive the Activity Monitor reference value.
	anonymous  uint64
	wired      uint64
	compressor uint64
}

func liveSnapshot() darwinSnapshot {
	return darwinSnapshot{
		pageSize:    16384,
		total:       68719476736,
		free:        64148,
		external:    816205,
		purgeable:   32642,
		speculative: 7448,
		anonymous:   2946848,
		wired:       250966,
		compressor:  55750,
	}
}

// activityMonitorUsed is Activity Monitor's "Memory Used": app memory
// (anonymous minus purgeable) plus wired plus compressed.
func (s darwinSnapshot) activityMonitorUsed() uint64 {
	app := s.anonymous - s.purgeable
	return (app + s.wired + s.compressor) * s.pageSize
}

func (s darwinSnapshot) pages() darwinVMPages {
	return darwinVMPages{
		Free:          s.free,
		External:      s.external,
		ExternalOK:    true,
		Purgeable:     s.purgeable,
		PurgeableOK:   true,
		Speculative:   s.speculative,
		SpeculativeOK: true,
	}
}

// Regression for #521: the RAM bar read ~98% on a machine Activity Monitor
// showed at ~77%, because reclaimable file cache counted as used.
func TestDarwinMemUsageExcludesFileCache(t *testing.T) {
	s := liveSnapshot()
	used, cached, cachedOK := darwinMemUsage(s.pageSize, s.total, s.pages())

	if !cachedOK {
		t.Fatal("cachedOK = false, want true when external+purgeable are readable")
	}
	if wantCached := (s.external + s.purgeable) * s.pageSize; cached != wantCached {
		t.Errorf("cached = %d, want %d", cached, wantCached)
	}

	// Within 5% of total RAM of what Activity Monitor reports. The residual gap
	// is memory the kernel reserves outside the VM page pool (hw.memsize is
	// larger than vm.pages), which this formula attributes to used.
	ref := s.activityMonitorUsed()
	delta := used - ref
	if used < ref {
		delta = ref - used
	}
	if off := float64(delta) / float64(s.total); off > 0.05 {
		t.Errorf("used = %d, Activity Monitor reference %d: off by %.1f%% of total RAM", used, ref, off*100)
	}

	// The pre-fix formula (total − free, cache counted as used) pinned the bar
	// near full. The fixed one must land well below that.
	buggy := s.total - s.free*s.pageSize
	if r := float64(buggy) / float64(s.total); r < 0.95 {
		t.Fatalf("snapshot no longer reproduces the bug: pre-fix ratio %.3f", r)
	}
	if r := float64(used) / float64(s.total); r > 0.85 {
		t.Errorf("used ratio %.3f still reads near-full; cache is not being excluded", r)
	}
}

func TestDarwinMemUsageDegradesPerMissingCounter(t *testing.T) {
	const free = 100_000

	tests := []struct {
		name        string
		pages       darwinVMPages
		wantCached  uint64 // page count
		wantCacheOK bool
	}{
		{
			name: "external and purgeable",
			pages: darwinVMPages{
				Free: free, External: 800_000, ExternalOK: true,
				Purgeable: 30_000, PurgeableOK: true,
			},
			wantCached: 830_000, wantCacheOK: true,
		},
		{
			name: "no external falls back to speculative",
			pages: darwinVMPages{
				Free: free, Purgeable: 30_000, PurgeableOK: true,
				Speculative: 7_000, SpeculativeOK: true,
			},
			wantCached: 37_000, wantCacheOK: true,
		},
		{
			name: "external present ignores speculative subset",
			pages: darwinVMPages{
				Free: free, External: 800_000, ExternalOK: true,
				Speculative: 7_000, SpeculativeOK: true,
			},
			wantCached: 800_000, wantCacheOK: true,
		},
		{
			name:       "no cache counters at all",
			pages:      darwinVMPages{Free: free},
			wantCached: 0, wantCacheOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			used, cached, cachedOK := darwinMemUsage(darwinPageSize, darwinTotal, tc.pages)
			if cachedOK != tc.wantCacheOK {
				t.Errorf("cachedOK = %v, want %v", cachedOK, tc.wantCacheOK)
			}
			if want := tc.wantCached * darwinPageSize; cached != want {
				t.Errorf("cached = %d, want %d", cached, want)
			}
			// used + free + cached always accounts for all of physical RAM.
			if want := darwinTotal - (free+tc.wantCached)*darwinPageSize; used != want {
				t.Errorf("used = %d, want %d", used, want)
			}
		})
	}
}

func TestDarwinMemUsageGuardsBadInput(t *testing.T) {
	full := darwinVMPages{Free: 1, External: 1, ExternalOK: true, Purgeable: 1, PurgeableOK: true}

	if used, cached, ok := darwinMemUsage(0, darwinTotal, full); used != 0 || cached != 0 || ok {
		t.Errorf("zero page size = (%d, %d, %v), want (0, 0, false)", used, cached, ok)
	}
	if used, cached, ok := darwinMemUsage(darwinPageSize, 0, full); used != 0 || cached != 0 || ok {
		t.Errorf("zero total = (%d, %d, %v), want (0, 0, false)", used, cached, ok)
	}

	// Page counts far beyond physical RAM must clamp instead of overflowing.
	huge := darwinVMPages{
		Free: 1 << 60, External: 1 << 60, ExternalOK: true,
		Purgeable: 1 << 60, PurgeableOK: true,
	}
	used, cached, ok := darwinMemUsage(darwinPageSize, darwinTotal, huge)
	if !ok {
		t.Error("cachedOK = false, want true")
	}
	if used != 0 {
		t.Errorf("used = %d, want 0 when everything is reclaimable", used)
	}
	if cached != darwinTotal {
		t.Errorf("cached = %d, want clamped to total %d", cached, darwinTotal)
	}
}
