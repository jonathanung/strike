package tool

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/jonathanung/strike-cli/internal/actionfacts"
	"github.com/jonathanung/strike-cli/internal/sandbox"
)

// checkBashNetworkAllow rejects known network clients whose destinations fall
// outside allow when allow is non-empty. Empty allow means unrestricted
// (same semantics as webfetch). Shares sandbox.CheckNetworkAllow with webfetch.
//
// Best-effort static guard: walks statements, wrappers, and command
// substitutions like the workspace boundary guard. When a network client is
// detected but the destination cannot be statically bound (variables,
// missing host), the call is denied so allowlist policy cannot be bypassed
// by unparseable forms.
func checkBashNetworkAllow(command string, allow []string) error {
	if len(allow) == 0 {
		return nil
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	// Prefer hosts projected by actionfacts (#888) when present, then the
	// dedicated argv preflight (fail-closed on known clients).
	facts := actionfacts.Analyze(actionfacts.Input{Tool: "bash", Command: command})
	for _, n := range facts.Network {
		host := strings.TrimSpace(n.Host)
		if host == "" || isAllDigits(host) {
			// Skip empty / port-only tokens some projections emit for -p N.
			continue
		}
		if err := sandbox.CheckNetworkAllow(host, allow); err != nil {
			return errNetworkDenied(fmt.Sprintf("host %q is not on the network allowlist (bash preflight)", host))
		}
	}
	return checkBashNetworkCommand(command, allow, 0)
}

func checkBashNetworkCommand(command string, allow []string, depth int) error {
	if depth > checkBashMaxDepth {
		return errNetworkDenied("nesting too deep to verify network destinations")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	for _, sub := range extractCommandSubstitutions(command) {
		if err := checkBashNetworkCommand(sub, allow, depth+1); err != nil {
			return err
		}
	}
	for _, stmt := range splitBashStatements(command) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := checkBashNetworkStatement(stmt, allow, depth); err != nil {
			return err
		}
	}
	return nil
}

func checkBashNetworkStatement(stmt string, allow []string, depth int) error {
	_, rest := peelRedirections(stmt)
	words := shellWords(rest)
	if len(words) == 0 {
		return nil
	}
	i := 0
	for i < len(words) && isEnvAssign(words[i]) {
		i++
	}
	if i >= len(words) {
		return nil
	}
	return checkBashNetworkWords(words[i:], allow, depth)
}

func checkBashNetworkWords(words []string, allow []string, depth int) error {
	if depth > checkBashMaxDepth {
		return errNetworkDenied("nesting too deep to verify network destinations")
	}
	if len(words) == 0 {
		return nil
	}
	cmd := commandBase(words[0])
	args := words[1:]

	switch cmd {
	case "sh", "bash", "zsh", "dash":
		return checkShellCNetwork(args, allow, depth+1)
	case "env":
		return checkEnvNetwork(args, allow, depth+1)
	case "eval":
		return checkBashNetworkCommand(strings.Join(args, " "), allow, depth+1)
	case "nohup", "nice", "time":
		return checkBashNetworkWords(stripLeadingWrapperFlags(cmd, args), allow, depth+1)
	case "timeout":
		return checkTimeoutNetwork(args, allow, depth+1)
	case "xargs":
		return checkXargsNetwork(args, allow, depth+1)
	case "curl":
		return checkCurlNetwork(args, allow)
	case "wget":
		return checkWgetNetwork(args, allow)
	case "ssh":
		return checkSSHNetwork(args, allow)
	case "scp":
		return checkSCPNetwork(args, allow)
	case "sftp":
		return checkSFTPNetwork(args, allow)
	case "nc", "ncat", "netcat":
		return checkNetcatNetwork(args, allow)
	}
	return nil
}

func checkShellCNetwork(args []string, allow []string, depth int) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--command":
			if i+1 >= len(args) {
				return nil
			}
			return checkBashNetworkCommand(args[i+1], allow, depth)
		case strings.HasPrefix(a, "-c") && a != "-c" && !strings.HasPrefix(a, "--"):
			return checkBashNetworkCommand(a[2:], allow, depth)
		case a == "--":
			continue
		case strings.HasPrefix(a, "-"):
			if a == "-o" || a == "-O" {
				i++
			}
			continue
		default:
			return nil
		}
	}
	return nil
}

