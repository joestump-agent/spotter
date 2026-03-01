// Governing: SPEC-0015 REQ "Notification Trigger", REQ "Cooldown Persistence", REQ "Cooldown Reset on Recovery", ADR-0026
package notifications

import (
	"context"
	"log/slog"
	"time"

	"spotter/ent"
	"spotter/ent/syncnotification"
	"spotter/ent/user"
	"spotter/internal/mailer"
	"spotter/internal/services"
)

// Notifier handles sync failure email notifications with cooldown.
type Notifier interface {
	NotifyIfNeeded(ctx context.Context, u *ent.User, provider string, syncErr error) error
	ClearCooldown(ctx context.Context, userID int, provider string) error
}

// NoopNotifier is used when SMTP is not configured.
type NoopNotifier struct{}

func NewNoopNotifier() *NoopNotifier {
	return &NoopNotifier{}
}

func (n *NoopNotifier) NotifyIfNeeded(_ context.Context, _ *ent.User, _ string, _ error) error {
	return nil
}

func (n *NoopNotifier) ClearCooldown(_ context.Context, _ int, _ string) error {
	return nil
}

// DBNotifier persists notification state in the database and sends email via SMTP.
type DBNotifier struct {
	client       *ent.Client
	mailer       mailer.Mailer
	cooldownDays int
	baseURL      string
	logger       *slog.Logger
}

func NewDBNotifier(client *ent.Client, m mailer.Mailer, cooldownDays int, baseURL string, logger *slog.Logger) *DBNotifier {
	if cooldownDays <= 0 {
		cooldownDays = 7
	}
	return &DBNotifier{
		client:       client,
		mailer:       m,
		cooldownDays: cooldownDays,
		baseURL:      baseURL,
		logger:       logger,
	}
}

// NotifyIfNeeded checks whether an email notification should be sent for a sync failure.
// It only sends for fatal errors (not retriable) and respects the cooldown window.
// Governing: SPEC-0015 REQ "Notification Trigger"
func (n *DBNotifier) NotifyIfNeeded(ctx context.Context, u *ent.User, provider string, syncErr error) error {
	// Only notify on fatal errors
	// Governing: SPEC-0015 REQ "Notification Trigger" condition 1
	errClass := services.ClassifyError(syncErr)
	if errClass != services.ErrorClassFatal {
		return nil
	}

	// Check cooldown window
	// Governing: SPEC-0015 REQ "Cooldown Persistence"
	existing, err := n.client.SyncNotification.Query().
		Where(
			syncnotification.HasUserWith(user.ID(u.ID)),
			syncnotification.Provider(provider),
		).
		Only(ctx)

	if err != nil && !ent.IsNotFound(err) {
		n.logger.Error("failed to query sync_notification", "error", err, "user_id", u.ID, "provider", provider)
		return err
	}

	if existing != nil {
		cutoff := time.Now().AddDate(0, 0, -n.cooldownDays)
		if existing.NotifiedAt.After(cutoff) {
			n.logger.Debug("within cooldown window, skipping notification",
				"user_id", u.ID, "provider", provider,
				"notified_at", existing.NotifiedAt,
				"cooldown_days", n.cooldownDays)
			return nil
		}
	}

	// Check if user has an email address
	// Governing: SPEC-0015 REQ "Notification Trigger" condition 2
	if u.Email == "" {
		n.logger.Warn("sync failure: no email configured for user, skipping notification",
			"username", u.Username, "provider", provider)
		return nil
	}

	// Check if SMTP is configured
	// Governing: SPEC-0015 REQ "Notification Trigger" condition 3
	if !n.mailer.IsConfigured() {
		n.logger.Debug("smtp disabled, skipping notification",
			"user_id", u.ID, "provider", provider)
		return nil
	}

	// Build and send the email
	// Governing: SPEC-0015 REQ "Email Content"
	subject, body := buildEmail(provider, syncErr, n.baseURL, n.cooldownDays)
	if err := n.mailer.Send(u.Email, subject, body); err != nil {
		// Governing: SPEC-0015 REQ "SMTP Configuration" scenario "SMTP send failure"
		// On send failure, do NOT write the sync_notification record so we can retry next tick
		n.logger.Error("failed to send notification email",
			"error", err, "user_id", u.ID, "provider", provider, "email", u.Email)
		return nil
	}

	// Upsert the sync_notification record
	// Governing: SPEC-0015 REQ "Cooldown Persistence"
	if existing != nil {
		_, err = n.client.SyncNotification.UpdateOne(existing).
			SetNotifiedAt(time.Now()).
			Save(ctx)
	} else {
		_, err = n.client.SyncNotification.Create().
			SetProvider(provider).
			SetNotifiedAt(time.Now()).
			SetUserID(u.ID).
			Save(ctx)
	}
	if err != nil {
		n.logger.Error("failed to upsert sync_notification", "error", err, "user_id", u.ID, "provider", provider)
		return err
	}

	n.logger.Info("sent sync failure notification",
		"user_id", u.ID, "provider", provider, "email", u.Email)
	return nil
}

// ClearCooldown deletes the sync_notification record for a user+provider pair,
// allowing a fresh notification window if the provider fails again.
// Governing: SPEC-0015 REQ "Cooldown Reset on Recovery"
func (n *DBNotifier) ClearCooldown(ctx context.Context, userID int, provider string) error {
	deleted, err := n.client.SyncNotification.Delete().
		Where(
			syncnotification.HasUserWith(user.ID(userID)),
			syncnotification.Provider(provider),
		).
		Exec(ctx)
	if err != nil {
		n.logger.Error("failed to clear sync_notification cooldown",
			"error", err, "user_id", userID, "provider", provider)
		return err
	}
	if deleted > 0 {
		n.logger.Info("cleared sync_notification cooldown",
			"user_id", userID, "provider", provider)
	}
	return nil
}
