package vibes

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no trailing commas",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "trailing comma before closing brace",
			input:    `{"key": "value",}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "trailing comma before closing bracket",
			input:    `["item1", "item2",]`,
			expected: `["item1", "item2"]`,
		},
		{
			name:     "trailing comma with whitespace",
			input:    `{"key": "value", }`,
			expected: `{"key": "value" }`,
		},
		{
			name:     "trailing comma with newline",
			input:    "{\n  \"key\": \"value\",\n}",
			expected: "{\n  \"key\": \"value\"\n}",
		},
		{
			name:     "multiple trailing commas",
			input:    `{"arr": ["a", "b",], "obj": {"x": 1,},}`,
			expected: `{"arr": ["a", "b"], "obj": {"x": 1}}`,
		},
		{
			name:     "nested objects with trailing commas",
			input:    `{"outer": {"inner": "value",},}`,
			expected: `{"outer": {"inner": "value"}}`,
		},
		{
			name:     "empty object",
			input:    `{}`,
			expected: `{}`,
		},
		{
			name:     "empty array",
			input:    `[]`,
			expected: `[]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeJSON(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSanitizeJSON_AITrailingCommaFailure tests the exact failure case from the issue
// where AI returns JSON with trailing commas causing json.Unmarshal to fail
func TestSanitizeJSON_AITrailingCommaFailure(t *testing.T) {
	// This is the exact failure string pattern from the issue description
	// AI responses often include trailing commas which Go's strict JSON parser rejects
	failureString := `{
  "summary": "A vibrant collection spanning multiple genres",
  "tracks": [
    {
      "id": "123",
      "name": "Test Track",
      "artist": "Test Artist",
      "reason": "Great opener for the mixtape",
    },
    {
      "id": "456",
      "name": "Another Track",
      "artist": "Another Artist",
      "reason": "Builds the energy",
    },
  ],
  "flow_description": "An energetic journey through sound",
  "opening_thoughts": "Let's get this party started!",
  "closing_thoughts": "Thanks for listening!",
}`

	// First, verify the original string fails to parse
	var beforeSanitize AIResponse
	err := json.Unmarshal([]byte(failureString), &beforeSanitize)
	require.Error(t, err, "Original string with trailing commas should fail to parse")
	assert.Contains(t, err.Error(), "invalid character", "Error should be about invalid character")

	// Apply sanitization
	sanitized := SanitizeJSON(failureString)

	// Now verify the sanitized string parses correctly
	var afterSanitize AIResponse
	err = json.Unmarshal([]byte(sanitized), &afterSanitize)
	require.NoError(t, err, "Sanitized string should parse successfully")

	// Verify the struct is populated correctly
	assert.Len(t, afterSanitize.Tracks, 2)
	assert.Equal(t, "123", afterSanitize.Tracks[0].ID)
	assert.Equal(t, "Test Track", afterSanitize.Tracks[0].Name)
	assert.Equal(t, "Test Artist", afterSanitize.Tracks[0].Artist)
	assert.Equal(t, "Great opener for the mixtape", afterSanitize.Tracks[0].Reason)
	assert.Equal(t, "456", afterSanitize.Tracks[1].ID)
	assert.Equal(t, "Another Track", afterSanitize.Tracks[1].Name)
	assert.Equal(t, "Another Artist", afterSanitize.Tracks[1].Artist)
	assert.Equal(t, "Builds the energy", afterSanitize.Tracks[1].Reason)
	assert.Equal(t, "An energetic journey through sound", afterSanitize.FlowDescription)
	assert.Equal(t, "Let's get this party started!", afterSanitize.OpeningThoughts)
	assert.Equal(t, "Thanks for listening!", afterSanitize.ClosingThoughts)
}

func TestExtractAndSanitizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "raw JSON with trailing comma",
			input:    `{"key": "value",}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON in markdown with trailing comma",
			input:    "```json\n{\"key\": \"value\",}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON in plain code block with trailing comma",
			input:    "```\n{\"key\": \"value\",}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON with surrounding text and trailing comma",
			input:    "Here is the response:\n{\"key\": \"value\",}\n\nThat's all!",
			expected: `{"key": "value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractAndSanitizeJSON(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple object",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "object with trailing comma",
			input:    `{"key": "value",}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "object embedded in text",
			input:    `Some text before {"key": "value",} and after`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "nested objects with braces in strings",
			input:    `{"message": "Use { and } carefully", "nested": {"a": 1,},}`,
			expected: `{"message": "Use { and } carefully", "nested": {"a": 1}}`,
		},
		{
			name:     "object with escaped quotes",
			input:    `{"quote": "He said \"hello\"",}`,
			expected: `{"quote": "He said \"hello\""}`,
		},
		{
			name:     "no JSON object",
			input:    `This is just plain text`,
			expected: ``,
		},
		{
			name:     "complex nested structure",
			input:    `{"arr": [{"x": 1,}, {"y": 2,},], "obj": {"nested": "value",},}`,
			expected: `{"arr": [{"x": 1}, {"y": 2}], "obj": {"nested": "value"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractJSONObject(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestExtractJSONObject_EnhancementResponse tests extraction with EnhancementAIResponse structure
func TestExtractJSONObject_EnhancementResponse(t *testing.T) {
	input := `{
  "reordered_tracks": [
    {
      "id": "EXISTING:1",
      "position": 1,
      "reason": "Great opener",
    },
  ],
  "new_tracks": [
    {
      "id": "ADD:100",
      "position": 2,
      "reason": "Perfect follow-up",
    },
  ],
  "flow_description": "A smooth journey",
  "enhancement_summary": "Reordered for better flow",
  "opening_thoughts": "Check out this enhanced playlist!",
}`

	result := ExtractJSONObject(input)

	var response EnhancementAIResponse
	err := json.Unmarshal([]byte(result), &response)
	require.NoError(t, err, "Should parse enhancement response after sanitization")

	assert.Len(t, response.ReorderedTracks, 1)
	assert.Equal(t, "EXISTING:1", response.ReorderedTracks[0].ID)
	assert.Equal(t, 1, response.ReorderedTracks[0].Position)

	assert.Len(t, response.NewTracks, 1)
	assert.Equal(t, "ADD:100", response.NewTracks[0].ID)
	assert.Equal(t, 2, response.NewTracks[0].Position)

	assert.Equal(t, "A smooth journey", response.FlowDescription)
	assert.Equal(t, "Reordered for better flow", response.EnhancementSummary)
	assert.Equal(t, "Check out this enhanced playlist!", response.OpeningThoughts)
}

// TestSanitizeJSON_PreservesValidJSON ensures sanitization doesn't break valid JSON
func TestSanitizeJSON_PreservesValidJSON(t *testing.T) {
	validJSON := `{
  "tracks": [
    {"id": "1", "name": "Track One", "artist": "Artist A", "reason": "Test"},
    {"id": "2", "name": "Track Two", "artist": "Artist B", "reason": "Test"}
  ],
  "flow_description": "A journey",
  "opening_thoughts": "Hello!",
  "closing_thoughts": "Goodbye!"
}`

	sanitized := SanitizeJSON(validJSON)

	// Parse both original and sanitized
	var original, sanitizedParsed AIResponse
	err := json.Unmarshal([]byte(validJSON), &original)
	require.NoError(t, err)

	err = json.Unmarshal([]byte(sanitized), &sanitizedParsed)
	require.NoError(t, err)

	// Verify they produce identical results
	assert.Equal(t, original.FlowDescription, sanitizedParsed.FlowDescription)
	assert.Equal(t, original.OpeningThoughts, sanitizedParsed.OpeningThoughts)
	assert.Equal(t, original.ClosingThoughts, sanitizedParsed.ClosingThoughts)
	assert.Equal(t, len(original.Tracks), len(sanitizedParsed.Tracks))
}
