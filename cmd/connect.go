package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codastre/cli/internal/keychain"
	"github.com/spf13/cobra"
)

var connectCmd = &cobra.Command{
	Use:   "connect <target>",
	Short: "Register codastre as an MCP server in claude, codex, or opencode",
	Long: `Writes the MCP server config for the given AI coding tool.

  claude    — ~/.claude.json (user), ~/.claude/mcp_settings.json (local),
              or .mcp.json in CWD (project); controlled by --scope
  codex     — ~/.codex/config.toml
  opencode  — ~/.config/opencode/opencode.json

Two connection modes:

  (default)  Direct HTTP — the agent talks straight to the server's /mcp
             endpoint. Simplest, but returns MASKED path tokens for repos with
             masking_scheme=hmac and does no local auto-sync.

  --stdio    Local proxy — the agent launches 'codastre serve', which unmasks
             paths, hydrates code snippets from local disk, and auto-syncs your
             branch. REQUIRED for hmac-masked repos. Launch your agent from
             inside the repository (serve resolves it from the local git root).

The API key is read from the OS keychain; run 'codastre login' first.`,
	Args: cobra.ExactArgs(1),
	RunE: runConnect,
}

var connectServerURL string
var connectName string
var connectScope string
var connectStdio bool

func init() {
	connectCmd.Flags().StringVar(&connectServerURL, "server", defaultServerURL(), "Server URL [$CODASTRE_SERVER]")
	connectCmd.Flags().StringVar(&connectName, "name", "codastre", "MCP server name written to the target config")
	connectCmd.Flags().StringVar(&connectScope, "scope", "user",
		`Claude config scope: user (global, ~/.claude.json), local (~/.claude/mcp_settings.json),`+
			` or project (.mcp.json in CWD). Only applies to the 'claude' target.`)
	connectCmd.Flags().BoolVar(&connectStdio, "stdio", false,
		"Write a local-proxy (codastre serve) config instead of direct HTTP. Required for hmac-masked repos.")
	rootCmd.AddCommand(connectCmd)
}

func runConnect(cmd *cobra.Command, args []string) error {
	target := strings.ToLower(args[0])
	serverURL := strings.TrimRight(connectServerURL, "/")
	mcpURL := serverURL + "/mcp"
	name := connectName

	host := extractHost(serverURL)
	store, isFallback, err := keychain.Open()
	if err != nil {
		return fmt.Errorf("open keychain: %w", err)
	}
	if isFallback {
		fmt.Fprintln(os.Stderr, "warning: OS keychain unavailable; using file storage")
	}
	apiKey, err := store.GetAPIKey(host)
	if err != nil {
		return fmt.Errorf("no API key for %s — run `codastre login` first: %w", host, err)
	}

	switch target {
	case "claude":
		return connectClaude(cmd, name, mcpURL, serverURL, apiKey, connectScope, connectStdio)
	case "codex":
		return connectCodex(cmd, name, mcpURL, serverURL, apiKey, connectStdio)
	case "opencode":
		return connectOpencode(cmd, name, mcpURL, serverURL, apiKey, connectStdio)
	default:
		return fmt.Errorf("unknown target %q — valid targets: claude, codex, opencode", target)
	}
}

