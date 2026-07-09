package components

// Governing: SPEC-0014 REQ "UI Tag Visual Differentiation", ADR-0025 (Unified Tag Taxonomy)

import (
	"sort"

	"spotter/ent"
)

// tagTypeOrder defines the display order of tag types on show pages: genres
// lead, AI tags trail (mirroring the legacy genres-then-AI layout), with the
// remaining types in between.
var tagTypeOrder = map[string]int{
	"genre":  0,
	"label":  1,
	"id3":    2,
	"source": 3,
	"ai":     4,
}

// TagViewsFromEnt converts eager-loaded Tag entities into TagViews sorted by
// tag type (see tagTypeOrder) and then by name, ready for TypedTagBadge.
func TagViewsFromEnt(entTags []*ent.Tag) []TagView {
	views := make([]TagView, 0, len(entTags))
	for _, t := range entTags {
		if t == nil || t.Name == "" {
			continue
		}
		views = append(views, TagView{Name: t.Name, TagType: string(t.TagType)})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].TagType != views[j].TagType {
			return tagTypeOrder[views[i].TagType] < tagTypeOrder[views[j].TagType]
		}
		return views[i].Name < views[j].Name
	})
	return views
}
