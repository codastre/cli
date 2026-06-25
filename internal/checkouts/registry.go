// Package checkouts persists a map from a normalized repo URL to the absolute
// path of a local clone, so `codastre query` / `codastre graph` can unmask a
// repo's path_tokens even when run from a different directory.
//
// Unmasking inverts per-component HMAC by enumerating a working tree (see
// internal/unmask); the masking key alone is not enough. The registry remembers
// where each repo is checked out — learned from in-repo runs — so a later
// cross-directory run can find the real paths to hash against. The file lives at
// ~/.config/codastre/checkouts.json (same config root as the keychain fallback).
package checkouts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

var mu sync.Mutex

// file returns the registry path, or "" when the home dir can't be resolved.
func file() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "checkouts.json")
}

// Lookup returns the remembered local checkout dir for a normalized repo URL.
func Lookup(repoURL string) (string, bool) {
	dir, ok := load()[repoURL]
	return dir, ok
}

// Remember records dir as a local checkout of repoURL and persists the registry.
// It is a no-op (no write) when the mapping is already current. Best-effort: a
// write failure is returned but callers may ignore it.
func Remember(repoURL, dir string) error {
	if repoURL == "" || dir == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	m := load()
	if m[repoURL] == dir {
		return nil
	}
	m[repoURL] = dir
	return save(m)
}

// load reads the registry, returning an empty map on any error (missing file,
// malformed JSON) so callers always get a usable map.
func load() map[string]string {
	p := file()
	if p == "" {
		return map[string]string{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]string{}
	}
	return m
}

// save writes the registry atomically (temp file + rename) with private perms.
func save(m map[string]string) error {
	p := file()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
