package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerEjectCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	// minimal project container config
	if err := os.WriteFile(filepath.Join(work, ".strike", "container.json"), []byte(`{"packages":["make"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := runContainerCLI([]string{"eject"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s out=%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "wrote") {
		t.Fatalf("out: %s", out.String())
	}
	data, err := os.ReadFile(filepath.Join(work, "Dockerfile.devcontainer"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "make") || !strings.Contains(string(data), "strike-config-hash:") {
		t.Fatalf("dockerfile: %s", data)
	}

	out.Reset()
	errb.Reset()
	code = runContainerCLI([]string{"drift"}, &out, &errb)
	if code != 0 {
		t.Fatalf("drift code=%d %s %s", code, errb.String(), out.String())
	}
}

func TestContainerDetectCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"engines":{"node":"20"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := runContainerCLI([]string{"detect"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code=%d err=%s out=%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "go.mod") || !strings.Contains(out.String(), "package.json") {
		t.Fatalf("out: %s", out.String())
	}

	out.Reset()
	errb.Reset()
	code = runContainerCLI([]string{"detect", "--json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("json code=%d %s %s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), `"go": true`) && !strings.Contains(out.String(), `"go":true`) {
		// pretty-printed
		if !strings.Contains(out.String(), `"needsGo"`) && !strings.Contains(out.String(), "needsGo") {
			t.Fatalf("json out: %s", out.String())
		}
	}
	if !strings.Contains(out.String(), "suggested") {
		t.Fatalf("missing suggested: %s", out.String())
	}
}
