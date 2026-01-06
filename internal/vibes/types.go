package vibes

import (
	"context"

	"spotter/ent"
)

// SeedType represents the type of seed data used for mixtape generation.
type SeedType string

const (
	SeedTypeNone   SeedType = ""
	SeedTypeArtist SeedType = "artist"
	SeedTypeAlbum  SeedType = "album"
	SeedTypeTracks SeedType = "tracks"
)

// Seed represents the seed data for mixtape generation.
// It can be an Artist, Album, or a list of Tracks.
type Seed struct {
	Type     SeedType
	Artist   *ent.Artist
	Album    *ent.Album
	Tracks   []*ent.Track
	TrackIDs []int // Track IDs if provided directly
}

// NewArtistSeed creates a seed from an artist.
func NewArtistSeed(artist *ent.Artist) *Seed {
	return &Seed{
		Type:   SeedTypeArtist,
		Artist: artist,
	}
}

// NewAlbumSeed creates a seed from an album.
func NewAlbumSeed(album *ent.Album) *Seed {
	return &Seed{
		Type:  SeedTypeAlbum,
		Album: album,
	}
}

// NewTracksSeed creates a seed from a list of tracks.
func NewTracksSeed(tracks []*ent.Track) *Seed {
	return &Seed{
		Type:   SeedTypeTracks,
		Tracks: tracks,
	}
}

// NewTrackIDsSeed creates a seed from a list of track IDs.
func NewTrackIDsSeed(trackIDs []int) *Seed {
	return &Seed{
		Type:     SeedTypeTracks,
		TrackIDs: trackIDs,
	}
}

// GenerationRequest contains the parameters for generating a mixtape.
type GenerationRequest struct {
	// Mixtape is the mixtape entity to generate tracks for
	Mixtape *ent.Mixtape
	// DJ is the DJ persona to use for generation
	DJ *ent.DJ
	// Seed is optional seed data (Artist, Album, or Tracks)
	Seed *Seed
	// MaxTracks overrides the mixtape's max_tracks setting if > 0
	MaxTracks int
	// UserID is the ID of the user generating the mixtape
	UserID int
}

// GeneratedTrack represents a track suggested by the AI.
type GeneratedTrack struct {
	// ID is the track ID from our library (if matched)
	ID int
	// ExternalID is the ID from the AI response (for debugging)
	ExternalID string
	// Name is the track name
	Name string
	// Artist is the artist name
	Artist string
	// Reason is the AI's explanation for including this track
	Reason string
	// Matched indicates if the track was found in the user's library
	Matched bool
	// MatchConfidence is the confidence score for fuzzy matches (0.0-1.0)
	MatchConfidence float64
}

// GenerationResult contains the result of a mixtape generation.
type GenerationResult struct {
	// Tracks is the list of generated tracks
	Tracks []GeneratedTrack
	// FlowDescription describes the overall flow of the mixtape
	FlowDescription string
	// OpeningThoughts is what the DJ would say to introduce the mixtape
	OpeningThoughts string
	// ClosingThoughts is what the DJ would say to close out the mixtape
	ClosingThoughts string
	// PromptUsed is the full prompt sent to the AI (for debugging)
	PromptUsed string
	// ModelUsed is the AI model used for generation
	ModelUsed string
	// TokensUsed is the number of tokens consumed
	TokensUsed int
	// MatchedCount is how many tracks were successfully matched
	MatchedCount int
	// UnmatchedCount is how many tracks could not be matched
	UnmatchedCount int
}

// GenerationStats contains statistics about a generation operation.
type GenerationStats struct {
	// TotalSuggested is the total tracks suggested by the AI
	TotalSuggested int
	// TotalMatched is the tracks matched to the library
	TotalMatched int
	// TotalUnmatched is the tracks that couldn't be matched
	TotalUnmatched int
	// MatchRate is the percentage of tracks matched
	MatchRate float64
	// TokensUsed is the total tokens consumed
	TokensUsed int
	// DurationMs is how long the generation took in milliseconds
	DurationMs int64
}

// Generator defines the interface for mixtape generation.
type Generator interface {
	// GenerateMixtape generates tracks for a mixtape based on the DJ persona and optional seed.
	GenerateMixtape(ctx context.Context, req *GenerationRequest) (*GenerationResult, error)
}

// HistoryEntry represents a track from the user's listening history.
type HistoryEntry struct {
	TrackName  string
	ArtistName string
	AlbumName  string
	PlayCount  int
}

// AvailableTrack represents a track available in the user's library for selection.
type AvailableTrack struct {
	ID      int
	Name    string
	Artist  string
	Album   string
	Genres  []string
	Tags    []string
	Energy  *float64
	Valence *float64
	BPM     *float64
}

// SeedArtistData contains artist data for the prompt template.
type SeedArtistData struct {
	Name      string
	Genres    []string
	Bio       string
	AISummary string
}

// SeedAlbumData contains album data for the prompt template.
type SeedAlbumData struct {
	Name      string
	Artist    string
	Year      int
	Genre     string
	AISummary string
}

