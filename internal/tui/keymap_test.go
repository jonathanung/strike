package tui

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultKeyMapBindingsMatchTheirRequiredKeysAndHaveHelp(t *testing.T) {
	keys := defaultKeyMap()
	tests := []struct {
		name    string
		binding key.Binding
		msg     tea.KeyMsg
	}{
		{"quit", keys.Quit, tea.KeyMsg{Type: tea.KeyCtrlC}},
		{"focus pane", keys.FocusPane, tea.KeyMsg{Type: tea.KeyCtrlJ}},
		{"cycle ctrl+l", keys.CycleWindow, tea.KeyMsg{Type: tea.KeyCtrlL}},
		{"cycle ctrl+o", keys.CycleWindow, tea.KeyMsg{Type: tea.KeyCtrlO}},
		{"palette", keys.Palette, tea.KeyMsg{Type: tea.KeyCtrlK}},
		{"interrupt", keys.Interrupt, tea.KeyMsg{Type: tea.KeyEsc}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !key.Matches(tt.msg, tt.binding) {
				t.Errorf("binding does not match %s", tt.msg.String())
			}
			help := tt.binding.Help()
			if help.Key == "" || help.Desc == "" {
				t.Errorf("binding help = %#v, want key and description", help)
			}
		})
	}
}

func TestCycleWindowAliasesHaveIdenticalEffects(t *testing.T) {
	tests := []tea.KeyMsg{
		{Type: tea.KeyCtrlL},
		{Type: tea.KeyCtrlO},
	}
	var got []struct {
		index int
		id    string
	}
	for _, msg := range tests {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, msg)
		got = append(got, struct {
			index int
			id    string
		}{m.windows.index, m.windows.active().id()})
	}
	if !reflect.DeepEqual(got[0], got[1]) {
		t.Errorf("cycle aliases have different results: ctrl+l=%#v ctrl+o=%#v", got[0], got[1])
	}
}
