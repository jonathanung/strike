package telemetry

import "math/bits"

// Bounds on a macOS VM page size: 4 KiB on Intel, 16 KiB on Apple silicon.
// A value outside these means the page pool is not what we think it is.
const (
	minDarwinPageSize = 4096
	maxDarwinPageSize = 1 << 21
)

// darwinPageSize resolves the page size the vm.* counters are expressed in.
//
// hw.pagesize is per-process and lies under Rosetta: an x86_64 binary reads
// 4096 while the kernel keeps its counters in the native 16 KiB pages. That
// scales every page count 4x low and puts the RAM bar back near-full — #521
// reproduces on the darwin/amd64 release artifact, which install.sh picks
// whenever uname -m reports x86_64.
//
// vm.pages is the kernel's own page-pool size, so totalBytes/vmPages recovers
// the real page size: the pool is within a few percent of physical RAM, far
// short of the 2x that would round to the wrong power of two.
func darwinPageSize(hwPageSize, totalBytes, vmPages uint64) (uint64, bool) {
	hwOK := hwPageSize >= minDarwinPageSize && hwPageSize <= maxDarwinPageSize
	if totalBytes == 0 || vmPages == 0 {
		// Nothing to cross-check against; hw.pagesize is all there is.
		return hwPageSize, hwOK
	}
	// Guard before the shift, which would otherwise be by -1 once the ratio
	// rounds to zero.
	ratio := totalBytes / vmPages
	if ratio < minDarwinPageSize {
		return 0, false
	}
	derived := uint64(1) << (bits.Len64(ratio) - 1)
	if derived > maxDarwinPageSize {
		return 0, false
	}
	// Rosetta only ever under-reports hw.pagesize, so a derived size below it
	// is not a translation correction — it means vm.pages is not the pool this
	// code assumes. Testing `ratio < minDarwinPageSize` alone would only catch
	// that on a 4 KiB machine: on Apple silicon a pool up to 4x too large still
	// lands in [4096, 16384) and would silently derive 8192 or 4096, putting
	// the bar back at ~94% with ok=true. Fail closed instead.
	if hwOK && derived < hwPageSize {
		return 0, false
	}
	return derived, true
}

// darwinVMPages holds the macOS VM page counts the RAM metric is derived from.
// Each optional field carries its own OK flag because the set of exported vm.*
// sysctls varies by kernel version, and a missing one must degrade rather than
// silently read as zero.
type darwinVMPages struct {
	// Free is vm.page_free_count (required by the caller).
	Free uint64

	// External is the file-backed page count — Activity Monitor "Cached Files".
	// Required: without it there is no way to tell cache from real pressure.
	External   uint64
	ExternalOK bool

	// Purgeable is volatile app memory the kernel can drop under pressure.
	Purgeable   uint64
	PurgeableOK bool
}

// darwinMemUsage computes Activity Monitor-style "Memory Used" and reclaimable
// "Cached Files" from macOS VM page counts. pageSize and totalBytes are bytes.
//
// used = total − free − cached, where cached is file-backed plus purgeable
// pages. Both classes are reclaimable on demand, so counting them as used made
// the RAM bar read near-100% on any machine with a warm file cache (#521).
// The remainder is anonymous + wired + compressor memory, which is what
// Activity Monitor reports as "Memory Used".
//
// ok is false when the file-backed count is unavailable. Reporting nothing is
// deliberate: total − free is exactly the near-100% reading this fix exists to
// remove, and publishing it as if it were measured is how #521 survived its
// first fix. Callers must surface "unavailable" instead.
//
// Pure function (no syscalls) so Linux CI can lock the formula.
func darwinMemUsage(pageSize, totalBytes uint64, p darwinVMPages) (used, cached uint64, ok bool) {
	if pageSize < minDarwinPageSize || totalBytes == 0 || !p.ExternalOK {
		return 0, 0, false
	}
	// A single class cannot exceed physical RAM. If one does, the counter does
	// not mean what this code thinks it means (a units change, or an OID
	// repurposed to carry something else), and clamping would turn that into a
	// confident wrong number — the #534 failure mode. Report unavailable.
	totalPages := totalBytes / pageSize
	if p.Free > totalPages || p.External > totalPages || p.Purgeable > totalPages {
		return 0, 0, false
	}

	cachedPages := p.External
	if p.PurgeableOK {
		// Purgeable pages are anonymous, so they never overlap External.
		cachedPages += p.Purgeable
	}
	// Each term is <= totalPages, so the sum cannot overflow. Apply the same
	// rule to the sum: free and cache are disjoint subsets of a pool strictly
	// smaller than RAM, so exceeding total is impossible on a healthy machine.
	// Clamping instead would report a confident "0 B used · 0.0%" if a counter
	// ever drifted in units — the same failure mode as the per-class guard.
	if p.Free+cachedPages > totalPages {
		return 0, 0, false
	}
	return totalBytes - (p.Free+cachedPages)*pageSize, cachedPages * pageSize, true
}
