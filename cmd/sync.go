package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/mcpclient"
	"github.com/codastre/cli/internal/unmask"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Watch HEAD and sync on changes (--once for a single eager sync then exit)",
	RunE:  runSync,
}

var syncOnce bool
var syncDedup bool
var syncServerURL string

func init() {
	syncCmd.Flags().BoolVar(&syncOnce, "once", false, "Single sync then exit")
	syncCmd.Flags().BoolVar(&syncDedup, "dedup", false,
		"With --once, skip when this HEAD was synced recently, is being synced, or "+
			"just failed (what the watcher and query do); for hooks that may fire often")
	syncCmd.Flags().StringVar(&syncServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	if syncOnce {
		return doSync(syncDedup)
	}
	return watchAndSync()
}

// watchAndSync watches .git/HEAD and related ref files; debounces 5 s before calling doSync.
// Falls back to 10 s mtime polling if fsnotify is unavailable (impl-spec §2.3).
func watchAndSync() error {
	repoRoot, err := findGitRoot(".")
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return pollSync(repoRoot)
	}
	defer watcher.Close()

	watchTargets := []string{
		filepath.Join(repoRoot, ".git", "HEAD"),
		filepath.Join(repoRoot, ".git", "refs", "heads"),
		filepath.Join(repoRoot, ".git", "packed-refs"),
	}
	for _, p := range watchTargets {
		_ = watcher.Add(p) // ignore errors for paths that may not exist yet
	}

	var debounce <-chan time.Time
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			debounce = time.After(5 * time.Second)
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watch error: %v\n", watchErr)
		case <-debounce:
			if err := doSync(true); err != nil {
				fmt.Fprintf(os.Stderr, "sync error: %v\n", err)
			}
			debounce = nil
		}
	}
}

// pollSync is the 10 s mtime polling fallback (impl-spec §2.3).
func pollSync(repoRoot string) error {
	fmt.Fprintln(os.Stderr, "warning: fsnotify unavailable, falling back to 10 s polling")
	headPath := filepath.Join(repoRoot, ".git", "HEAD")
	var lastMtime time.Time
	for {
		stat, err := os.Stat(headPath)
		if err == nil {
			mt := stat.ModTime()
			if !lastMtime.IsZero() && mt.After(lastMtime) {
				if err := doSync(true); err != nil {
					fmt.Fprintf(os.Stderr, "sync error: %v\n", err)
				}
			}
			lastMtime = mt
		}
		time.Sleep(10 * time.Second)
	}
}

// doSync performs one SYNC round-trip: resolve the local repo and its base index,
// diff the base against HEAD, mask the changed paths, and push the overlay delta
// over MCP SYNC.
//
// It no-ops quietly (returning nil) when there is nothing to do — HEAD is the
// base branch, HEAD is the base commit, the repo isn't registered, or no base
// index is ready — so the HEAD watcher does not spam errors on every commit to
// the base branch.
//
// Every no-op and every successful sync is recorded in .git (see sync_state.go),
// and a recorded ref pair short-circuits before the first network call. Without
// that, any write under .git/refs — including ones that change nothing we care
// about — re-sent an identical diff, and N agent sessions in one working copy
// each ran their own watcher, so a quiet repo still produced a steady stream of
// duplicate SYNCs.
//
// dedup is false for an explicit `codastre sync --once`: the user asked for a
// sync, so they get one even if the refs have not moved. `--once --dedup` (the
// plugin's session-start hook) opts back in.
func doSync(dedup bool) error {
	return syncRepo(syncServerURL, dedup, os.Stderr)
}

// syncRepo is doSync against an explicit server, logging to log. `codastre
// query` calls it for its eager sync (search-effectiveness C) with its own
// --server and a quiet log, so the watcher and the query share one SYNC path.
func syncRepo(serverURL string, dedup bool, log io.Writer) error {
	repoRoot, err := findGitRoot(".")
	if err != nil {
		return err
	}

	toName, toSHA, err := git.HeadRef(repoRoot)
	if err != nil {
		return err
	}
	prev := readSyncState(repoRoot)
	if dedup && syncIsRedundant(prev, toName, toSHA, time.Now()) {
		return nil
	}
	rec := syncRecorder{log: log, root: repoRoot, toName: toName, toSHA: toSHA}
	// While HEAD is unpushed every new local commit maps to the same pushed base;
	// re-sending that SYNC per commit would cost a job each time for nothing. The
	// target is local knowledge, so this check costs no request.
	if dedup && prev != nil && prev.ToRef == toName && prev.Status == syncDone &&
		time.Since(prev.SyncedAt) < syncStateTTL {
		if sent, ok := fetchableTarget(repoRoot, toSHA); ok && sent == prev.sentOrHead() {
			rec.done(prev.IndexID, sent)
			return nil
		}
	}
	// Claim the ref pair before the first request, so parallel callers (several
	// queries, several sessions' watchers) wait this attempt out instead of each
	// sending their own. Every exit below overwrites the claim.
	rec.write(syncPending, "", "")
	if err := syncAttempt(serverURL, log, rec); err != nil {
		rec.write(syncFailed, "", "")
		return err
	}
	return nil
}

