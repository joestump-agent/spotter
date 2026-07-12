// Governing: ADR-0027 (image storage: local filesystem),
// SPEC metadata-enrichment-pipeline REQ-ENRICH-031 (images resized using pure-Go nfnt/resize),
// SPEC metadata-enrichment-pipeline REQ-ENRICH-032 (WebP, PNG, JPEG, GIF formats supported via stdlib + x/image/webp),
// SPEC metadata-enrichment-pipeline REQ-ENRICH-033 (existing local images skipped unless forced refresh)
package enrichers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nfnt/resize"
	_ "golang.org/x/image/webp"

	"spotter/internal/resilience"
)

const (
	// MaxImageSize defines the default maximum width or height for downloaded
	// images, used when no value is configured via SetMaxImageSize.
	MaxImageSize = 1024

	// downloadTimeout bounds a single image download.
	downloadTimeout = 30 * time.Second
)

// maxImageSize is the effective maximum image dimension. It defaults to
// MaxImageSize and can be overridden from config via SetMaxImageSize.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-031 (configurable via metadata.images.max_width)
var maxImageSize = MaxImageSize

// SetMaxImageSize overrides the maximum image dimension used when resizing
// downloaded images. Values <= 0 are ignored, keeping the MaxImageSize
// default. Call once at startup (not safe for concurrent use with downloads).
func SetMaxImageSize(size int) {
	if size > 0 {
		maxImageSize = size
	}
}

// imageHTTPClient is used for all image downloads so requests have a sane
// timeout instead of the http.DefaultClient's unbounded one.
var imageHTTPClient = &http.Client{Timeout: downloadTimeout}

// ImageFileName returns a collision-free local filename for a downloaded
// image: {entityID}-{imageType}-{shortURLHash}.png. Including a hash of the
// source URL ensures multiple images of the same type for one entity never
// overwrite each other. Downloads are always saved as PNG.
// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-030 (unique local paths per image), ADR-0027
func ImageFileName(entityID int, imageType, url string) string {
	return fmt.Sprintf("%d-%s-%s.png", entityID, imageType, ShortURLHash(url))
}

// ShortURLHash returns a short, stable hex hash of the given URL, suitable
// for embedding in filenames.
func ShortURLHash(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])[:12]
}

// redactURL strips the query string and fragment from a URL for safe logging,
// since some providers (e.g. Navidrome) embed credentials in query parameters.
func redactURL(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		// Unparseable: fall back to truncating at the query separator.
		if i := strings.IndexAny(rawURL, "?#"); i >= 0 {
			return rawURL[:i]
		}
		return rawURL
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// DownloadAndSaveImage fetches an image from a URL, resizes it if necessary,
// converts it to PNG format, and saves it to the specified local path.
// It creates the destination directory if it doesn't exist. The image is
// written to a temporary file in the target directory and renamed into place
// on success, so an interrupted download never leaves a partial file that the
// exists-shortcut would later adopt.
// It returns the final path of the saved image or an error if any step fails.
func DownloadAndSaveImage(ctx context.Context, url, localPath string, logger *slog.Logger) (string, error) {
	if url == "" {
		return "", fmt.Errorf("image URL cannot be empty")
	}
	if localPath == "" {
		return "", fmt.Errorf("local path cannot be empty")
	}

	// Check if the file already exists. If so, do not re-download.
	if _, err := os.Stat(localPath); err == nil {
		logger.Debug("Image already exists locally, skipping download", "path", localPath)
		return localPath, nil
	}

	// Credential-bearing query strings (e.g. Navidrome tokens) must never leak
	// into logs or errors, so only the redacted URL is ever recorded below.
	logger.Debug("Downloading image", "url", redactURL(url))

	// Fetch the image from the provided URL with a bounded, cancellable request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create image request for %s: %w", redactURL(url), err)
	}
	resp, err := imageHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to start image download from %s: %w", redactURL(url), err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Warn("failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		// Governing: ADR-0020, SPEC error-handling REQ-ERR-002/REQ-ERR-003
		return "", resilience.NewHTTPStatusError(resp.StatusCode, fmt.Errorf("failed to download image, status: %s", resp.Status))
	}

	// Decode the image data. The blank import of image decoders allows
	// image.Decode to handle various formats (jpeg, gif, png, webp).
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to decode image from %s: %w", redactURL(url), err)
	}

	// Resize the image if its dimensions exceed the maximum allowed size.
	// Thumbnail bounds both dimensions while preserving aspect ratio and
	// never upscales smaller images.
	// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-031
	if img.Bounds().Dx() > maxImageSize || img.Bounds().Dy() > maxImageSize {
		img = resize.Thumbnail(uint(maxImageSize), uint(maxImageSize), img, resize.Lanczos3)
		logger.Debug("Resized image", "url", redactURL(url), "new_width", img.Bounds().Dx(), "new_height", img.Bounds().Dy())
	}

	// Ensure the destination directory exists.
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create image directory %s: %w", dir, err)
	}

	// Write to a temp file in the target directory, then rename into place so
	// interrupted downloads are never adopted by the exists-shortcut above.
	tmp, err := os.CreateTemp(dir, ".download-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary image file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup on any failure path; harmless after a successful rename.
	defer func() { _ = os.Remove(tmpPath) }()

	// Encode the (potentially resized) image as a PNG and save it to the file.
	if err := png.Encode(tmp, img); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("failed to encode image to png: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("failed to close temporary image file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, localPath); err != nil {
		return "", fmt.Errorf("failed to move image into place at %s: %w", localPath, err)
	}

	logger.Debug("Successfully saved image", "path", localPath)

	return localPath, nil
}
