package cmd

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestSyncDisabledRecentlyIgnoresRefAndExpires(t *testing.T) {
	now := time.Now()
	st := &syncState{ToRef: "feat/a", ToSHA: "aaa", Status: syncDisabled, SyncedAt: now.Add(-time.Minute)}
	if !syncDisabledRecently(st, now) {
		t.Fatal("want disabled within the window")
	}
	if syncDisabledRecently(st, now.Add(syncStateTTL)) {
		t.Fatal("want the window to lapse so a re-enabled server is picked up")
	}
	if syncDisabledRecently(&syncState{Status: syncDone, SyncedAt: now}, now) {
		t.Fatal("a done record is not a disabled one")
	}
	if syncDisabledRecently(nil, now) {
		t.Fatal("no record is not disabled")
	}
}

func TestBranchSyncDisabledReadsCheckoutState(t *testing.T) {
	root := newRepoDir(t)
	co := localCheckout{root: root, branch: "feat/b", head: "bbb"}
	if branchSyncDisabled(co, true) {
		t.Fatal("no state yet: want enabled")
	}
	recordSyncDisabled(syncRecorder{log: io.Discard, root: root, toName: "feat/a", toSHA: "aaa"}, "idx")
	if !branchSyncDisabled(co, true) {
		t.Fatal("recorded on another branch: want disabled for the whole checkout")
	}
	if branchSyncDisabled(co, false) {
		t.Fatal("a query not targeting the checkout is never affected")
	}
}

// With branch sync off, doctor must not advise `codastre sync --once` (it would
// do nothing) nor warn about an operator's deliberate choice.
func TestIndexLagVerdictSyncOff(t *testing.T) {
	f := indexLag{branch: "feat/x", head: "h", baseRef: "main", baseSHA: "b", ahead: 40, syncOff: true}.verdict()
	if !f.ok {
		t.Fatalf("want informational, got warning: %s", f.detail)
	}
	if want := "branch sync is disabled"; !strings.Contains(f.detail, want) {
		t.Fatalf("detail %q lacks %q", f.detail, want)
	}
}
