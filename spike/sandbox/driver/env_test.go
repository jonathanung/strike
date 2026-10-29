package main

import (
	"strings"
	"testing"
)

func TestCleanEnvDropsProviderKeys(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/ubuntu",
		"OPENAI_API_KEY=sk-not-real",
		"ANTHROPIC_API_KEY=sk-ant-not-real",
		"AWS_SECRET_ACCESS_KEY=secret",
		"LANG=C",
		"NOT_A_SECRET_BUT_UNLISTED=1",
	}
	got := cleanEnv(in)
	joined := strings.Join(got, "\n")
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/ubuntu", "LANG=C"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("dropped %s from %q", keep, got)
		}
	}
	for _, drop := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "NOT_A_SECRET"} {
		if strings.Contains(joined, drop) {
			t.Fatalf("kept %s in %q", drop, got)
		}
	}
}
