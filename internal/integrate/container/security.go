package container

// SecurityFlags are hardened defaults applied to every strike-managed container.
type SecurityFlags struct {
	SecurityOpt []string
	CapDrop     []string
	CapAdd      []string
	PidsLimit   int64
}

// DefaultSecurityFlags returns no-new-privileges, drop ALL, add back common FS caps.
func DefaultSecurityFlags() SecurityFlags {
	return SecurityFlags{
		SecurityOpt: []string{"no-new-privileges"},
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"CHOWN", "DAC_OVERRIDE", "SETGID", "SETUID", "FOWNER"},
		PidsLimit:   512,
	}
}
