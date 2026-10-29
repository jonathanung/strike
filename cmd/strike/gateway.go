package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/frontend/server"
)

const gatewayUsage = `Print a one-time pairing URL for a running gateway.

Usage:
  strike gateway url [--qr] [--addr <base>] [--token-file <path>] [--label <name>]
  strike gateway qr [--addr <base>] [--token-file <path>] [--label <name>]
  strike gateway devices [--addr <base>] [--token-file <path>]
  strike gateway revoke <device-id> [--addr <base>] [--token-file <path>]

The URL carries a single-use code in the fragment (#pair=). It does not
carry the daemon token. A phone or another browser POSTs that code to
/v1/pair/redeem. See docs/gateway.md.

strike connect is the SSH helper (#1265). This command only talks to a
gateway that is already listening.

--addr is the daemon base URL (default http://127.0.0.1:8787).
The token is read from --token-file or ~/.strike/daemon.token and is not printed.
`

type gatewayFlags struct {
	addr      string
	tokenFile string
	label     string
	qr        bool
	id        string
}

func runGatewayCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, gatewayUsage)
		return 0
	}
	cmd := args[0]
	rest := args[1:]
	if cmd == "qr" {
		cmd = "url"
		rest = append([]string{"--qr"}, rest...)
	}
	switch cmd {
	case "url":
		return gatewayURL(rest, stdout, stderr)
	case "devices":
		return gatewayDevices(rest, stdout, stderr)
	case "revoke":
		return gatewayRevoke(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "strike: unknown gateway command %q\n%s", cmd, gatewayUsage)
		return 2
	}
}

func parseGatewayFlags(args []string, needID bool) (gatewayFlags, error) {
	var opt gatewayFlags
	fs := flag.NewFlagSet("strike gateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opt.addr, "addr", "http://127.0.0.1:8787", "")
	fs.StringVar(&opt.tokenFile, "token-file", "", "")
	fs.StringVar(&opt.label, "label", "cli", "")
	fs.BoolVar(&opt.qr, "qr", false, "")
	if err := fs.Parse(args); err != nil {
		return gatewayFlags{}, err
	}
	if needID {
		if fs.NArg() != 1 {
			return gatewayFlags{}, errors.New("revoke requires one device id")
		}
		opt.id = strings.TrimSpace(fs.Arg(0))
		if opt.id == "" || strings.Contains(opt.id, "/") {
			return gatewayFlags{}, errors.New("invalid device id")
		}
	} else if fs.NArg() != 0 {
		return gatewayFlags{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opt.addr = strings.TrimRight(strings.TrimSpace(opt.addr), "/")
	if opt.addr == "" {
		opt.addr = "http://127.0.0.1:8787"
	}
	if !strings.Contains(opt.addr, "://") {
		opt.addr = "http://" + opt.addr
	}
	if strings.TrimSpace(opt.tokenFile) == "" {
		opt.tokenFile = daemonTokenPath()
	}
	return opt, nil
}

func readDaemonToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", errors.New("token file is empty")
	}
	return tok, nil
}

func gatewayURL(args []string, stdout, stderr io.Writer) int {
	opt, err := parseGatewayFlags(args, false)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 2
	}
	tok, err := readDaemonToken(opt.tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "strike: %s\nThe daemon token was not printed. Start serve with --auth --token-file, or pass --token-file.\n", err)
		return 1
	}
	status, body, err := gatewayPOST(opt.addr+"/v1/pair", tok, map[string]string{"label": opt.label})
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if strings.Contains(body, tok) {
		fmt.Fprintln(stderr, "strike: refused to print a response that contains the daemon token")
		return 1
	}
	var parsed struct {
		Error   string             `json:"error"`
		URL     string             `json:"url"`
		Code    string             `json:"code"`
		Gateway server.GatewayInfo `json:"gateway"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		fmt.Fprintf(stderr, "strike: gateway response was not JSON (HTTP %d)\n", status)
		return 1
	}
	if parsed.Error == "auth_disabled" {
		printGatewayReachability(stdout, parsed.Gateway)
		fmt.Fprintln(stdout, "Pairing codes are off because this gateway was started without --auth.")
		if opt.qr {
			if u := plainGatewayURL(parsed.Gateway); u != "" {
				fmt.Fprintln(stdout, server.QRBlocks(u))
			}
		}
		return 0
	}
	if status != http.StatusCreated || parsed.URL == "" {
		fmt.Fprintf(stderr, "strike: pairing request failed (HTTP %d)\n", status)
		return 1
	}
	if strings.Contains(parsed.URL, "?token=") || strings.Contains(parsed.URL, tok) {
		fmt.Fprintln(stderr, "strike: refused to print a pairing URL that contains the daemon token")
		return 1
	}
	fmt.Fprintln(stdout, parsed.URL)
	if opt.qr {
		fmt.Fprintln(stdout, server.QRBlocks(parsed.URL))
	}
	printGatewayReachability(stdout, parsed.Gateway)
	return 0
}

func gatewayDevices(args []string, stdout, stderr io.Writer) int {
	opt, err := parseGatewayFlags(args, false)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 2
	}
	tok, err := readDaemonToken(opt.tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "strike: %s\nThe daemon token was not printed.\n", err)
		return 1
	}
	status, body, err := gatewayDo(http.MethodGet, opt.addr+"/v1/pair/devices", tok, nil)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if strings.Contains(body, tok) {
		fmt.Fprintln(stderr, "strike: refused to print a response that contains the daemon token")
		return 1
	}
	if status != http.StatusOK {
		fmt.Fprintf(stderr, "strike: device list failed (HTTP %d)\n", status)
		return 1
	}
	var parsed struct {
		Devices []server.DeviceView `json:"devices"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		fmt.Fprintln(stderr, "strike: device list was not JSON")
		return 1
	}
	if len(parsed.Devices) == 0 {
		fmt.Fprintln(stdout, "No paired devices.")
		return 0
	}
	for _, d := range parsed.Devices {
		state := "active"
		if d.Revoked {
			state = "revoked"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", d.ID, state, d.Label, d.CreatedAt)
	}
	return 0
}