func checkEnvNetwork(args []string, allow []string, depth int) error {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-u") && a != "-u" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			i++
			continue
		}
		if isEnvAssign(a) {
			i++
			continue
		}
		return checkBashNetworkWords(args[i:], allow, depth)
	}
	if i < len(args) {
		return checkBashNetworkWords(args[i:], allow, depth)
	}
	return nil
}

func checkTimeoutNetwork(args []string, allow []string, depth int) error {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				i++
				continue
			}
			switch a {
			case "-k", "--kill-after", "-s", "--signal":
				i += 2
			default:
				i++
			}
			continue
		}
		i++ // duration
		if i >= len(args) {
			return nil
		}
		return checkBashNetworkWords(args[i:], allow, depth)
	}
	if i < len(args) {
		return checkBashNetworkWords(args[i:], allow, depth)
	}
	return nil
}

func checkXargsNetwork(args []string, allow []string, depth int) error {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				i++
				continue
			}
			switch a {
			case "-n", "-P", "-s", "-E", "-e", "-I", "-i", "-L", "-l",
				"--max-args", "--max-procs", "--max-chars", "--eof",
				"--replace", "--max-lines", "-a", "--arg-file",
				"-d", "--delimiter":
				i += 2
			default:
				if len(a) > 2 && a[1] != '-' && strings.ContainsAny(a[2:], "0123456789") {
					i++
					continue
				}
				i++
			}
			continue
		}
		break
	}
	if i >= len(args) {
		return nil
	}
	return checkBashNetworkWords(args[i:], allow, depth)
}

// curl option forms that take a following argument (or =value).
var curlOptWithArg = map[string]struct{}{
	"-o": {}, "--output": {}, "-O": {}, "--remote-name": {},
	"-d": {}, "--data": {}, "--data-raw": {}, "--data-binary": {}, "--data-urlencode": {},
	"-H": {}, "--header": {}, "-A": {}, "--user-agent": {},
	"-e": {}, "--referer": {}, "-u": {}, "--user": {},
	"-b": {}, "--cookie": {}, "-c": {}, "--cookie-jar": {},
	"-T": {}, "--upload-file": {}, "-X": {}, "--request": {},
	"-x": {}, "--proxy": {}, "--proxy-user": {},
	"-w": {}, "--write-out": {}, "-m": {}, "--max-time": {},
	"--connect-timeout": {}, "-r": {}, "--range": {},
	"-E": {}, "--cert": {}, "--key": {}, "--cacert": {}, "--capath": {},
	"--resolve": {}, "--connect-to": {}, "--url": {},
	"--unix-socket": {}, "--abstract-unix-socket": {},
	"-F": {}, "--form": {}, "--form-string": {},
	"--retry": {}, "--retry-delay": {}, "--retry-max-time": {},
	"-Y": {}, "--speed-limit": {}, "-y": {}, "--speed-time": {},
	"--max-filesize": {}, "--proto": {}, "--proto-redir": {},
	"--interface": {}, "--local-port": {}, "--dns-servers": {},
	"--doh-url": {}, "--proxy-header": {}, "--pinnedpubkey": {},
	"--hostpubmd5": {}, "--hostpubsha256": {},
	"-K": {}, "--config": {}, "--stderr": {},
	"--trace": {}, "--trace-ascii": {}, "--dump-header": {}, "-D": {},
	"--output-dir": {}, "--parallel-max": {},
	"-a": {}, // --append used with -o; no arg but listed for completeness elsewhere
}

