package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A generated stdio config with no hydration flags must stay byte-identical to
// what earlier versions wrote, so upgrading the CLI never silently pins a budget
// a future release might tune.
func TestServeArgs_OmitsHydrationFlagsByDefault(t *testing.T) {
	got := strings.Join(serveArgs("http://srv"), " ")
	if got != "serve --server http://srv" {
		t.Errorf("serveArgs = %q, want the bare serve invocation", got)
	}
}

// `codastre connect --stdio --max-snippet-lines N` / `--no-snippets` bake the
// budget into the config the agent client launches, which is the only lever
// available when the client owns the process (MCP stdio args are fixed at
// config time).
func TestServeArgs_BakesHydrationFlags(t *testing.T) {
	t.Cleanup(func() { connectMaxSnippetLines, connectNoSnippets = 0, false })
	connectMaxSnippetLines = 30
	connectNoSnippets = true

	got := strings.Join(serveArgs("http://srv"), " ")
	for _, want := range []string{"--no-snippets", "--max-snippet-lines 30"} {
		if !strings.Contains(got, want) {
			t.Errorf("serveArgs = %q, missing %q", got, want)
		}
	}
}

// Codex takes TOML, so the args have to be quoted individually rather than
// interpolated as one string.
func TestCodexStdioSection_QuotesEachArg(t *testing.T) {
	t.Cleanup(func() { connectMaxSnippetLines = 0 })
	connectMaxSnippetLines = 30

	got := codexStdioSection("codastre", "http://srv")
	if !strings.Contains(got, `args = ["serve", "--server", "http://srv", "--max-snippet-lines", "30"]`) {
		t.Errorf("codexStdioSection args line wrong:\n%s", got)
	}
}

func TestSnippetsTopEnvDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no config file: env or built-in only
	for _, tc := range []struct {
		env  string
		want int
	}{
		{"", 0}, {"5", 5}, {" 1 ", 1}, {"all", -1}, {"ALL", -1}, {"-1", -1}, {"three", 0},
	} {
		t.Setenv("CODASTRE_SNIPPETS_TOP", tc.env)
		if got := defaultSnippetsTop(); got != tc.want {
			t.Errorf("CODASTRE_SNIPPETS_TOP=%q → %d, want %d", tc.env, got, tc.want)
		}
	}
}

// Unset env falls back to the shared config file; a set env var beats it.
func TestSnippetDefaultsFallBackToConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "codastre")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"hydration": {"snippets_top": 5, "max_snippet_lines": 40}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODASTRE_SNIPPETS_TOP", "")
	t.Setenv("CODASTRE_MAX_SNIPPET_LINES", "")
	if got := defaultSnippetsTop(); got != 5 {
		t.Errorf("snippets_top from file = %d, want 5", got)
	}
	if got := defaultMaxSnippetLines(); got != 40 {
		t.Errorf("max_snippet_lines from file = %d, want 40", got)
	}
	t.Setenv("CODASTRE_SNIPPETS_TOP", "all")
	t.Setenv("CODASTRE_MAX_SNIPPET_LINES", "12")
	if defaultSnippetsTop() != -1 || defaultMaxSnippetLines() != 12 {
		t.Error("env var must beat the config file")
	}
}

// The env vars exist for the case the flags can't reach: a plugin that ships a
// fixed `.mcp.json` running bare `codastre serve`, vendored from upstream and
// not ours to edit. Without them that setup has no operator-level lever at all.
func TestSnippetEnvDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := defaultMaxSnippetLines(); got != 0 {
		t.Errorf("unset CODASTRE_MAX_SNIPPET_LINES → %d, want 0 (built-in default)", got)
	}
	if defaultNoSnippets() {
		t.Error("unset CODASTRE_NO_SNIPPETS → true, want false")
	}

	t.Setenv("CODASTRE_MAX_SNIPPET_LINES", " 25 ")
	if got := defaultMaxSnippetLines(); got != 25 {
		t.Errorf("CODASTRE_MAX_SNIPPET_LINES=25 → %d, want 25", got)
	}
	// A typo must not silently mean "no snippets"; it falls back to the default.
	t.Setenv("CODASTRE_MAX_SNIPPET_LINES", "eighty")
	if got := defaultMaxSnippetLines(); got != 0 {
		t.Errorf("unparseable CODASTRE_MAX_SNIPPET_LINES → %d, want 0", got)
	}

	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("CODASTRE_NO_SNIPPETS", v)
		if !defaultNoSnippets() {
			t.Errorf("CODASTRE_NO_SNIPPETS=%q → false, want true", v)
		}
	}
	for _, v := range []string{"0", "false", "no", ""} {
		t.Setenv("CODASTRE_NO_SNIPPETS", v)
		if defaultNoSnippets() {
			t.Errorf("CODASTRE_NO_SNIPPETS=%q → true, want false", v)
		}
	}
}
