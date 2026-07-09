// Governing: SPEC-0015 REQ "Email Content", ADR-0026
package notifications

import (
	"bytes"
	"fmt"
	"text/template"
	"time"

	"spotter/internal/services"
)

var emailTemplate = template.Must(template.New("syncFailure").Parse(`Spotter Sync Failure: {{ .Provider }}

Hi,

Your {{ .Provider }} sync has encountered a fatal error that requires your attention.

Provider: {{ .Provider }}
Error: {{ .ErrorSummary }}
When: {{ .Timestamp }}

This error will not resolve on its own. Please check your provider connection
in Spotter Preferences:

{{ .PreferencesURL }}

You will not receive another notification for this provider for {{ .CooldownDays }} days
unless the issue is resolved and a new failure occurs.

— Spotter
`))

// providerDisplayNames maps internal provider keys (see internal/providers)
// to the human-readable names used in email subjects and bodies.
// Governing: SPEC-0015 REQ "Email Content"
var providerDisplayNames = map[string]string{
	"spotify":   "Spotify",
	"lastfm":    "Last.fm",
	"navidrome": "Navidrome",
}

// providerDisplayName returns the display name for a provider key, falling
// back to the raw key for unknown providers.
// Governing: SPEC-0015 REQ "Email Content"
func providerDisplayName(provider string) string {
	if name, ok := providerDisplayNames[provider]; ok {
		return name
	}
	return provider
}

// Governing: SPEC-0015 REQ "Email Content" — no credential leakage
// sanitizeError returns a safe, human-readable error summary without any
// tokens, passwords, salts, or raw API response bodies.
func sanitizeError(syncErr error) string {
	errClass := services.ClassifyError(syncErr)
	switch errClass {
	case services.ErrorClassFatal:
		return "Authentication failed (fatal)"
	default:
		return "Sync error"
	}
}

type emailData struct {
	Provider       string
	ErrorSummary   string
	Timestamp      string
	PreferencesURL string
	CooldownDays   int
}

func buildEmail(provider string, syncErr error, baseURL string, cooldownDays int) (subject, body string) {
	// Governing: SPEC-0015 REQ "Email Content" — display name and spec subject format
	displayName := providerDisplayName(provider)
	subject = fmt.Sprintf("[Spotter] %s sync error — action required", displayName)

	data := emailData{
		Provider:       displayName,
		ErrorSummary:   sanitizeError(syncErr),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		PreferencesURL: baseURL + "/preferences/providers",
		CooldownDays:   cooldownDays,
	}

	var buf bytes.Buffer
	if err := emailTemplate.Execute(&buf, data); err != nil {
		body = fmt.Sprintf("Spotter: %s sync failed. Please check your provider connection at %s/preferences/providers", displayName, baseURL)
		return subject, body
	}

	return subject, buf.String()
}
