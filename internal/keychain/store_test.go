package keychain

import (
	"testing"

	"github.com/99designs/keyring"
)

// TestPrimaryOpenErrorsWithoutOSKeychain proves the core fix: when none of the
// configured OS secret stores is available (the Qonto tooling host), the
// primary keyring.Open returns an error, so Open() falls through to the file
// backend instead of silently returning a dir-less file keyring. We can't
// uninstall the macOS Keychain, so we assert the mechanism directly: a config
// limited to Linux-only backends is unavailable here and must error.
func TestPrimaryOpenErrorsWithoutOSKeychain(t *testing.T) {
	_, err := keyring.Open(keyring.Config{
		ServiceName:     serviceName,
		AllowedBackends: []keyring.BackendType{keyring.SecretServiceBackend, keyring.KWalletBackend},
	})
	if err == nil {
		t.Fatal("expected keyring.Open to error when no allowed backend is available")
	}
	t.Logf("primary Open error (triggers fallback): %v", err)
}

// TestFileFallbackRoundTrips proves the fallback branch works: a file backend
// with FileDir set stores and retrieves the key. Before the fix this branch was
// dead code because the primary Open never errored.
func TestFileFallbackRoundTrips(t *testing.T) {
	dir := t.TempDir()
	ring, err := keyring.Open(keyring.Config{
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		FileDir:          dir,
		FilePasswordFunc: func(string) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatalf("open file keyring: %v", err)
	}
	s := &Store{ring: ring, isFallback: true}
	if err := s.SetAPIKey("codastre-api.tooling-production.qonto.co", "sk-repro"); err != nil {
		t.Fatalf("SetAPIKey error: %v", err)
	}
	got, err := s.GetAPIKey("codastre-api.tooling-production.qonto.co")
	if err != nil {
		t.Fatalf("GetAPIKey error: %v", err)
	}
	if got != "sk-repro" {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

// TestOSKeychainRoundTrip confirms the primary path still uses the real OS
// keychain on a machine that has one (local macOS dev), unaffected by the
// AllowedBackends restriction.
func TestOSKeychainRoundTrip(t *testing.T) {
	store, isFallback, err := Open()
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	if isFallback {
		t.Skip("no OS keychain on this host; covered by fallback tests")
	}
	const host = "codastre-keychain-selftest.example.com"
	if err := store.SetAPIKey(host, "sk-selftest"); err != nil {
		t.Fatalf("SetAPIKey error: %v", err)
	}
	t.Cleanup(func() { _ = store.DeleteAPIKey(host) })
	got, err := store.GetAPIKey(host)
	if err != nil {
		t.Fatalf("GetAPIKey error: %v", err)
	}
	if got != "sk-selftest" {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}
