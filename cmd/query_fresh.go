package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/codastre/cli/internal/git"
)

// Search-effectiveness Phase C: the CLI plane searches what the checkout holds.
//
// Before this, `codastre query` sent no ref and never synced, so it searched the
// base alone — and the server answered `freshness: "fresh"`, which means "the
// base build is complete", not "matches HEAD". An agent on a branch was searching
// code 26 commits old and being told it was fresh. Three pieces fix that, all
// scoped to a query whose target IS the checkout it runs in:
//
//   - the current branch rides as `ref`, so the server unions its overlay;
//   - an eager sync pushes HEAD's diff first, on a client-side budget;
//   - the header names the commit actually searched and how far it trails HEAD.

// defaultEagerSyncBudget is the client's own wait for the eager sync. Separate
// from the server's 3 s inline-sync budget, which starts only when the QUERY
// request arrives: spending that budget here first would leave the server none.
const defaultEagerSyncBudget = 2 * time.Second

// localCheckout is the working copy a query runs in, when it is the query's target.
type localCheckout struct {
	root   string
	branch string // HEAD's branch; the short sha on a detached HEAD (what sync names it)
	head   string
}

// checkoutFor returns the CWD checkout when tgt resolves to it: auto mode (the
// target came from this checkout's remotes) or an explicit --repo-url naming it.
// A federated or --index-id query has no single checkout to speak for.
func checkoutFor(tgt target) (localCheckout, bool) {
	if tgt.federated || tgt.indexID != "" || tgt.repoURL == "" {
		return localCheckout{}, false
	}
	root, err := findGitRoot(".")
	if err != nil {
		return localCheckout{}, false
	}
	if !tgt.auto && !dirMatchesRepo(root, tgt.repoURL) {
		return localCheckout{}, false
	}
	branch, head, err := git.HeadRef(root)
	if err != nil {
		return localCheckout{}, false
	}
	return localCheckout{root: root, branch: branch, head: head}, true
}

// queryRefFor picks the ref to send: an explicit --ref always wins, --base-only
// sends none, and otherwise a query against its own checkout sends the branch.
// On the base branch that is harmless — the server answers base-only, fresh.
func queryRefFor(explicit string, baseOnly bool, co localCheckout, ok bool) string {
	if explicit != "" {
		return explicit
	}
	if baseOnly || !ok {
		return ""
	}
	return co.branch
}

// eagerSyncBudget reads $CODASTRE_EAGER_SYNC_BUDGET as a Go duration ("2s",
// "500ms") or whole seconds ("2"); "0" disables the eager sync. Anything
// unparseable falls back to the default rather than failing the query.
func eagerSyncBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CODASTRE_EAGER_SYNC_BUDGET"))
	if raw == "" {
		return defaultEagerSyncBudget
	}
	if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
		return d
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	return defaultEagerSyncBudget
}

// eagerSync runs the HEAD watcher's sync once, waiting at most budget. The
// sync's own dedup makes the common case free: a HEAD already synced returns
// before any network call. On timeout the query goes ahead and the server says
// `syncing`; the sync records nothing until it succeeds, so the next query
// retries it. Errors are reported on log and never fail the query.
func eagerSync(serverURL string, budget time.Duration, log io.Writer) {
	if budget <= 0 {
		return
	}
	done := make(chan error, 1)
	go func() { done <- syncRepo(serverURL, true, io.Discard) }()
	select {
	case err := <-done:
		if err != nil {
			fmt.Fprintf(log, "note: eager sync failed (%v) — results may predate HEAD\n", err)
		}
	case <-time.After(budget):
		fmt.Fprintf(log, "note: eager sync still running after %s — querying anyway\n", budget)
	}
}

// searchedRefs is one repo's searched commits as the server reports them (C).
type searchedRefs struct {
	BaseRef    string `json:"base_ref"`
	BaseSHA    string `json:"base_sha"`
	OverlayRef string `json:"overlay_ref"`
	OverlaySHA string `json:"overlay_sha"`
}

// searchedLine renders which commit answered for the checkout's repo and how
// far it trails HEAD: "searched: overlay feat/x @ 1aa1e6b · at HEAD", or
// "searched: base main @ 1aa1e6b · 3 commits behind HEAD". Empty when the
// server sent no refs (it predates C) — callers then show `freshness` alone.
func searchedLine(payload json.RawMessage, co localCheckout) string {
	var env struct {
		Repos map[string]searchedRefs `json:"repos"`
	}
	if json.Unmarshal(payload, &env) != nil || len(env.Repos) != 1 {
		return ""
	}
	var refs searchedRefs
	for _, r := range env.Repos {
		refs = r
	}
	kind, name, sha := "base", refs.BaseRef, refs.BaseSHA
	if refs.OverlaySHA != "" {
		kind, name, sha = "overlay", refs.OverlayRef, refs.OverlaySHA
	}
	if sha == "" {
		return ""
	}
	return fmt.Sprintf("searched: %s %s @ %s · %s", kind, name, shortSHA(sha), lagPhrase(co, sha))
}

// lagPhrase says how the searched commit relates to HEAD.
func lagPhrase(co localCheckout, sha string) string {
	if sha == co.head {
		return "at HEAD"
	}
	n, err := git.CountCommits(co.root, sha, co.head)
	switch {
	case err != nil:
		// The commit is not in this clone (never fetched, or rewritten away).
		return "not in this checkout — HEAD relation unknown"
	case n == 0:
		// HEAD is an ancestor: the index is AHEAD of the working tree.
		return "ahead of HEAD (results may show code you do not have)"
	case n == 1:
		return "1 commit behind HEAD"
	default:
		return fmt.Sprintf("%d commits behind HEAD", n)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
