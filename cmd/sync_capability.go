package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/codastre/cli/internal/unmask"
)

// Branch sync can be switched off by the server's operator (or enabled only for
// some tenants, accounts and repos) to shed the Qdrant write load of overlays.
// The server gates SYNC itself, answering status "sync_disabled"; the client
// learns that — up front from /v1/me/capabilities, or from the SYNC answer — and
// records it in the sync state so that, for one syncStateTTL window:
//
//   - the HEAD watcher and the eager sync send nothing (no diff, no request);
//   - `codastre query` sends no ref and searches the base, instead of naming a
//     branch whose overlay will never be built.

// syncDisabledStatus is the SYNC no-op status of a gated call.
const syncDisabledStatus = "sync_disabled"

// syncDisabledRecently reports whether the server said branch sync is off within
// the last window. It is keyed on the working copy, not the ref pair: the policy
// is about the repo and the caller, so a new branch or commit does not reopen it.
func syncDisabledRecently(st *syncState, now time.Time) bool {
	return st != nil && st.Status == syncDisabled && now.Sub(st.SyncedAt) < syncStateTTL
}

// branchSyncDisabled is the query-side read: true when the checkout's last word
// from the server was "sync disabled", so the query should search the base only.
func branchSyncDisabled(co localCheckout, inCheckout bool) bool {
	return inCheckout && syncDisabledRecently(readSyncState(co.root), time.Now())
}

// recordSyncDisabled stores the server's "sync disabled" and tells the user once
// per window.
func recordSyncDisabled(rec syncRecorder, indexID string) {
	rec.write(syncDisabled, indexID, "")
	fmt.Fprintln(rec.log, "sync: branch sync is disabled on this server — "+
		"searches cover the base branch only")
}

// syncAllowedByServer asks the server before any diff is computed. A false
// answer is recorded; true (including any error, which fails open) carries on.
func syncAllowedByServer(serverURL, apiKey, repoID string, rec syncRecorder) bool {
	if unmask.SyncEnabled(serverURL, apiKey, repoID) {
		return true
	}
	recordSyncDisabled(rec, "")
	return false
}

// noteBaseOnly tells a query's user why it ignored their branch.
func noteBaseOnly(log io.Writer) {
	fmt.Fprintln(log, "note: branch sync is disabled on this server — searching the base branch only")
}
