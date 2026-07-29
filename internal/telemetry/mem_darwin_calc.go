package telemetry

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
	if pageSize == 0 || totalBytes == 0 || !p.ExternalOK {
		return 0, 0, false
	}
	// Clamp every page count to the physical page pool before any arithmetic,
	// so a bogus sysctl value can neither overflow a sum nor scale past total.
	totalPages := totalBytes / pageSize
	cachedPages := clampPages(p.External, totalPages)
	if p.PurgeableOK {
		// Purgeable pages are anonymous, so they never overlap External.
		cachedPages = clampPages(cachedPages+clampPages(p.Purgeable, totalPages), totalPages)
	}
	freePages := clampPages(p.Free, totalPages)

	reclaimable := (freePages + cachedPages) * pageSize
	if reclaimable > totalBytes {
		reclaimable = totalBytes
	}
	return totalBytes - reclaimable, cachedPages * pageSize, true
}

func clampPages(pages, max uint64) uint64 {
	if pages > max {
		return max
	}
	return pages
}
