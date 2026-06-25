package cmd

import (
	"fmt"
	"io"
	"os"

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
// Unmasking inverts per-component HMAC by enumerating the LOCAL working tree (see
// internal/unmask), so a token only resolves when the queried repo is the current
// checkout. When the run explicitly targets a different repo (--repo-url) than the
// CWD repo, no local file can match its tokens — building the resolver would be
// pure cost with every lookup missing. We skip it and, when that repo is actually
// HMAC-masked, tell the user how to get real paths instead of silently printing
// masked tokens. A federated (--all) or --index-id run keeps the best-effort CWD
// resolver: it unmasks results from the CWD repo and leaves the rest masked.
func resolveUnmask(
	warnW io.Writer,
	tgt target,
	serverURL, apiKey string,
) func(pathToken string, maskKeyRev int) (string, bool) {
	cwdURL := autoTarget() // normalized origin of the CWD repo, "" if none

	if tgt.repoURL != "" && tgt.repoURL != cwdURL {
		info, err := unmask.ResolveRepo(serverURL, apiKey, tgt.repoURL)
		if err == nil && info != nil && info.IsMasked() {
			if cwdURL == "" {
				fmt.Fprintf(warnW, "warning: paths shown masked — run inside a checkout of %s to unmask, or pass --no-unmask\n", tgt.repoURL)
			} else {
				fmt.Fprintf(warnW, "warning: paths shown masked — results are for %s but this is the %s checkout; run inside the target repo to unmask, or pass --no-unmask\n", tgt.repoURL, cwdURL)
			}
		}
		return nil
	}

	return cwdUnmask(serverURL, apiKey)
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
