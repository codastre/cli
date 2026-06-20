package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newTestCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetOut(&bytes.Buffer{})
	return c
}

func TestClaudeEntryStdio(t *testing.T) {
	e := claudeEntry("http://srv/mcp", "http://srv", "k", true)
	if e["type"] != "stdio" {
		t.Fatalf("type = %v, want stdio", e["type"])
	}
	if e["command"] == "" || e["command"] == nil {
		t.Fatal("command must be set")
	}
	args, ok := e["args"].([]string)
	if !ok || len(args) != 3 || args[0] != "serve" || args[1] != "--server" || args[2] != "http://srv" {
		t.Fatalf("args = %v, want [serve --server http://srv]", e["args"])
	}
	if _, ok := e["headers"]; ok {
		t.Fatal("stdio entry must not embed an Authorization header")
	}
}

func TestClaudeEntryHTTP(t *testing.T) {
	e := claudeEntry("http://srv/mcp", "http://srv", "secret", false)
	if e["type"] != "http" {
		t.Fatalf("type = %v, want http", e["type"])
	}
	if e["url"] != "http://srv/mcp" {
		t.Fatalf("url = %v", e["url"])
	}
	h, ok := e["headers"].(map[string]string)
	if !ok || h["Authorization"] != "Bearer secret" {
		t.Fatalf("headers = %v", e["headers"])
	}
}

func TestOpencodeEntryStdio(t *testing.T) {
	e := opencodeEntry("http://srv/mcp", "http://srv", "k", true)
	if e["type"] != "local" {
		t.Fatalf("type = %v, want local", e["type"])
	}
	cmd, ok := e["command"].([]string)
	if !ok || len(cmd) != 4 || cmd[1] != "serve" || cmd[2] != "--server" || cmd[3] != "http://srv" {
		t.Fatalf("command = %v", e["command"])
	}
	if e["enabled"] != true {
		t.Fatalf("enabled = %v, want true", e["enabled"])
	}
}

func TestOpencodeEntryHTTP(t *testing.T) {
	e := opencodeEntry("http://srv/mcp", "http://srv", "k", false)
	if e["type"] != "remote" {
		t.Fatalf("type = %v, want remote", e["type"])
	}
}

func TestCodexStdioSection(t *testing.T) {
	s := codexStdioSection("codastre", "http://srv")
	if !strings.Contains(s, "[mcp_servers.codastre]") {
		t.Fatalf("missing section header: %q", s)
	}
	if !strings.Contains(s, "command = ") {
		t.Fatalf("missing command key: %q", s)
	}
	if !strings.Contains(s, `args = ["serve", "--server", "http://srv"]`) {
		t.Fatalf("missing args: %q", s)
	}
	if strings.Contains(s, "bearer_token_env_var") {
		t.Fatalf("stdio section must not reference a bearer token: %q", s)
	}
}

func TestConnectClaudeStdioWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := connectClaude(newTestCmd(), "codastre", "http://srv/mcp", "http://srv", "k", "user", true); err != nil {
		t.Fatalf("connectClaude: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var data struct {
		McpServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	entry := data.McpServers["codastre"]
	if entry.Type != "stdio" || entry.Command == "" {
		t.Fatalf("entry = %+v", entry)
	}
	if len(entry.Args) != 3 || entry.Args[0] != "serve" {
		t.Fatalf("args = %v", entry.Args)
	}
}

func TestConnectCodexStdioWritesSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := connectCodex(newTestCmd(), "codastre", "http://srv/mcp", "http://srv", "k", true); err != nil {
		t.Fatalf("connectCodex: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "[mcp_servers.codastre]") || !strings.Contains(got, "command = ") {
		t.Fatalf("config = %q", got)
	}
	if strings.Contains(got, "bearer_token_env_var") {
		t.Fatalf("stdio config must not write a bearer token: %q", got)
	}
}

func TestConnectOpencodeStdioWritesLocalEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := connectOpencode(newTestCmd(), "codastre", "http://srv/mcp", "http://srv", "k", true); err != nil {
		t.Fatalf("connectOpencode: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var data struct {
		Mcp map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	entry := data.Mcp["codastre"]
	if entry.Type != "local" || len(entry.Command) != 4 || !entry.Enabled {
		t.Fatalf("entry = %+v", entry)
	}
}