func gatewayRevoke(args []string, stdout, stderr io.Writer) int {
	opt, err := parseGatewayFlags(args, true)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 2
	}
	tok, err := readDaemonToken(opt.tokenFile)
	if err != nil {
		fmt.Fprintf(stderr, "strike: %s\nThe daemon token was not printed.\n", err)
		return 1
	}
	status, body, err := gatewayDo(http.MethodDelete, opt.addr+"/v1/pair/devices/"+opt.id, tok, nil)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if strings.Contains(body, tok) {
		fmt.Fprintln(stderr, "strike: refused to print a response that contains the daemon token")
		return 1
	}
	if status == http.StatusNotFound {
		fmt.Fprintln(stderr, "strike: no active paired device with that id")
		return 1
	}
	if status != http.StatusOK {
		fmt.Fprintf(stderr, "strike: revoke failed (HTTP %d)\n", status)
		return 1
	}
	fmt.Fprintf(stdout, "Revoked %s\n", opt.id)
	return 0
}

func printGatewayReachability(w io.Writer, info server.GatewayInfo) {
	if info.OptIn != "" {
		fmt.Fprintln(w, info.OptIn)
	}
	if info.LanWarning != "" {
		fmt.Fprintln(w, info.LanWarning)
	}
	if info.AdvertiseWarning != "" {
		fmt.Fprintln(w, info.AdvertiseWarning)
	}
	if len(info.Unbound) > 0 {
		fmt.Fprintf(w, "This host has %s up. That address is not listening.\n", strings.Join(info.Unbound, ", "))
	}
}

func plainGatewayURL(info server.GatewayInfo) string {
	for _, u := range info.URLs {
		if u.Kind == "bind" {
			return u.URL
		}
	}
	if len(info.URLs) > 0 {
		return info.URLs[0].URL
	}
	return ""
}

func gatewayPOST(url, token string, body any) (int, string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, "", err
	}
	return gatewayDo(http.MethodPost, url, token, raw)
}

func gatewayDo(method, url, token string, body []byte) (int, string, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, "", err
	}
	return res.StatusCode, string(data), nil
}
