package components

import (
	"strconv"
)

// FormatNumber formats an integer with comma separators (e.g. 1,234,567)
func FormatNumber(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return s
	}
	var result string
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result += ","
		}
		result += string(c)
	}
	return result
}
