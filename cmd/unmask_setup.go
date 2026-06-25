package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/codastre/cli/internal/checkouts"
	"github.com/codastre/cli/internal/git"
	"github.com/codastre/cli/internal/keychain"
	"github.com/codastre/cli/internal/unmask"
)

// setupUnmask wires the client-side masking-key lifecycle for the repo at
// repoRoot and returns an UnmaskPath function for the MCP proxy, or nil when
// unmasking is unnecessary or unavailable (not a git repo, no origin, repo not
// found, or masking_scheme != hmac).
//
// Lifecycle:
//   - resolve the local origin remote to its server-side repo_id;
//   - if the repo is HMAC-masked, build a Resolver whose key fetcher reads the
//     keychain first and falls back to GET /v1/repos/{id}/masking-key, caching
//     every returned revision. This both populates the keychain on first use and
//     transparently recovers from key rotation (a newly-seen rev triggers a
//     re-fetch that returns the live revs during the grace window);
//   - warm the current revision so the first QUERY doesn't pay the build cost.
//
// Failures are non-fatal: they print a warning and return nil so the proxy still
// serves (masked) results rather than refusing to start.
func setupUnmask(
	serverURL, apiKey, repoRoot string,
	store *keychain.Store,
) func(pathToken string, maskKeyRev int) (string, bool) {
	if repoRoot == "" {
		return nil
	}
	remote, err := getRemoteURL(repoRoot)
	if err != nil {
		return nil // no origin → nothing to resolve against
	}
	normalized, err := git.Normalize(remote)
	if err != nil {
		return nil
	}

	info, err := unmask.ResolveRepo(serverURL, apiKey, normalized)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not resolve repo for unmasking: %v\n", err)
		return nil
	}
	if info == nil || !info.IsMasked() {
		return nil // unregistered, or masking_scheme == none → tokens are cleartext
	}

	host := extractHost(serverURL)
	fetch := func(rev int, force bool) ([]byte, bool, error) {
		return fetchMaskKey(serverURL, apiKey, host, info.RepoID, rev, force, store)
	}

	resolver := unmask.New(repoRoot, fetch)
	if _, err := resolver.LoadRev(info.MaskKeyRev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to load masking key rev %d: %v\n", info.MaskKeyRev, err)
		return nil
	}
	return resolver.Unmask
}

// fetchMaskKey returns the masking key for (repoID, rev), reading the keychain
// first and falling back to GET /v1/repos/{id}/masking-key. Every revision the
// server returns is cached, so a rotation (a newly-seen rev) is fetched once and
// then served from the keychain. ok is false when the server has no such rev.
//
// When force is true the keychain read is skipped and the server is queried
// directly; the fetched keys overwrite the cache. This recovers from a rev whose
// key changed under the same number (disable → re-enable masking reuses rev 1),
// where the cached key would otherwise be stale forever.
func fetchMaskKey(
	serverURL, apiKey, host, repoID string,
	rev int,
	force bool,
	store *keychain.Store,
) ([]byte, bool, error) {
	if !force {
		if key, err := store.GetMaskKey(host, repoID, rev); err == nil && len(key) > 0 {
			return key, true, nil
		}
	}
	keys, err := unmask.FetchMaskingKeys(serverURL, apiKey, repoID)
	if err != nil {
		return nil, false, err
	}
	for r, k := range keys {
		_ = store.SetMaskKey(host, repoID, r, k) // best-effort cache (overwrites stale)
	}
	key, ok := keys[rev]
	return key, ok, nil
}

