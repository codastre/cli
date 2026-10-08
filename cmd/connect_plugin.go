package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// pluginInstallTimeout bounds each `claude plugin …` call. Adding a marketplace
// clones a git repository, so this is generous — but a connect must not hang on it.
const pluginInstallTimeout = 2 * time.Minute

// commandRunner runs an external command and returns its combined output. A var
// so tests can stand in for the `claude` binary.
var commandRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// claudeLookPath locates the `claude` binary. A var for the same reason.
var claudeLookPath = func() (string, error) { return exec.LookPath("claude") }

// pluginInstallArgs returns the two `claude plugin` invocations that install the
// Codastre integration at user scope (every project on this machine).
func pluginInstallArgs(src integrationsSource) [][]string {
	return [][]string{
		{"plugin", "marketplace", "add", src.Source},
		{"plugin", "install", "codastre@" + src.MarketplaceName, "--scope", "user"},
	}
}

// installClaudePlugin installs the Codastre integration for Claude Code through
// its plugin marketplace and reports whether it did. Best-effort: any failure
// (no `claude` on PATH, offline, marketplace error) degrades to printing the
// manual commands rather than failing the connect, which then writes its own entry.
func installClaudePlugin(cmd *cobra.Command, serverURL string) bool {
	src := resolveIntegrationsSource(discoverWithTimeout(cmd, serverURL))
	out := cmd.OutOrStdout()

	bin, err := claudeLookPath()
	if err != nil {
		fmt.Fprintf(out, "\n`claude` not found on PATH — skipped installing the Codastre plugin.\n")
		printIntegrationCommands(cmd, src)
		return false
	}

	fmt.Fprintf(out, "\nInstalling the Codastre plugin for Claude Code (from %s)…\n", src.Label)
	steps := pluginInstallArgs(src)
	for i, args := range steps {
		ctx, cancel := context.WithTimeout(cmdContext(cmd), pluginInstallTimeout)
		output, err := commandRunner(ctx, bin, args...)
		cancel()
		// A marketplace that is already registered makes `marketplace add` fail on
		// some Claude Code versions; the install step is the one that decides.
		if err != nil && i < len(steps)-1 {
			continue
		}
		if err != nil {
			fmt.Fprintf(out, "warning: `claude %s` failed: %v\n", strings.Join(args, " "), err)
			if msg := strings.TrimSpace(string(output)); msg != "" {
				fmt.Fprintf(out, "  %s\n", strings.ReplaceAll(msg, "\n", "\n  "))
			}
			printIntegrationCommands(cmd, src)
			return false
		}
	}
	fmt.Fprintf(out, "Installed plugin codastre@%s (user scope). Restart Claude Code to load it.\n"+
		"Skip this next time with --no-plugin.\n", src.MarketplaceName)
	return true
}

// pluginEntrySuffices reports whether the plugin's MCP entry — a bare `codastre
// serve`, which resolves its server from $CODASTRE_SERVER or the config `login`
// wrote — is equivalent to the entry connect was asked for. A different --server
// or baked snippet flags are things only connect's own entry can carry.
func pluginEntrySuffices(serverURL string) bool {
	return serverURL == strings.TrimRight(defaultServerURL(), "/") &&
		connectMaxSnippetLines == 0 && !connectNoSnippets
}

// useClaudePluginEntry leaves the MCP server to the plugin: it removes any entry
// a previous connect wrote under name (in scope, plus the local file for user
// scope, mirroring connectClaude) so Claude Code doesn't list Codastre twice.
func useClaudePluginEntry(cmd *cobra.Command, name, scope, path string, stdio bool) error {
	out := cmd.OutOrStdout()
	paths := []string{path}
	if scope == "user" {
		if localPath, err := claudePathForScope("local"); err == nil {
			paths = append(paths, localPath)
		}
	}
	for _, p := range paths {
		removed, err := removeJSONEntry(p, "mcpServers", name)
		if err != nil {
			return err
		}
		if removed {
			fmt.Fprintf(out, "Removed MCP server %q from %s — the plugin provides it now.\n", name, p)
		}
	}
	fmt.Fprintln(out, "The plugin registers its own `codastre serve` MCP server; no separate entry written.")
	if !stdio {
		fmt.Fprintln(out, "Note: the plugin always uses the local proxy, not direct HTTP. Use --no-plugin for an HTTP entry.")
	}
	printModeHint(cmd, true)
	return nil
}

// printIntegrationCommands prints the manual install commands for src.
func printIntegrationCommands(cmd *cobra.Command, src integrationsSource) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Install it yourself with:\n\n")
	for _, args := range pluginInstallArgs(src) {
		fmt.Fprintf(out, "  claude %s\n", strings.Join(args, " "))
	}
}

// cmdContext returns the command's context, or Background when there is none:
// cobra only populates it during Execute, so a command constructed directly
// (tests, or any non-Execute caller) carries nil, and context.WithTimeout panics on that.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// discoverWithTimeout fetches server discovery with a short deadline; nil on failure.
func discoverWithTimeout(cmd *cobra.Command, serverURL string) *serverDiscovery {
	ctx, cancel := context.WithTimeout(cmdContext(cmd), 3*time.Second)
	defer cancel()
	return discover(ctx, serverURL)
}