func checkCurlNetwork(args []string, allow []string) error {
	var hosts []string
	sawURLOpt := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, p := range args[i+1:] {
				h, ok, err := hostFromURLArg(p)
				if err != nil {
					return err
				}
				if ok {
					hosts = append(hosts, h)
				}
			}
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name, val, hasVal := splitOpt(a)
			// Proxy is itself an egress destination.
			if name == "-x" || name == "--proxy" {
				if !hasVal {
					if i+1 >= len(args) {
						return errNetworkDenied("curl proxy destination is missing")
					}
					i++
					val = args[i]
				}
				h, ok, err := hostFromURLArg(val)
				if err != nil {
					return err
				}
				if !ok {
					return errNetworkDenied("curl proxy destination is not statically bound")
				}
				hosts = append(hosts, h)
				continue
			}
			if name == "--url" || name == "--doh-url" {
				sawURLOpt = true
				if !hasVal {
					if i+1 >= len(args) {
						return errNetworkDenied("curl URL is missing")
					}
					i++
					val = args[i]
				}
				h, ok, err := hostFromURLArg(val)
				if err != nil {
					return err
				}
				if !ok {
					return errNetworkDenied("curl URL is not statically bound")
				}
				hosts = append(hosts, h)
				continue
			}
			if name == "--resolve" || name == "--connect-to" {
				// HOST:PORT:ADDR — first field is the logical host being contacted.
				if !hasVal {
					if i+1 >= len(args) {
						continue
					}
					i++
					val = args[i]
				}
				if h, ok := hostFromResolveSpec(val); ok {
					hosts = append(hosts, h)
				}
				continue
			}
			if _, need := curlOptWithArg[name]; need {
				if !hasVal {
					i++
				}
				continue
			}
			// Combined short options without args (-fsSL) or unknown long flags.
			continue
		}
		// Positional URL.
		h, ok, err := hostFromURLArg(a)
		if err != nil {
			return err
		}
		if ok {
			hosts = append(hosts, h)
			sawURLOpt = true
		}
	}
	if len(hosts) == 0 {
		if sawURLOpt {
			return errNetworkDenied("curl destination could not be determined")
		}
		// curl with no URL (e.g. only -h) — nothing to enforce.
		return nil
	}
	return checkHostsAllowed(hosts, allow)
}

func checkWgetNetwork(args []string, allow []string) error {
	var hosts []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, p := range args[i+1:] {
				h, ok, err := hostFromURLArg(p)
				if err != nil {
					return err
				}
				if ok {
					hosts = append(hosts, h)
				}
			}
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name, val, hasVal := splitOpt(a)
			// wget -O file, -o logfile, -P prefix, -e directive, --header, -U, -t, -T, -w, -Q, -O-
			switch name {
			case "-O", "--output-document", "-o", "--output-file", "-a", "--append-output",
				"-i", "--input-file", "-B", "--base", "-P", "--directory-prefix",
				"-e", "--execute", "--header", "-U", "--user-agent", "-t", "--tries",
				"-T", "--timeout", "-w", "--wait", "-Q", "--quota",
				"--bind-address", "--proxy-user", "--proxy-password",
				"--user", "--password", "--load-cookies", "--save-cookies",
				"--post-data", "--post-file", "--method", "--body-data", "--body-file",
				"--secure-protocol", "--certificate", "--private-key", "--ca-certificate",
				"--ca-directory", "--random-file", "--egd-file":
				if !hasVal {
					i++
				}
				_ = val
				continue
			case "--proxy":
				// wget --proxy=on/off is not a host; --proxy=http://… rare.
				if hasVal && (strings.Contains(val, "://") || strings.Contains(val, ".")) {
					h, ok, err := hostFromURLArg(val)
					if err != nil {
						return err
					}
					if ok {
						hosts = append(hosts, h)
					}
				}
				continue
			default:
				continue
			}
		}
		h, ok, err := hostFromURLArg(a)
		if err != nil {
			return err
		}
		if ok {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		// wget with only flags — nothing to enforce.
		return nil
	}
	return checkHostsAllowed(hosts, allow)
}

