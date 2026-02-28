package tags

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Rock", "rock"},
		{"  Hip Hop  ", "hip hop"},
		{"Electronic  Music", "electronic music"},
		{"  JAZZ  ", "jazz"},
		{"", ""},
		{"indie rock", "indie rock"},
		{"\tDeath\t\tMetal\t", "death metal"},
	}

	for _, tt := range tests {
		got := Normalize(tt.input)
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
