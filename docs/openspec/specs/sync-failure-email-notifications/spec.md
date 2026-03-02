# SPEC-0015: Sync Failure Email Notifications

**Status:** accepted
**Version:** 0.1.0
**Last Updated:** 2026-03-02
**Governing ADRs:** ADR-0026 (sync failure email notifications with 7-day cooldown)

## Overview

When a provider sync fails fatally (invalid credentials, revoked OAuth token, unreachable
provider), the user currently only receives an in-browser SSE toast — invisible when their
browser is closed. This spec defines out-of-band email notifications for fatal sync failures,
with a 7-day per-provider cooldown to prevent alert fatigue.

See ADR-0026 for the architectural decision record behind this capability.

---

## Requirements

### Requirement: User Email Address

Users MUST be able to provide an email address via the Account Preferences page. The email
address is OPTIONAL; if absent, email notifications SHALL be silently skipped with a warning
logged at the `warn` level. The address SHALL be stored in the existing `User.email` field
(max 320 chars, RFC 5321). Spotter MUST NOT send email to an address that has not been
explicitly provided by the user.

#### Scenario: Email entered and saved

- **WHEN** a user submits a valid email address in Account Preferences
- **THEN** the address is persisted to `User.email` and a success toast is displayed

#### Scenario: Email field cleared

- **WHEN** a user clears the email field and saves
- **THEN** `User.email` is set to NULL and future notifications are skipped

#### Scenario: Invalid email format

- **WHEN** a user submits a string that is not a valid email address
- **THEN** the save is rejected with a descriptive validation error and `User.email` is unchanged

#### Scenario: No email configured

- **WHEN** a fatal sync failure occurs and `User.email` is NULL
- **THEN** no email is sent, and a `warn`-level log entry is written:
  `"sync failure: no email configured for user, skipping notification"`

---

### Requirement: SMTP Configuration

Spotter MUST support configurable SMTP delivery via a new `[smtp]` section in the application
config. The following fields SHALL be supported:

| Field      | Type   | Required | Description                              |
|------------|--------|----------|------------------------------------------|
| `host`     | string | yes      | SMTP server hostname                     |
| `port`     | int    | yes      | SMTP server port (e.g. 587, 465, 25)     |
| `username` | string | no       | SMTP auth username                       |
| `password` | string | no       | SMTP auth password                       |
| `from`     | string | yes      | Sender address (e.g. `spotter@example.com`) |
| `tls`      | bool   | no       | Enable STARTTLS (default: true)          |

When `smtp.host` is not configured, the notification subsystem MUST be treated as disabled.
All notification send attempts MUST be silently skipped and logged at `debug` level when SMTP
is disabled. The application MUST start and function normally regardless of SMTP configuration.

#### Scenario: SMTP not configured

- **WHEN** `smtp.host` is empty or absent in config
- **THEN** the application starts normally, all notification code paths log at `debug` and return

#### Scenario: SMTP configured, email sent successfully

- **WHEN** SMTP is configured and `client.SendMail()` returns no error
- **THEN** the notification is recorded in `sync_notification` with `notified_at = now()`

#### Scenario: SMTP send failure

- **WHEN** SMTP is configured but `client.SendMail()` returns an error
- **THEN** the error is logged at `error` level, the `sync_notification` record is NOT written
  (preserving the ability to retry on the next sync tick), and the sync job continues normally

---

### Requirement: Notification Trigger

The notification system MUST send an email when ALL of the following conditions are true:

1. A provider sync terminates with `ErrorClassFatal` (as classified by `BackoffManager`)
2. `User.email` is non-empty
3. SMTP is configured
4. No `sync_notification` record exists for `(user_id, provider)` with `notified_at` within
   the last 7 days (the cooldown window)

The notification MUST NOT be sent for `ErrorClassRetriable` failures (transient network
errors, rate limits, 5xx responses). It MUST be sent at most once per trigger event — it MUST
NOT retry on SMTP failure within the same sync tick.

#### Scenario: First fatal failure

- **WHEN** a provider sync fails with `ErrorClassFatal` for the first time for a given user+provider
- **THEN** an email is sent and a `sync_notification` record is written

#### Scenario: Repeated fatal failures within cooldown

- **WHEN** a provider sync fails with `ErrorClassFatal` and a `sync_notification` record exists
  with `notified_at` within the last 7 days
- **THEN** no email is sent

#### Scenario: Transient failure

- **WHEN** a provider sync fails with `ErrorClassRetriable` (timeout, 429, 5xx)
- **THEN** no email is sent regardless of frequency

