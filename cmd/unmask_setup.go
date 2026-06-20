package cmd

import (
	"fmt"
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
	fetch := func(rev int) ([]byte, bool, error) {
		return fetchMaskKey(serverURL, apiKey, host, info.RepoID, rev, store)
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
func fetchMaskKey(
	serverURL, apiKey, host, repoID string,
	rev int,
	store *keychain.Store,
) ([]byte, bool, error) {
	if key, err := store.GetMaskKey(host, repoID, rev); err == nil && len(key) > 0 {
		return key, true, nil
	}
	keys, err := unmask.FetchMaskingKeys(serverURL, apiKey, repoID)
	if err != nil {
		return nil, false, err
	}
	for r, k := range keys {
		_ = store.SetMaskKey(host, repoID, r, k) // best-effort cache
	}
	key, ok := keys[rev]
	return key, ok, nil
}

// queryUnmask builds an UnmaskPath for one-shot `codastre query` runs, scoped to
// the CWD's repository. It is a best-effort display enhancement: any setup
// failure (no keychain, not in a repo, repo unmasked) yields nil and the caller
// falls back to showing raw path_tokens.
func queryUnmask(apiKey string) func(pathToken string, maskKeyRev int) (string, bool) {
	store, _, err := keychain.Open()
	if err != nil {
		return nil
	}
	repoRoot, err := findGitRoot(".")
	if err != nil {
		return nil
	}
	return setupUnmask(queryServerURL, apiKey, repoRoot, store)
}
