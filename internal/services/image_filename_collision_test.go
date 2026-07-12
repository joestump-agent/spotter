package services

// Filename-collision regression tests for issue #343.
//
// Image rows are deduped by URL, but the local filename used to be
// {entityID}-{imageType}{ext}. N same-type images for one entity therefore
// collapsed onto a single file: the first download created the file and every
// later row hit the os.Stat exists-branch and pointed its local_path at image
// #1's bytes. Filenames now include a per-image URL hash discriminator
// (enrichers.ImageFileName -> {id}-{type}-{shortURLHash}.png).
//
// Governing: ADR-0027 (filesystem image storage),
// SPEC metadata-enrichment-pipeline REQ-ENRICH-030 (unique local paths per image)

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"

	"spotter/ent/albumimage"
	"spotter/ent/artistimage"
	"spotter/ent/enttest"
	"spotter/internal/config"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedFromPath derives a stable per-URL seed from the trailing integer in the
// request path (e.g. /fanart/3.png -> 3), defaulting to 0 when none is present.
func seedFromPath(p string) int {
	base := strings.TrimSuffix(path.Base(p), ".png")
	if n, err := strconv.Atoi(base); err == nil {
		return n
	}
	return 0
}

// newImageDownloadTestService returns a MetadataService and an httptest server
// that serves a distinct, valid PNG per URL path. Distinct image dimensions per
// seed guarantee distinct PNG bytes after the download pipeline re-encodes to
// PNG, so collapsed files (multiple rows sharing one file) are detectable.
func newImageDownloadTestService(t *testing.T) (*MetadataService, *httptest.Server) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", fmt.Sprintf("file:imgdl_%s?mode=memory&cache=shared&_fk=1", t.Name()))
	t.Cleanup(func() { client.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seed := seedFromPath(r.URL.Path)
		n := seed + 2 // distinct dimensions per seed => distinct PNG bytes
		m := image.NewRGBA(image.Rect(0, 0, n, n))
		c := color.RGBA{R: uint8(seed * 17), G: uint8(seed*29 + 7), B: uint8(255 - seed), A: 255}
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				m.Set(x, y, c)
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, m)
	}))
	t.Cleanup(server.Close)

	svc := &MetadataService{
		client: client,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: &config.Config{},
	}
	return svc, server
}

// TestDownloadArtistImage_SameTypeImagesGetDistinctFiles verifies that an
// artist with 5 fanart images stores 5 distinct files with distinct contents,
// and each DB row's local_path points at its own bytes.
func TestDownloadArtistImage_SameTypeImagesGetDistinctFiles(t *testing.T) {
	svc, server := newImageDownloadTestService(t)
	ctx := context.Background()
	baseDir := t.TempDir()

	u, err := svc.client.User.Create().SetUsername("imguser").Save(ctx)
	require.NoError(t, err)
	art, err := svc.client.Artist.Create().SetName("Fanart Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	const count = 5
	for i := 0; i < count; i++ {
		_, err := svc.client.ArtistImage.Create().
			SetArtist(art).
			SetSource("fanart").
			SetURL(fmt.Sprintf("%s/fanart/%d.png", server.URL, i)).
			SetImageType(artistimage.ImageTypeFanart).
			Save(ctx)
		require.NoError(t, err)
	}

	images, err := svc.client.ArtistImage.Query().WithArtist().All(ctx)
	require.NoError(t, err)
	require.Len(t, images, count)

	for _, img := range images {
		require.NoError(t, svc.downloadArtistImage(ctx, u, img, baseDir))
	}

	updated, err := svc.client.ArtistImage.Query().All(ctx)
	require.NoError(t, err)

	paths := make(map[string]bool, count)
	contents := make(map[string]bool, count)
	for _, img := range updated {
		require.NotEmpty(t, img.LocalPath, "image %d must have a local path", img.ID)
		paths[img.LocalPath] = true

		data, err := os.ReadFile(img.LocalPath)
		require.NoError(t, err, "file for image %d must exist on disk", img.ID)
		contents[string(data)] = true
	}

	assert.Len(t, paths, count, "%d same-type images must produce %d distinct files, not collapse onto one", count, count)
	assert.Len(t, contents, count, "each image row must point at its own bytes")
}

// TestDownloadAlbumImage_SameTypeImagesGetDistinctFiles mirrors the artist test
// for album images (metadata.go downloadAlbumImage).
func TestDownloadAlbumImage_SameTypeImagesGetDistinctFiles(t *testing.T) {
	svc, server := newImageDownloadTestService(t)
	ctx := context.Background()
	baseDir := t.TempDir()

	u, err := svc.client.User.Create().SetUsername("albimguser").Save(ctx)
	require.NoError(t, err)
	art, err := svc.client.Artist.Create().SetName("Cover Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	alb, err := svc.client.Album.Create().SetName("Cover Album").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)

	const count = 3
	for i := 0; i < count; i++ {
		_, err := svc.client.AlbumImage.Create().
			SetAlbum(alb).
			SetSource("fanart").
			SetURL(fmt.Sprintf("%s/covers/%d.png", server.URL, i)).
			SetImageType(albumimage.ImageTypeCoverFront).
			Save(ctx)
		require.NoError(t, err)
	}

	images, err := svc.client.AlbumImage.Query().WithAlbum().All(ctx)
	require.NoError(t, err)
	require.Len(t, images, count)

	for _, img := range images {
		require.NoError(t, svc.downloadAlbumImage(ctx, u, img, baseDir))
	}

	updated, err := svc.client.AlbumImage.Query().All(ctx)
	require.NoError(t, err)

	paths := make(map[string]bool, count)
	for _, img := range updated {
		require.NotEmpty(t, img.LocalPath, "image %d must have a local path", img.ID)
		paths[img.LocalPath] = true
	}
	assert.Len(t, paths, count, "%d same-type album images must produce %d distinct files", count, count)
}

// TestDownloadArtistImage_ExistingFileSkipsRedownload verifies REQ-ENRICH-033
// still holds with hashed filenames: re-downloading the SAME image (same URL)
// hits the exists-branch and does not re-fetch.
func TestDownloadArtistImage_ExistingFileSkipsRedownload(t *testing.T) {
	svc, server := newImageDownloadTestService(t)
	ctx := context.Background()
	baseDir := t.TempDir()

	u, err := svc.client.User.Create().SetUsername("skipuser").Save(ctx)
	require.NoError(t, err)
	art, err := svc.client.Artist.Create().SetName("Skip Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)

	_, err = svc.client.ArtistImage.Create().
		SetArtist(art).
		SetSource("fanart").
		SetURL(server.URL + "/fanart/same.png").
		SetImageType(artistimage.ImageTypeFanart).
		Save(ctx)
	require.NoError(t, err)

	img, err := svc.client.ArtistImage.Query().WithArtist().Only(ctx)
	require.NoError(t, err)

	require.NoError(t, svc.downloadArtistImage(ctx, u, img, baseDir))
	first, err := svc.client.ArtistImage.Query().Only(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, first.LocalPath)

	// Overwrite the on-disk bytes; a second download for the same URL must
	// keep the existing file (exists-branch) rather than re-fetching.
	require.NoError(t, os.WriteFile(first.LocalPath, []byte("sentinel"), 0644))

	img, err = svc.client.ArtistImage.Query().WithArtist().Only(ctx)
	require.NoError(t, err)
	require.NoError(t, svc.downloadArtistImage(ctx, u, img, baseDir))

	data, err := os.ReadFile(first.LocalPath)
	require.NoError(t, err)
	assert.Equal(t, "sentinel", string(data), "same-URL re-download must hit the exists-branch and keep the file")
}
