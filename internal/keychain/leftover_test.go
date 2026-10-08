package keychain

import (
	"testing"

	"github.com/99designs/keyring"
)

func newUpgradedStore(t *testing.T) (*Store, keyring.Keyring, keyring.Keyring) {
	t.Helper()
	keychain, err := openFileRing(t.TempDir())
	if err != nil {
		t.Fatalf("open stand-in keychain: %v", err)
	}
	leftover, err := openFileRing(t.TempDir())
	if err != nil {
		t.Fatalf("open leftover file store: %v", err)
	}
	return &Store{ring: keychain, leftoverFileStore: leftover}, keychain, leftover
}

func TestLeftoverAPIKeyMovesIntoKeychain(t *testing.T) {
	s, keychain, leftover := newUpgradedStore(t)
	if err := (&Store{ring: leftover}).SetAPIKey("codastre-api.domain.com", "sk-leftover"); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}

	got, err := s.GetAPIKey("codastre-api.domain.com")
	if err != nil || got != "sk-leftover" {
		t.Fatalf("GetAPIKey = %q, %v; want sk-leftover", got, err)
	}
	if _, err := keychain.Get("codastre-api.domain.com"); err != nil {
		t.Fatalf("key not moved into keychain: %v", err)
	}
	if _, err := leftover.Get("codastre-api.domain.com"); err != keyring.ErrKeyNotFound {
		t.Fatalf("key still in leftover file store: %v", err)
	}
}

func TestLeftoverMaskKeyMovesIntoKeychain(t *testing.T) {
	s, keychain, leftover := newUpgradedStore(t)
	if err := (&Store{ring: leftover}).SetMaskKey("host", "repo-1", 2, []byte{0xde, 0xad}); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}

	got, err := s.GetMaskKey("host", "repo-1", 2)
	if err != nil || string(got) != "\xde\xad" {
		t.Fatalf("GetMaskKey = %x, %v", got, err)
	}
	if _, err := keychain.Get(maskKeyID("host", "repo-1", 2)); err != nil {
		t.Fatalf("mask key not moved into keychain: %v", err)
	}
}

func TestKeychainKeyWinsOverLeftover(t *testing.T) {
	s, _, leftover := newUpgradedStore(t)
	if err := (&Store{ring: leftover}).SetAPIKey("host", "sk-stale"); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}
	if err := s.SetAPIKey("host", "sk-fresh"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}

	if got, err := s.GetAPIKey("host"); err != nil || got != "sk-fresh" {
		t.Fatalf("GetAPIKey = %q, %v; want sk-fresh", got, err)
	}
}

func TestLogoutClearsLeftover(t *testing.T) {
	s, _, leftover := newUpgradedStore(t)
	if err := (&Store{ring: leftover}).SetAPIKey("host", "sk-leftover"); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}

	if err := s.DeleteAPIKey("host"); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	if _, err := s.GetAPIKey("host"); err != keyring.ErrKeyNotFound {
		t.Fatalf("GetAPIKey after logout = %v; want ErrKeyNotFound", err)
	}
	if err := s.DeleteAPIKey("host"); err != nil {
		t.Fatalf("second DeleteAPIKey not idempotent: %v", err)
	}
}

func TestMissingKeyStaysNotFound(t *testing.T) {
	s, _, _ := newUpgradedStore(t)
	if _, err := s.GetAPIKey("nowhere"); err != keyring.ErrKeyNotFound {
		t.Fatalf("GetAPIKey = %v; want ErrKeyNotFound", err)
	}
}