#### Scenario: Fatal failure after cooldown expiry

- **WHEN** a provider sync fails with `ErrorClassFatal` and the last notification was more than
  7 days ago
- **THEN** a new email is sent and `notified_at` is updated to now()

---

### Requirement: Cooldown Persistence

Notification state MUST be persisted in the database in a `sync_notification` table so that
cooldown windows survive application restarts. The table SHALL have a unique constraint on
`(user_id, provider)`. The cooldown duration SHALL default to 7 days and MUST be configurable
via `notifications.failure_cooldown_days` in the application config.

#### Scenario: App restart during cooldown

- **WHEN** the application restarts while a cooldown window is active
- **THEN** the cooldown is honoured — no duplicate email is sent on the next sync tick

#### Scenario: Cooldown duration configured

- **WHEN** `notifications.failure_cooldown_days` is set to a value N
- **THEN** the cooldown window is N days instead of 7

---

### Requirement: Cooldown Reset on Recovery

When a provider sync succeeds after a prior failure, OR when a user explicitly reconnects a
provider, the `sync_notification` record for that `(user_id, provider)` MUST be deleted. This
ensures that a new failure after recovery starts a fresh 7-day window rather than remaining
silenced for the duration of the original window.

#### Scenario: Sync recovers after fatal failure

- **WHEN** a provider sync that previously had a `sync_notification` record completes successfully
- **THEN** the `sync_notification` record is deleted

#### Scenario: Provider reconnected via OAuth

- **WHEN** a user completes an OAuth reconnect flow for a provider (e.g. re-linking Spotify)
- **THEN** the `sync_notification` record for that provider is deleted

#### Scenario: NavidromeAuth refreshed on login

- **WHEN** a user successfully logs in (which updates `NavidromeAuth.password`)
- **THEN** the `sync_notification` record for the `navidrome` provider is deleted

---

### Requirement: Email Content

The notification email MUST include enough information for the user to understand what failed
and how to fix it. The email SHALL be sent as `text/plain` with an optional `text/html`
alternative part (multipart/alternative). The email MUST contain:

- The name of the affected provider (e.g. "Spotify", "Navidrome", "Last.fm")
- A human-readable summary of the failure reason
- A direct link to the Spotter preferences page for that provider
- The Spotter instance base URL (derived from config)

The email MUST NOT include raw passwords, tokens, salts, or any credential material.
The email SHOULD include the timestamp (UTC) of the first failure in the current window.

#### Scenario: Email contains provider name and action link

- **WHEN** an email is sent for a Spotify fatal failure
- **THEN** the subject reads `"[Spotter] Spotify sync error — action required"` and the body
  contains a link to `/preferences/providers`

#### Scenario: No credential leakage

- **WHEN** the failure reason contains an auth error message
- **THEN** the email body contains the sanitised error class (e.g. "Authentication failed")
  and MUST NOT contain any token, password, salt, or raw API response body

---

### Requirement: Preferences UI — Email Address and Notification Status

The Account Preferences page MUST provide:

- A text input for the user's email address with format validation
- A save action that updates `User.email`
- A status indicator showing whether SMTP is configured on this instance (read-only)
- If `User.email` is set and SMTP is configured: a "Test notification" button that sends a
  test email to confirm delivery

#### Scenario: SMTP not configured, user has email set

- **WHEN** the user visits Account Preferences and SMTP is not configured
- **THEN** a warning badge is shown next to the email field:
  `"Email notifications are disabled — SMTP is not configured on this instance"`

#### Scenario: Test notification sent

- **WHEN** the user clicks "Test notification" and SMTP is configured
- **THEN** a test email is delivered and a success toast is shown

#### Scenario: Test notification fails

- **WHEN** the user clicks "Test notification" and SMTP send fails
- **THEN** an error toast is shown with the failure reason (sanitised), and the error is logged

---

## Implementation Notes

| Requirement | Implementing File(s) | Notes |
|---|---|---|
| Notification Trigger, Cooldown Persistence | `internal/services/notification.go` | NotificationService, cooldown management |
| Cooldown Persistence, Notification Trigger | `internal/services/backoff_manager.go` | BackoffManager, tier transitions |
| Cooldown Reset on Recovery | `internal/handlers/auth.go` | ClearCooldown call on login (Navidrome provider) |
| SMTP Configuration, Email Content | `internal/mailer/` | Email templates and SMTP sending |
| User Email Address, Preferences UI | `internal/views/`, `internal/handlers/` | Preferences page email input and SMTP status |
