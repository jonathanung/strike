package engine

import (
	"testing"

	"github.com/jonathanung/strike/pkg/protocol"
)

func TestRequestSystemSplitsStableAndDynamic(t *testing.T) {
	stableLayers := []promptLayer{
		{Kind: protocol.PromptLayerShared, Text: "shared"},
		{Kind: protocol.PromptLayerPersona, Text: "persona"},
		{Kind: protocol.PromptLayerInstruction, Text: "inst"},
	}
	env := func(date string) promptLayer {
		return promptLayer{Kind: protocol.PromptLayerEnvironment, Text: "Today's date: " + date}
	}
	tests := []struct {
		name        string
		layers      []promptLayer
		wantKinds   []string
		wantSystem  string
		wantBlocks  []string
		wantDynamic []bool
	}{
		{
			name:        "stable then dynamic",
			layers:      append(append([]promptLayer{}, stableLayers...), env("Mon"), promptLayer{Kind: protocol.PromptLayerLedger, Text: "ledger"}),
			wantKinds:   []string{"shared", "persona", "instruction", "environment", "decision_ledger"},
			wantSystem:  "shared\n\npersona\n\ninst\n\nToday's date: Mon\n\nledger\n",
			wantBlocks:  []string{"shared\n\npersona\n\ninst\n", "Today's date: Mon\n\nledger\n"},
			wantDynamic: []bool{false, true},
		},
		{
			name: "interleaved dynamic moves to tail",
			layers: []promptLayer{
				{Kind: protocol.PromptLayerShared, Text: "shared"},
				env("Mon"),
				{Kind: protocol.PromptLayerMemory, Text: "mem"},
				{Kind: protocol.PromptLayerInstruction, Text: "inst"},
			},
			wantKinds:   []string{"shared", "instruction", "environment", "project_memory"},
			wantSystem:  "shared\n\ninst\n\nToday's date: Mon\n\nmem\n",
			wantBlocks:  []string{"shared\n\ninst\n", "Today's date: Mon\n\nmem\n"},
			wantDynamic: []bool{false, true},
		},
		{
			name:        "no dynamic layers",
			layers:      stableLayers,
			wantKinds:   []string{"shared", "persona", "instruction"},
			wantSystem:  "shared\n\npersona\n\ninst\n",
			wantBlocks:  []string{"shared\n\npersona\n\ninst\n"},
			wantDynamic: []bool{false},
		},
		{
			name:       "empty",
			wantSystem: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ordered, system, blocks := requestSystem(tt.layers)
			if len(ordered) != len(tt.wantKinds) {
				t.Fatalf("ordered = %+v, want kinds %v", ordered, tt.wantKinds)
			}
			for i, l := range ordered {
				if l.Kind != tt.wantKinds[i] {
					t.Errorf("ordered[%d].Kind = %q, want %q", i, l.Kind, tt.wantKinds[i])
				}
			}
			if system != tt.wantSystem {
				t.Errorf("system = %q, want %q", system, tt.wantSystem)
			}
			if system != joinPromptLayerTexts(ordered) {
				t.Errorf("system diverges from joined ordered layers")
			}
			if len(blocks) != len(tt.wantBlocks) {
				t.Fatalf("blocks = %+v, want %d", blocks, len(tt.wantBlocks))
			}
			for i, b := range blocks {
				if b.Text != tt.wantBlocks[i] || b.Dynamic != tt.wantDynamic[i] {
					t.Errorf("blocks[%d] = %+v, want {%q %v}", i, b, tt.wantBlocks[i], tt.wantDynamic[i])
				}
			}
		})
	}
}

// Date rollover alone must not touch the stable block bytes.
func TestRequestSystemDateOnlyChangeKeepsStableBlock(t *testing.T) {
	build := func(date string) []promptLayer {
		return []promptLayer{
			{Kind: protocol.PromptLayerShared, Text: "shared"},
			{Kind: protocol.PromptLayerInstruction, Text: "inst"},
			{Kind: protocol.PromptLayerEnvironment, Text: "Today's date: " + date},
		}
	}
	_, _, mon := requestSystem(build("Mon Jan 5 2026"))
	_, _, tue := requestSystem(build("Tue Jan 6 2026"))
	if mon[0].Text != tue[0].Text {
		t.Fatalf("stable block changed on date rollover: %q vs %q", mon[0].Text, tue[0].Text)
	}
	if mon[1].Text == tue[1].Text {
		t.Fatal("dynamic block should reflect the new date")
	}
}
