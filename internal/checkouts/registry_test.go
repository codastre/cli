package checkouts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRememberAndLookup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, ok := Lookup("github.com/acme/api"); ok {
		t.Fatal("expected empty registry to miss")
	}

	if err := Remember("github.com/acme/api", "/ws/api"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	dir, ok := Lookup("github.com/acme/api")
	if !ok || dir != "/ws/api" {
		t.Fatalf("lookup = (%q, %v), want (/ws/api, true)", dir, ok)
	}

	// Overwrite with a new location.
	if err := Remember("github.com/acme/api", "/ws/api2"); err != nil {
		t.Fatalf("re-remember: %v", err)
	}
	if dir, _ := Lookup("github.com/acme/api"); dir != "/ws/api2" {
		t.Fatalf("after overwrite lookup = %q, want /ws/api2", dir)
	}

	// Entry persisted across a fresh read.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "codastre", "checkouts.json")); err != nil {
		t.Fatalf("registry file not written: %v", err)
	}
}

func TestRememberIgnoresEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Remember("", "/ws/api"); err != nil {
		t.Fatal(err)
	}
	if err := Remember("github.com/acme/api", ""); err != nil {
		t.Fatal(err)
	}
	if p := file(); p != "" {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("empty inputs should not create the registry file")
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
	if err := os.WriteFile(filepath.Join(dir, "checkouts.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A malformed file must not panic or block a fresh Remember.
	if _, ok := Lookup("x"); ok {
		t.Fatal("garbage file should yield empty registry")
	}
	if err := Remember("github.com/acme/api", "/ws/api"); err != nil {
		t.Fatalf("remember over garbage: %v", err)
	}
	if dir, ok := Lookup("github.com/acme/api"); !ok || dir != "/ws/api" {
		t.Fatalf("lookup after recovery = (%q, %v)", dir, ok)
	}
}
