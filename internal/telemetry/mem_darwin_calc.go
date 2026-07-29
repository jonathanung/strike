package telemetry

// darwinMemPressure computes Activity Monitor-like "Memory Used" and reclaimable
// cache from VM page counts. pageSize and totalBytes are bytes; the remaining
// arguments are page counts from sysctl (vm.page_* / vm.compressor_page_count).
//
// Used ≈ wired + compressor + active − purgeable (purgeable is a subset of
// active/internal app pages). File cache (inactive + speculative + purgeable)
// is reported separately so the RAM bar does not treat macOS file cache as
// pressure — matching htop without "show cached memory" and Activity Monitor
// "Memory Used" vs "Cached Files".
//
// Pure function (no syscalls) so Linux CI can lock the formula.
func darwinMemPressure(pageSize, totalBytes uint64, free, active, inactive, speculative, wired, purgeable, compressor uint64) (used, cached uint64) {
	if pageSize == 0 {
		return 0, 0
	}
	// Purgeable pages are counted inside active/internal; subtract once.
	appActive := active
	if purgeable < appActive {
		appActive -= purgeable
	} else {
		appActive = 0
	}
	usedPages := wired + compressor + appActive
	cachedPages := inactive + speculative + purgeable

	used = usedPages * pageSize
	cached = cachedPages * pageSize
	if totalBytes > 0 {
		if used > totalBytes {
			used = totalBytes
		}
		if cached > totalBytes {
			cached = totalBytes
		}
	}
	_ = free // free is neither used nor cache; kept for call-site clarity / future
	return used, cached
}