// syncAttempt is one SYNC round-trip for HEAD; every non-error exit records the
// outcome through rec (an error is recorded as failed by the caller).
func syncAttempt(serverURL string, log io.Writer, rec syncRecorder) error {
	repoRoot, toName, toSHA := rec.root, rec.toName, rec.toSHA
	apiKey, warn, err := resolveAPIKey(serverURL, "")
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintln(log, "warning: "+warn)
	}

	remote, err := getRemoteURL(repoRoot)
	if err != nil {
		rec.done("", "")
		return nil // no origin → nothing to sync against
	}
	normalized, err := git.Normalize(remote)
	if err != nil {
		rec.done("", "")
		return nil
	}

	info, err := unmask.ResolveRepo(serverURL, apiKey, normalized)
	if err != nil {
		return err
	}
	if info == nil {
		// Recorded, so an unregistered checkout costs one lookup per TTL rather
		// than one per query.
		rec.done("", "")
		fmt.Fprintf(log, "sync: %s is not registered — REGISTER it first\n", normalized)
		return nil
	}

	base, err := unmask.ResolveBaseIndex(serverURL, apiKey, info.RepoID)
	if err != nil {
		return err
	}
	if base == nil {
		rec.done("", "")
		fmt.Fprintln(log, "sync: no ready base index yet — skipping")
		return nil
	}

	// The base branch is never an overlay: an overlay named after the base ref
	// would duplicate the base for every changed file, and keeping the base fresh
	// is the server's base_rollforward job, not ours. So a base branch that has
	// moved past base_ref_sha is the server's business, not an overlay to push —
	// skip it here (and record the skip so the watcher stops re-resolving it).
	if toName == base.BaseRefName {
		rec.done(base.IndexID, "")
		fmt.Fprintf(log,
			"sync: on base branch %s — nothing to overlay (base freshness is server-side)\n",
			toName)
		return nil
	}

	// The server reads every blob from its own clone, fetching the commit from
	// origin: it cannot see a commit that was never pushed, and a SYNC naming one
	// only costs it a failed fetch. Send the newest commit of HEAD's history that
	// origin has; with none, there is nothing the server could apply.
	sent, ok := fetchableTarget(repoRoot, toSHA)
	if !ok {
		rec.done(base.IndexID, "")
		fmt.Fprintln(log, "sync: HEAD is not on origin and has no pushed ancestor on its "+
			"upstream — push the branch to make it searchable")
		return nil
	}
	if sent != toSHA {
		fmt.Fprintf(log, "sync: HEAD has unpushed commits — syncing its pushed base %s\n", shortSHA(sent))
	}
	if sent == base.BaseRefSHA {
		rec.done(base.IndexID, sent)
		return nil // on the base commit; nothing to overlay
	}

	diff, err := git.ComputeDiff(repoRoot, base.BaseRefSHA, sent)
	if err != nil {
		return err
	}

	args := map[string]any{
		"index_id":      base.IndexID,
		"from_ref_name": base.BaseRefName,
		"from_ref_sha":  base.BaseRefSHA,
		"to_ref_name":   toName,
		"to_ref_sha":    sent,
		"mask_key_rev":  info.MaskKeyRev,
		"diff_omitted":  diff.DiffOmitted,
		"manifest_mode": diff.ManifestMode,
	}

	if diff.DiffOmitted {
		// Over cap: send refs only; the server diffs its own clone.
		args["diff"] = []map[string]any{}
	} else {
		key, err := syncMaskKey(serverURL, apiKey, info)
		if err != nil {
			return err
		}
		args["diff"] = buildDiffTuples(diff.Entries, info.MaskingScheme, key)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload, err := mcpclient.Call(ctx, mcpclient.Config{ServerURL: serverURL, APIKey: apiKey}, "SYNC", args)
	if err != nil {
		return err
	}

	status, jobID := parseSyncResult(payload)
	rec.done(base.IndexID, sent)
	fmt.Fprintf(log, "sync: %s → %s (status=%s job=%s)\n", base.BaseRefName, toName, status, jobID)
	return nil
}

// fetchableTarget is the commit to SYNC for head: head itself when origin has it,
// else its newest pushed ancestor on the upstream; ok=false when there is none.
func fetchableTarget(repoRoot, head string) (string, bool) {
	if git.OnRemote(repoRoot, "origin", head) {
		return head, true
	}
	if pb := git.PushedBase(repoRoot, head); pb != "" && git.OnRemote(repoRoot, "origin", pb) {
		return pb, true
	}
	return "", false
}

// syncRecorder stamps HEAD's ref pair with an attempt's outcome so identical
// wake-ups — including those from other agent sessions sharing this working
// copy — return before touching the network. Best-effort: a write failure only
// costs a redundant SYNC next time.
type syncRecorder struct {
	log           io.Writer
	root          string
	toName, toSHA string
}

func (r syncRecorder) done(indexID, sent string) { r.write(syncDone, indexID, sent) }

func (r syncRecorder) write(status, indexID, sent string) {
	if sent == r.toSHA {
		sent = ""
	}
	st := syncState{IndexID: indexID, ToRef: r.toName, ToSHA: r.toSHA, SyncedAt: time.Now(),
		Status: status, SentSHA: sent}
	if err := writeSyncState(r.root, st); err != nil {
		fmt.Fprintf(r.log, "sync: could not record sync state: %v\n", err)
	}
}

// syncMaskKey returns the masking key needed to mask the diff, or nil for an
// unmasked repo (where no key is required).
func syncMaskKey(serverURL, apiKey string, info *unmask.RepoInfo) ([]byte, error) {
	if !info.IsMasked() {
		return nil, nil
	}
	store, _, err := keychain.Open()
	if err != nil {
		return nil, fmt.Errorf("open keychain: %w", err)
	}
	host := extractHost(serverURL)
	key, ok, err := fetchMaskKey(serverURL, apiKey, host, info.RepoID, info.MaskKeyRev, false, store)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no masking key for rev %d", info.MaskKeyRev)
	}
	return key, nil
}

// findGitRoot walks up from start until it finds a .git directory.
func findGitRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a git repository")
		}
		dir = parent
	}
}
