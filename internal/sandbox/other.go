//go:build !linux && !darwin

package sandbox

func probePlatform() availInfo {
	return availInfo{
		warn: "OS process sandbox is not supported on this platform; bash runs unsandboxed",
	}
}

func wrapPlatform(argv []string, policy Policy) []string {
	_ = policy
	// Unreachable when Available is false; Wrap degrades before calling.
	return cloneArgv(argv)
}
