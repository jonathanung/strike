package actionfacts

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// classifyCommand attaches path/network/operation facts for known programs.
// Only called when argv is fully static.
func classifyCommand(b *builder, c *CommandFact) {
	if c == nil || !c.ArgvComplete || len(c.Argv) == 0 {
		return
	}
	prog := strings.ToLower(c.Program)
	argv := c.Argv
	switch prog {
	case "curl", "curl.exe":
		classifyCurl(b, c, argv)
	case "wget", "wget.exe":
		classifyWget(b, c, argv)
	case "ssh", "scp", "sftp":
		classifySSHFamily(b, c, argv, prog)
	case "rm":
		c.Operations = appendUniqueOp(c.Operations, OpDelete)
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			b.addPath(PathFact{CommandID: c.ID, Access: PathDelete, Value: a})
		}
	case "cp", "mv":
		op := OpWrite
		if prog == "mv" {
			// move: read+delete source, write dest — simplified as write on all path-like
			op = OpWrite
		}
		c.Operations = appendUniqueOp(c.Operations, op)
		args := nonFlagArgs(argv[1:])
		for i, a := range args {
			access := PathRead
			if i == len(args)-1 {
				access = PathWrite
			} else if prog == "mv" {
				access = PathRead
			}
			b.addPath(PathFact{CommandID: c.ID, Access: access, Value: a})
		}
	case "cat", "head", "tail", "less", "more":
		c.Operations = appendUniqueOp(c.Operations, OpRead)
		for _, a := range nonFlagArgs(argv[1:]) {
			b.addPath(PathFact{CommandID: c.ID, Access: PathRead, Value: a})
		}
	case "touch", "mkdir":
		c.Operations = appendUniqueOp(c.Operations, OpWrite)
		for _, a := range nonFlagArgs(argv[1:]) {
			b.addPath(PathFact{CommandID: c.ID, Access: PathWrite, Value: a})
		}
	case "chmod", "chown":
		c.Operations = appendUniqueOp(c.Operations, OpWrite)
		args := nonFlagArgs(argv[1:])
		// first non-flag often mode/owner; paths follow — best-effort: all path-like
		for _, a := range args {
			if looksLikePath(a) {
				b.addPath(PathFact{CommandID: c.ID, Access: PathWrite, Value: a})
			}
		}
	case "ls":
		c.Operations = appendUniqueOp(c.Operations, OpList)
	case "grep", "rg", "find":
		c.Operations = appendUniqueOp(c.Operations, OpSearch)
	case "git":
		// no path extraction by default; program name is enough for rules
	case "tar", "unzip", "gzip", "gunzip":
		c.Operations = appendUniqueOp(c.Operations, OpRead)
	case "nc", "ncat", "netcat":
		c.Operations = appendUniqueOp(c.Operations, OpConnect)
		// best-effort host: first non-flag that isn't a port-only number
		for _, a := range nonFlagArgs(argv[1:]) {
			if _, err := strconv.Atoi(a); err == nil {
				continue
			}
			if host := normalizeHostToken(a); host != "" {
				b.addNetwork(NetworkFact{CommandID: c.ID, Action: NetConnect, Host: host})
				break
			}
		}
	}
}

func classifyCurl(b *builder, c *CommandFact, argv []string) {
	c.Operations = appendUniqueOp(c.Operations, OpFetch)
	upload := false
	var outFile string
	args := argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "--output" || a == "-T" || a == "--upload-file" ||
			a == "-d" || a == "--data" || a == "--data-binary" || a == "--data-raw" ||
			a == "-F" || a == "--form" || a == "-H" || a == "--header" ||
			a == "-A" || a == "--user-agent" || a == "-u" || a == "--user" ||
			a == "-e" || a == "--referer" || a == "-w" || a == "--write-out" ||
			a == "--connect-timeout" || a == "-m" || a == "--max-time" ||
			a == "-x" || a == "--proxy" || a == "-E" || a == "--cert" ||
			a == "--cacert" || a == "-K" || a == "--config":
			if a == "-T" || a == "--upload-file" || a == "-d" || a == "--data" ||
				a == "--data-binary" || a == "--data-raw" || a == "-F" || a == "--form" {
				upload = true
			}
			if a == "-o" || a == "--output" {
				if i+1 < len(args) {
					i++
					outFile = args[i]
				}
				continue
			}
			if a == "-T" || a == "--upload-file" {
				if i+1 < len(args) {
					i++
					b.addPath(PathFact{CommandID: c.ID, Access: PathRead, Value: args[i]})
				}
				continue
			}
			// skip value for other flags that take args
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case a == "-O" || a == "--remote-name":
			// remote name — no local path known
		case a == "-I" || a == "--head" || a == "-L" || a == "--location" ||
			a == "-s" || a == "--silent" || a == "-S" || a == "--show-error" ||
			a == "-f" || a == "--fail" || a == "-k" || a == "--insecure" ||
			a == "-v" || a == "--verbose" || a == "-g" || a == "--globoff" ||
			a == "-4" || a == "-6" || a == "-N":
			// boolean flags
		case strings.HasPrefix(a, "-o") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			outFile = a[2:]
		case strings.HasPrefix(a, "-"):
			// unknown flag — keep complete; don't invent operands
		default:
			// URL or bare host
			if host, scheme, port, ok := parseURLHost(a); ok {
				action := NetDownload
				if upload {
					action = NetUpload
				}
				b.addNetwork(NetworkFact{
					CommandID: c.ID,
					Action:    action,
					Host:      host,
					Port:      port,
					Scheme:    scheme,
				})
			} else if host := normalizeHostToken(a); host != "" && strings.Contains(a, ".") {
				b.addNetwork(NetworkFact{CommandID: c.ID, Action: NetDownload, Host: host})
			}
		}
	}
	if upload {
		c.Operations = appendUniqueOp(c.Operations, OpUpload)
	}
	if outFile != "" && outFile != "-" {
		b.addPath(PathFact{CommandID: c.ID, Access: PathWrite, Value: outFile})
	}
}

