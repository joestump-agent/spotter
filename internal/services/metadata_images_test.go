package services

// Tests for the image download orchestrators: DownloadImages (artist + album
// paths, failure handling, stale-path repair, exists-on-disk shortcut) and
// SyncAllArtistImages / SyncAllAlbumImages. All fetches go to a local
// httptest server; no network is touched.
//
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-030/033, ADR-0027

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"spotter/ent"
	"spotter/ent/albumimage"
	"spotter/ent/artistimage"
	"spotter/ent/syncevent"
	"spotter/internal/enrichers"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPNG returns a valid encoded PNG so the shared image pipeline can decode
// and re-encode it.
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		for y := 0; y < 4; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 60), G: uint8(y * 60), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// newImageTestServer serves a valid PNG on /ok*.png and 404 elsewhere.
func newImageTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	pngBytes := testPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Ext(r.URL.Path) == ".png" && len(r.URL.Path) >= 3 && r.URL.Path[:3] == "/ok" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func createImageFixtures(t *testing.T, svc *MetadataService, u *ent.User) (*ent.Artist, *ent.Album) {
	t.Helper()
	ctx := context.Background()
	art, err := svc.client.Artist.Create().SetName("Image Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	alb, err := svc.client.Album.Create().SetName("Image Album").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)
	return art, alb
}

// TestDownloadImages_DownloadsPendingAndSkipsFailures verifies the happy path
// for both artist and album images, that a failing URL is logged and skipped
// without failing the pass, and that a second run has nothing left to do.
func TestDownloadImages_DownloadsPendingAndSkipsFailures(t *testing.T) {
	svc := newOrchestratorTestService(t)
	srv := newImageTestServer(t)
	svc.config.Metadata.Images.Directory = t.TempDir()

	u := createTestUser(t, svc, "testuser")
	art, alb := createImageFixtures(t, svc, u)
	ctx := context.Background()

	artistImg, err := svc.client.ArtistImage.Create().
		SetArtist(art).SetURL(srv.URL + "/ok-artist.png").SetSource("test").SetImageType("thumbnail").Save(ctx)
	require.NoError(t, err)
	albumImg, err := svc.client.AlbumImage.Create().
		SetAlbum(alb).SetURL(srv.URL + "/ok-album.png").SetSource("test").SetImageType("cover_front").Save(ctx)
	require.NoError(t, err)
	brokenImg, err := svc.client.ArtistImage.Create().
		SetArtist(art).SetURL(srv.URL + "/missing.png").SetSource("test").SetImageType("banner").Save(ctx)
	require.NoError(t, err)

	count, err := svc.DownloadImages(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "two downloads succeed, the 404 is skipped")

	gotArtistImg, err := svc.client.ArtistImage.Get(ctx, artistImg.ID)
	require.NoError(t, err)
	require.NotEmpty(t, gotArtistImg.LocalPath, "artist image local_path must be persisted")
	assert.FileExists(t, gotArtistImg.LocalPath)
	assert.Equal(t, filepath.Join(svc.config.Metadata.Images.Directory, "artists"), filepath.Dir(gotArtistImg.LocalPath))

	gotAlbumImg, err := svc.client.AlbumImage.Get(ctx, albumImg.ID)
	require.NoError(t, err)
	require.NotEmpty(t, gotAlbumImg.LocalPath, "album image local_path must be persisted")
	assert.FileExists(t, gotAlbumImg.LocalPath)
	assert.Equal(t, filepath.Join(svc.config.Metadata.Images.Directory, "albums"), filepath.Dir(gotAlbumImg.LocalPath))

	gotBroken, err := svc.client.ArtistImage.Get(ctx, brokenImg.ID)
	require.NoError(t, err)
	assert.Empty(t, gotBroken.LocalPath, "failed download must leave local_path empty")

	// One image_downloaded event per successful download.
	assert.Equal(t, 2, syncEventCount(t, svc, syncevent.EventTypeImageDownloaded))

	// Second run: only the broken image is retried; it fails again.
	count, err = svc.DownloadImages(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "already-downloaded images must not be re-downloaded")
}

// TestDownloadImages_RepairsStalePathsAndRedownloads verifies the self-heal
// path: when a stored local_path no longer exists on disk it is cleared and
// the image is downloaded again.
func TestDownloadImages_RepairsStalePathsAndRedownloads(t *testing.T) {
	svc := newOrchestratorTestService(t)
	srv := newImageTestServer(t)
	svc.config.Metadata.Images.Directory = t.TempDir()

	u := createTestUser(t, svc, "testuser")
	art, alb := createImageFixtures(t, svc, u)
	ctx := context.Background()

	// Stale records pointing at files that do not exist (e.g. container recreated).
	staleArtistImg, err := svc.client.ArtistImage.Create().
		SetArtist(art).SetURL(srv.URL + "/ok-artist.png").SetSource("test").SetImageType("thumbnail").
		SetLocalPath(filepath.Join(svc.config.Metadata.Images.Directory, "gone", "artist.png")).Save(ctx)
	require.NoError(t, err)
	staleAlbumImg, err := svc.client.AlbumImage.Create().
		SetAlbum(alb).SetURL(srv.URL + "/ok-album.png").SetSource("test").SetImageType("cover_front").
		SetLocalPath(filepath.Join(svc.config.Metadata.Images.Directory, "gone", "album.png")).Save(ctx)
	require.NoError(t, err)

	count, err := svc.DownloadImages(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "stale paths must be repaired and both images re-downloaded")

	gotArtistImg, err := svc.client.ArtistImage.Get(ctx, staleArtistImg.ID)
	require.NoError(t, err)
	require.NotEmpty(t, gotArtistImg.LocalPath)
	assert.NotContains(t, gotArtistImg.LocalPath, "gone", "stale path must be replaced")
	assert.FileExists(t, gotArtistImg.LocalPath)

	gotAlbumImg, err := svc.client.AlbumImage.Get(ctx, staleAlbumImg.ID)
	require.NoError(t, err)
	require.NotEmpty(t, gotAlbumImg.LocalPath)
	assert.FileExists(t, gotAlbumImg.LocalPath)
}

// TestDownloadArtistImage_AdoptsExistingFileWithoutRefetch verifies the
// exists-on-disk shortcut: when the target file is already present, the DB is
// updated without re-downloading.
func TestDownloadArtistImage_AdoptsExistingFileWithoutRefetch(t *testing.T) {
	svc := newOrchestratorTestService(t)
	baseDir := t.TempDir()
	svc.config.Metadata.Images.Directory = baseDir

	u := createTestUser(t, svc, "testuser")
	art, _ := createImageFixtures(t, svc, u)
	ctx := context.Background()

	// URL points at a server that always fails: a fetch attempt would error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	url := srv.URL + "/anything.png"
	img, err := svc.client.ArtistImage.Create().
		SetArtist(art).SetURL(url).SetSource("test").SetImageType("thumbnail").Save(ctx)
	require.NoError(t, err)

	// Pre-create the file at the exact path the downloader would compute.
	artistDir := filepath.Join(baseDir, "artists")
	require.NoError(t, os.MkdirAll(artistDir, 0o755))
	expectedPath := filepath.Join(artistDir, enrichers.ImageFileName(art.ID, "thumbnail", url))
	require.NoError(t, os.WriteFile(expectedPath, testPNG(t), 0o644))

	count, err := svc.DownloadImages(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "existing file must be adopted without a fetch")

	got, err := svc.client.ArtistImage.Get(ctx, img.ID)
	require.NoError(t, err)
	assert.Equal(t, expectedPath, got.LocalPath)
}

// TestSyncAllArtistImages verifies that image refresh consults all artist
// enrichers, saves new image records, counts only artists that yielded
// images, and survives enricher failures.
func TestSyncAllArtistImages(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	withImages, err := svc.client.Artist.Create().SetName("Has Images").SetUser(u).Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Artist.Create().SetName("No Images").SetUser(u).Save(ctx)
	require.NoError(t, err)

	stub := &stubFullEnricher{
		typ: enrichers.TypeFanart,
		artistImages: []enrichers.ImageData{
			{URL: "https://img.test/artist-1.png", Type: "thumbnail", Source: "fanart", IsPrimary: true, Width: 500, Height: 500, Likes: 3},
			{URL: "https://img.test/artist-2.png", Type: "background", Source: "fanart"},
		},
		imagesOnlyFor: "Has Images",
	}
	// A failing enricher must not abort the pass.
	failing := &stubFullEnricher{typ: enrichers.TypeSpotify, artistImagesErr: assert.AnError}
	registerStub(t, svc, stub)
	registerStub(t, svc, failing)

	count, err := svc.SyncAllArtistImages(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "only the artist that yielded images is counted; the failing enricher must not abort the pass")

	imgs, err := svc.client.ArtistImage.Query().
		Where(artistimage.HasArtistWith(artistByName("Has Images"))).All(ctx)
	require.NoError(t, err)
	assert.Len(t, imgs, 2)

	// Running again must not duplicate image records (URL dedup in saveArtistImages).
	_, err = svc.SyncAllArtistImages(ctx, u)
	require.NoError(t, err)
	total, err := svc.client.ArtistImage.Query().
		Where(artistimage.HasArtistWith(artistByName("Has Images"))).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, total, "re-sync must not duplicate image rows")

	require.NotZero(t, withImages.ID)
	assert.GreaterOrEqual(t, syncEventCount(t, svc, syncevent.EventTypeImageDownloaded), 1)
}

