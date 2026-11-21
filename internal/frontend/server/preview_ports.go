package server

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

// procNetFiles are read for listening sockets. Missing files (macOS,
// Windows, restricted containers) mean detection is unsupported and the
// user enters a port by hand.
var procNetFiles = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// listeningLoopbackPorts returns sorted, de-duplicated TCP ports that
// accept connections on the loopback interface (bound to loopback or to
// every address). ok is false when no /proc table could be read.
func listeningLoopbackPorts() (ports []int, ok bool) {
	seen := map[int]bool{}
	for _, path := range procNetFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ok = true
		for _, p := range parseProcNetTCP(data) {
			seen[p] = true
		}
	}
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports, ok
}

// tcpListen is the st column value for a listening socket.
const tcpListen = "0A"

// parseProcNetTCP reads a /proc/net/tcp or tcp6 table and returns the local
// ports of listening sockets reachable via loopback.
func parseProcNetTCP(data []byte) []int {
	var out []int
	sc := bufio.NewScanner(bytes.NewReader(data))
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != tcpListen {
			continue
		}
		hostHex, portHex, found := strings.Cut(f[1], ":")
		if !found {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 {
			continue
		}
		ip := procHexIP(hostHex)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			out = append(out, int(port))
		}
	}
	return out
}

// procHexIP decodes the kernel's address column: 4 bytes (IPv4) or 16 bytes
// (IPv6), each 32-bit word in host (little-endian on every Linux arch Strike
// ships for) byte order.
func procHexIP(s string) net.IP {
	raw, err := hex.DecodeString(s)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil
	}
	ip := make(net.IP, len(raw))
	for i := 0; i < len(raw); i += 4 {
		ip[i], ip[i+1], ip[i+2], ip[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return ip
}
