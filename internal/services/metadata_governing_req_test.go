package services

// Regression guard for issue #54: governing comments must cite the REQ
// identifier the metadata-enrichment-pipeline spec actually assigns to a
// behavior. Two drifts were confirmed and fixed:
//   - "enricher error logged, pipeline continues" is REQ-ENRICH-013, not -012
//     (-012 is "OpenAI runs last").
//   - "local path stored" is REQ-ENRICH-030, not -032 (-032 is WebP/PNG/JPEG/
//     GIF format support).
//
// This test anchors those pairings to the spec text so a renumbering forces an
// intentional update here, and scans the source tree so the mislabels cannot
// silently reappear.
//
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-012 (OpenAI runs last),
// REQ-ENRICH-013 (enricher error logged, pipeline continues),
// REQ-ENRICH-030 (image downloaded and local path stored),
// REQ-ENRICH-032 (WebP/PNG/JPEG/GIF format support)

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// moduleRoot walks up from the test's working directory until it finds the
// go.mod for module "spotter".
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
			if strings.HasPrefix(strings.TrimSpace(string(data)), "module spotter") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate module root (go.mod for module spotter)")
		}
		dir = parent
	}
}

// specREQDefinitions parses the metadata-enrichment-pipeline spec and returns a
// map of REQ identifier -> its definition sentence.
func specREQDefinitions(t *testing.T, root string) map[string]string {
	t.Helper()
	specPath := filepath.Join(root, "docs", "openspec", "specs", "metadata-enrichment-pipeline", "spec.md")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	// Matches lines like: **REQ-ENRICH-012** — The OpenAI enricher MUST run last...
	re := regexp.MustCompile(`(?m)^\*\*(REQ-ENRICH-\d+)\*\*\s*[—-]+\s*(.+)$`)
	defs := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		defs[m[1]] = m[2]
	}
	if len(defs) == 0 {
		t.Fatal("no REQ-ENRICH definitions parsed from spec")
	}
	return defs
}

// TestSpecAnchorsForGoverningCitations pins the meaning of the drift-prone REQ
// identifiers to the spec text. If the spec is renumbered these assertions fail
// and signal that the source scan below (and the cited comments) must change.
func TestSpecAnchorsForGoverningCitations(t *testing.T) {
	defs := specREQDefinitions(t, moduleRoot(t))

	anchors := []struct {
		req      string
		mustHave []string
	}{
		{"REQ-ENRICH-012", []string{"OpenAI", "last"}},
		{"REQ-ENRICH-013", []string{"error", "continue"}},
		{"REQ-ENRICH-030", []string{"local", "path"}},
		{"REQ-ENRICH-032", []string{"WebP", "PNG", "JPEG", "GIF"}},
	}
	for _, a := range anchors {
		def, ok := defs[a.req]
		if !ok {
			t.Errorf("%s not defined in spec", a.req)
			continue
		}
		for _, want := range a.mustHave {
			if !strings.Contains(def, want) {
				t.Errorf("%s definition %q does not mention %q; if the spec was renumbered, update the governing citations and this guard", a.req, def, want)
			}
		}
	}
}

// TestGoverningCommentsDoNotDrift scans every source .go file and fails if a
// governing comment reintroduces a known-mislabeled REQ citation, or attaches a
// drift-prone gloss to the wrong REQ number.
func TestGoverningCommentsDoNotDrift(t *testing.T) {
	root := moduleRoot(t)

	// Forbidden: a REQ number paired with a gloss the spec assigns elsewhere.
	forbidden := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{
			regexp.MustCompile(`REQ-ENRICH-012\s*\(enricher error logged`),
			"'enricher error logged, pipeline continues' is REQ-ENRICH-013, not -012 (which is 'OpenAI runs last')",
		},
		{
			regexp.MustCompile(`REQ-ENRICH-032\s*\(local path`),
			"'local path stored' is REQ-ENRICH-030, not -032 (which is WebP/PNG/JPEG/GIF format support)",
		},
	}

	// Positive: whenever these glosses appear, they must carry the right number
	// on the same comment line.
	requirePairing := []struct {
		gloss *regexp.Regexp
		req   string
	}{
		{regexp.MustCompile(`enricher error logged, pipeline continues`), "REQ-ENRICH-013"},
		{regexp.MustCompile(`local path stored on entity after download`), "REQ-ENRICH-030"},
	}

	skipDir := map[string]bool{".git": true, "node_modules": true, "ent": true}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDir[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_templ.go") {
			return nil
		}
		// Do not lint this guard file itself: it names the forbidden patterns.
		if strings.HasSuffix(path, "metadata_governing_req_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "Governing:") && !strings.Contains(line, "REQ-ENRICH-") {
				continue
			}
			for _, f := range forbidden {
				if f.pattern.MatchString(line) {
					t.Errorf("%s:%d cites a drifted REQ number: %s\n  line: %s", rel, i+1, f.why, strings.TrimSpace(line))
				}
			}
			for _, p := range requirePairing {
				if p.gloss.MatchString(line) && !strings.Contains(line, p.req) {
					t.Errorf("%s:%d gloss %q must cite %s\n  line: %s", rel, i+1, p.gloss.String(), p.req, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