// connectClaude merges an HTTP entry into the Claude config file for the given scope:
//
//	user    → ~/.claude.json                       (global; all directories)
//	local   → ~/.claude/mcp_settings.json          (machine-local; all directories, not synced)
//	project → .mcp.json in the current directory   (committed to git; this repo only)
//
// When writing to user scope, any stale entry in the local-scope file is removed.
func connectClaude(cmd *cobra.Command, name, mcpURL, serverURL, apiKey, scope string, stdio bool) error {
	path, err := claudePathForScope(scope)
	if err != nil {
		return err
	}

	data, err := readJSONFile(path)
	if err != nil {
		return err
	}

	servers, _ := data["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	_, existed := servers[name]
	servers[name] = claudeEntry(mcpURL, serverURL, apiKey, stdio)
	data["mcpServers"] = servers

	if err := writeJSONFile(path, data); err != nil {
		return err
	}

	// When promoting to user scope, remove any stale local-scope entry to avoid duplicates.
	if scope == "user" {
		if localPath, err := claudePathForScope("local"); err == nil {
			_ = removeJSONEntry(localPath, "mcpServers", name)
		}
	}

	verb := "Wrote"
	if existed {
		verb = "Updated"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s MCP server %q → %s\n", verb, name, path)
	printModeHint(cmd, stdio)
	return nil
}

// connectCodex merges into ~/.codex/config.toml. In stdio mode it writes a
// command/args section that launches `codastre serve` (no token needed — serve
// reads the keychain). In HTTP mode it uses Codex's bearer_token_env_var
// convention and prints the export line the user must add to their shell profile.
func connectCodex(cmd *cobra.Command, name, mcpURL, serverURL, apiKey string, stdio bool) error {
	path, err := codexConfigPath()
	if err != nil {
		return err
	}

	var section, envVar string
	if stdio {
		section = codexStdioSection(name, serverURL)
	} else {
		envVar = strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
		section = fmt.Sprintf("[mcp_servers.%s]\nurl = %q\nbearer_token_env_var = %q\n", name, mcpURL, envVar)
	}

	existed, err := replaceTOMLSection(path, "mcp_servers."+name, section)
	if err != nil {
		return err
	}
	verb := "Wrote"
	if existed {
		verb = "Updated"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s MCP server %q → %s\n", verb, name, path)

	if stdio {
		printModeHint(cmd, true)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"\nCodex reads the token from an environment variable. Add this to your shell profile\n"+
			"(~/.zshrc or ~/.bash_profile), then restart your shell:\n\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  export %s=%q\n", envVar, apiKey)
	printModeHint(cmd, false)
	return nil
}

// connectOpencode merges an MCP entry into ~/.config/opencode/opencode.json:
// a "local" (command) entry in stdio mode, or a "remote" (url) entry otherwise.
func connectOpencode(cmd *cobra.Command, name, mcpURL, serverURL, apiKey string, stdio bool) error {
	path, err := opencodeConfigPath()
	if err != nil {
		return err
	}
	data, err := readJSONFile(path)
	if err != nil {
		return err
	}

	mcp, _ := data["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	_, existed := mcp[name]
	mcp[name] = opencodeEntry(mcpURL, serverURL, apiKey, stdio)
	data["mcp"] = mcp

	if err := writeJSONFile(path, data); err != nil {
		return err
	}
	verb := "Wrote"
	if existed {
		verb = "Updated"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s MCP server %q → %s\n", verb, name, path)
	printModeHint(cmd, stdio)
	return nil
}

// ── config paths ──────────────────────────────────────────────────────────────

// claudePathForScope returns the Claude config file path for the given scope.
func claudePathForScope(scope string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	switch scope {
	case "user":
		return filepath.Join(home, ".claude.json"), nil
	case "local":
		return filepath.Join(home, ".claude", "mcp_settings.json"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("working directory: %w", err)
		}
		return filepath.Join(cwd, ".mcp.json"), nil
	default:
		return "", fmt.Errorf("unknown scope %q — valid scopes: user, local, project", scope)
	}
}

func codexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func opencodeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

// readJSONFile reads a JSON object from path, returning an empty map if the
// file does not exist. Creates parent directories as needed.
func readJSONFile(path string) (map[string]any, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// writeJSONFile writes data as pretty-printed JSON to path with mode 0600.
func writeJSONFile(path string, data map[string]any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// removeJSONEntry deletes data[outerKey][innerKey] in the JSON file at path.
// A no-op if the file, the outer key, or the inner key do not exist.
func removeJSONEntry(path, outerKey, innerKey string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil // don't corrupt an unparseable file
	}
	outer, _ := data[outerKey].(map[string]any)
	if outer == nil {
		return nil
	}
	if _, ok := outer[innerKey]; !ok {
		return nil
	}
	delete(outer, innerKey)
	data[outerKey] = outer
	return writeJSONFile(path, data)
}

// ── TOML helper ───────────────────────────────────────────────────────────────

// replaceTOMLSection writes section into path at the position of any existing
// [sectionKey] block, or appends it if absent. Returns true if a prior entry
// was replaced. Creates parent directories and the file if needed.
//
// sectionKey must not contain a leading '['; section must not have a leading newline.
func replaceTOMLSection(path, sectionKey, section string) (existed bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create dir: %w", err)
	}

	existing := ""
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil {
		existing = string(b)
	}

	header := "[" + sectionKey + "]"
	idx := strings.Index(existing, header)
	if idx >= 0 {
		existed = true
		// Find the next top-level section after this one, or EOF.
		end := len(existing)
		if next := strings.Index(existing[idx+len(header):], "\n["); next >= 0 {
			end = idx + len(header) + next + 1
		}
		existing = strings.TrimRight(existing[:idx], "\n") + "\n" + existing[end:]
		existing = strings.TrimRight(existing, "\n")
	} else {
		existing = strings.TrimRight(existing, "\n")
	}

	var out string
	if existing == "" {
		out = section
	} else {
		out = existing + "\n\n" + section
	}

	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return existed, nil
}
