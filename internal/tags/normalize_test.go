package tags

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "mixed case", input: "Alternative Rock", want: "alternative rock"},
		{name: "all uppercase", input: "JAZZ", want: "jazz"},
		{name: "leading whitespace", input: "  indie pop", want: "indie pop"},
		{name: "trailing whitespace", input: "hip hop  ", want: "hip hop"},
		{name: "leading and trailing whitespace", input: "  electronic  ", want: "electronic"},
		{name: "internal whitespace collapse", input: "post   punk  revival", want: "post punk revival"},
		{name: "tabs and newlines", input: "\tDream\n Pop\t", want: "dream pop"},
		{name: "already normalized", input: "folk", want: "folk"},
		{name: "empty string", input: "", want: ""},
		{name: "only whitespace", input: "   ", want: ""},
		{name: "mixed whitespace types", input: " Ambient \t Drone \n Music ", want: "ambient drone music"},
		{name: "death metal tabs", input: "\tDeath\t\tMetal\t", want: "death metal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.input)
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Governing: SPEC-0014 REQ "Tag Normalization"
func TestDisplayName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "trims whitespace, preserves casing", input: "  Shoegaze  ", want: "Shoegaze"},
		{name: "collapses internal whitespace", input: "Post   Punk  Revival", want: "Post Punk Revival"},
		{name: "tabs and newlines", input: "\tDream\n Pop\t", want: "Dream Pop"},
		{name: "already clean", input: "Folk", want: "Folk"},
		{name: "empty string", input: "", want: ""},
		{name: "only whitespace", input: "   ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DisplayName(tt.input)
			if got != tt.want {
				t.Errorf("DisplayName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			// The normalized key must always be the lowercase of the display name.
			if norm := Normalize(tt.input); norm != strings.ToLower(got) {
				t.Errorf("Normalize(%q) = %q, want lowercase of DisplayName %q", tt.input, norm, got)
			}
		})
	}
}
