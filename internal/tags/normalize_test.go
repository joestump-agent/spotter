package tags

import "testing"

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
