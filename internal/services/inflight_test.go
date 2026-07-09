// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041
package services

import (
	"sync"
	"testing"
)

func TestInFlightGuard_AcquireRelease(t *testing.T) {
	g := NewInFlightGuard()

	if !g.TryAcquire("sync", 1) {
		t.Fatal("first TryAcquire should succeed")
	}
	if g.TryAcquire("sync", 1) {
		t.Fatal("second TryAcquire for same (job, user) should fail while in flight")
	}

	// A different user or job type is independent.
	if !g.TryAcquire("sync", 2) {
		t.Fatal("TryAcquire for a different user should succeed")
	}
	if !g.TryAcquire("metadata", 1) {
		t.Fatal("TryAcquire for a different job type should succeed")
	}

	g.Release("sync", 1)
	if !g.TryAcquire("sync", 1) {
		t.Fatal("TryAcquire after Release should succeed")
	}
}

func TestInFlightGuard_ReleaseUnknownKeyIsNoop(t *testing.T) {
	g := NewInFlightGuard()
	// Releasing a key that was never acquired must not panic or corrupt state.
	g.Release("sync", 42)
	if !g.TryAcquire("sync", 42) {
		t.Fatal("TryAcquire should succeed after spurious Release")
	}
}

func TestInFlightGuard_ConcurrentAcquire(t *testing.T) {
	g := NewInFlightGuard()

	const goroutines = 50
	var wg sync.WaitGroup
	acquired := make(chan struct{}, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.TryAcquire("sync", 7) {
				acquired <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(acquired)

	count := 0
	for range acquired {
		count++
	}
	if count != 1 {
		t.Fatalf("expected exactly one goroutine to acquire, got %d", count)
	}
}
