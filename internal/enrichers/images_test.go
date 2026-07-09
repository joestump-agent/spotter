package enrichers

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDownloadAndSaveImage(t *testing.T) {
	// Create a dummy logger that discards output
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Test Case 1: Successful download and save
	t.Run("successful download and save", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			img := image.NewRGBA(image.Rect(0, 0, 10, 10))
			png.Encode(w, img)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "test.png")

		resultPath, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.NoError(t, err)
		assert.Equal(t, localPath, resultPath)
		assert.FileExists(t, localPath)
	})

	// Test Case 2: File already exists
	t.Run("file already exists", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// This handler should not be called
			t.Fatal("server was called when it should have been skipped")
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "existing.png")

		// Create the file beforehand
		f, err := os.Create(localPath)
		assert.NoError(t, err)
		f.Close()

		resultPath, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.NoError(t, err)
		assert.Equal(t, localPath, resultPath)
	})

	// Test Case 3: Invalid URL (server error)
	t.Run("invalid url", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "notfound.png")

		_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.Error(t, err)
	})

	// Test Case 4: Bad image data
	t.Run("bad image data", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "this is not an image")
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "bad.png")

		_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.Error(t, err)
	})

	// Test Case 5: Directory creation failure (read-only)
	t.Run("directory creation failure", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("skipping test as root user")
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			img := image.NewRGBA(image.Rect(0, 0, 10, 10))
			png.Encode(w, img)
		}))
		defer server.Close()

		// Create a read-only directory
		tempDir := t.TempDir()
		readOnlyDir := filepath.Join(tempDir, "readonly")
		err := os.Mkdir(readOnlyDir, 0555)
		assert.NoError(t, err)

		localPath := filepath.Join(readOnlyDir, "subdir", "image.png")

		_, err = DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.Error(t, err)
	})

	// Test Case 6: Image resizing
	t.Run("image resizing", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			img := image.NewRGBA(image.Rect(0, 0, MaxImageSize+100, MaxImageSize+100))
			png.Encode(w, img)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "resized.png")

		_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.NoError(t, err)

		f, err := os.Open(localPath)
		assert.NoError(t, err)
		defer f.Close()

		img, _, err := image.DecodeConfig(f)
		assert.NoError(t, err)
		assert.Equal(t, MaxImageSize, img.Width)
	})

	// Test Case 7: Both dimensions are bounded (resize.Thumbnail, not width-only)
	t.Run("bounds both dimensions", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Tall image: within max width, but exceeds max height
			img := image.NewRGBA(image.Rect(0, 0, 100, MaxImageSize+500))
			png.Encode(w, img)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "tall.png")

		_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.NoError(t, err)

		f, err := os.Open(localPath)
		assert.NoError(t, err)
		defer f.Close()

		img, _, err := image.DecodeConfig(f)
		assert.NoError(t, err)
		assert.LessOrEqual(t, img.Width, MaxImageSize)
		assert.Equal(t, MaxImageSize, img.Height)
	})

	// Test Case 8: Small images are never upscaled
	t.Run("no upscaling", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			img := image.NewRGBA(image.Rect(0, 0, 32, 16))
			png.Encode(w, img)
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "small.png")

		_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.NoError(t, err)

		f, err := os.Open(localPath)
		assert.NoError(t, err)
		defer f.Close()

		img, _, err := image.DecodeConfig(f)
		assert.NoError(t, err)
		assert.Equal(t, 32, img.Width)
		assert.Equal(t, 16, img.Height)
	})

	// Test Case 9: Failed downloads leave no partial file behind
	t.Run("failed download leaves no file", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "this is not an image")
		}))
		defer server.Close()

		tempDir := t.TempDir()
		localPath := filepath.Join(tempDir, "partial.png")

		_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
		assert.Error(t, err)
		assert.NoFileExists(t, localPath)

		// No leftover temp files either.
		entries, err := os.ReadDir(tempDir)
		assert.NoError(t, err)
		assert.Empty(t, entries, "no temp files should remain after a failed download")
	})
}

// TestSetMaxImageSize verifies the configured max dimension is applied.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-031 (configurable via metadata.images.max_width)
func TestSetMaxImageSize(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	SetMaxImageSize(200)
	t.Cleanup(func() { maxImageSize = MaxImageSize })

	// Values <= 0 are ignored.
	SetMaxImageSize(0)
	SetMaxImageSize(-5)
	assert.Equal(t, 200, maxImageSize)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 400, 400))
		png.Encode(w, img)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	localPath := filepath.Join(tempDir, "configured.png")

	_, err := DownloadAndSaveImage(context.Background(), server.URL, localPath, logger)
	assert.NoError(t, err)

	f, err := os.Open(localPath)
	assert.NoError(t, err)
	defer f.Close()

	img, _, err := image.DecodeConfig(f)
	assert.NoError(t, err)
	assert.Equal(t, 200, img.Width)
	assert.Equal(t, 200, img.Height)
}

// TestImageFileName verifies filenames are unique per source URL so multiple
// images of the same type for one entity never collide.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-030 (unique local paths per image)
func TestImageFileName(t *testing.T) {
	a := ImageFileName(42, "thumbnail", "https://example.com/one.jpg")
	b := ImageFileName(42, "thumbnail", "https://example.com/two.jpg")
	c := ImageFileName(42, "thumbnail", "https://example.com/one.jpg")

	assert.NotEqual(t, a, b, "different URLs must yield different filenames")
	assert.Equal(t, a, c, "same URL must yield a stable filename")
	assert.Contains(t, a, "42-thumbnail-")
	assert.Contains(t, a, ".png")
}
