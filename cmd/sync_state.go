package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// syncStateTTL bounds how long a recorded sync suppresses an identical one.
// Repeats are suppressed rather than dropped forever so the client self-heals:
// a GC'd overlay or a rolled-forward base (which changes from_ref_sha while
// to_ref_sha stands still, and re-roots the overlay on its next sync) is picked
// up within one TTL instead of never.
const syncStateTTL = 15 * time.Minute

// syncState is the last SYNC outcome for a working copy — the dedup key the HEAD
// watcher checks before doing anything.
//
// It lives in .git/ rather than in memory because every open agent session runs
// its own `codastre serve` HEAD watcher against the same working copy: an
// in-process guard dedups one watcher, a file dedups all of them. .git/ is also
// never committed and dies with the clone, which is exactly the lifetime we want.
type syncState struct {
	IndexID  string    `json:"index_id"`
	ToRef    string    `json:"to_ref"`
	ToSHA    string    `json:"to_sha"`
	SyncedAt time.Time `json:"synced_at"`
}

func syncStatePath(repoRoot string) string {
	return filepath.Join(repoRoot, ".git", "codastre-sync-state.json")
}

// readSyncState returns the recorded state, or nil when there is none (or it is
// unreadable/corrupt — a missing dedup hint only costs one redundant SYNC).
func readSyncState(repoRoot string) *syncState {
	raw, err := os.ReadFile(syncStatePath(repoRoot))
	if err != nil {
		return nil
	}
	var st syncState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil
	}
	return &st
}

// writeSyncState records the outcome atomically (temp + rename) so a concurrent
// watcher never reads a half-written file. Errors are returned but callers treat
// them as non-fatal: an unrecorded sync means one extra SYNC later, not a failure.
func writeSyncState(repoRoot string, st syncState) error {
	path := syncStatePath(repoRoot)
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codastre-sync-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// syncIsRedundant reports whether the ref pair was already handled recently
// enough to skip. A different branch, a new commit, or an expired record all sync
// again. Deliberately keyed on the refs alone, not IndexID: the check has to run
// before any network call (that is the point — a redundant SYNC should cost zero
// requests), and a working copy maps to exactly one repo and base index.
func syncIsRedundant(st *syncState, toRef, toSHA string, now time.Time) bool {
	if st == nil || st.ToSHA == "" {
		return false
	}
	if st.ToRef != toRef || st.ToSHA != toSHA {
		return false
	}
	return now.Sub(st.SyncedAt) < syncStateTTL
}
