// Package unmask implements the client-side masking-key lifecycle: it fetches a
// repo's HMAC masking key(s) from the server, builds per-revision token→path
// reverse maps from the local working tree, and exposes an UnmaskPath function
// the MCP proxy uses to turn path_tokens back into real paths.
//
// HMAC is one-way, so a token cannot be inverted arithmetically. Instead the
// client enumerates its own tracked files (`git ls-files`), HMAC-masks each path
// with the repo key, and inverts the resulting map. A token is therefore only
// resolvable when the corresponding file exists in the local checkout — which is
// exactly the condition under which the proxy can also hydrate its snippet.
package unmask

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/codastre/cli/internal/masking"
)

// refreshCooldown bounds how often a single rev is force-refreshed from the
// server. A response full of tokens for files not in the local checkout would
// otherwise refetch on every miss; one refresh per window is enough to recover
// from a key change while keeping that cost off the hot path.
const refreshCooldown = 60 * time.Second

// KeyFetcher returns the raw masking key for a given revision. ok is false when
// the server has no such revision (e.g. masking disabled, or an unknown rev).
// When force is true the fetcher must bypass any local cache (keychain) and
// re-read from the server — used to recover when a rev number was reused with a
// new key (disable → re-enable masking lands back on rev 1 with fresh bytes).
type KeyFetcher func(rev int, force bool) (key []byte, ok bool, err error)

// Resolver holds per-revision token→path reverse maps for one repo and resolves
// path_tokens against them. It is safe for concurrent use.
type Resolver struct {
	repoRoot string
	fetch    KeyFetcher

	mu          sync.RWMutex
	maps        map[int]map[string]string // rev → (path_token → repo-relative path)
	lastRefresh map[int]time.Time         // rev → last forced refresh attempt
}

// New returns a Resolver rooted at repoRoot that lazily fetches keys via fetch.
func New(repoRoot string, fetch KeyFetcher) *Resolver {
	return &Resolver{
		repoRoot:    repoRoot,
		fetch:       fetch,
		maps:        make(map[int]map[string]string),
		lastRefresh: make(map[int]time.Time),
	}
}

// LoadRev fetches the key for rev (if not already loaded) and builds its reverse
// map. Calling it at startup for the repo's current rev warms the common path so
// the first QUERY does not pay the build cost. A rev the server doesn't have is a
// no-op returning ok=false.
func (r *Resolver) LoadRev(rev int) (ok bool, err error) {
	r.mu.RLock()
	_, loaded := r.maps[rev]
	r.mu.RUnlock()
	if loaded {
		return true, nil
	}
	_, err = r.ensure(rev, false)
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	_, loaded = r.maps[rev]
	r.mu.RUnlock()
	return loaded, nil
}

// Unmask returns the real repo-relative path for pathToken at maskKeyRev.
//
// Resolution order:
//  1. exact-rev map (the common case);
//  2. if the rev is positive and not yet loaded, fetch + build it, then retry;
//  3. if it's loaded but still misses, the cached key for this rev may be stale —
//     disabling then re-enabling masking reuses the rev number with a *new* key.
//     Force a refresh (bypassing the keychain), rebuild, and retry. Rate-limited
//     to one refresh per rev per refreshCooldown so unresolvable tokens (files
//     not checked out locally) don't trigger a refetch every time;
//  4. fall back to scanning every loaded rev — this covers GRAPH responses,
//     which carry no top-level mask_key_rev and pass rev 0.
//
// Returns ("", false) when no loaded revision maps the token (token belongs to a
// file not present in the local checkout, or the repo isn't masked).
func (r *Resolver) Unmask(pathToken string, maskKeyRev int) (string, bool) {
	if pathToken == "" {
		return "", false
	}

	if path, ok := r.lookup(maskKeyRev, pathToken); ok {
		return path, true
	}

	if maskKeyRev > 0 {
		if _, err := r.ensure(maskKeyRev, false); err == nil {
			if path, ok := r.lookup(maskKeyRev, pathToken); ok {
				return path, true
			}
		}
		if r.shouldRefresh(maskKeyRev) {
			if _, err := r.ensure(maskKeyRev, true); err == nil {
				if path, ok := r.lookup(maskKeyRev, pathToken); ok {
					return path, true
				}
			}
		}
	}

	return r.scanAll(pathToken)
}

// shouldRefresh reports whether rev is eligible for a forced key refresh (never
// refreshed, or last attempt was longer ago than refreshCooldown).
func (r *Resolver) shouldRefresh(rev int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	last, ok := r.lastRefresh[rev]
	return !ok || time.Since(last) >= refreshCooldown
}

// lookup checks a single rev's map under the read lock.
func (r *Resolver) lookup(rev int, token string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.maps[rev]
	if !ok {
		return "", false
	}
	path, ok := m[token]
	return path, ok
}

// scanAll tries every loaded rev. A token is collision-resistant across keys, so
// a hit in any map is authoritative.
func (r *Resolver) scanAll(token string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.maps {
		if path, ok := m[token]; ok {
			return path, true
		}
	}
	return "", false
}

// ensure builds and caches the reverse map for rev. When force is false it
// returns the cached map if present; when force is true it always re-fetches the
// key (bypassing any local cache) and rebuilds, replacing the cached map — used
// to recover from a rev whose key changed server-side. Concurrent callers for
// the same rev may both build; the result is identical and the last write wins.
func (r *Resolver) ensure(rev int, force bool) (map[string]string, error) {
	if !force {
		r.mu.RLock()
		if m, ok := r.maps[rev]; ok {
			r.mu.RUnlock()
			return m, nil
		}
		r.mu.RUnlock()
	} else {
		// Record the attempt up front so a response full of unresolvable tokens
		// triggers at most one forced refresh per cooldown window.
		r.mu.Lock()
		r.lastRefresh[rev] = time.Now()
		r.mu.Unlock()
	}

	key, ok, err := r.fetch(rev, force)
	if err != nil {
		return nil, fmt.Errorf("fetch masking key rev %d: %w", rev, err)
	}
	if !ok {
		return nil, nil
	}

	files, err := listTrackedFiles(r.repoRoot)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(files))
	for _, path := range files {
		token := masking.MaskPath(key, path)
		if token != "" {
			m[token] = path
		}
	}

	r.mu.Lock()
	r.maps[rev] = m
	r.mu.Unlock()
	return m, nil
}

// listTrackedFiles returns the repo-relative paths of all tracked files.
func listTrackedFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	files := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}
