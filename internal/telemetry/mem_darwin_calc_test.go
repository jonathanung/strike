package telemetry

import "testing"

const (
	testPageSize = 16384                   // Apple silicon
	testTotal    = 24 * 1024 * 1024 * 1024 // 24 GiB, the machine in #521
)

// darwinSnapshot is a real sysctl/vm_stat capture from a 64 GiB M-series Mac.
// Fed through darwinMemUsage it yields 53.76 GB used / 13.91 GB cached (78.2%);
// the same snapshot's Mach counters put Activity Monitor's "Memory Used" at
// 52.77 GB. Before this fix the same machine reported 67.67 GB (98.5%).
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
			used, cached, ok := darwinMemUsage(testPageSize, testTotal, tc.pages)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if used != 0 || cached != 0 {
					t.Errorf("unavailable = (%d, %d), want (0, 0)", used, cached)
				}
				return
			}
			if want := tc.wantCached * testPageSize; cached != want {
				t.Errorf("cached = %d, want %d", cached, want)
			}
			// used + free + cached always accounts for all of physical RAM.
			if want := testTotal - (free+tc.wantCached)*testPageSize; used != want {
				t.Errorf("used = %d, want %d", used, want)
			}
		})
	}
}

func TestDarwinMemUsageGuardsBadInput(t *testing.T) {
	full := darwinVMPages{Free: 1, External: 1, ExternalOK: true, Purgeable: 1, PurgeableOK: true}

	if used, cached, ok := darwinMemUsage(0, testTotal, full); used != 0 || cached != 0 || ok {
		t.Errorf("zero page size = (%d, %d, %v), want (0, 0, false)", used, cached, ok)
	}
	if used, cached, ok := darwinMemUsage(testPageSize, 0, full); used != 0 || cached != 0 || ok {
		t.Errorf("zero total = (%d, %d, %v), want (0, 0, false)", used, cached, ok)
	}

	// A page count larger than physical RAM means the counter does not mean
	// what we think it does. Clamping it would render a confident 0.0% used;
	// report unavailable instead.
	const totalPages = testTotal / testPageSize
	for _, n := range []uint64{totalPages + 1, 1 << 60, 1 << 63, ^uint64(0)} {
		for _, tc := range []struct {
			name  string
			pages darwinVMPages
		}{
			{"external", darwinVMPages{Free: 1, External: n, ExternalOK: true}},
			{"purgeable", darwinVMPages{Free: 1, External: 1, ExternalOK: true, Purgeable: n, PurgeableOK: true}},
			{"free", darwinVMPages{Free: n, External: 1, ExternalOK: true}},
		} {
			used, cached, ok := darwinMemUsage(testPageSize, testTotal, tc.pages)
			if ok || used != 0 || cached != 0 {
				t.Errorf("%s = %d: got (%d, %d, %v), want (0, 0, false)", tc.name, n, used, cached, ok)
			}
		}
	}

	// Terms individually within bounds but summing past total: clamping here
	// would render a confident "0 B used · 0.0%" as if it were measured.
	const half = totalPages/2 + 1
	overSum := darwinVMPages{
		Free: half, External: half, ExternalOK: true,
	}
	if used, cached, ok := darwinMemUsage(testPageSize, testTotal, overSum); ok || used != 0 || cached != 0 {
		t.Errorf("free+cached over total = (%d, %d, %v), want (0, 0, false)", used, cached, ok)
	}

	// Exactly accounting for every page is legitimate: used is 0 because
	// nothing is left, not because a counter was truncated.
	exact := darwinVMPages{Free: 1, External: totalPages - 1, ExternalOK: true}
	used, cached, ok := darwinMemUsage(testPageSize, testTotal, exact)
	if !ok {
		t.Fatal("free+cached == total: ok = false, want true")
	}
	if used != 0 || cached != (totalPages-1)*testPageSize {
		t.Errorf("free+cached == total = (%d, %d), want (0, %d)", used, cached, (totalPages-1)*testPageSize)
	}
}

// Regression for #521 on the darwin/amd64 release artifact: under Rosetta,
// hw.pagesize reports 4096 while the kernel keeps its vm.* counters in native
// 16 KiB pages, scaling every count 4x low and pinning the RAM bar near full.
func TestDarwinPageSizeCorrectsRosettaLie(t *testing.T) {
	const (
		total   = 68719476736 // 64 GiB
		vmPages = 4100101     // kernel page pool, measured on the same machine
	)

	tests := []struct {
		name       string
		hwPageSize uint64
		total      uint64
		vmPages    uint64
		want       uint64
		wantOK     bool
	}{
		{"native arm64", 16384, total, vmPages, 16384, true},
		{"rosetta lies 4096", 4096, total, vmPages, 16384, true},
		{"intel 4 KiB pages", 4096, 17179869184, 4128768, 4096, true},
		{"no pool to check against", 16384, total, 0, 16384, true},
		{"no pool and no hw.pagesize", 0, total, 0, 0, false},
		{"absurd pool implies bad page size", 4096, total, 1 << 40, 0, false},

		// A pool larger than RAM lands in [4096, 16384) on Apple silicon, so
		// the ratio floor alone would derive 8192/4096 and put the bar back at
		// ~94% with ok=true. Rosetta only under-reports hw.pagesize, so a
		// derived size below it is always an anomaly.
		{"pool one page over total", 16384, total, total/16384 + 1, 0, false},
		{"pool 3x total derives 4096", 16384, total, 3 * (total / 16384), 0, false},
		{"pool 2x total derives 8192", 16384, total, 2 * (total / 16384), 0, false},
		// Tiny pool would derive an absurd page size; bounded from above.
		{"single-page pool", 16384, total, 1, 0, false},
		{"hw.pagesize absurd, no pool", 1 << 40, total, 0, 1 << 40, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := darwinPageSize(tc.hwPageSize, tc.total, tc.vmPages)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("pageSize = %d, want %d", got, tc.want)
			}
		})
	}
}

// The Rosetta page size must not merely be corrected in isolation — it has to
// move the RAM bar back off near-full.
func TestDarwinMemUsageUnaffectedByRosettaPageSize(t *testing.T) {
	s := liveSnapshot()
	const vmPages = 4100101

	native, ok := darwinPageSize(16384, s.total, vmPages)
	if !ok {
		t.Fatal("native page size unavailable")
	}
	rosetta, ok := darwinPageSize(4096, s.total, vmPages)
	if !ok {
		t.Fatal("rosetta page size unavailable")
	}
	if native != rosetta {
		t.Fatalf("page size differs by arch: native %d, rosetta %d", native, rosetta)
	}

	usedNative, cachedNative, _ := darwinMemUsage(native, s.total, s.pages())
	usedRosetta, cachedRosetta, _ := darwinMemUsage(rosetta, s.total, s.pages())
	if usedNative != usedRosetta || cachedNative != cachedRosetta {
		t.Errorf("arch-dependent reading: native (%d, %d), rosetta (%d, %d)",
			usedNative, cachedNative, usedRosetta, cachedRosetta)
	}
	// Trusting hw.pagesize=4096 is what reproduced #521 at ~95%.
	usedUncorrected, _, _ := darwinMemUsage(4096, s.total, s.pages())
	if r := float64(usedUncorrected) / float64(s.total); r < 0.9 {
		t.Fatalf("uncorrected page size no longer reproduces the bug: ratio %.3f", r)
	}
	if r := float64(usedNative) / float64(s.total); r > 0.85 {
		t.Errorf("corrected used ratio %.3f still reads near-full", r)
	}
}
