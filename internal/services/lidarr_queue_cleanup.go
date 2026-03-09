package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"spotter/ent"
	"spotter/ent/lidarrqueue"
)

// Governing: SPEC-0017 REQ "Queue Cleanup", ADR-0029

// CleanupLidarrQueue deletes stale entries from the Lidarr submission queue:
//   - Submitted entries older than 7 days (successfully processed)
//   - Failed entries with 10+ attempts older than 30 days (permanently failed)
//
// For permanently-failed entries, the corresponding entity's lidarr_status is
// cleared so the UI no longer shows a stale "queued" badge.
func CleanupLidarrQueue(ctx context.Context, client *ent.Client) error {
	now := time.Now()

	// Delete submitted entries older than 7 days
	submittedCutoff := now.AddDate(0, 0, -7)
	submittedCount, err := client.LidarrQueue.Delete().
		Where(
			lidarrqueue.StatusEQ(lidarrqueue.StatusSubmitted),
			lidarrqueue.UpdatedAtLT(submittedCutoff),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("cleanup submitted lidarr queue entries: %w", err)
	}

	// For permanently-failed entries, clear lidarr_status on the entity before
	// deleting the queue row. Otherwise the UI shows "queued" forever.
	failedCutoff := now.AddDate(0, 0, -30)
	failedItems, err := client.LidarrQueue.Query().
		Where(
			lidarrqueue.StatusEQ(lidarrqueue.StatusFailed),
			lidarrqueue.AttemptsGTE(maxAttempts),
			lidarrqueue.UpdatedAtLT(failedCutoff),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query failed lidarr queue entries for cleanup: %w", err)
	}

	for _, item := range failedItems {
		clearEntityLidarrStatus(ctx, client, item)
	}

	// Now delete the failed queue rows
	failedCount := 0
	if len(failedItems) > 0 {
		ids := make([]int, len(failedItems))
		for i, item := range failedItems {
			ids[i] = item.ID
		}
		failedCount, err = client.LidarrQueue.Delete().
			Where(lidarrqueue.IDIn(ids...)).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("cleanup failed lidarr queue entries: %w", err)
		}
	}

	if submittedCount > 0 || failedCount > 0 {
		slog.Info("lidarr queue cleanup completed",
			"submitted_deleted", submittedCount,
			"failed_deleted", failedCount,
		)
	}

	return nil
}

// clearEntityLidarrStatus resets lidarr_status on the artist or album entity
// when a queue entry is permanently failed and being cleaned up.
func clearEntityLidarrStatus(ctx context.Context, client *ent.Client, item *ent.LidarrQueue) {
	switch item.EntityType {
	case lidarrqueue.EntityTypeArtist:
		if err := client.Artist.UpdateOneID(item.EntityID).
			ClearLidarrStatus().
			Exec(ctx); err != nil {
			slog.Warn("failed to clear artist lidarr_status during cleanup",
				"entity_id", item.EntityID, "error", err)
		}
	case lidarrqueue.EntityTypeAlbum:
		if err := client.Album.UpdateOneID(item.EntityID).
			ClearLidarrStatus().
			Exec(ctx); err != nil {
			slog.Warn("failed to clear album lidarr_status during cleanup",
				"entity_id", item.EntityID, "error", err)
		}
	}
}
