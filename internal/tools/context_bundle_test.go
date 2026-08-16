package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/tool"
)

func TestContextBundleToolGetAndItem(t *testing.T) {
	t.Parallel()
	bundle, err := tool.NormalizeContextBundle(tool.ContextBundle{
		Goal: "do work",
		Items: []tool.ContextBundleItem{
			{ID: "custom", Kind: "note", Text: "secret-ish"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tc := &tool.Context{
		ContextBundle: &bundle,
		Ask:           func(context.Context, tool.AskRequest) error { return nil },
	}
	tl := NewContextBundle()

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"get"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"attached": true`) && !strings.Contains(res.Output, `"attached":true`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Output, "do work") {
		t.Fatalf("missing goal: %s", res.Output)
	}

	res, err = tl.Execute(context.Background(), json.RawMessage(`{"action":"item","id":"custom"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "secret-ish") {
		t.Fatalf("item = %s", res.Output)
	}

	_, err = tl.Execute(context.Background(), json.RawMessage(`{"action":"item","id":"nope"}`), tc)
	if err == nil {
		t.Fatal("expected missing item error")
	}
}

func TestContextBundleToolEmpty(t *testing.T) {
	t.Parallel()
	tc := &tool.Context{Ask: func(context.Context, tool.AskRequest) error { return nil }}
	res, err := NewContextBundle().Execute(context.Background(), nil, tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"attached":false`) && !strings.Contains(res.Output, `"attached": false`) {
		t.Fatalf("output = %s", res.Output)
	}
}
