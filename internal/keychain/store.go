// Package keychain wraps the OS keychain (via 99designs/keyring) with a
// file-based fallback at ~/.config/codastre/keys (mode 0600).
// Keys are stored under "codastre/<server-host>/<repo_id>/<mask_key_rev>" (impl-spec §2.5).
package keychain

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/99designs/keyring"
)

const serviceName = "codastre"

// Store wraps the chosen keyring backend.
type Store struct {
	ring       keyring.Keyring
	isFallback bool
}

// Open returns a Store backed by the OS keychain, or by a file fallback if the
// keychain is unavailable. isFallback=true when the file backend is active.
func Open() (*Store, bool, error) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName:              serviceName,
		KeychainTrustApplication: true,
	})
	if err == nil {
		return &Store{ring: ring}, false, nil
	}

	// Fall back to file storage.
	dir := fallbackDir()
	if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
		return nil, false, fmt.Errorf("keychain unavailable (%v); file fallback failed: %w", err, mkErr)
	}
	fring, ferr := keyring.Open(keyring.Config{
		AllowedBackends: []keyring.BackendType{keyring.FileBackend},
		FileDir:         dir,
		FilePasswordFunc: func(string) (string, error) {
			return "", nil // unencrypted; security derives from 0700 dir
		},
	})
	if ferr != nil {
		return nil, false, fmt.Errorf("keychain unavailable (%v); file fallback failed: %w", err, ferr)
	}
	return &Store{ring: fring, isFallback: true}, true, nil
}

// IsFallback reports whether the file backend is in use instead of the OS keychain.
func (s *Store) IsFallback() bool { return s.isFallback }

// GetAPIKey retrieves the API key stored for a server host.
func (s *Store) GetAPIKey(serverHost string) (string, error) {
	item, err := s.ring.Get(serverHost)
	if err != nil {
		return "", err
	}
	return string(item.Data), nil
}

// SetAPIKey stores the API key for a server host.
func (s *Store) SetAPIKey(serverHost, apiKey string) error {
	return s.ring.Set(keyring.Item{
		Key:         serverHost,
		Data:        []byte(apiKey),
		Label:       fmt.Sprintf("codastre API key (%s)", serverHost),
		Description: "codastre API key",
	})
}

// DeleteAPIKey removes the stored API key for a server host.
// Returns nil if no key was stored (idempotent logout).
func (s *Store) DeleteAPIKey(serverHost string) error {
	err := s.ring.Remove(serverHost)
	if err == keyring.ErrKeyNotFound {
		return nil
	}
	return err
}

// GetMaskKey retrieves the repo masking key for a given revision.
// Returns the raw key bytes (decoded from hex storage).
func (s *Store) GetMaskKey(serverHost, repoID string, rev int) ([]byte, error) {
	item, err := s.ring.Get(maskKeyID(serverHost, repoID, rev))
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(string(item.Data))
}

// SetMaskKey stores a repo masking key.
func (s *Store) SetMaskKey(serverHost, repoID string, rev int, key []byte) error {
	id := maskKeyID(serverHost, repoID, rev)
	return s.ring.Set(keyring.Item{
		Key:         id,
		Data:        []byte(hex.EncodeToString(key)),
		Label:       fmt.Sprintf("codastre mask key (%s)", id),
		Description: "codastre masking key",
	})
}

func maskKeyID(serverHost, repoID string, rev int) string {
	return fmt.Sprintf("%s/%s/%d", serverHost, repoID, rev)
}

func fallbackDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "codastre", "keys")
}
