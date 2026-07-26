//go:build ignore

// Flatten internal/tui/_src/<group>/*.go into internal/tui/ for the Go toolchain.
// Source of truth is _src/; run: go generate ./internal/tui
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	srcRoot := filepath.Join(root, "_src")
	ents, err := os.ReadDir(srcRoot)
	if err != nil {
		fail(err)
	}
	// Remove previously generated flatten files (keep doc.go, gen_src.go).
	keep := map[string]bool{"doc.go": true, "gen_src.go": true, "gen_sync_test.go": true}
	rootEnts, _ := os.ReadDir(root)
	for _, e := range rootEnts {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || keep[e.Name()] {
			continue
		}
		_ = os.Remove(filepath.Join(root, e.Name()))
	}
	n := 0
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(srcRoot, ent.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			fail(err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") {
				continue
			}
			in := filepath.Join(dir, f.Name())
			out := filepath.Join(root, f.Name())
			data, err := os.ReadFile(in)
			if err != nil {
				fail(err)
			}
			// Drop any stale origin marker from older generator versions.
			data = stripOrigin(data)
			if err := os.WriteFile(out, data, 0o644); err != nil {
				fail(err)
			}
			n++
		}
	}
	fmt.Printf("gen_src: flattened %d files from _src/\n", n)
}

func stripOrigin(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	out := lines[:0]
	for _, ln := range lines {
		if bytes.HasPrefix(ln, []byte("// Code origin: _src/")) {
			continue
		}
		out = append(out, ln)
	}
	return bytes.Join(out, []byte("\n"))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
