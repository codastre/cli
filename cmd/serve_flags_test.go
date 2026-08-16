package cmd

import (
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

// The env vars exist for the case the flags can't reach: a plugin that ships a
// fixed `.mcp.json` running bare `codastre serve`, vendored from upstream and
// not ours to edit. Without them that setup has no operator-level lever at all.
func TestSnippetEnvDefaults(t *testing.T) {
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
