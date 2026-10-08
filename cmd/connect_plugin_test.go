package cmd

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fakeMarketplaces is what the fake `claude plugin marketplace list --json` prints.
var fakeMarketplaces = "[]"

// fakeClaude stands in for the `claude` binary: it records each invocation and
// fails the ones whose joined args start with a prefix in failOn.
func fakeClaude(t *testing.T, found bool, failOn ...string) *[]string {
	t.Helper()
	var calls []string
	t.Cleanup(func() { fakeMarketplaces = "[]" })
	origRun, origLook := commandRunner, claudeLookPath
	t.Cleanup(func() { commandRunner, claudeLookPath = origRun, origLook })
	claudeLookPath = func() (string, error) {
		if !found {
			return "", errors.New("not found")
		}
		return "/usr/local/bin/claude", nil
	}
	commandRunner = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		for _, p := range failOn {
			if strings.HasPrefix(joined, p) {
				return []byte("boom"), errors.New("exit status 1")
			}
		}
		if joined == "plugin marketplace list --json" {
			return []byte(fakeMarketplaces), nil
		}
		return nil, nil
	}
	return &calls
}

func runInstall(t *testing.T, serverURL string) string {
	t.Helper()
	out := &bytes.Buffer{}
	c := &cobra.Command{}
	c.SetOut(out)
	installClaudePlugin(c, serverURL)
	return out.String()
}

func TestInstallClaudePluginAtUserScope(t *testing.T) {
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()
	calls := fakeClaude(t, true)

	got := runInstall(t, srv.URL)
	want := []string{
		"plugin marketplace list --json",
		"plugin marketplace add https://github.com/acme-private/ai-marketplace.git",
		"plugin install codastre@acme-plugins --scope user",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", *calls, want)
	}
	if !strings.Contains(got, "Installed plugin codastre@acme-plugins") {
		t.Fatalf("missing success line:\n%s", got)
	}
}

func TestInstallClaudePluginRefreshesAnAlreadyAddedMarketplace(t *testing.T) {
	// A shared company marketplace, added earlier under another spelling of its
	// source: matched by name, refreshed rather than re-added.
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()
	calls := fakeClaude(t, true)
	fakeMarketplaces = `[{"name":"other","source":"github","repo":"x/y"},
		{"name":"acme-plugins","source":"github","repo":"acme-private/ai-marketplace"}]`

	got := runInstall(t, srv.URL)
	want := []string{
		"plugin marketplace list --json",
		"plugin marketplace update acme-plugins",
		"plugin install codastre@acme-plugins --scope user",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", *calls, want)
	}
	if !strings.Contains(got, "Installed plugin") {
		t.Fatalf("missing success line:\n%s", got)
	}
}

func TestInstallClaudePluginSurvivesAFailedRefresh(t *testing.T) {
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()
	fakeClaude(t, true, "plugin marketplace update")
	fakeMarketplaces = `[{"name":"acme-plugins"}]`

	if got := runInstall(t, srv.URL); !strings.Contains(got, "Installed plugin") {
		t.Fatalf("an offline refresh must not stop the install:\n%s", got)
	}
}

func TestInstallClaudePluginToleratesExistingMarketplace(t *testing.T) {
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()
	calls := fakeClaude(t, true, "plugin marketplace add")

	got := runInstall(t, srv.URL)
	if len(*calls) != 3 || !strings.Contains(got, "Installed plugin") {
		t.Fatalf("a failing marketplace add must not stop the install; calls=%q out:\n%s", *calls, got)
	}
}

func TestInstallClaudePluginFailureFallsBackToCommands(t *testing.T) {
	srv := integrationsDiscoveryServer(t)
	defer srv.Close()
	fakeClaude(t, true, "plugin install")

	got := runInstall(t, srv.URL)
	for _, want := range []string{"warning:", "boom", "claude plugin install codastre@acme-plugins --scope user"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestInstallClaudePluginWithoutClaudeBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	calls := fakeClaude(t, false)

	got := runInstall(t, srv.URL)
	if len(*calls) != 0 {
		t.Fatalf("ran commands without a claude binary: %q", *calls)
	}
	if !strings.Contains(got, "not found on PATH") ||
		!strings.Contains(got, "codastre@"+hostedIntegrations.MarketplaceName+" --scope user") {
		t.Fatalf("want skip notice + hosted commands, got:\n%s", got)
	}
}

// connectClaudeWithPlugin runs connectClaude (user scope, HTTP) against a fake
// `claude` and a temp HOME seeded with stale codastre entries in both the user
// and local files. defaultServer is what a bare `codastre serve` would resolve.
func connectClaudeWithPlugin(t *testing.T, defaultServer string, failOn ...string) (home, out string) {
	t.Helper()
	srv := integrationsDiscoveryServer(t)
	t.Cleanup(srv.Close)
	home = t.TempDir()
	t.Setenv("HOME", home)
	if defaultServer == "" {
		defaultServer = srv.URL
	}
	t.Setenv("CODASTRE_SERVER", defaultServer)
	fakeClaude(t, true, failOn...)

	stale := map[string]any{"mcpServers": map[string]any{"codastre": map[string]any{"type": "http"}}}
	for _, p := range []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "mcp_settings.json")} {
		if _, err := readJSONFile(p); err != nil { // creates the parent dir
			t.Fatal(err)
		}
		if err := writeJSONFile(p, stale); err != nil {
			t.Fatal(err)
		}
	}

	buf := &bytes.Buffer{}
	c := &cobra.Command{}
	c.SetOut(buf)
	if err := connectClaude(c, "codastre", srv.URL+"/mcp", srv.URL, "k", "user", false); err != nil {
		t.Fatalf("connectClaude: %v", err)
	}
	return home, buf.String()
}

func hasCodastreEntry(t *testing.T, path string) bool {
	t.Helper()
	data, err := readJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := data["mcpServers"].(map[string]any)
	_, ok := servers["codastre"]
	return ok
}

func TestConnectClaudeLeavesMCPEntryToThePlugin(t *testing.T) {
	home, out := connectClaudeWithPlugin(t, "")

	for _, p := range []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "mcp_settings.json")} {
		if hasCodastreEntry(t, p) {
			t.Fatalf("%s still has a codastre entry; the plugin provides it:\n%s", p, out)
		}
	}
	for _, want := range []string{"no separate entry written", "always uses the local proxy"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestConnectClaudeKeepsEntryWhenServerDiffersFromPluginDefault(t *testing.T) {
	home, out := connectClaudeWithPlugin(t, "https://other.example.com")

	if !hasCodastreEntry(t, filepath.Join(home, ".claude.json")) {
		t.Fatalf("--server differs from the plugin's default; connect must write its own entry:\n%s", out)
	}
	if !strings.Contains(out, "alongside the plugin") {
		t.Fatalf("want a duplicate-server note, got:\n%s", out)
	}
}

func TestConnectClaudeWritesEntryWhenPluginInstallFails(t *testing.T) {
	home, out := connectClaudeWithPlugin(t, "", "plugin install")

	if !hasCodastreEntry(t, filepath.Join(home, ".claude.json")) {
		t.Fatalf("plugin install failed; connect must fall back to its own entry:\n%s", out)
	}
	if strings.Contains(out, "alongside the plugin") {
		t.Fatalf("no plugin installed, yet printed the duplicate note:\n%s", out)
	}
}
