package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/container"
)

func TestPrintLaunchResult(t *testing.T) {
	var b bytes.Buffer
	printLaunchResult(&b, container.LaunchResult{ID: "abcdefghijklmnop", Name: "strike-repo-abc", Mode: container.LaunchModeAttached})
	if !strings.Contains(b.String(), "attached to existing") || !strings.Contains(b.String(), "strike-repo-abc") {
		t.Fatalf("%s", b.String())
	}
	b.Reset()
	printLaunchResult(&b, container.LaunchResult{ID: "id1", Name: "n", Mode: container.LaunchModeStarted})
	if !strings.Contains(b.String(), "started container") {
		t.Fatalf("%s", b.String())
	}
}

func TestResolveStaleContainerChoiceFlags(t *testing.T) {
	stale := &container.StaleContainerError{Reason: "hash mismatch", Name: "c1"}
	var errb bytes.Buffer
	got, err := resolveStaleContainerChoice(cliOptions{containerAttachStale: true}, stale, strings.NewReader(""), &errb)
	if err != nil || got != "attach" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = resolveStaleContainerChoice(cliOptions{containerRebuild: true}, stale, strings.NewReader(""), &errb)
	if err != nil || got != "rebuild" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = resolveStaleContainerChoice(cliOptions{containerCancelStale: true}, stale, strings.NewReader(""), &errb)
	if err != nil || got != "cancel" {
		t.Fatalf("%q %v", got, err)
	}
	// non-interactive without flags (piped reader is never interactive)
	_, err = resolveStaleContainerChoice(cliOptions{}, stale, strings.NewReader("a\n"), &errb)
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("want non-interactive error, got %v", err)
	}
}

func TestStripLaunchInsideArgs(t *testing.T) {
	in := []string{"--launch-inside-container", "--container-rebuild", "--model", "x", "--container-attach-stale"}
	out := stripLaunchInsideArgs(in)
	if len(out) != 2 || out[0] != "--model" || out[1] != "x" {
		t.Fatalf("%v", out)
	}
}
