package tui

import (
	"encoding/json"
	"testing"
)

func TestParseEditMetadata(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		wantOK  bool
		wantOld string
		wantNew string
		wantCnt int
	}{
		{
			name:    "valid full JSON with count",
			raw:     json.RawMessage(`{"oldString":"foo","newString":"bar","count":3}`),
			wantOK:  true,
			wantOld: "foo",
			wantNew: "bar",
			wantCnt: 3,
		},
		{
			name:    "valid without count",
			raw:     json.RawMessage(`{"oldString":"a","newString":"b"}`),
			wantOK:  true,
			wantOld: "a",
			wantNew: "b",
			wantCnt: 0,
		},
		{
			name:    "empty strings for old and new still ok",
			raw:     json.RawMessage(`{"oldString":"","newString":""}`),
			wantOK:  true,
			wantOld: "",
			wantNew: "",
		},
		{
			name:   "empty raw",
			raw:    nil,
			wantOK: false,
		},
		{
			name:   "empty byte slice",
			raw:    json.RawMessage{},
			wantOK: false,
		},
		{
			name:   "invalid JSON",
			raw:    json.RawMessage(`{not json`),
			wantOK: false,
		},
		{
			name:   "write-shaped metadata",
			raw:    json.RawMessage(`{"exists":true,"oldSize":1,"newSize":2}`),
			wantOK: false,
		},
		{
			name:   "missing oldString",
			raw:    json.RawMessage(`{"newString":"bar"}`),
			wantOK: false,
		},
		{
			name:   "missing newString",
			raw:    json.RawMessage(`{"oldString":"foo"}`),
			wantOK: false,
		},
		{
			name:   "wrong type oldString number",
			raw:    json.RawMessage(`{"oldString":1,"newString":"bar"}`),
			wantOK: false,
		},
		{
			name:   "wrong type newString number",
			raw:    json.RawMessage(`{"oldString":"foo","newString":2}`),
			wantOK: false,
		},
		{
			name:   "null",
			raw:    json.RawMessage(`null`),
			wantOK: false,
		},
		{
			name:   "null oldString",
			raw:    json.RawMessage(`{"oldString":null,"newString":"bar"}`),
			wantOK: false,
		},
		{
			name:   "null newString",
			raw:    json.RawMessage(`{"oldString":"foo","newString":null}`),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseEditMetadata(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (meta=%+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.OldString != tt.wantOld || got.NewString != tt.wantNew || got.Count != tt.wantCnt {
				t.Errorf("meta = %+v, want old=%q new=%q count=%d", got, tt.wantOld, tt.wantNew, tt.wantCnt)
			}
		})
	}
}
