// Package config persists CLI-wide settings at ~/.config/codastre/config.json
// (the same config root as the keychain fallback and the checkouts registry).
//
// Its first job is remembering the server URL for a self-hosted deployment, so
// operators configure it once — `codastre login --server https://…` records it —
// instead of passing --server to every subsequent command. Resolution precedence
// lives in cmd.defaultServerURL: $CODASTRE_SERVER, then this file, then the
// hosted default.
//
// It also holds machine-wide defaults that more than one client reads — the
// snippet-hydration budget today — with the same precedence: flag, then env,
// then this file, then the built-in. Other tools (the companion app) may add
// their own keys, so writes preserve keys this package does not know.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

var mu sync.Mutex

// settings is the on-disk shape. Fields are omitempty so the file only carries
// what has actually been set, leaving room to grow without churn.
type settings struct {
	ServerURL string    `json:"server_url,omitempty"`
	Hydration Hydration `json:"hydration"`
}

// Hydration is the snippet budget for `codastre query --snippets` and
// `codastre serve`. Zero means "not set here" (fall through to the built-in);
// SnippetsTop < 0 hydrates every hit.
type Hydration struct {
	SnippetsTop     int `json:"snippets_top,omitempty"`
	MaxSnippetLines int `json:"max_snippet_lines,omitempty"`
}

// HydrationDefaults returns the persisted hydration budget (zero fields unset).
func HydrationDefaults() Hydration {
	return load().Hydration
}

// file returns the config path, or "" when the home dir can't be resolved.
func file() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "codastre", "config.json")
}

// ServerURL returns the persisted default server URL, or ("", false) when unset.
func ServerURL() (string, bool) {
	s := load().ServerURL
	if s == "" {
		return "", false
	}
	return s, true
}

// SetServerURL persists url as the default server. Best-effort: a write failure
// is returned but callers may ignore it. It is a no-op (no write) when unchanged
// or when url is empty.
func SetServerURL(url string) error {
	if url == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	s := load()
	if s.ServerURL == url {
		return nil
	}
	return saveKey("server_url", url)
}

// load reads the config, returning a zero settings on any error (missing file,
// malformed JSON) so callers always get a usable value.
func load() settings {
	var s settings
	p := file()
	if p == "" {
		return s
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

// saveKey sets one top-level key and writes the config atomically (temp file +
// rename) with private perms. It rewrites the file as a raw map, not as
// settings, so keys written by other tools survive a CLI write.
func saveKey(key string, value any) error {
	p := file()
	if p == "" {
		return nil
	}
	raw := map[string]json.RawMessage{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &raw)
	}
	v, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw[key] = v
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