func checkSSHNetwork(args []string, allow []string) error {
	var hosts []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				h, ok, err := hostFromSSHDest(args[i+1])
				if err != nil {
					return err
				}
				if !ok {
					return errNetworkDenied("ssh destination is not statically bound")
				}
				hosts = append(hosts, h)
			}
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name, val, hasVal := splitOpt(a)
			// Jump hosts are egress destinations.
			if name == "-J" || name == "--jump" {
				if !hasVal {
					if i+1 >= len(args) {
						return errNetworkDenied("ssh jump host is missing")
					}
					i++
					val = args[i]
				}
				if err := appendSSHJumpHosts(&hosts, val); err != nil {
					return err
				}
				continue
			}
			if name == "-o" {
				if !hasVal {
					if i+1 >= len(args) {
						continue
					}
					i++
					val = args[i]
				}
				low := strings.ToLower(val)
				if strings.HasPrefix(low, "proxyjump=") {
					if idx := strings.IndexByte(val, '='); idx >= 0 {
						if err := appendSSHJumpHosts(&hosts, val[idx+1:]); err != nil {
							return err
						}
					}
				}
				continue
			}
			// Options with args: -i, -F, -l, -p, -b, -c, -D, -L, -R, -W, -S, …
			if sshOptTakesArg(name) {
				if !hasVal {
					i++
				}
				continue
			}
			continue
		}
		// First positional is [user@]host; remainder is remote command.
		h, ok, err := hostFromSSHDest(a)
		if err != nil {
			return err
		}
		if !ok {
			return errNetworkDenied("ssh destination is not statically bound")
		}
		hosts = append(hosts, h)
		break
	}
	if len(hosts) == 0 {
		return errNetworkDenied("ssh destination is missing")
	}
	return checkHostsAllowed(hosts, allow)
}

func sshOptTakesArg(name string) bool {
	switch name {
	case "-b", "-c", "-D", "-E", "-e", "-F", "-I", "-i", "-L", "-l",
		"-m", "-O", "-o", "-p", "-Q", "-R", "-S", "-W", "-w",
		"--":
		return true
	default:
		// Long options are uncommon for OpenSSH CLI; treat unknown -x as no-arg.
		return false
	}
}

func checkSCPNetwork(args []string, allow []string) error {
	var hosts []string
	sawRemote := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, p := range args[i+1:] {
				h, ok, err := hostFromSCPArg(p)
				if err != nil {
					return err
				}
				if ok {
					hosts = append(hosts, h)
					sawRemote = true
				}
			}
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name, _, hasVal := splitOpt(a)
			// scp shares many ssh options; -J jump, -o, -P port, -i, -F, -c, -S
			if name == "-J" || name == "-o" || name == "-P" || name == "-i" ||
				name == "-F" || name == "-c" || name == "-S" || name == "-l" {
				if name == "-J" {
					if !hasVal {
						if i+1 >= len(args) {
							return errNetworkDenied("scp jump host is missing")
						}
						i++
						val := args[i]
						for _, part := range strings.Split(val, ",") {
							h, ok, err := hostFromSSHDest(strings.TrimSpace(part))
							if err != nil {
								return err
							}
							if !ok {
								return errNetworkDenied("scp jump host is not statically bound")
							}
							hosts = append(hosts, h)
						}
						continue
					}
				}
				if !hasVal {
					i++
				}
				continue
			}
			continue
		}
		h, ok, err := hostFromSCPArg(a)
		if err != nil {
			return err
		}
		if ok {
			hosts = append(hosts, h)
			sawRemote = true
		}
	}
	if !sawRemote && len(hosts) == 0 {
		// Local-only scp is unusual; if no remote form, nothing to enforce.
		return nil
	}
	if len(hosts) == 0 {
		return errNetworkDenied("scp/sftp destination is not statically bound")
	}
	return checkHostsAllowed(hosts, allow)
}

