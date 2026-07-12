package services

import (
	"sync"
	"testing"
	"time"
)

// Regression guard for issue #48. The end-to-end concurrency test exercises the
// pairing path, but the in-memory SQLite table-level locking masks the
// read-decide-write race, so it does not reliably fail when the per-playlist
// lock is absent. These white-box tests assert the lock primitive itself, so a
// future change that neutralizes lockPlaylist (e.g. makes it a no-op) fails
// deterministically.
//
// Governing: SPEC playlist-sync-navidrome (per-playlist serialization of
// sync/pair/rebuild so link-during-sync cannot silently lose a pairing).

// TestLockPlaylist_SerializesSamePlaylist fails deterministically if lockPlaylist
// stops providing mutual exclusion: the widened read-modify-write window makes
// concurrent unlocked access lose updates, so counter < goroutines.
func TestLockPlaylist_SerializesSamePlaylist(t *testing.T) {
	s := &PlaylistSyncService{locks: make(map[int]*sync.Mutex)}

	const goroutines = 50
	counter := 0
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			unlock := s.lockPlaylist(1)
			defer unlock()
			tmp := counter
			// Widen the window between read and write; without a real lock this
			// makes lost updates overwhelmingly likely (and trips -race).
			time.Sleep(50 * time.Microsecond)
			counter = tmp + 1
		}()
	}
	close(start)
	wg.Wait()

	if counter != goroutines {
		t.Fatalf("lockPlaylist did not serialize same-playlist access: counter=%d, want %d (per-playlist lock missing or broken)", counter, goroutines)
	}
}

// TestLockPlaylist_DistinctIDsIndependent ensures different playlist IDs do not
// share a lock (no false serialization / deadlock across unrelated playlists).
func TestLockPlaylist_DistinctIDsIndependent(t *testing.T) {
	s := &PlaylistSyncService{locks: make(map[int]*sync.Mutex)}

	unlock1 := s.lockPlaylist(1)
	defer unlock1()

	done := make(chan struct{})
	go func() {
		unlock2 := s.lockPlaylist(2) // must not block on id=1's lock
		unlock2()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lockPlaylist(2) blocked on lockPlaylist(1): distinct playlist IDs must have independent locks")
	}
}
