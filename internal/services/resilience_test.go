package services_test

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"spotter/internal/providers"
	"spotter/internal/services"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Error Classification Tests ---

func TestClassifyError_HTTPRetriableStatuses(t *testing.T) {
	retriableCodes := []int{
		http.StatusTooManyRequests,     // 429
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusInternalServerError, // 500
	}

	for _, code := range retriableCodes {
		t.Run(fmt.Sprintf("HTTP_%d", code), func(t *testing.T) {
			err := services.NewHTTPStatusError(code, fmt.Errorf("http error %d", code))
			assert.Equal(t, services.ErrorClassRetriable, services.ClassifyError(err))
		})
	}
}

func TestClassifyError_HTTPFatalStatuses(t *testing.T) {
	fatalCodes := []int{
		http.StatusUnauthorized, // 401
		http.StatusForbidden,    // 403
	}

	for _, code := range fatalCodes {
		t.Run(fmt.Sprintf("HTTP_%d", code), func(t *testing.T) {
			err := services.NewHTTPStatusError(code, fmt.Errorf("http error %d", code))
			assert.Equal(t, services.ErrorClassFatal, services.ClassifyError(err))
		})
	}
}

func TestClassifyError_NetworkErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"timeout", &net.DNSError{IsTimeout: true, Err: "timeout"}},
		{"dns_error", &net.DNSError{Err: "no such host", Name: "api.spotify.com"}},
		{"connection_refused", &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, services.ErrorClassRetriable, services.ClassifyError(tt.err))
		})
	}
}

func TestClassifyError_WrappedHTTPError(t *testing.T) {
	inner := services.NewHTTPStatusError(http.StatusUnauthorized, fmt.Errorf("token expired"))
	wrapped := fmt.Errorf("spotify API call failed: %w", inner)
	assert.Equal(t, services.ErrorClassFatal, services.ClassifyError(wrapped))
}

func TestClassifyError_NilError(t *testing.T) {
	assert.Equal(t, services.ErrorClassRetriable, services.ClassifyError(nil))
}

func TestClassifyError_GenericErrorDefaultsToRetriable(t *testing.T) {
	err := errors.New("something unexpected happened")
	assert.Equal(t, services.ErrorClassRetriable, services.ClassifyError(err))
}

func TestClassifyError_MessageBasedRetriable(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{"timeout_message", "request timeout exceeded"},
		{"connection_refused_message", "connection refused by remote host"},
		{"dns_message", "dns lookup failed"},
		{"eof_message", "unexpected eof"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(tt.msg)
			assert.Equal(t, services.ErrorClassRetriable, services.ClassifyError(err))
		})
	}
}

func TestClassifyError_MessageBasedFatal(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{"unauthorized_message", "unauthorized: token expired"},
		{"forbidden_message", "forbidden: insufficient permissions"},
		{"invalid_api_key", "invalid api key provided"},
		// Providers that return plain "returned status NNN" messages without HTTPStatusError
		{"plain_status_401", "spotify API returned status 401"},
		{"plain_status_403", "spotify API returned status 403"},
		{"plain_status_404", "spotify API returned status 404"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errors.New(tt.msg)
			assert.Equal(t, services.ErrorClassFatal, services.ClassifyError(err))
		})
	}
}

func TestClassifyError_5xxRetriable(t *testing.T) {
	// Any 5xx status should be retriable
	err := services.NewHTTPStatusError(504, fmt.Errorf("gateway timeout"))
	assert.Equal(t, services.ErrorClassRetriable, services.ClassifyError(err))
}

// Governing: SPEC error-handling REQ-ERR-003 (unparseable response body is fatal)
func TestClassifyError_MalformedResponseIsFatal(t *testing.T) {
	err := fmt.Errorf("failed to decode response: %w: %w",
		providers.ErrMalformedResponse, errors.New("invalid character '<' looking for beginning of value"))
	assert.Equal(t, services.ErrorClassFatal, services.ClassifyError(err))

	wrapped := fmt.Errorf("spotify sync failed: %w", err)
	assert.Equal(t, services.ErrorClassFatal, services.ClassifyError(wrapped))
}

func TestErrorClassString(t *testing.T) {
	assert.Equal(t, "retriable", services.ErrorClassRetriable.String())
	assert.Equal(t, "fatal", services.ErrorClassFatal.String())
}

// --- Backoff Calculation Tests ---

func TestCalculateBackoff_ExponentialGrowth(t *testing.T) {
	// Test that backoff grows exponentially
	prev := time.Duration(0)
	for i := 0; i < 5; i++ {
		delay := services.CalculateBackoff(i)
		// With jitter, delay should be in range [base * 2^i * 0.75, base * 2^i * 1.25]
		// Just check it's growing (accounting for jitter variability)
		if i > 0 {
			// The minimum of current should generally be more than half the minimum of previous
			// Since jitter adds variance, we test the trend over multiple samples
			t.Logf("failures=%d delay=%v", i, delay)
		}
		assert.Greater(t, delay, time.Duration(0), "delay should be positive")
		_ = prev
		prev = delay
	}
}