// TestSyncAllAlbumImages mirrors the artist image sync for albums.
func TestSyncAllAlbumImages(t *testing.T) {
	svc := newOrchestratorTestService(t)
	u := createTestUser(t, svc, "testuser")
	ctx := context.Background()

	art, err := svc.client.Artist.Create().SetName("Album Image Artist").SetUser(u).Save(ctx)
	require.NoError(t, err)
	_, err = svc.client.Album.Create().SetName("Covered Album").SetUser(u).SetArtist(art).Save(ctx)
	require.NoError(t, err)

	stub := &stubFullEnricher{
		typ: enrichers.TypeFanart,
		albumImages: []enrichers.ImageData{
			{URL: "https://img.test/cover.png", Type: "cover_front", Source: "fanart", IsPrimary: true},
			{URL: "", Type: "cover_back", Source: "fanart"}, // empty URL must be skipped
		},
	}
	failing := &stubFullEnricher{typ: enrichers.TypeSpotify, albumImagesErr: assert.AnError}
	registerStub(t, svc, stub)
	registerStub(t, svc, failing)

	count, err := svc.SyncAllAlbumImages(ctx, u)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	imgs, err := svc.client.AlbumImage.Query().
		Where(albumimage.HasAlbumWith(albumByName("Covered Album"))).All(ctx)
	require.NoError(t, err)
	require.Len(t, imgs, 1, "empty-URL image data must be skipped")
	assert.Equal(t, "https://img.test/cover.png", imgs[0].URL)
	assert.True(t, imgs[0].IsPrimary)

	// Re-sync must not duplicate rows.
	_, err = svc.SyncAllAlbumImages(ctx, u)
	require.NoError(t, err)
	total, err := svc.client.AlbumImage.Query().
		Where(albumimage.HasAlbumWith(albumByName("Covered Album"))).Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
}
