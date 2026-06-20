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

	"github.com/codastre/cli/internal/masking"
)

// KeyFetcher returns the raw masking key for a given revision. ok is false when
// the server has no such revision (e.g. masking disabled, or an unknown rev).
type KeyFetcher func(rev int) (key []byte, ok bool, err error)

// Resolver holds per-revision token→path reverse maps for one repo and resolves
// path_tokens against them. It is safe for concurrent use.
type Resolver struct {
	repoRoot string
	fetch    KeyFetcher

	mu   sync.RWMutex
	maps map[int]map[string]string // rev → (path_token → repo-relative path)
}

// New returns a Resolver rooted at repoRoot that lazily fetches keys via fetch.
func New(repoRoot string, fetch KeyFetcher) *Resolver {
	return &Resolver{
		repoRoot: repoRoot,
		fetch:    fetch,
		maps:     make(map[int]map[string]string),
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
	_, err = r.ensure(rev)
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
//  3. fall back to scanning every loaded rev — this covers GRAPH responses,
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
		if _, err := r.ensure(maskKeyRev); err == nil {
			if path, ok := r.lookup(maskKeyRev, pathToken); ok {
				return path, true
			}
		}
	}

	return r.scanAll(pathToken)
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

// ensure builds and caches the reverse map for rev if absent. Concurrent callers
// for the same rev may both build; the result is identical and the last write
// wins, so no double-checked locking is needed for correctness.
func (r *Resolver) ensure(rev int) (map[string]string, error) {
	r.mu.RLock()
	if m, ok := r.maps[rev]; ok {
		r.mu.RUnlock()
		return m, nil
	}
	r.mu.RUnlock()

	key, ok, err := r.fetch(rev)
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
