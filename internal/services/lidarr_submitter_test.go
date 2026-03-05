package services

import (
	"math"
	"testing"
	"time"
)

func TestComputeBackoff(t *testing.T) {
	tests := []struct {
		name        string
		attempts    int
		minDelay    time.Duration
		maxDelay    time.Duration
	}{
		{
			name:     "first attempt: ~1m base + up to 1m jitter",
			attempts: 1,
			minDelay: 1 * time.Minute,
			maxDelay: 2 * time.Minute,
		},
		{
			name:     "second attempt: ~2m base + up to 1m jitter",
			attempts: 2,
			minDelay: 2 * time.Minute,
			maxDelay: 3 * time.Minute,
		},
		{
			name:     "third attempt: ~4m base + up to 1m jitter",
			attempts: 3,
			minDelay: 4 * time.Minute,
			maxDelay: 5 * time.Minute,
		},
		{
			name:     "tenth attempt: capped at 1h",
			attempts: 10,
			minDelay: 59 * time.Minute, // Could be near cap
			maxDelay: 1 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			retryAt := ComputeBackoff(tt.attempts)
			after := time.Now()

			// Verify the retry time is in the future
			if retryAt.Before(before) {
				t.Error("retry_at should be in the future")
			}

			delay := retryAt.Sub(before)

			// For attempts >= 7, base delay exceeds maxBackoff, so cap applies
			expectedBase := baseBackoff * time.Duration(math.Pow(2, float64(tt.attempts-1)))
			if expectedBase > maxBackoff {
				// Capped: delay should be <= maxBackoff
				if delay > maxBackoff+time.Second {
					t.Errorf("delay %v exceeds max backoff %v", delay, maxBackoff)
				}
			} else {
				// Not capped: delay should be >= base
				if delay < tt.minDelay-time.Second {
					t.Errorf("delay %v is less than minimum %v", delay, tt.minDelay)
				}
				if delay > tt.maxDelay+time.Second {
					t.Errorf("delay %v exceeds maximum %v (including jitter tolerance)", delay, tt.maxDelay)
				}
			}

			_ = after // used for bounds
		})
	}
}

func TestComputeBackoff_MaxCap(t *testing.T) {
	// Very high attempts should still be capped at maxBackoff
	for i := 0; i < 100; i++ {
		retryAt := ComputeBackoff(20)
		delay := time.Until(retryAt)
		if delay > maxBackoff+time.Second {
			t.Errorf("attempt 20 delay %v exceeds max backoff %v", delay, maxBackoff)
		}
	}
}

func TestMaxAttemptsConstant(t *testing.T) {
	if maxAttempts != 10 {
		t.Errorf("maxAttempts should be 10, got %d", maxAttempts)
	}
}