// resolveUnmask builds the path-unmasking function for a one-shot query/graph run
// and explains (to warnW) when unmasking cannot apply.
//
// Unmasking inverts per-component HMAC by enumerating a working tree (see
// internal/unmask) — the masking key alone can't reverse a one-way hash, so a
// token only resolves against the real paths of a local clone of that repo.
//
// Resolution, in order:
//   - target is the CWD repo (or a federated --all / --index-id run): unmask
//     against the CWD checkout (best-effort; --all leaves other repos masked);
//   - target is a different repo: unmask against an explicit --repo-path, else a
//     clone remembered from a previous in-repo run (~/.config/codastre);
//   - none of those: if the target repo is HMAC-masked, warn how to get real
//     paths rather than silently printing masked tokens.
//
// Every in-repo run records the CWD repo→path mapping so later cross-directory
// runs against that repo can find it automatically.
func resolveUnmask(
	warnW io.Writer,
	tgt target,
	repoPath, serverURL, apiKey string,
) func(pathToken string, maskKeyRev int) (string, bool) {
	cwdRoot, cwdURL := cwdRepo()
	if cwdURL != "" {
		_ = checkouts.Remember(cwdURL, cwdRoot) // learn this checkout's location
	}

	// Same repo as the CWD, or a non-repo-scoped run (--all / --index-id): the
	// CWD checkout is the right (or only knowable) source of candidate paths.
	if tgt.repoURL == "" || tgt.repoURL == cwdURL {
		return cwdUnmask(serverURL, apiKey)
	}

	// Cross-repo: find a local clone of the target to hash against.
	if dir := locateCheckout(warnW, repoPath, tgt.repoURL); dir != "" {
		if store, _, err := keychain.Open(); err == nil {
			if fn := setupUnmask(serverURL, apiKey, dir, store); fn != nil {
				return fn
			}
		}
	}

	// No local clone available. Only nag when there is actually something to
	// unmask (an HMAC-masked target); cleartext repos need no warning.
	if info, err := unmask.ResolveRepo(serverURL, apiKey, tgt.repoURL); err == nil && info != nil && info.IsMasked() {
		fmt.Fprintf(warnW, "warning: paths shown masked — no local checkout of %s found; pass --repo-path <dir>, run once inside it, or use --no-unmask\n", tgt.repoURL)
	}
	return nil
}

// cwdRepo returns the git root and normalized origin URL of the current
// directory's repository, or ("", "") when the CWD is not a git repo with an
// origin remote.
func cwdRepo() (root, url string) {
	root, err := findGitRoot(".")
	if err != nil {
		return "", ""
	}
	remote, err := getRemoteURL(root)
	if err != nil {
		return "", ""
	}
	norm, err := git.Normalize(remote)
	if err != nil {
		return "", ""
	}
	return root, norm
}

// locateCheckout returns a local clone directory whose origin matches repoURL: an
// explicit --repo-path (validated) takes priority over the remembered registry.
// A --repo-path that doesn't match the target is reported to warnW and ignored,
// then the registry is tried as a fallback. Returns "" when none is known or valid.
func locateCheckout(warnW io.Writer, repoPath, repoURL string) string {
	if repoPath != "" {
		if dirMatchesRepo(repoPath, repoURL) {
			return repoPath
		}
		fmt.Fprintf(warnW, "warning: --repo-path %s is not a checkout of %s; ignoring it\n", repoPath, repoURL)
	}
	if dir, ok := checkouts.Lookup(repoURL); ok && dirMatchesRepo(dir, repoURL) {
		return dir
	}
	return ""
}

// dirMatchesRepo reports whether dir is a git checkout whose origin normalizes to
// repoURL — guards against a stale registry entry or a wrong --repo-path.
func dirMatchesRepo(dir, repoURL string) bool {
	root, err := findGitRoot(dir)
	if err != nil {
		return false
	}
	remote, err := getRemoteURL(root)
	if err != nil {
		return false
	}
	norm, err := git.Normalize(remote)
	return err == nil && norm == repoURL
}

// cwdUnmask builds an UnmaskPath for one-shot `codastre query` / `codastre graph`
// runs, scoped to the CWD's repository. It is a best-effort display enhancement:
// any setup failure (no keychain, not in a repo, repo unmasked) yields nil and
// the caller falls back to showing raw path_tokens. setupUnmask itself returns
// nil when the repo's masking_scheme != hmac, so we only build the (cost-bearing)
// reverse map when unmasking is actually needed.
func cwdUnmask(serverURL, apiKey string) func(pathToken string, maskKeyRev int) (string, bool) {
	store, _, err := keychain.Open()
	if err != nil {
		return nil
	}
	repoRoot, err := findGitRoot(".")
	if err != nil {
		return nil
	}
	return setupUnmask(serverURL, apiKey, repoRoot, store)
}
