package cmd

import (
	"os"
	"strconv"
	"strings"

	"github.com/codastre/cli/internal/config"
)

// defaultServerURL resolves the default server URL in precedence order: the
// CODASTRE_SERVER env var, then the persisted CLI config (recorded by
// `codastre login --server`), then the hosted API. This lets a self-hosted
// deployment be configured once instead of passing --server to every command.
func defaultServerURL() string {
	if v := os.Getenv("CODASTRE_SERVER"); v != "" {
		return v
	}
	if v, ok := config.ServerURL(); ok {
		return v
	}
	return "https://api.codastre.com"
}

// Snippet-hydration defaults for `serve`, resolved from the environment so they
// can be set WITHOUT editing the MCP config's args. That is the case that
// matters: an agent plugin ships a fixed `.mcp.json` running bare
// `codastre serve` — often a vendored copy nobody downstream is supposed to edit
// — so an env var in the client's config is the only lever an operator has.
// Per-call control is separate; see mcpshim/overrides.go.

// defaultMaxSnippetLines reads $CODASTRE_MAX_SNIPPET_LINES. 0 (or an
// unparseable value) means "use the proxy's built-in budget".
func defaultMaxSnippetLines() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CODASTRE_MAX_SNIPPET_LINES")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// defaultNoSnippets reads $CODASTRE_NO_SNIPPETS. Accepts the usual truthy
// spellings; anything else (including unset) is false.
func defaultNoSnippets() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODASTRE_NO_SNIPPETS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
