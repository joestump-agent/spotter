// Package version holds the build-time version string injected via ldflags.
package version

import (
	"regexp"
)

// Version is set at build time via -ldflags "-X spotter/internal/version.Version=..."
// Priority: git tag > branch name > short SHA. Defaults to "dev".
var Version = "dev"

const repo = "https://github.com/joestump/spotter"

// isSHA returns true if s looks like a short git SHA (7 hex chars).
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,8}$`)

// Link returns the GitHub URL appropriate for the current version:
//   - Tag (starts with "v"): links to the GitHub release page
//   - Short SHA (7-8 hex chars): links to the specific commit
//   - Branch name: links to the branch tree
func Link() string {
	if Version == "dev" {
		return repo
	}
	if len(Version) > 0 && Version[0] == 'v' {
		return repo + "/releases/tag/" + Version
	}
	if shaPattern.MatchString(Version) {
		return repo + "/commit/" + Version
	}
	return repo + "/tree/" + Version
}
