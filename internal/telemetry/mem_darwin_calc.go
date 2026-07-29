package telemetry

// darwinVMPages holds the macOS VM page counts the RAM metric is derived from.
// Each optional field carries its own OK flag because the set of exported
// vm.* sysctls varies by kernel version, and a missing one must degrade rather
// than silently read as zero.
type darwinVMPages struct {
	// Free is vm.page_free_count (required by the caller).
	Free uint64

	// External is the file-backed page count — Activity Monitor "Cached Files".
	External   uint64
	ExternalOK bool

	// Purgeable is volatile app memory the kernel can drop under pressure.
	Purgeable   uint64
	PurgeableOK bool

	// Speculative is read-ahead file cache. Only used as an External fallback;
	// it is already a subset of External when that count is available.
	Speculative   uint64
	SpeculativeOK bool
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
// Pure function (no syscalls) so Linux CI can lock the formula.
func darwinMemUsage(pageSize, totalBytes uint64, p darwinVMPages) (used, cached uint64, cachedOK bool) {
	if pageSize == 0 || totalBytes == 0 {
		return 0, 0, false
	}
	totalPages := totalBytes / pageSize

	var cachedPages uint64
	switch {
	case p.ExternalOK:
		cachedPages = p.External
		cachedOK = true
	case p.SpeculativeOK:
		// Kernel without an external-page count: read-ahead is the only file
		// cache class visible. Under-reports cache, never over-reports it.
		cachedPages = p.Speculative
		cachedOK = true
	}
	if p.PurgeableOK {
		// Purgeable pages are anonymous, so they never overlap External.
		cachedPages += p.Purgeable
		cachedOK = true
	}

	// Clamp page counts before scaling so bogus sysctl values cannot overflow.
	if cachedPages > totalPages {
		cachedPages = totalPages
	}
	freePages := p.Free
	if freePages > totalPages {
		freePages = totalPages
	}

	cached = cachedPages * pageSize
	reclaimable := (freePages + cachedPages) * pageSize
	if reclaimable > totalBytes {
		reclaimable = totalBytes
	}
	return totalBytes - reclaimable, cached, cachedOK
}