// SeedTrackData contains track data for the prompt template.
type SeedTrackData struct {
	Name   string
	Artist string
	Album  string
}

// TemplateData contains all data needed to render the mixtape generation prompt.
type TemplateData struct {
	// DJ information
	DJName         string
	DJSystemPrompt string
	GenresInclude  []string
	GenresExclude  []string
	Vibes          []string
	ArtistsInclude []string
	ArtistsExclude []string

	// Seed information
	SeedType   SeedType
	SeedArtist *SeedArtistData
	SeedAlbum  *SeedAlbumData
	SeedTracks []SeedTrackData

	// User context
	ListeningHistory []HistoryEntry
	DislikedTracks   []SeedTrackData
	AvailableTracks  []AvailableTrack

	// Mixtape settings
	MixtapeName        string
	MixtapeDescription string
	MaxTracks          int
}

// AIResponse represents the parsed response from the AI.
type AIResponse struct {
	Tracks []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Artist string `json:"artist"`
		Reason string `json:"reason"`
	} `json:"tracks"`
	FlowDescription string `json:"flow_description"`
	OpeningThoughts string `json:"opening_thoughts"`
	ClosingThoughts string `json:"closing_thoughts"`
}

// EnhancementMode defines how the enhancement should be applied.
type EnhancementMode string

const (
	// EnhancementModeOneTime applies changes directly to Navidrome without creating a Mixtape.
	EnhancementModeOneTime EnhancementMode = "one_time"
	// EnhancementModeConvertToMixtape converts the playlist into a DJ-managed Mixtape.
	EnhancementModeConvertToMixtape EnhancementMode = "convert_to_mixtape"
)

// EnhancementRequest contains the parameters for enhancing a playlist.
type EnhancementRequest struct {
	// PlaylistID is the ID of the playlist to enhance
	PlaylistID int
	// DJID is the ID of the DJ persona to use
	DJID int
	// Mode determines whether to apply one-time or convert to mixtape
	Mode EnhancementMode
	// MaxNewTracks is the maximum number of new tracks to suggest (default: 5)
	MaxNewTracks int
	// UserID is the ID of the user making the request
	UserID int
}

// ExistingTrack represents a track already in the playlist.
type ExistingTrack struct {
	ID       int      // Internal track ID
	Name     string   // Track name
	Artist   string   // Artist name
	Album    string   // Album name
	Genres   []string // Track genres
	Energy   *float64 // Energy level (0-1)
	BPM      *float64 // Beats per minute
	Position int      // Current position in playlist
}

// EnhancedTrack represents a track in the enhanced playlist.
type EnhancedTrack struct {
	// ID is the track ID (prefixed with EXISTING: or ADD:)
	ID string
	// InternalID is the numeric track ID in our database
	InternalID int
	// Name is the track name
	Name string
	// Artist is the artist name
	Artist string
	// Position is the new position in the playlist (1-based)
	Position int
	// Reason explains why this track is placed here
	Reason string
	// IsNew indicates if this is a newly added track
	IsNew bool
	// Matched indicates if the track was found in the library
	Matched bool
}

// EnhancementResult contains the result of a playlist enhancement.
type EnhancementResult struct {
	// ReorderedTracks contains all tracks in their new order
	ReorderedTracks []EnhancedTrack
	// NewTracks contains only the newly added tracks
	NewTracks []EnhancedTrack
	// FlowDescription describes the enhanced playlist's journey
	FlowDescription string
	// EnhancementSummary summarizes what was changed
	EnhancementSummary string
	// OpeningThoughts is the DJ's commentary
	OpeningThoughts string
	// PromptUsed is the full prompt sent to the AI
	PromptUsed string
	// ModelUsed is the AI model used
	ModelUsed string
	// TokensUsed is the number of tokens consumed
	TokensUsed int
	// OriginalTrackCount is how many tracks were originally in the playlist
	OriginalTrackCount int
	// FinalTrackCount is the total tracks after enhancement
	FinalTrackCount int
	// TracksAdded is how many new tracks were added
	TracksAdded int
}

// EnhancementTemplateData contains all data needed to render the enhancement prompt.
type EnhancementTemplateData struct {
	// DJ information
	DJName         string
	DJSystemPrompt string
	GenresInclude  []string
	GenresExclude  []string
	Vibes          []string
	ArtistsInclude []string
	ArtistsExclude []string

	// Playlist information
	PlaylistName        string
	PlaylistDescription string
	ExistingTracks      []ExistingTrack

	// Available tracks for addition
	AvailableTracks []AvailableTrack

	// User context
	ListeningHistory []HistoryEntry

	// Enhancement settings
	MaxNewTracks int
}

// EnhancementAIResponse represents the parsed AI response for enhancement.
type EnhancementAIResponse struct {
	ReorderedTracks []struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
		Reason   string `json:"reason"`
	} `json:"reordered_tracks"`
	NewTracks []struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
		Reason   string `json:"reason"`
	} `json:"new_tracks"`
	FlowDescription    string `json:"flow_description"`
	EnhancementSummary string `json:"enhancement_summary"`
	OpeningThoughts    string `json:"opening_thoughts"`
}