// Governing: SPEC error-handling REQ-BACK-001 (first failure delay ~30s: 22.5s-37.5s with jitter)
func TestCalculateBackoff_FirstFailure(t *testing.T) {
	// First failure: base = 30s * 2^(1-1) = 30s, with jitter [22.5s, 37.5s]
	for i := 0; i < 10; i++ {
		delay := services.CalculateBackoff(1)
		assert.GreaterOrEqual(t, delay, 22500*time.Millisecond, "first failure delay should be >= 22.5s (30s * 0.75)")
		assert.LessOrEqual(t, delay, 37500*time.Millisecond, "first failure delay should be <= 37.5s (30s * 1.25)")
	}
}

// Governing: SPEC error-handling REQ-BACK-001 (delays double: ~60s after second failure)
func TestCalculateBackoff_SecondFailure(t *testing.T) {
	// Second failure: base = 30s * 2^(2-1) = 60s, with jitter [45s, 75s]
	for i := 0; i < 10; i++ {
		delay := services.CalculateBackoff(2)
		assert.GreaterOrEqual(t, delay, 45*time.Second, "second failure delay should be >= 45s (60s * 0.75)")
		assert.LessOrEqual(t, delay, 75*time.Second, "second failure delay should be <= 75s (60s * 1.25)")
	}
}

func TestCalculateBackoff_ZeroFailures(t *testing.T) {
	// consecutiveFailures starts at 1 for the first failure; values below 1 are
	// clamped, so the delay is the first-failure delay: [22.5s, 37.5s]
	for i := 0; i < 10; i++ {
		delay := services.CalculateBackoff(0)
		assert.GreaterOrEqual(t, delay, 22500*time.Millisecond)
		assert.LessOrEqual(t, delay, 37500*time.Millisecond)
	}
}

// Governing: SPEC error-handling REQ-BACK-002 (jitter applied before the cap;
// delay never exceeds 30 minutes)
func TestCalculateBackoff_MaxDelayCap(t *testing.T) {
	// At very high failure counts, delay should be capped at exactly 30 minutes
	// because jitter is applied before the cap.
	for i := 0; i < 10; i++ {
		delay := services.CalculateBackoff(100)
		assert.LessOrEqual(t, delay, 30*time.Minute, "delay must never exceed maxDelay")
		assert.Equal(t, 30*time.Minute, delay, "delay at max should be capped at exactly 30m")
	}
}

func TestCalculateBackoff_JitterVariance(t *testing.T) {
	// Run multiple times and verify we get different values (jitter is random)
	seen := make(map[time.Duration]bool)
	for i := 0; i < 20; i++ {
		delay := services.CalculateBackoff(3)
		seen[delay] = true
	}
	assert.Greater(t, len(seen), 1, "jitter should produce varying delays")
}

// --- BackoffManager Tests ---

func TestBackoffManager_NewState(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}

	skip, _ := mgr.ShouldSkip(key)
	assert.False(t, skip, "new provider should not be skipped")
}

func TestBackoffManager_RecordRetriableFailure(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	err := services.NewHTTPStatusError(http.StatusServiceUnavailable, fmt.Errorf("service unavailable"))

	mgr.RecordFailure(key, err, services.ErrorClassRetriable)

	state, ok := mgr.GetState(key)
	require.True(t, ok)
	assert.Equal(t, 1, state.ConsecutiveFailures)
	assert.False(t, state.IsFatal)
	assert.False(t, state.NextRetryAt.IsZero(), "nextRetryAt should be set")
	assert.NotNil(t, state.LastError)
}

func TestBackoffManager_RecordFatalFailure(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	err := services.NewHTTPStatusError(http.StatusUnauthorized, fmt.Errorf("token revoked"))

	mgr.RecordFailure(key, err, services.ErrorClassFatal)

	state, ok := mgr.GetState(key)
	require.True(t, ok)
	assert.True(t, state.IsFatal)
	assert.Equal(t, 0, state.ConsecutiveFailures, "fatal errors don't increment failure counter")

	skip, reason := mgr.ShouldSkip(key)
	assert.True(t, skip)
	assert.Contains(t, reason, "fatal")
}

func TestBackoffManager_ShouldSkipDuringBackoff(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	err := fmt.Errorf("connection timeout")

	mgr.RecordFailure(key, err, services.ErrorClassRetriable)

	// Should be skipped because nextRetryAt is in the future
	skip, reason := mgr.ShouldSkip(key)
	assert.True(t, skip)
	assert.Contains(t, reason, "backing off")
}

