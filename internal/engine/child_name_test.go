package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestFormatChildCompletedNoticeIncludesName(t *testing.T) {
	got := formatChildCompletedNotice(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "abcdef12xyz"},
		Status:      protocol.ChildStatusCompleted,
		Summary:     "done",
		Name:        "explorer",
		Handoff: protocol.CompletionHandoff{
			Summary:      "done",
			FilesChanged: []string{},
			Findings:     []string{},
			Blockers:     []string{},
		},
	})
	if !strings.Contains(got, "name=explorer") {
		t.Fatalf("notice missing name: %q", got)
	}
	if !strings.Contains(got, "session=abcdef12") {
		t.Fatalf("notice missing short session: %q", got)
	}
	if !strings.Contains(got, "done") {
		t.Fatalf("notice missing summary: %q", got)
	}
	if !strings.Contains(got, "handoff: ") {
		t.Fatalf("notice missing handoff JSON: %q", got)
	}
}
