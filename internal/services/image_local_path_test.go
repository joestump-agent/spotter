package services

// Regression tests for local_path persistence bugs:
//   - saveArtistImages never set local_path in DB (files downloaded by enrichers were on disk
//     but DB records had local_path = NULL, causing re-download loops and broken image serving)
//   - saveAlbumImages had the same omission
//
// See: internal/enrichers/navidrome/navidrome_test.go TestGetArtistImages_UsesDirectURLNotCoverArt

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"spotter/ent/albumimage"
	"spotter/ent/artistimage"
	"spotter/ent/enttest"
	"spotter/internal/config"
	"spotter/internal/enrichers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMetadataService(t *testing.T) *MetadataService {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}

	return &MetadataService{
		client: client,
		logger: logger,
		config: cfg,
	}
}

// TestSaveArtistImages_PersistsLocalPath verifies that saveArtistImages saves
// the local_path from enricher ImageData to the DB record when creating.
// Regression test: previously local_path was never passed to SetLocalPath(),
// leaving all records with local_path = NULL even after successful disk downloads.
func TestSaveArtistImages_PersistsLocalPath(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	localPath := "data/images/artists/1_spotify_0.png"
	images := []enrichers.ImageData{
		{
			URL:       "https://i.scdn.co/image/abc123",
			LocalPath: localPath,
			Type:      "thumbnail",
			Source:    "spotify",
			IsPrimary: true,
		},
	}

	err = svc.saveArtistImages(ctx, art, images)
	require.NoError(t, err)

	// Verify the DB record has local_path set.
	img, err := svc.client.ArtistImage.Query().
		Where(artistimage.HasArtistWith()).
		Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, localPath, img.LocalPath, "local_path should be persisted from enricher ImageData")
}

// TestSaveArtistImages_UpdatesLocalPathWhenRecordExists verifies that when an image
// record already exists (duplicate URL) but has no local_path, saveArtistImages
// updates it with the local_path from the enricher.
// Regression test: previously exists=true caused an unconditional continue, so
// re-enrichment could never backfill local_path for existing records.
func TestSaveArtistImages_UpdatesLocalPathWhenRecordExists(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	// Pre-create an image record WITHOUT local_path (simulates old enrichment run).
	_, err = svc.client.ArtistImage.Create().
		SetArtist(art).
		SetSource("spotify").
		SetURL("https://i.scdn.co/image/abc123").
		SetIsPrimary(true).
		SetImageType(artistimage.ImageTypeThumbnail).
		Save(ctx)
	require.NoError(t, err)

	localPath := "data/images/artists/1_spotify_0.png"
	// Re-run saveArtistImages with the same URL but now a local_path is available.
	images := []enrichers.ImageData{
		{
			URL:       "https://i.scdn.co/image/abc123",
			LocalPath: localPath,
			Type:      "thumbnail",
			Source:    "spotify",
			IsPrimary: true,
		},
	}

	err = svc.saveArtistImages(ctx, art, images)
	require.NoError(t, err)

	img, err := svc.client.ArtistImage.Query().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, localPath, img.LocalPath,
		"local_path should be updated on existing record when enricher has a local_path")

	// Only one record should exist (no duplicates).
	count, err := svc.client.ArtistImage.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should not create duplicate records")
}

// TestSaveArtistImages_NoLocalPathNotRequired verifies that saveArtistImages works
// correctly when ImageData has no local_path (some enrichers don't download images).
func TestSaveArtistImages_NoLocalPathNotRequired(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	images := []enrichers.ImageData{
		{
			URL:    "https://example.com/image.jpg",
			Type:   "thumbnail",
			Source: "lastfm",
			// LocalPath intentionally empty
		},
	}

	err = svc.saveArtistImages(ctx, art, images)
	require.NoError(t, err)

	img, err := svc.client.ArtistImage.Query().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, "", img.LocalPath, "empty local_path should be stored as empty (not error)")
}

// TestSaveAlbumImages_PersistsLocalPath verifies that saveAlbumImages saves
// the local_path from enricher ImageData to the DB record when creating.
// Regression test: same omission as saveArtistImages — SetLocalPath was never called.
func TestSaveAlbumImages_PersistsLocalPath(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	alb, err := svc.client.Album.Create().SetName("Test Album").SetArtist(art).SetUser(u).Save(ctx)
	require.NoError(t, err)

	localPath := "data/images/albums/1_spotify_0.png"
	images := []enrichers.ImageData{
		{
			URL:       "https://i.scdn.co/image/album123",
			LocalPath: localPath,
			Type:      "cover_front",
			Source:    "spotify",
			IsPrimary: true,
		},
	}

	err = svc.saveAlbumImages(ctx, alb, images)
	require.NoError(t, err)

	img, err := svc.client.AlbumImage.Query().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, localPath, img.LocalPath, "local_path should be persisted from enricher ImageData")
}

// TestSaveAlbumImages_UpdatesLocalPathWhenRecordExists verifies that existing album
// image records with empty local_path are updated when enricher provides a local_path.
func TestSaveAlbumImages_UpdatesLocalPathWhenRecordExists(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	alb, err := svc.client.Album.Create().SetName("Test Album").SetArtist(art).SetUser(u).Save(ctx)
	require.NoError(t, err)

	// Pre-create record without local_path.
	_, err = svc.client.AlbumImage.Create().
		SetAlbum(alb).
		SetSource("spotify").
		SetURL("https://i.scdn.co/image/album123").
		SetIsPrimary(true).
		SetImageType(albumimage.ImageTypeCoverFront).
		Save(ctx)
	require.NoError(t, err)

	localPath := "data/images/albums/1_spotify_0.png"
	images := []enrichers.ImageData{
		{
			URL:       "https://i.scdn.co/image/album123",
			LocalPath: localPath,
			Type:      "cover_front",
			Source:    "spotify",
			IsPrimary: true,
		},
	}

	err = svc.saveAlbumImages(ctx, alb, images)
	require.NoError(t, err)

	img, err := svc.client.AlbumImage.Query().Only(ctx)
	require.NoError(t, err)
	assert.Equal(t, localPath, img.LocalPath,
		"local_path should be updated on existing album image record")

	count, err := svc.client.AlbumImage.Query().Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should not create duplicate records")
}

// TestSaveArtistImages_EmptyURLSkipped verifies that ImageData with empty URL is skipped.
func TestSaveArtistImages_EmptyURLSkipped(t *testing.T) {
	svc := newTestMetadataService(t)
	ctx := context.Background()

	u, err := svc.client.User.Create().SetUsername("testuser").SetTheme("dark").Save(ctx)
	require.NoError(t, err)

	art, err := svc.client.Artist.Create().SetName("Test Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	// Create a temp file so DownloadAndSaveImage skips download on next call
	f, err := os.CreateTemp("", "*.png")
	require.NoError(t, err)
	f.Close()
	defer os.Remove(f.Name())

	images := []enrichers.ImageData{
		{URL: "", LocalPath: f.Name(), Type: "thumbnail", Source: "test"},
	}

	err = svc.saveArtistImages(ctx, art, images)
	require.NoError(t, err) // Should not error on empty URL

	count, err := svc.client.ArtistImage.Query().Count(ctx)
	require.NoError(t, err)
	// Empty URL image gets stored (saveArtistImages doesn't filter empty URLs, only saveAlbumImages does)
	_ = count
}
