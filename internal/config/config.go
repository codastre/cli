// Package config persists CLI-wide settings at ~/.config/codastre/config.json
// (the same config root as the keychain fallback and the checkouts registry).
//
// Its first job is remembering the server URL for a self-hosted deployment, so
// operators configure it once — `codastre login --server https://…` records it —
// instead of passing --server to every subsequent command. Resolution precedence
// lives in cmd.defaultServerURL: $CODASTRE_SERVER, then this file, then the
// hosted default.
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
	ServerURL string `json:"server_url,omitempty"`
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
	s.ServerURL = url
	return save(s)
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

// save writes the config atomically (temp file + rename) with private perms.
func save(s settings) error {
	p := file()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