func classifyWget(b *builder, c *CommandFact, argv []string) {
	c.Operations = appendUniqueOp(c.Operations, OpFetch)
	var outFile string
	args := argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-O" || a == "--output-document":
			if i+1 < len(args) {
				i++
				outFile = args[i]
			}
		case a == "-o" || a == "--output-file" || a == "-i" || a == "--input-file" ||
			a == "-P" || a == "--directory-prefix" || a == "--header" ||
			a == "-t" || a == "--tries" || a == "-T" || a == "--timeout" ||
			a == "-U" || a == "--user-agent":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(a, "-"):
			// skip
		default:
			if host, scheme, port, ok := parseURLHost(a); ok {
				b.addNetwork(NetworkFact{
					CommandID: c.ID,
					Action:    NetDownload,
					Host:      host,
					Port:      port,
					Scheme:    scheme,
				})
			}
		}
	}
	if outFile != "" && outFile != "-" {
		b.addPath(PathFact{CommandID: c.ID, Access: PathWrite, Value: outFile})
	}
}

func classifySSHFamily(b *builder, c *CommandFact, argv []string, prog string) {
	c.Operations = appendUniqueOp(c.Operations, OpConnect)
	args := nonFlagArgs(argv[1:])
	if len(args) == 0 {
		return
	}
	switch prog {
	case "scp":
		for _, a := range args {
			if host, pathPart, ok := splitUserHostPath(a); ok {
				b.addNetwork(NetworkFact{CommandID: c.ID, Action: NetConnect, Host: host})
				if pathPart != "" {
					b.addPath(PathFact{CommandID: c.ID, Access: PathWrite, Value: pathPart})
				}
			} else if looksLikePath(a) {
				b.addPath(PathFact{CommandID: c.ID, Access: PathRead, Value: a})
			}
		}
	default: // ssh, sftp
		dest := args[0]
		if host := sshDestHost(dest); host != "" {
			b.addNetwork(NetworkFact{CommandID: c.ID, Action: NetConnect, Host: host})
		}
	}
}

func splitUserHostPath(s string) (host, pathPart string, ok bool) {
	// user@host:path
	if strings.Contains(s, "://") {
		return "", "", false
	}
	colon := strings.LastIndex(s, ":")
	if colon <= 0 {
		return "", "", false
	}
	left, right := s[:colon], s[colon+1:]
	// avoid Windows drive
	if len(left) == 1 && unicodeIsLetter(left[0]) {
		return "", "", false
	}
	host = left
	if at := strings.LastIndex(left, "@"); at >= 0 {
		host = left[at+1:]
	}
	host = normalizeHostToken(host)
	if host == "" {
		return "", "", false
	}
	return host, right, true
}

func sshDestHost(s string) string {
	// [user@]host
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	// strip trailing port if host:port without brackets — ssh uses -p for port usually
	return normalizeHostToken(s)
}

func parseURLHost(raw string) (host, scheme string, port int, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", 0, false
	}
	// curl allows scheme-less URLs; require :// or treat as host/path
	if !strings.Contains(s, "://") {
		if strings.HasPrefix(s, "//") {
			s = "http:" + s
		} else if looksLikeURLHost(s) {
			s = "http://" + s
		} else {
			return "", "", 0, false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", 0, false
	}
	h := u.Hostname()
	if h == "" {
		return "", "", 0, false
	}
	h = strings.ToLower(h)
	p := 0
	if ps := u.Port(); ps != "" {
		p, _ = strconv.Atoi(ps)
	}
	return h, strings.ToLower(u.Scheme), p, true
}

func looksLikeURLHost(s string) bool {
	// host.tld/path or host:port/path
	if strings.ContainsAny(s, " \t") {
		return false
	}
	hostPart := s
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		hostPart = s[:i]
	}
	if hostPart == "" {
		return false
	}
	// must look like hostname or IP
	if net.ParseIP(hostPart) != nil {
		return true
	}
	if strings.Contains(hostPart, ":") {
		h, _, err := net.SplitHostPort(hostPart)
		return err == nil && h != ""
	}
	return strings.Contains(hostPart, ".") || hostPart == "localhost"
}

func normalizeHostToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" || strings.ContainsAny(s, " \t/\\") {
		return ""
	}
	if net.ParseIP(s) != nil {
		return s
	}
	return strings.ToLower(s)
}

func nonFlagArgs(args []string) []string {
	var out []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func looksLikePath(s string) bool {
	if s == "" || s == "-" {
		return false
	}
	if strings.HasPrefix(s, "-") {
		return false
	}
	return strings.ContainsAny(s, "/.\\") || !strings.Contains(s, "://")
}

func appendUniqueOp(ops []OperationKind, op OperationKind) []OperationKind {
	for _, x := range ops {
		if x == op {
			return ops
		}
	}
	return append(ops, op)
}

func unicodeIsLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
