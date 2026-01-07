package vibes

import (
	"regexp"
	"strings"
)

// trailingCommaRegex matches trailing commas before closing braces or brackets.
// This handles cases like: "key": "value", } or "item", ]
var trailingCommaRegex = regexp.MustCompile(`,(\s*[}\]])`)

// SanitizeJSON cleans up common JSON issues in AI responses, particularly:
// - Trailing commas before closing braces/brackets (e.g., {"key": "value",})
// - Extra whitespace that might cause parsing issues
func SanitizeJSON(input string) string {
	// Remove trailing commas before } or ]
	// The regex captures the whitespace and closing character, replacing ", }" with " }"
	sanitized := trailingCommaRegex.ReplaceAllString(input, "$1")

	return sanitized
}

// ExtractAndSanitizeJSON extracts JSON from text (handling markdown code blocks)
// and sanitizes it for safe parsing.
func ExtractAndSanitizeJSON(response string) string {
	jsonStr := extractJSONFromText(response)
	return SanitizeJSON(jsonStr)
}

// extractJSONFromText extracts JSON from the response, handling markdown code blocks.
func extractJSONFromText(response string) string {
	var jsonStr string

	// Try to find JSON in markdown code blocks with json language tag
	if start := strings.Index(response, "```json"); start != -1 {
		start += 7
		if end := strings.Index(response[start:], "```"); end != -1 {
			jsonStr = strings.TrimSpace(response[start : start+end])
		}
	}

	// Try plain code blocks
	if jsonStr == "" {
		if start := strings.Index(response, "```"); start != -1 {
			start += 3
			// Skip any language identifier on the same line
			if newline := strings.Index(response[start:], "\n"); newline != -1 {
				start += newline + 1
			}
			if end := strings.Index(response[start:], "```"); end != -1 {
				jsonStr = strings.TrimSpace(response[start : start+end])
			}
		}
	}

	// Try to find raw JSON object
	if jsonStr == "" {
		if start := strings.Index(response, "{"); start != -1 {
			if end := strings.LastIndex(response, "}"); end != -1 && end > start {
				jsonStr = strings.TrimSpace(response[start : end+1])
			}
		}
	}

	if jsonStr == "" {
		jsonStr = strings.TrimSpace(response)
	}

	return jsonStr
}

// ExtractJSONObject attempts to extract a JSON object from text using brace matching.
// This is useful when the JSON might be embedded in other text.
func ExtractJSONObject(text string) string {
	// Find JSON object
	start := strings.Index(text, "{")
	if start == -1 {
		return ""
	}

	// Find the matching closing brace
	depth := 0
	inString := false
	escape := false

	for i := start; i < len(text); i++ {
		c := text[i]

		if escape {
			escape = false
			continue
		}

		if c == '\\' && inString {
			escape = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return SanitizeJSON(text[start : i+1])
			}
		}
	}

	return ""
}
