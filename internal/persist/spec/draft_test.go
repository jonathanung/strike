package spec

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseRequirementsDraft(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []Requirement
		wantErr bool
	}{
		{
			name: "bare array",
			in:   `[{"story":"As a user I export CSV","criteria":["WHEN I click export THE SYSTEM SHALL download a csv"]}]`,
			want: []Requirement{{Story: "As a user I export CSV", Criteria: []string{"WHEN I click export THE SYSTEM SHALL download a csv"}}},
		},
		{
			name: "fenced with prose and wrapper object",
			in:   "Here you go:\n```json\n{\"requirements\":[{\"story\":\" s1 \",\"criteria\":[\" c1 \",\"\"]},{\"story\":\"  \",\"criteria\":[\"dropped\"]}]}\n```",
			want: []Requirement{{Story: "s1", Criteria: []string{"c1"}}},
		},
		{
			name: "ids from the model are ignored",
			in:   `[{"id":"R7","story":"s","criteria":[]}]`,
			want: []Requirement{{Story: "s", Criteria: []string{}}},
		},
		{name: "not json", in: "I cannot help with that.", wantErr: true},
		{name: "empty list", in: "[]", wantErr: true},
		{name: "only blank stories", in: `[{"story":""}]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRequirementsDraft(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRequirementsDraftCapsRows(t *testing.T) {
	in := "["
	for i := 0; i < MaxDraftRequirements+5; i++ {
		if i > 0 {
			in += ","
		}
		in += `{"story":"s"}`
	}
	in += "]"
	got, err := ParseRequirementsDraft(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxDraftRequirements {
		t.Fatalf("rows = %d", len(got))
	}
}
