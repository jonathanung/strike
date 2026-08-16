package sandbox

import "sync"

// availInfo is the cached probe result for the platform backend.
type availInfo struct {
	ok   bool
	name string
	warn string
}

var (
	availMu       sync.Mutex
	availOnce     sync.Once
	availCached   availInfo
	platformProbe = probePlatform
)

func availability() availInfo {
	availOnce.Do(func() {
		availCached = platformProbe()
	})
	availMu.Lock()
	defer availMu.Unlock()
	return availCached
}

func resetAvailabilityForTest() {
	availMu.Lock()
	defer availMu.Unlock()
	availOnce = sync.Once{}
	availCached = availInfo{}
	clearLauncherForTest()
}

// forceSetAvailabilityForTest pins the probe result without running the backend.
func forceSetAvailabilityForTest(info availInfo) {
	availMu.Lock()
	defer availMu.Unlock()
	// Mark Once done so probePlatform is not re-run, then pin the value.
	availOnce.Do(func() {})
	availCached = info
}