func checkSFTPNetwork(args []string, allow []string) error {
	// sftp [options] [user@]host[:path]
	var hosts []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				h, ok, err := hostFromSFTPDest(args[i+1])
				if err != nil {
					return err
				}
				if !ok {
					return errNetworkDenied("sftp destination is not statically bound")
				}
				hosts = append(hosts, h)
			}
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name, val, hasVal := splitOpt(a)
			if name == "-J" || name == "--jump" {
				if !hasVal {
					if i+1 >= len(args) {
						return errNetworkDenied("sftp jump host is missing")
					}
					i++
					val = args[i]
				}
				if err := appendSSHJumpHosts(&hosts, val); err != nil {
					return err
				}
				continue
			}
			if name == "-o" {
				if !hasVal {
					if i+1 >= len(args) {
						continue
					}
					i++
					val = args[i]
				}
				low := strings.ToLower(val)
				if strings.HasPrefix(low, "proxyjump=") {
					if idx := strings.IndexByte(val, '='); idx >= 0 {
						if err := appendSSHJumpHosts(&hosts, val[idx+1:]); err != nil {
							return err
						}
					}
				}
				continue
			}
			if sshOptTakesArg(name) || name == "-b" || name == "-B" || name == "-R" || name == "-D" {
				if !hasVal {
					i++
				}
				continue
			}
			continue
		}
		h, ok, err := hostFromSFTPDest(a)
		if err != nil {
			return err
		}
		if !ok {
			return errNetworkDenied("sftp destination is not statically bound")
		}
		hosts = append(hosts, h)
		break
	}
	if len(hosts) == 0 {
		return errNetworkDenied("sftp destination is missing")
	}
	return checkHostsAllowed(hosts, allow)
}

func hostFromSFTPDest(raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, nil
	}
	// sftp accepts [user@]host or [user@]host:path
	if c := strings.IndexByte(raw, ':'); c > 0 && !strings.Contains(raw, "://") {
		// Only treat as host:path when left side looks like a host (not C:\ path).
		left := raw[:c]
		if !strings.Contains(left, "/") {
			return hostFromSSHDest(left)
		}
	}
	return hostFromSSHDest(raw)
}

func checkNetcatNetwork(args []string, allow []string) error {
	// nc [options] host port
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name, _, hasVal := splitOpt(a)
			// Options with args: -p, -s, -e, -c, -X, -x, -w, -q, -P
			switch name {
			case "-p", "-s", "-e", "-c", "-X", "-x", "-w", "-q", "-P",
				"--source-port", "--source", "--wait", "--proxy":
				if !hasVal {
					i++
				}
			}
			continue
		}
		positionals = append(positionals, a)
	}
	if len(positionals) == 0 {
		return nil
	}
	host := positionals[0]
	// If first positional is a port-only listen mode (-l), host may be absent.
	h, ok, err := hostFromSSHDest(host)
	if err != nil {
		return err
	}
	if !ok {
		// Numeric-only might be port in some nc forms; skip if no host-like token.
		if isAllDigits(host) {
			return nil
		}
		return errNetworkDenied("netcat destination is not statically bound")
	}
	return checkHostsAllowed([]string{h}, allow)
}

func appendSSHJumpHosts(hosts *[]string, val string) error {
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		h, ok, err := hostFromSSHDest(part)
		if err != nil {
			return err
		}
		if !ok {
			return errNetworkDenied("ssh jump host is not statically bound")
		}
		*hosts = append(*hosts, h)
	}
	return nil
}

func checkHostsAllowed(hosts []string, allow []string) error {
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			return errNetworkDenied("empty network destination")
		}
		if err := sandbox.CheckNetworkAllow(h, allow); err != nil {
			// Dest class only — hostname/IP, not full URL (may carry secrets in path).
			return errNetworkDenied(fmt.Sprintf("host %q is not on the network allowlist (bash preflight)", h))
		}
	}
	return nil
}

func errNetworkDenied(msg string) error {
	return ErrNetworkDenied(msg)
}

