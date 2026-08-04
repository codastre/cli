package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRepoDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSyncStateRoundTrips(t *testing.T) {
	root := newRepoDir(t)
	now := time.Now().UTC().Truncate(time.Second)
	want := syncState{IndexID: "idx-1", ToRef: "feat/x", ToSHA: "deadbeef", SyncedAt: now}

	if err := writeSyncState(root, want); err != nil {
		t.Fatalf("writeSyncState: %v", err)
	}
	got := readSyncState(root)
	if got == nil {
		t.Fatal("readSyncState returned nil after a successful write")
	}
	if got.IndexID != want.IndexID || got.ToRef != want.ToRef || got.ToSHA != want.ToSHA {
		t.Errorf("round-trip mismatch: got %+v want %+v", *got, want)
	}
	if !got.SyncedAt.Equal(now) {
		t.Errorf("SyncedAt = %v, want %v", got.SyncedAt, now)
	}
}

// Writing must not leave the temp file behind: the state file lives in .git, and
// litter there is both confusing and picked up by `git status` tooling.
func TestWriteSyncStateLeavesNoTempFiles(t *testing.T) {
	root := newRepoDir(t)
	if err := writeSyncState(root, syncState{ToRef: "b", ToSHA: "s", SyncedAt: time.Now()}); err != nil {
		t.Fatalf("writeSyncState: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "codastre-sync-state.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf(".git contains %v, want only codastre-sync-state.json", names)
	}
}

func TestReadSyncStateMissingOrCorruptIsNil(t *testing.T) {
	root := newRepoDir(t)
	if got := readSyncState(root); got != nil {
		t.Errorf("no state file: got %+v, want nil", *got)
	}
	if err := os.WriteFile(syncStatePath(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readSyncState(root); got != nil {
		t.Errorf("corrupt state file: got %+v, want nil", *got)
	}
}

func TestSyncIsRedundant(t *testing.T) {
	now := time.Now()
	fresh := &syncState{IndexID: "idx", ToRef: "feat/x", ToSHA: "sha1", SyncedAt: now.Add(-time.Minute)}

	cases := []struct {
		name  string
		st    *syncState
		toRef string
		toSHA string
		want  bool
	}{
		{"same refs within ttl", fresh, "feat/x", "sha1", true},
		{"new commit on same branch", fresh, "feat/x", "sha2", false},
		{"different branch same sha", fresh, "feat/y", "sha1", false},
		{"expired record", &syncState{ToRef: "feat/x", ToSHA: "sha1",
			SyncedAt: now.Add(-syncStateTTL - time.Second)}, "feat/x", "sha1", false},
		{"no record", nil, "feat/x", "sha1", false},
		{"empty record", &syncState{}, "feat/x", "sha1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := syncIsRedundant(tc.st, tc.toRef, tc.toSHA, now); got != tc.want {
				t.Errorf("syncIsRedundant = %v, want %v", got, tc.want)
			}
		})
	}
}