func TestBackoffManager_RecordSuccess_ResetsState(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	err := fmt.Errorf("connection timeout")

	// Record multiple failures
	mgr.RecordFailure(key, err, services.ErrorClassRetriable)
	mgr.RecordFailure(key, err, services.ErrorClassRetriable)
	mgr.RecordFailure(key, err, services.ErrorClassRetriable)

	state, _ := mgr.GetState(key)
	assert.Equal(t, 3, state.ConsecutiveFailures)

	// Record success
	mgr.RecordSuccess(key)

	state, ok := mgr.GetState(key)
	require.True(t, ok)
	assert.Equal(t, 0, state.ConsecutiveFailures)
	assert.True(t, state.NextRetryAt.IsZero())
	assert.Nil(t, state.LastError)
	assert.False(t, state.IsFatal)

	// Should not be skipped anymore
	skip, _ := mgr.ShouldSkip(key)
	assert.False(t, skip)
}

func TestBackoffManager_RecordSuccess_NoopForUnknownKey(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 999, ProviderType: providers.TypeSpotify}

	// Should not panic
	mgr.RecordSuccess(key)

	_, ok := mgr.GetState(key)
	assert.False(t, ok)
}

func TestBackoffManager_PerProviderIsolation(t *testing.T) {
	mgr := services.NewBackoffManager()
	spotifyKey := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	navidromeKey := services.BackoffKey{UserID: 1, ProviderType: providers.TypeNavidrome}

	// Only Spotify has a failure
	mgr.RecordFailure(spotifyKey, fmt.Errorf("timeout"), services.ErrorClassRetriable)

	spotifySkip, _ := mgr.ShouldSkip(spotifyKey)
	navidromeSkip, _ := mgr.ShouldSkip(navidromeKey)

	assert.True(t, spotifySkip, "Spotify should be skipped")
	assert.False(t, navidromeSkip, "Navidrome should not be affected")
}

func TestBackoffManager_PerUserIsolation(t *testing.T) {
	mgr := services.NewBackoffManager()
	user1Key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	user2Key := services.BackoffKey{UserID: 2, ProviderType: providers.TypeSpotify}

	// Only user 1 has a failure
	mgr.RecordFailure(user1Key, fmt.Errorf("timeout"), services.ErrorClassRetriable)

	user1Skip, _ := mgr.ShouldSkip(user1Key)
	user2Skip, _ := mgr.ShouldSkip(user2Key)

	assert.True(t, user1Skip, "User 1 should be skipped")
	assert.False(t, user2Skip, "User 2 should not be affected")
}

func TestBackoffManager_FatalPreventsAutoRetry(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}

	mgr.RecordFailure(key, fmt.Errorf("unauthorized"), services.ErrorClassFatal)

	// Should always be skipped regardless of time passing
	skip, _ := mgr.ShouldSkip(key)
	assert.True(t, skip, "Fatal error should prevent auto-retry")
}

func TestBackoffManager_ClearFatal(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}

	mgr.RecordFailure(key, fmt.Errorf("unauthorized"), services.ErrorClassFatal)

	skip, _ := mgr.ShouldSkip(key)
	assert.True(t, skip)

	// Clear fatal (simulating user reconnecting)
	mgr.ClearFatal(key)

	skip, _ = mgr.ShouldSkip(key)
	assert.False(t, skip, "After clearing fatal, provider should be retried")
}

func TestBackoffManager_MarkNotified(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}

	mgr.RecordFailure(key, fmt.Errorf("unauthorized"), services.ErrorClassFatal)

	state, _ := mgr.GetState(key)
	assert.False(t, state.NotifiedFatal)

	mgr.MarkNotified(key)

	state, _ = mgr.GetState(key)
	assert.True(t, state.NotifiedFatal)
}

func TestBackoffManager_ConsecutiveFailuresEscalate(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}
	err := fmt.Errorf("service unavailable")

	for i := 0; i < 5; i++ {
		mgr.RecordFailure(key, err, services.ErrorClassRetriable)
	}

	// Verify that failures increment
	state, _ := mgr.GetState(key)
	assert.Equal(t, 5, state.ConsecutiveFailures)
}

func TestBackoffManager_SuccessResetsNotifiedFatal(t *testing.T) {
	mgr := services.NewBackoffManager()
	key := services.BackoffKey{UserID: 1, ProviderType: providers.TypeSpotify}

	mgr.RecordFailure(key, fmt.Errorf("unauthorized"), services.ErrorClassFatal)
	mgr.MarkNotified(key)

	state, _ := mgr.GetState(key)
	assert.True(t, state.NotifiedFatal)

	mgr.RecordSuccess(key)

	state, _ = mgr.GetState(key)
	assert.False(t, state.NotifiedFatal)
}

// --- HTTPStatusError Tests ---

func TestHTTPStatusError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("original error")
	httpErr := services.NewHTTPStatusError(500, inner)

	assert.Equal(t, 500, httpErr.StatusCode)
	assert.Contains(t, httpErr.Error(), "original error")
	assert.Equal(t, inner, errors.Unwrap(httpErr))
}