// hostFromURLArg extracts a hostname from a URL or host:port form.
// ok=false means the token is not a network destination (e.g. local path).
// err is set when the token looks like a destination but is not statically bound.
func hostFromURLArg(raw string) (host string, ok bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, nil
	}
	if !isStaticToken(raw) {
		// Looks like URL/host with expansion — fail closed.
		if looksLikeNetworkDest(raw) {
			return "", false, errNetworkDenied(fmt.Sprintf("network destination %q is not statically bound", redactDest(raw)))
		}
		return "", false, nil
	}
	// URL with scheme
	if strings.Contains(raw, "://") {
		u, perr := url.Parse(raw)
		if perr != nil || u.Host == "" {
			return "", false, errNetworkDenied(fmt.Sprintf("invalid network URL %q", redactDest(raw)))
		}
		h := u.Hostname()
		if h == "" {
			return "", false, errNetworkDenied(fmt.Sprintf("invalid network URL %q", redactDest(raw)))
		}
		return h, true, nil
	}
	// scheme-relative //host/path
	if strings.HasPrefix(raw, "//") {
		u, perr := url.Parse("http:" + raw)
		if perr != nil || u.Hostname() == "" {
			return "", false, errNetworkDenied(fmt.Sprintf("invalid network URL %q", redactDest(raw)))
		}
		return u.Hostname(), true, nil
	}
	// host:port (no path) — common curl form without scheme
	if h, ok := hostPortOnly(raw); ok {
		return h, true, nil
	}
	// Bare hostname / IP (must look like a host, not a flag or local file)
	if looksLikeBareHost(raw) {
		return strings.ToLower(strings.TrimSuffix(raw, ".")), true, nil
	}
	return "", false, nil
}

func hostFromSSHDest(raw string) (host string, ok bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, nil
	}
	if !isStaticToken(raw) {
		return "", false, errNetworkDenied(fmt.Sprintf("ssh destination %q is not statically bound", redactDest(raw)))
	}
	// Strip user@
	if at := strings.LastIndexByte(raw, '@'); at >= 0 {
		raw = raw[at+1:]
	}
	// IPv6 in brackets [2001:db8::1]
	if strings.HasPrefix(raw, "[") {
		end := strings.IndexByte(raw, ']')
		if end <= 1 {
			return "", false, errNetworkDenied("invalid ssh destination")
		}
		return strings.ToLower(raw[1:end]), true, nil
	}
	// host or host:port — for ssh, :port is uncommon (use -p); still strip.
	if h, ok := hostPortOnly(raw); ok {
		return h, true, nil
	}
	if looksLikeBareHost(raw) || net.ParseIP(raw) != nil {
		return strings.ToLower(strings.TrimSuffix(raw, ".")), true, nil
	}
	// ssh destination can be a bare hostname with no dots (e.g. "gateway")
	if isSimpleHostname(raw) {
		return strings.ToLower(raw), true, nil
	}
	return "", false, errNetworkDenied(fmt.Sprintf("ssh destination %q is not statically bound", redactDest(raw)))
}

func hostFromSCPArg(raw string) (host string, ok bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, nil
	}
	// scp remote form: [user@]host:path  (colon required; local paths have no host)
	// Windows drive letters (C:\) are not treated as remote on Unix agents.
	if !strings.Contains(raw, ":") {
		return "", false, nil
	}
	// Avoid treating URLs as scp — delegate to URL parser.
	if strings.Contains(raw, "://") {
		return hostFromURLArg(raw)
	}
	if !isStaticToken(raw) {
		return "", false, errNetworkDenied(fmt.Sprintf("scp destination %q is not statically bound", redactDest(raw)))
	}
	// Split on first unbracketed colon.
	hostPart := raw
	if strings.HasPrefix(raw, "[") {
		end := strings.IndexByte(raw, ']')
		if end < 0 {
			return "", false, errNetworkDenied("invalid scp destination")
		}
		if end+1 < len(raw) && raw[end+1] == ':' {
			return hostFromSSHDest(raw[:end+1])
		}
		return "", false, nil
	}
	// user@host:path
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 {
		return "", false, nil
	}
	hostPart = raw[:colon]
	// Local relative path like "./file:name" — rare; require host-like left side.
	return hostFromSSHDest(hostPart)
}

func hostFromResolveSpec(spec string) (string, bool) {
	// curl --resolve host:port:address
	spec = strings.TrimSpace(spec)
	if spec == "" || !isStaticToken(spec) {
		return "", false
	}
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return "", false
	}
	h := parts[0]
	if h == "" {
		return "", false
	}
	return strings.ToLower(h), true
}

