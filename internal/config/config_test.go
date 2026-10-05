package config

import (
	"os"
	"path/filepath"
	"strings"
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

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "codastre")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHydrationDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := HydrationDefaults(); got != (Hydration{}) {
		t.Fatalf("no config → %+v, want zero", got)
	}
	writeConfig(t, `{"hydration": {"snippets_top": -1, "max_snippet_lines": 40}}`)
	if got := HydrationDefaults(); got != (Hydration{SnippetsTop: -1, MaxSnippetLines: 40}) {
		t.Fatalf("HydrationDefaults = %+v, want {-1 40}", got)
	}
}

// The file is shared with other Codastre clients (the companion app); a CLI
// write must not drop keys it does not model.
func TestSetServerURLPreservesUnknownKeys(t *testing.T) {
	p := writeConfig(t, `{"hydration": {"snippets_top": 5}, "companion": {"theme": "dark"}}`)
	if err := SetServerURL("https://srv"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{`"companion"`, `"theme": "dark"`, `"snippets_top": 5`, `"server_url": "https://srv"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config lost %s after SetServerURL:\n%s", want, b)
		}
	}
}
