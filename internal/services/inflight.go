// Governing: SPEC metadata-enrichment-pipeline REQ-ENRICH-041 (overlapping per-user
// background runs are skipped), ADR-0013 (goroutine ticker scheduling)
package services

import "sync"

// inFlightKey identifies a background job run by job type and user.
type inFlightKey struct {
	job    string
	userID int
}

// InFlightGuard tracks in-flight background jobs keyed by (job type, user ID)
// so schedulers can skip users whose previous run has not finished yet.
// It is safe for concurrent use.
type InFlightGuard struct {
	mu      sync.Mutex
	running map[inFlightKey]struct{}
}

// NewInFlightGuard returns an empty guard.
func NewInFlightGuard() *InFlightGuard {
	return &InFlightGuard{running: make(map[inFlightKey]struct{})}
}

// TryAcquire marks (job, userID) as in flight. It returns false if a run for
// the same key is already active, in which case the caller must skip the run
// and must NOT call Release.
func (g *InFlightGuard) TryAcquire(job string, userID int) bool {
	key := inFlightKey{job: job, userID: userID}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, active := g.running[key]; active {
		return false
	}
	g.running[key] = struct{}{}
	return true
}

// Release clears the in-flight mark for (job, userID). It must be called
// exactly once for every successful TryAcquire.
func (g *InFlightGuard) Release(job string, userID int) {
	key := inFlightKey{job: job, userID: userID}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, key)
}
