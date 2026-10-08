// Package keychain wraps the OS keychain (via 99designs/keyring) with a
// file-based fallback at ~/.config/codastre/keys (mode 0600).
// Keys are stored under "codastre/<server-host>/<repo_id>/<mask_key_rev>" (impl-spec §2.5).
package keychain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/99designs/keyring"
)

const serviceName = "codastre"

// osKeychainBackends are the persistent OS-native secret stores we treat as
// "secure". We list them explicitly rather than letting keyring auto-select,
// because with no AllowedBackends the library silently falls through to the
// file backend — but without a FileDir, so Open() succeeds and the first Set()
// fails with "No directory provided for file keyring". Restricting to these
// makes keyring.Open error when none is available, so we take the explicit file
// fallback below (which sets FileDir and reports isFallback=true).
var osKeychainBackends = []keyring.BackendType{
	keyring.KeychainBackend,      // macOS
	keyring.SecretServiceBackend, // Linux (GNOME/libsecret via D-Bus)
	keyring.KWalletBackend,       // Linux (KDE)
	keyring.WinCredBackend,       // Windows
}

// Store wraps the chosen keyring backend.
type Store struct {
	ring              keyring.Keyring
	leftoverFileStore keyring.Keyring
	isFallback        bool
}

// Open returns a Store backed by the OS keychain, or by a file fallback if the
// keychain is unavailable. isFallback=true when the file backend is active.
func Open() (*Store, bool, error) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName:                    serviceName,
		AllowedBackends:                osKeychainBackends,
		KeychainTrustApplication:       true,
		KeychainAccessibleWhenUnlocked: true,
	})
	if err == nil {
		return &Store{ring: ring, leftoverFileStore: openLeftoverFileStore()}, false, nil
	}

	// Fall back to file storage.
	dir := fallbackDir()
	if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
		return nil, false, fmt.Errorf("keychain unavailable (%v); file fallback failed: %w", err, mkErr)
	}
	fring, ferr := openFileRing(dir)
	if ferr != nil {
		return nil, false, fmt.Errorf("keychain unavailable (%v); file fallback failed: %w", err, ferr)
	}
	return &Store{ring: fring, isFallback: true}, true, nil
}

func openFileRing(dir string) (keyring.Keyring, error) {
	return keyring.Open(keyring.Config{
		AllowedBackends: []keyring.BackendType{keyring.FileBackend},
		FileDir:         dir,
		FilePasswordFunc: func(string) (string, error) {
			return "", nil // unencrypted; security derives from 0700 dir
		},
	})
}

func openLeftoverFileStore() keyring.Keyring {
	dir := fallbackDir()
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	ring, err := openFileRing(dir)
	if err != nil {
		return nil
	}
	return ring
}

func (s *Store) get(id string) (keyring.Item, error) {
	item, err := s.ring.Get(id)
	if isNotFound(err) && s.leftoverFileStore != nil {
		return s.moveLeftoverIntoKeychain(id)
	}
	return item, err
}

func (s *Store) moveLeftoverIntoKeychain(id string) (keyring.Item, error) {
	item, err := s.leftoverFileStore.Get(id)
	if err != nil {
		return keyring.Item{}, keyring.ErrKeyNotFound
	}
	if s.ring.Set(item) == nil {
		_ = s.leftoverFileStore.Remove(id)
	}
	return item, nil
}

// IsFallback reports whether the file backend is in use instead of the OS keychain.
func (s *Store) IsFallback() bool { return s.isFallback }

// GetAPIKey retrieves the API key stored for a server host.
func (s *Store) GetAPIKey(serverHost string) (string, error) {
	item, err := s.get(serverHost)
	if err != nil {
		return "", err
	}
	return string(item.Data), nil
}

// SetAPIKey stores the API key for a server host.
// Deletes any pre-existing entry first so the item is always created fresh
// rather than updated; the update path in the keyring library skips re-applying
// accessibility and trust flags, so recreation is the only way to guarantee
// they take effect.
func (s *Store) SetAPIKey(serverHost, apiKey string) error {
	if err := s.ring.Remove(serverHost); err != nil && err != keyring.ErrKeyNotFound {
		_ = err // best-effort; proceed and let Set report any failure
	}
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
	if s.leftoverFileStore != nil {
		if err := removeIfPresent(s.leftoverFileStore, serverHost); err != nil {
			return err
		}
	}
	return removeIfPresent(s.ring, serverHost)
}

func removeIfPresent(ring keyring.Keyring, id string) error {
	if err := ring.Remove(id); err != nil && !isNotFound(err) {
		return err
	}
	return nil
}

// The file backend's Remove returns fs.ErrNotExist instead of keyring.ErrKeyNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, keyring.ErrKeyNotFound) || errors.Is(err, fs.ErrNotExist)
}

// GetMaskKey retrieves the repo masking key for a given revision.
// Returns the raw key bytes (decoded from hex storage).
func (s *Store) GetMaskKey(serverHost, repoID string, rev int) ([]byte, error) {
	item, err := s.get(maskKeyID(serverHost, repoID, rev))
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
