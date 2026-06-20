package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// codastreBinary returns an absolute path to the running codastre binary so the
// stdio MCP config keeps working regardless of the launching client's PATH —
// GUI agents (Claude Desktop, VS Code) frequently don't inherit the interactive
// shell PATH. Falls back to the bare name "codastre" when the path can't be
// resolved (e.g. the executable was moved).
func codastreBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "codastre"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// claudeEntry builds the mcpServers entry for the claude config (the same JSON
// shape Claude Code expects). In stdio mode the agent launches `codastre serve`
// (which unmasks paths, hydrates snippets, and auto-syncs); in HTTP mode it
// talks directly to the server's /mcp endpoint with a bearer header.
func claudeEntry(mcpURL, serverURL, apiKey string, stdio bool) map[string]any {
	if stdio {
		return map[string]any{
			"type":    "stdio",
			"command": codastreBinary(),
			"args":    []string{"serve", "--server", serverURL},
		}
	}
	return map[string]any{
		"type": "http",
		"url":  mcpURL,
		"headers": map[string]string{
			"Authorization": "Bearer " + apiKey,
		},
	}
}

// opencodeEntry builds the opencode mcp entry: a "local" command entry in stdio
// mode (proxied through `codastre serve`), or a "remote" url entry otherwise.
func opencodeEntry(mcpURL, serverURL, apiKey string, stdio bool) map[string]any {
	if stdio {
		return map[string]any{
			"type":    "local",
			"command": []string{codastreBinary(), "serve", "--server", serverURL},
			"enabled": true,
		}
	}
	return map[string]any{
		"type": "remote",
		"url":  mcpURL,
		"headers": map[string]string{
			"Authorization": "Bearer " + apiKey,
		},
	}
}

// codexStdioSection renders the TOML [mcp_servers.<name>] block that launches
// `codastre serve`. No bearer token is written — serve reads the API key from
// the OS keychain at runtime.
func codexStdioSection(name, serverURL string) string {
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = %q\nargs = [\"serve\", \"--server\", %q]\n",
		name, codastreBinary(), serverURL)
}

// printModeHint writes a one-line reminder about what the chosen mode does, so
// developers understand why a masked repo needs the stdio proxy.
func printModeHint(cmd *cobra.Command, stdio bool) {
	if stdio {
		fmt.Fprintln(cmd.OutOrStdout(),
			"\nLocal proxy: launch your agent from inside the repository so `codastre serve`\n"+
				"can unmask paths, hydrate snippets, and auto-sync your branch.")
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(),
		"\nDirect HTTP. If this repo uses path masking (masking_scheme=hmac), re-run with\n"+
			"--stdio so the agent can read code — the HTTP path returns masked path tokens.")
}
