package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// PathToURI converts an absolute filesystem path to a file:// URI.
func PathToURI(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs)
	// url.URL with file scheme: Path must be absolute with forward slashes.
	u := url.URL{Scheme: "file"}
	if runtime.GOOS == "windows" {
		// file:///C:/path
		p := filepath.ToSlash(abs)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		u.Path = p
	} else {
		u.Path = abs
	}
	return u.String()
}

// URIToPath converts a file:// URI to a local filesystem path.
// Non-file URIs return the input unchanged.
func URIToPath(uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return uri
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		// /C:/foo → C:/foo
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		return filepath.FromSlash(p)
	}
	if u.Host != "" && u.Host != "localhost" {
		// file://hostname/path (rare)
		return filepath.Join("/", u.Host, p)
	}
	return p
}
