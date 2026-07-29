package telemetry

import "testing"

const (
	darwinPageSize = 16384                   // Apple silicon
	darwinTotal    = 24 * 1024 * 1024 * 1024 // 24 GiB, the machine in #521
)

// darwinSnapshot is a real sysctl/vm_stat capture from a 64 GiB M-series Mac,
// taken while Activity Monitor reported ~52.8 GB used and ~13.5 GB cached files.
type darwinSnapshot struct {
	pageSize  uint64
	total     uint64
	free      uint64
	external  uint64 // vm_stat "File-backed pages"
	purgeable uint64

	// Mach-only counters, used to derive the Activity Monitor reference value.
	anonymous  uint64
	wired      uint64
	compressor uint64
}

func liveSnapshot() darwinSnapshot {
	return darwinSnapshot{
		pageSize:   16384,
		total:      68719476736,
		free:       64148,
		external:   816205,
		purgeable:  32642,
		anonymous:  2946848,
		wired:      250966,
		compressor: 55750,
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
		Free:        s.free,
		External:    s.external,
		ExternalOK:  true,
		Purgeable:   s.purgeable,
		PurgeableOK: true,
	}
}

// Regression for #521: the RAM bar read ~98% on a machine Activity Monitor
// showed at ~77%, because reclaimable file cache counted as used.
func TestDarwinMemUsageExcludesFileCache(t *testing.T) {
	s := liveSnapshot()
	used, cached, ok := darwinMemUsage(s.pageSize, s.total, s.pages())

	if !ok {
		t.Fatal("ok = false, want true when external+purgeable are readable")
	}
	if wantCached := (s.external + s.purgeable) * s.pageSize; cached != wantCached {
		t.Errorf("cached = %d, want %d", cached, wantCached)
	}

	// This reconciles the sysctl formula against the same snapshot's Mach
	// counters — it is an internal consistency check, not a live comparison
	// against Activity Monitor. The only expected difference is memory the
	// kernel reserves outside the VM page pool (hw.memsize exceeds
	// vm.pages * pagesize), which this formula attributes to used. On this
	// snapshot that carveout is 60387 pages, 1.44% of total; anything beyond
	// 2% means the two decompositions have genuinely diverged.
	ref := s.activityMonitorUsed()
	delta := used - ref
	if used < ref {
		delta = ref - used
	}
	if off := float64(delta) / float64(s.total); off > 0.02 {
		t.Errorf("used = %d, Mach-counter reference %d: off by %.2f%% of total RAM", used, ref, off*100)
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
		name       string
		pages      darwinVMPages
		wantCached uint64 // page count
		wantOK     bool
	}{
		{
			name: "external and purgeable",
			pages: darwinVMPages{
				Free: free, External: 800_000, ExternalOK: true,
				Purgeable: 30_000, PurgeableOK: true,
			},
			wantCached: 830_000, wantOK: true,
		},
		{
			name: "purgeable missing still measures file cache",
			pages: darwinVMPages{
				Free: free, External: 800_000, ExternalOK: true,
			},
			wantCached: 800_000, wantOK: true,
		},
		{
			// Reporting total-free here is what let #521 survive its first fix:
			// it reads ~98% and is indistinguishable from a real measurement.
			name:   "no external counter reports unavailable",
			pages:  darwinVMPages{Free: free, Purgeable: 30_000, PurgeableOK: true},
			wantOK: false,
		},
		{
			name:   "no counters at all reports unavailable",
			pages:  darwinVMPages{Free: free},
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			used, cached, ok := darwinMemUsage(darwinPageSize, darwinTotal, tc.pages)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if used != 0 || cached != 0 {
					t.Errorf("unavailable = (%d, %d), want (0, 0)", used, cached)
				}
				return
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

	// Page counts far beyond physical RAM must clamp, never wrap. 1<<63 each
	// would overflow uint64 if the counts were summed before clamping.
	for _, n := range []uint64{1 << 60, 1 << 63, ^uint64(0)} {
		huge := darwinVMPages{
			Free: n, External: n, ExternalOK: true,
			Purgeable: n, PurgeableOK: true,
		}
		used, cached, ok := darwinMemUsage(darwinPageSize, darwinTotal, huge)
		if !ok {
			t.Errorf("%d: ok = false, want true", n)
		}
		if used != 0 {
			t.Errorf("%d: used = %d, want 0 when everything is reclaimable", n, used)
		}
		if cached != darwinTotal {
			t.Errorf("%d: cached = %d, want clamped to total %d", n, cached, darwinTotal)
		}
	}
}
