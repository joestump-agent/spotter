package enrichers

import (
	"context"

	"spotter/ent"
)

// Type identifies the enricher source.
type Type string

const (
	TypeMusicBrainz Type = "musicbrainz"
	TypeSpotify     Type = "spotify"
	TypeFanart      Type = "fanart"
	TypeLastFM      Type = "lastfm"
	TypeNavidrome   Type = "navidrome"
	TypeOpenAI      Type = "openai"
	TypeLidarr      Type = "lidarr"
)

// Priority defines the order in which enrichers run (lower = earlier).
type Priority int

// ArtistData contains enrichment data for an artist.
type ArtistData struct {
	MusicBrainzID string
	SpotifyID     string
	LastFMURL     string
	NavidromeID   string
	LidarrID      string
	SortName      string
	Bio           string
	Tags          []string
	Genres        []string
	Popularity    *int
	FollowerCount *int
	// AI-generated fields
	AISummary   string
	AIBiography string
	AITags      []string
}

// AlbumData contains enrichment data for an album.
type AlbumData struct {
	MusicBrainzID string
	SpotifyID     string
	LidarrID      string
	ReleaseDate   string
	Year          int
	Genre         string
	Tags          []string
	AlbumType     string // album, single, compilation, ep
	Label         string
	TotalTracks   int
	Popularity    int
	// AI-generated fields
	AISummary          string
	AITags             []string
	DominantColors     []string
	CoverArtCommentary string
}

// TrackData contains enrichment data for a track.
type TrackData struct {
	MusicBrainzID    string
	SpotifyID        string
	NavidromeID      string
	LidarrID         string
	LidarrStatus     string
	ISRC             string
	DurationMs       int
	TrackNumber      int
	DiscNumber       int
	BPM              *float64
	MusicalKey       string
	Energy           *float64
	Danceability     *float64
	Valence          *float64
	Acousticness     *float64
	Instrumentalness *float64
	Popularity       *int
	Tags             []string
	Genres           []string
	SpotifyURL       string
	MusicBrainzURL   string
	// AI-generated fields
	AISummary string
	AITags    []string
}

// ImageData contains data about an image to download.
type ImageData struct {
	URL       string
	LocalPath string
	Type      string // thumbnail, background, logo, banner, fanart, cover_front, cover_back, cd_art, etc.
	Source    string // provider name
	Width     int
	Height    int
	Likes     int  // popularity from fanart.tv
	IsPrimary bool // whether this should be the primary image
}

// Enricher is the base interface that all metadata enrichers must implement.
type Enricher interface {
	// Type returns the identifier for this enricher.
	Type() Type

	// Name returns a human-readable name for this enricher.
	Name() string

	// IsAvailable checks if this enricher is properly configured and can be used.
	IsAvailable() bool
}

// ArtistEnricher is implemented by enrichers that can add metadata to artists.
type ArtistEnricher interface {
	Enricher

	// EnrichArtist fetches and returns enrichment data for an artist.
	// The enricher should use available identifiers (name, IDs) to find the artist.
	EnrichArtist(ctx context.Context, artist *ent.Artist) (*ArtistData, error)

	// GetArtistImages returns available images for the artist.
	GetArtistImages(ctx context.Context, artist *ent.Artist) ([]ImageData, error)
}

// AlbumEnricher is implemented by enrichers that can add metadata to albums.
type AlbumEnricher interface {
	Enricher

	// EnrichAlbum fetches and returns enrichment data for an album.
	EnrichAlbum(ctx context.Context, album *ent.Album) (*AlbumData, error)

	// GetAlbumImages returns available images for the album.
	GetAlbumImages(ctx context.Context, album *ent.Album) ([]ImageData, error)
}

// TrackEnricher is implemented by enrichers that can add metadata to tracks.
type TrackEnricher interface {
	Enricher

	// EnrichTrack fetches and returns enrichment data for a track.
	EnrichTrack(ctx context.Context, track *ent.Track) (*TrackData, error)
}

// IDMatcher is implemented by enrichers that can match local entities to external IDs.
// This is typically the first step before enrichment - finding the correct external entity.
type IDMatcher interface {
	Enricher

	// MatchArtist attempts to find an external ID for the given artist.
	MatchArtist(ctx context.Context, name string) (externalID string, confidence float64, err error)

	// MatchAlbum attempts to find an external ID for the given album.
	MatchAlbum(ctx context.Context, albumName, artistName string) (externalID string, confidence float64, err error)

	// MatchTrack attempts to find an external ID for the given track.
	MatchTrack(ctx context.Context, trackName, artistName, albumName string) (externalID string, confidence float64, err error)
}

// Factory defines the function signature for creating an enricher instance.
// Returns nil, nil if the enricher is not configured/available.
type Factory func(ctx context.Context, user *ent.User) (Enricher, error)

// Registry holds all registered enricher factories.
type Registry struct {
	factories map[Type]Factory
}

// NewRegistry creates a new enricher registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[Type]Factory),
	}
}

// Register adds a factory for the given enricher type.
func (r *Registry) Register(t Type, factory Factory) {
	r.factories[t] = factory
}

// Get returns the factory for the given enricher type.
func (r *Registry) Get(t Type) (Factory, bool) {
	f, ok := r.factories[t]
	return f, ok
}

// Types returns all registered enricher types.
func (r *Registry) Types() []Type {
	types := make([]Type, 0, len(r.factories))
	for t := range r.factories {
		types = append(types, t)
	}
	return types
}

// ParseType converts a string to an enricher Type.
func ParseType(s string) (Type, bool) {
	switch Type(s) {
	case TypeMusicBrainz, TypeSpotify, TypeFanart, TypeLastFM, TypeNavidrome, TypeOpenAI, TypeLidarr:
		return Type(s), true
	default:
		return "", false
	}
}

// DefaultOrder returns the default enricher execution order.
// MusicBrainz first for ID matching, then others for metadata, OpenAI last for AI enrichment.
func DefaultOrder() []Type {
	return []Type{
		TypeMusicBrainz,
		TypeLidarr,
		TypeNavidrome,
		TypeSpotify,
		TypeLastFM,
		TypeFanart,
		TypeOpenAI,
	}
}
