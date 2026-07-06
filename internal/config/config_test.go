package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetAndGetServerURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, ok := ServerURL(); ok {
		t.Fatal("expected empty config to have no server URL")
	}

	if err := SetServerURL("https://codastre.internal"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := ServerURL()
	if !ok || got != "https://codastre.internal" {
		t.Fatalf("ServerURL = (%q, %v), want (https://codastre.internal, true)", got, ok)
	}

	// Overwrite with a new server.
	if err := SetServerURL("https://other.internal"); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	if got, _ := ServerURL(); got != "https://other.internal" {
		t.Fatalf("after overwrite ServerURL = %q, want https://other.internal", got)
	}

	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "codastre", "config.json")); err != nil {
		t.Fatalf("config file not written: %v", err)
	}
}

func TestSetServerURLIgnoresEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetServerURL(""); err != nil {
		t.Fatal(err)
	}
	if p := file(); p != "" {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("empty input should not create the config file")
		}
	}
}

func TestLoadTolerantOfGarbage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "codastre")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ServerURL(); ok {
		t.Fatal("garbage config should yield no server URL, not a value")
	}
}
