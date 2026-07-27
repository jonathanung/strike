package protocol

import "testing"

func TestRewindPointsCompletedTurns(t *testing.T) {
	events := []Event{
		ModelSelected{Provider: "echo", Model: "echo"},
		UserMessage{Text: "first prompt"},
		TextDelta{Text: "a"},
		TurnCompleted{StopReason: "end_turn"},
		UserMessage{Text: "second prompt"},
		TextDelta{Text: "b"},
		TurnCompleted{StopReason: "end_turn"},
	}
	pts := RewindPoints(events)
	if len(pts) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(pts), pts)
	}
	if pts[0].Turn != 1 || pts[0].KeepEvents != 4 || pts[0].Preview != "first prompt" {
		t.Fatalf("pt0 = %+v", pts[0])
	}
	if pts[1].Turn != 2 || pts[1].KeepEvents != 7 || pts[1].Preview != "second prompt" {
		t.Fatalf("pt1 = %+v", pts[1])
	}
}

func TestRewindPointsIgnoresChildTurns(t *testing.T) {
	events := []Event{
		UserMessage{Text: "root"},
		TurnCompleted{},
		UserMessage{Text: "child", Correlation: Correlation{ParentSessionID: "p", Depth: 1}},
		TurnCompleted{Correlation: Correlation{ParentSessionID: "p", Depth: 1}},
		UserMessage{Text: "root2"},
		TurnCompleted{},
	}
	pts := RewindPoints(events)
	if len(pts) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(pts), pts)
	}
	if pts[0].Preview != "root" || pts[1].Preview != "root2" {
		t.Fatalf("previews = %q, %q", pts[0].Preview, pts[1].Preview)
	}
}

func TestRewindPointsSessionRewoundDropsLast(t *testing.T) {
	events := []Event{
		UserMessage{Text: "a"},
		TurnCompleted{},
		UserMessage{Text: "b"},
		TurnCompleted{},
		SessionRewound{Removed: 2},
		UserMessage{Text: "c"},
		TurnCompleted{},
	}
	pts := RewindPoints(events)
	if len(pts) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(pts), pts)
	}
	if pts[0].Preview != "a" || pts[1].Preview != "c" {
		t.Fatalf("previews = %q, %q", pts[0].Preview, pts[1].Preview)
	}
	if pts[1].Turn != 2 {
		t.Fatalf("turn after rewound path = %d, want 2", pts[1].Turn)
	}
}

func TestRewindPreviewTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 80; i++ {
		long += "x"
	}
	got := rewindPreview(long)
	if len([]rune(got)) != 48 {
		t.Fatalf("len runes = %d, want 48 (%q)", len([]rune(got)), got)
	}
}
