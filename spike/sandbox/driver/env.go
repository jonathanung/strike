package main

import "strings"

// cleanEnv keeps a small allow-list so a host provider key cannot ride into
// virtiofsd or qemu. QEMU does not forward its environment into the guest;
// this is still the list we are willing to put next to the hypervisor.
func cleanEnv(environ []string) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"LANG": true, "LC_ALL": true, "TERM": true, "TMPDIR": true,
		"XDG_RUNTIME_DIR": true, "NIX_SSL_CERT_FILE": true, "SSL_CERT_FILE": true,
	}
	var out []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !allow[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