func splitOpt(a string) (name, val string, hasVal bool) {
	if strings.HasPrefix(a, "--") {
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			return a[:eq], a[eq+1:], true
		}
		return a, "", false
	}
	// Short option: -oVALUE or -o
	if len(a) > 2 && a[0] == '-' && a[1] != '-' {
		// Combined flags like -fsSL have no value; only treat as name+val when
		// the option is a known single-letter that takes an arg — callers check name.
		return a[:2], a[2:], true // e.g. -oout.txt → name=-o val=out.txt
	}
	return a, "", false
}

func isStaticToken(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "$`") {
		return false
	}
	if strings.Contains(s, "$(") {
		return false
	}
	return true
}

func looksLikeNetworkDest(s string) bool {
	low := strings.ToLower(s)
	if strings.Contains(low, "://") || strings.HasPrefix(s, "//") {
		return true
	}
	if strings.Contains(s, "@") {
		return true
	}
	// host:path or host:port with expansion
	if strings.Contains(s, ":") && !strings.HasPrefix(s, "/") {
		return true
	}
	return false
}

func looksLikeBareHost(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") {
		return false
	}
	if strings.ContainsAny(s, "/\\") {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	// Require at least one dot for bare hostnames in URL position (curl example.com)
	// to avoid treating local filenames as hosts. Single-label still allowed for ssh.
	if !strings.Contains(s, ".") {
		return false
	}
	return isSimpleHostname(s) || validateLooseHostname(s)
}

func isSimpleHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for i, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			if (r == '-' || r == '.') && (i == 0 || i == len(s)-1) {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func validateLooseHostname(s string) bool {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if err := sandboxHostValidate(s); err != nil {
		return false
	}
	return true
}

// sandboxHostValidate reuses allowlist hostname rules without exporting them.
func sandboxHostValidate(host string) error {
	// Mirror sandbox.validateAllowHostname for non-wildcard hosts.
	if host == "" || strings.ContainsAny(host, ":/\\ ") || strings.Contains(host, "*") {
		return fmt.Errorf("invalid")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("invalid")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("invalid")
		}
	}
	return nil
}

func hostPortOnly(s string) (host string, ok bool) {
	// IPv6 [addr]:port
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end <= 1 {
			return "", false
		}
		host = s[1:end]
		rest := s[end+1:]
		if rest == "" {
			return strings.ToLower(host), net.ParseIP(host) != nil
		}
		if rest[0] != ':' {
			return "", false
		}
		port := rest[1:]
		if !isAllDigits(port) {
			return "", false
		}
		return strings.ToLower(host), true
	}
	// hostname:port or ip:port — single colon, port numeric, no path slash
	if strings.Contains(s, "/") {
		return "", false
	}
	colon := strings.LastIndexByte(s, ':')
	if colon <= 0 {
		return "", false
	}
	// IPv6 without brackets has multiple colons — not host:port form.
	if strings.Count(s, ":") != 1 {
		// Could be bare IPv6
		if net.ParseIP(s) != nil {
			return s, true
		}
		return "", false
	}
	host = s[:colon]
	port := s[colon+1:]
	if !isAllDigits(port) {
		return "", false
	}
	if net.ParseIP(host) != nil {
		return host, true
	}
	if isSimpleHostname(host) {
		return strings.ToLower(host), true
	}
	return "", false
}

// redactDest keeps only a coarse destination class for error messages
// (no full URL path/query which may carry tokens).
func redactDest(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	// Truncate long tokens.
	if len(raw) > 64 {
		return raw[:64] + "…"
	}
	// Strip path after colon for scp-like forms.
	if at := strings.IndexByte(raw, '@'); at >= 0 {
		rest := raw[at+1:]
		if c := strings.IndexByte(rest, ':'); c >= 0 {
			return raw[:at+1+c]
		}
	}
	if c := strings.IndexByte(raw, ':'); c > 0 && !strings.Contains(raw[:c], "/") {
		// host:path → host
		if !isAllDigits(raw[c+1:]) {
			return raw[:c]
		}
	}
	return raw
}
