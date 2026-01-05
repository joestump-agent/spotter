package components

import (
	"testing"

	"spotter/ent"

	"github.com/stretchr/testify/assert"
)

// TestTrackTableRow_ImageURL tests the ImageURL method for various scenarios
func TestTrackTableRow_ImageURL(t *testing.T) {
	tests := []struct {
		name     string
		row      TrackTableRow
		expected string
	}{
		{
			name: "ExplicitImageURL takes priority",
			row: TrackTableRow{
				ExplicitImageURL: "https://example.com/explicit.jpg",
				Track: &ent.Track{
					Edges: ent.TrackEdges{
						Album: &ent.Album{
							ID: 123,
							Edges: ent.AlbumEdges{
								Images: []*ent.AlbumImage{{ID: 1, LocalPath: "/path/to/image.jpg"}},
							},
						},
					},
				},
			},
			expected: "https://example.com/explicit.jpg",
		},
		{
			name: "Track with album images returns album image URL",
			row: TrackTableRow{
				Track: &ent.Track{
					ID: 1,
					Edges: ent.TrackEdges{
						Album: &ent.Album{
							ID: 42,
							Edges: ent.AlbumEdges{
								Images: []*ent.AlbumImage{{ID: 1, LocalPath: "/path/to/image.jpg"}},
							},
						},
					},
				},
			},
			expected: "/library/album/42.png",
		},
		{
			name: "Listen with album images returns album image URL",
			row: TrackTableRow{
				Listen: &ent.Listen{
					ID: 1,
					Edges: ent.ListenEdges{
						Album: &ent.Album{
							ID: 99,
							Edges: ent.AlbumEdges{
								Images: []*ent.AlbumImage{{ID: 1, LocalPath: "/path/to/image.jpg"}},
							},
						},
					},
				},
			},
			expected: "/library/album/99.png",
		},
		{
			name: "Track album takes precedence over Listen album",
			row: TrackTableRow{
				Track: &ent.Track{
					ID: 1,
					Edges: ent.TrackEdges{
						Album: &ent.Album{
							ID: 10,
							Edges: ent.AlbumEdges{
								Images: []*ent.AlbumImage{{ID: 1, LocalPath: "/path/to/track-album.jpg"}},
							},
						},
					},
				},
				Listen: &ent.Listen{
					ID: 1,
					Edges: ent.ListenEdges{
						Album: &ent.Album{
							ID: 20,
							Edges: ent.AlbumEdges{
								Images: []*ent.AlbumImage{{ID: 2, LocalPath: "/path/to/listen-album.jpg"}},
							},
						},
					},
				},
			},
			expected: "/library/album/10.png",
		},
		{
			name: "Album without images returns empty string",
			row: TrackTableRow{
				Track: &ent.Track{
					ID: 1,
					Edges: ent.TrackEdges{
						Album: &ent.Album{
							ID: 42,
							Edges: ent.AlbumEdges{
								Images: []*ent.AlbumImage{},
							},
						},
					},
				},
			},
			expected: "",
		},
		{
			name: "Album with nil images returns empty string",
			row: TrackTableRow{
				Track: &ent.Track{
					ID: 1,
					Edges: ent.TrackEdges{
						Album: &ent.Album{
							ID: 42,
						},
					},
				},
			},
			expected: "",
		},
		{
			name: "Track without album returns empty string",
			row: TrackTableRow{
				Track: &ent.Track{
					ID: 1,
				},
			},
			expected: "",
		},
		{
			name:     "Empty row returns empty string",
			row:      TrackTableRow{},
			expected: "",
		},
		{
			name: "Nil track returns empty string",
			row: TrackTableRow{
				Track: nil,
			},
			expected: "",
		},
		{
			name: "Listen without album edge returns empty string",
			row: TrackTableRow{
				Listen: &ent.Listen{
					ID: 1,
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.row.ImageURL()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTrackTableRow_Album tests the Album method for various scenarios
func TestTrackTableRow_Album(t *testing.T) {
	albumFromTrack := &ent.Album{ID: 1, Name: "Track Album"}
	albumFromListen := &ent.Album{ID: 2, Name: "Listen Album"}

	tests := []struct {
		name     string
		row      TrackTableRow
		expected *ent.Album
	}{
		{
			name: "Track with album returns track album",
			row: TrackTableRow{
				Track: &ent.Track{
					Edges: ent.TrackEdges{
						Album: albumFromTrack,
					},
				},
			},
			expected: albumFromTrack,
		},
		{
			name: "Listen with album returns listen album",
			row: TrackTableRow{
				Listen: &ent.Listen{
					Edges: ent.ListenEdges{
						Album: albumFromListen,
					},
				},
			},
			expected: albumFromListen,
		},
		{
			name: "Track album takes precedence over Listen album",
			row: TrackTableRow{
				Track: &ent.Track{
					Edges: ent.TrackEdges{
						Album: albumFromTrack,
					},
				},
				Listen: &ent.Listen{
					Edges: ent.ListenEdges{
						Album: albumFromListen,
					},
				},
			},
			expected: albumFromTrack,
		},
		{
			name: "Track without album falls back to Listen album",
			row: TrackTableRow{
				Track: &ent.Track{},
				Listen: &ent.Listen{
					Edges: ent.ListenEdges{
						Album: albumFromListen,
					},
				},
			},
			expected: albumFromListen,
		},
		{
			name:     "Empty row returns nil",
			row:      TrackTableRow{},
			expected: nil,
		},
		{
			name:     "Nil track returns nil",
			row:      TrackTableRow{Track: nil},
			expected: nil,
		},
		{
			name: "Track without album edge returns nil",
			row: TrackTableRow{
				Track: &ent.Track{},
			},
			expected: nil,
		},
		{
			name: "Listen without album edge returns nil",
			row: TrackTableRow{
				Listen: &ent.Listen{},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.row.Album()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTrackTableRow_ImageURL_Regression_AlbumViewMissingCoverArt tests the specific bug
// where album view was missing cover art because Track.Edges.Album was not loaded
func TestTrackTableRow_ImageURL_Regression_AlbumViewMissingCoverArt(t *testing.T) {
	// This test reproduces the bug where albums/show.templ was rendering
	// tracks without cover art because the Album edge wasn't being loaded properly

	albumWithImages := &ent.Album{
		ID:   123,
		Name: "Test Album",
		Edges: ent.AlbumEdges{
			Images: []*ent.AlbumImage{
				{ID: 1, LocalPath: "/data/images/album-123.jpg"},
			},
		},
	}

	// Simulate a track row as it would be created by toTrackRowsFromAlbumStats
	row := TrackTableRow{
		Track: &ent.Track{
			ID:   456,
			Name: "Test Track",
			Edges: ent.TrackEdges{
				Album: albumWithImages,
			},
		},
		PlayCount: 10,
		Index:     1,
	}

	// The ImageURL should return a valid album image URL
	imageURL := row.ImageURL()
	assert.NotEmpty(t, imageURL, "ImageURL should not be empty when track has album with images")
	assert.Equal(t, "/library/album/123.png", imageURL)

	// The Album method should return the album
	album := row.Album()
	assert.NotNil(t, album, "Album should not be nil when track has album edge")
	assert.Equal(t, 123, album.ID)
}

// TestTrackTableRow_ImageURL_Regression_PlaylistViewMissingCoverArt tests the playlist view bug
func TestTrackTableRow_ImageURL_Regression_PlaylistViewMissingCoverArt(t *testing.T) {
	// Test case for playlist view where tracks might come from PlaylistTrack edges

	albumWithImages := &ent.Album{
		ID:   789,
		Name: "Playlist Album",
		Edges: ent.AlbumEdges{
			Images: []*ent.AlbumImage{
				{ID: 1, LocalPath: "/data/images/album-789.jpg"},
			},
		},
	}

	// Simulate a track row as it would be created for playlist view
	row := TrackTableRow{
		Track: &ent.Track{
			ID:   100,
			Name: "Playlist Track",
			Edges: ent.TrackEdges{
				Album: albumWithImages,
			},
		},
		Index: 1,
	}

	imageURL := row.ImageURL()
	assert.NotEmpty(t, imageURL, "ImageURL should not be empty for playlist tracks with album images")
	assert.Equal(t, "/library/album/789.png", imageURL)
}

// TestTrackTableRow_ImageURL_Regression_ArtistViewMissingCoverArt tests the artist view bug
func TestTrackTableRow_ImageURL_Regression_ArtistViewMissingCoverArt(t *testing.T) {
	// Test case for artist view top tracks

	albumWithImages := &ent.Album{
		ID:   555,
		Name: "Artist Album",
		Edges: ent.AlbumEdges{
			Images: []*ent.AlbumImage{
				{ID: 1, LocalPath: "/data/images/album-555.jpg"},
			},
		},
	}

	// Simulate a track row as it would be created by toTrackRowsFromStats
	row := TrackTableRow{
		Track: &ent.Track{
			ID:   200,
			Name: "Top Track",
			Edges: ent.TrackEdges{
				Album: albumWithImages,
			},
		},
		PlayCount: 50,
	}

	imageURL := row.ImageURL()
	assert.NotEmpty(t, imageURL, "ImageURL should not be empty for artist top tracks with album images")
	assert.Equal(t, "/library/album/555.png", imageURL)

	album := row.Album()
	assert.NotNil(t, album, "Album should be retrievable for artist top tracks")
}

// TestTrackTableRow_ExplicitImageURL_Override tests that ExplicitImageURL properly overrides
func TestTrackTableRow_ExplicitImageURL_Override(t *testing.T) {
	// When ExplicitImageURL is set, it should always be returned
	// regardless of what Track or Listen has

	row := TrackTableRow{
		ExplicitImageURL: "https://cdn.example.com/custom-image.jpg",
		Track: &ent.Track{
			Edges: ent.TrackEdges{
				Album: &ent.Album{
					ID: 999,
					Edges: ent.AlbumEdges{
						Images: []*ent.AlbumImage{
							{ID: 1, LocalPath: "/should/not/use/this.jpg"},
						},
					},
				},
			},
		},
	}

	imageURL := row.ImageURL()
	assert.Equal(t, "https://cdn.example.com/custom-image.jpg", imageURL,
		"ExplicitImageURL should take precedence over Track.Album images")
}
